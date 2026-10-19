package model

import (
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

var xcodePlanDisplayGroupKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// XCodePlanDisplay is optional presentation-only metadata for the XCode plan page.
type XCodePlanDisplay struct {
	GroupKey   string   `json:"group_key"`
	GroupTitle string   `json:"group_title"`
	OfferKind  string   `json:"offer_kind"`
	Highlights []string `json:"highlights"`
	Features   []string `json:"features"`

	invalidRaw string
	invalid    bool
}

func (display *XCodePlanDisplay) Validate() error {
	if display == nil {
		return nil
	}
	if !xcodePlanDisplayGroupKey.MatchString(display.GroupKey) || strings.TrimSpace(display.GroupTitle) == "" || utf8.RuneCountInString(display.GroupTitle) > 128 {
		return errors.New("invalid XCode plan display group")
	}
	if display.OfferKind != "standard" && display.OfferKind != "trial" {
		return errors.New("invalid XCode plan display offer kind")
	}
	if len(display.Highlights) > 4 || len(display.Features) > 16 {
		return errors.New("invalid XCode plan display item count")
	}
	for _, lines := range [][]string{display.Highlights, display.Features} {
		for _, line := range lines {
			if strings.TrimSpace(line) == "" || utf8.RuneCountInString(line) > 256 {
				return errors.New("invalid XCode plan display item")
			}
		}
	}
	return nil
}

func (display *XCodePlanDisplay) IsInvalid() bool {
	return display != nil && display.invalid
}

func (display XCodePlanDisplay) Value() (driver.Value, error) {
	if display.invalid {
		return display.invalidRaw, nil
	}
	if err := display.Validate(); err != nil {
		return nil, err
	}
	encoded, err := common.Marshal(display)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

func (display XCodePlanDisplay) MarshalJSON() ([]byte, error) {
	if display.invalid {
		return []byte("null"), nil
	}
	type wire XCodePlanDisplay
	return common.Marshal(wire(display))
}

func (display *XCodePlanDisplay) Scan(value any) error {
	if value == nil {
		*display = XCodePlanDisplay{}
		return nil
	}

	var raw string
	switch value := value.(type) {
	case string:
		raw = value
	case []byte:
		raw = string(value)
	default:
		return errors.New("invalid XCode plan display database value")
	}
	var decoded XCodePlanDisplay
	if err := common.UnmarshalJsonStr(raw, &decoded); err != nil || decoded.Validate() != nil {
		// XCODE: 空字符串也是损坏记录；单独标记，避免错误元数据被当成合法配置。
		*display = XCodePlanDisplay{invalidRaw: raw, invalid: true}
		return nil
	}
	*display = decoded
	return nil
}

func (display *XCodePlanDisplay) UnmarshalJSON(data []byte) error {
	type wire XCodePlanDisplay
	var fields map[string]common.RawMessage
	if err := common.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"group_key", "group_title", "offer_kind", "highlights", "features"} {
		if _, ok := fields[key]; !ok {
			return errors.New("incomplete XCode plan display")
		}
		delete(fields, key)
	}
	if len(fields) != 0 {
		return errors.New("unknown XCode plan display field")
	}
	var decoded wire
	if err := common.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.Highlights == nil || decoded.Features == nil {
		return errors.New("invalid XCode plan display arrays")
	}
	*display = XCodePlanDisplay(decoded)
	return display.Validate()
}
