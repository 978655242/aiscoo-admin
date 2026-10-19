package service

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
)

var (
	ErrXCodeUsageRequestInvalid = errors.New("XCODE_USAGE_REQUEST_INVALID")
	ErrXCodeUsageUnavailable    = errors.New("XCODE_USAGE_UNAVAILABLE")
)

// Counters retain native integer facts until the final decimal-string projection.
type xcodeUsageTotals [5]int64

func (totals *xcodeUsageTotals) add(row model.LogAccountUsageAggregate) error {
	values := xcodeUsageTotals{row.ConsumeRecordCount, row.PromptTokens, row.CompletionTokens, row.ChargedQuota, row.RefundedQuota}
	for i, value := range values {
		if (value > 0 && totals[i] > math.MaxInt64-value) || (value < 0 && totals[i] < math.MinInt64-value) {
			return ErrXCodeUsageUnavailable
		}
		totals[i] += value
	}
	return nil
}

func (totals xcodeUsageTotals) summary() (dto.XCodeUsageSummary, error) {
	prompt, completion, charged, refunded := totals[1], totals[2], totals[3], totals[4]
	if (completion > 0 && prompt > math.MaxInt64-completion) || (completion < 0 && prompt < math.MinInt64-completion) ||
		(refunded > 0 && charged < math.MinInt64+refunded) || (refunded < 0 && charged > math.MaxInt64+refunded) {
		return dto.XCodeUsageSummary{}, ErrXCodeUsageUnavailable
	}
	return dto.XCodeUsageSummary{
		ConsumeRecordCount: strconv.FormatInt(totals[0], 10),
		PromptTokens:       strconv.FormatInt(prompt, 10),
		CompletionTokens:   strconv.FormatInt(completion, 10),
		TotalTokens:        strconv.FormatInt(prompt+completion, 10),
		ChargedQuota:       strconv.FormatInt(charged, 10),
		RefundedQuota:      strconv.FormatInt(refunded, 10),
		NetQuota:           strconv.FormatInt(charged-refunded, 10),
	}, nil
}

func GetXCodeAccountUsage(userID int, query dto.XCodeUsageQuery, now time.Time) (*dto.XCodeAccountUsageSnapshot, error) {
	if userID <= 0 {
		return nil, ErrXCodeUsageRequestInvalid
	}
	location := time.Local
	if query.TimeZone != "" {
		if query.TimeZone == "Local" || query.TimeZone == "server-local" {
			return nil, ErrXCodeUsageRequestInvalid
		}
		var err error
		location, err = time.LoadLocation(query.TimeZone)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid time_zone", ErrXCodeUsageRequestInvalid)
		}
	}
	zoneName := location.String()
	if zoneName == "Local" {
		zoneName = "server-local"
	}
	localNow := now.In(location)
	endDate := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.UTC)
	startDate := endDate
	granularity := "day"
	switch query.Range {
	case "", "today":
		granularity = "hour"
	case "7d":
		startDate = endDate.AddDate(0, 0, -6)
	case "30d":
		startDate = endDate.AddDate(0, 0, -29)
	case "custom":
		var err error
		startDate, err = time.Parse("2006-01-02", query.StartDate)
		if err != nil || startDate.Format("2006-01-02") != query.StartDate {
			return nil, fmt.Errorf("%w: invalid start_date", ErrXCodeUsageRequestInvalid)
		}
		endDate, err = time.Parse("2006-01-02", query.EndDate)
		if err != nil || endDate.Format("2006-01-02") != query.EndDate || endDate.Before(startDate) || endDate.After(startDate.AddDate(0, 0, 29)) {
			return nil, fmt.Errorf("%w: invalid end_date or range exceeds 30 dates", ErrXCodeUsageRequestInvalid)
		}
	default:
		return nil, fmt.Errorf("%w: invalid range", ErrXCodeUsageRequestInvalid)
	}
	start, err := xcodeLocalDateStart(startDate, location)
	if err != nil {
		return nil, err
	}
	endDateExclusive := endDate.AddDate(0, 0, 1)
	end, err := xcodeLocalDateStart(endDateExclusive, location)
	if err != nil {
		return nil, err
	}
	boundaries := make([]int64, 0, 32)
	buckets := make([]dto.XCodeUsageBucket, 0, 31)
	for date, cursor := startDate, start; cursor.Before(end); {
		var next time.Time
		label := cursor.Format("2006-01-02")
		if granularity == "hour" {
			next = cursor.Add(time.Hour)
			if next.After(end) {
				next = end
			}
			label = cursor.Format("15:04 -07:00")
		} else {
			date = date.AddDate(0, 0, 1)
			next, err = xcodeLocalDateStart(date, location)
			if err != nil {
				return nil, err
			}
		}
		if !next.After(cursor) {
			return nil, fmt.Errorf("%w: nonexistent local calendar date", ErrXCodeUsageRequestInvalid)
		}
		boundaries = append(boundaries, cursor.UTC().Unix())
		buckets = append(buckets, dto.XCodeUsageBucket{StartTime: cursor.UnixMilli(), EndTime: next.UnixMilli(), Label: label})
		cursor = next
	}
	boundaries = append(boundaries, end.UTC().Unix())
	if !common.LogConsumeEnabled {
		return nil, ErrXCodeUsageUnavailable
	}
	display, err := GetXCodeQuotaDisplay()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrXCodeUsageUnavailable, err)
	}
	rows, err := model.AggregateAccountUsageLogs(userID, boundaries)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrXCodeUsageUnavailable, err)
	}
	type modelTotals struct {
		total   xcodeUsageTotals
		buckets []xcodeUsageTotals
	}
	var total xcodeUsageTotals
	bucketTotals := make([]xcodeUsageTotals, len(buckets))
	models := make(map[string]*modelTotals)
	for _, row := range rows {
		if row.BucketIndex < 0 || row.BucketIndex >= len(buckets) {
			return nil, ErrXCodeUsageUnavailable
		}
		modelTotal := models[row.ModelName]
		if modelTotal == nil {
			modelTotal = &modelTotals{buckets: make([]xcodeUsageTotals, len(buckets))}
			models[row.ModelName] = modelTotal
		}
		for _, target := range []*xcodeUsageTotals{&total, &bucketTotals[row.BucketIndex], &modelTotal.total, &modelTotal.buckets[row.BucketIndex]} {
			if err := target.add(row); err != nil {
				return nil, err
			}
		}
	}
	summary, err := total.summary()
	if err != nil {
		return nil, err
	}
	for i := range buckets {
		buckets[i].XCodeUsageSummary, err = bucketTotals[i].summary()
		if err != nil {
			return nil, err
		}
	}
	modelIDs := make([]string, 0, len(models))
	for modelID := range models {
		modelIDs = append(modelIDs, modelID)
	}
	slices.Sort(modelIDs)
	modelUsage := make([]dto.XCodeModelUsage, 0, len(models))
	for _, modelID := range modelIDs {
		modelTotal := models[modelID]
		modelSummary, err := modelTotal.total.summary()
		if err != nil {
			return nil, err
		}
		modelBuckets := make([]dto.XCodeUsageBucket, len(buckets))
		for i := range modelBuckets {
			bucketSummary, err := modelTotal.buckets[i].summary()
			if err != nil {
				return nil, err
			}
			modelBuckets[i] = dto.XCodeUsageBucket{
				StartTime: buckets[i].StartTime, EndTime: buckets[i].EndTime, Label: buckets[i].Label,
				XCodeUsageSummary: bucketSummary,
			}
		}
		modelUsage = append(modelUsage, dto.XCodeModelUsage{ModelID: modelID, XCodeUsageSummary: modelSummary, Buckets: modelBuckets})
	}
	return &dto.XCodeAccountUsageSnapshot{
		SchemaVersion: 1, ServerTime: now.UnixMilli(), AsOf: now.UnixMilli(),
		Scope: "new-api-account", Source: "consume-refund-logs",
		RetentionBasis: "Retained recorded consume and refund logs; not guaranteed complete financial history.",
		TimeZone:       zoneName, RangeStartDate: startDate.Format("2006-01-02"), RangeEndDate: endDate.Format("2006-01-02"),
		Granularity: granularity, Display: display, Summary: summary, Buckets: buckets, Models: modelUsage,
		UnsupportedMetrics: []string{"agent_duration", "tool_execution", "tool_error_rate", "decode_speed", "reasoning_tokens", "cache_token_breakdown"},
	}, nil
}

func xcodeLocalDateStart(date time.Time, location *time.Location) (time.Time, error) {
	year, month, day := date.Date()
	start := time.Date(year, month, day, 0, 0, 0, 0, location)
	y, m, d := start.Date()
	if y != year || m != month || d != day {
		// Date can normalize a missing midnight into the preceding calendar date.
		_, start = start.ZoneBounds()
		y, m, d = start.Date()
		if start.IsZero() || y != year || m != month || d != day {
			return time.Time{}, fmt.Errorf("%w: nonexistent local calendar date", ErrXCodeUsageRequestInvalid)
		}
	}
	previous := start.Add(-time.Nanosecond)
	y, m, d = previous.Date()
	if y == year && m == month && d == day {
		// A repeated midnight must include its first occurrence.
		_, offset := start.Zone()
		_, previousOffset := previous.Zone()
		start = start.Add(time.Duration(offset-previousOffset) * time.Second)
	}
	return start, nil
}
