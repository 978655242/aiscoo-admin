package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestXCodePlanDisplayPersistenceAndWebProjection(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.XCodeAuthorization{}))

	previousPayment := *operation_setting.GetPaymentSetting()
	previousGeneral, previousQuotaPerUnit := *operation_setting.GetGeneralSetting(), common.QuotaPerUnit
	operation_setting.GetPaymentSetting().ComplianceConfirmed = true
	operation_setting.GetPaymentSetting().ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	operation_setting.GetGeneralSetting().QuotaDisplayType = "USD"
	common.QuotaPerUnit = 500000
	t.Cleanup(func() {
		*operation_setting.GetPaymentSetting() = previousPayment
		*operation_setting.GetGeneralSetting(), common.QuotaPerUnit = previousGeneral, previousQuotaPerUnit
	})

	call := func(handler gin.HandlerFunc, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var encoded []byte
		if body != nil {
			var err error
			encoded, err = common.Marshal(body)
			require.NoError(t, err)
		}
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(method, path, bytes.NewReader(encoded))
		context.Request.Header.Set("Content-Type", "application/json")
		if method == http.MethodPut {
			context.Params = gin.Params{{Key: "id", Value: path[strings.LastIndexByte(path, '/')+1:]}}
		}
		handler(context)
		return recorder
	}

	metadata := &model.XCodePlanDisplay{
		GroupKey: "pro-tier", GroupTitle: strings.Repeat("a", 127) + "😀", OfferKind: "standard",
		Highlights: []string{strings.Repeat("a", 255) + "😀"}, Features: []string{strings.Repeat("a", 255) + "😀"},
	}
	dtoMetadata := &dto.XCodePlanDisplay{
		GroupKey: metadata.GroupKey, GroupTitle: metadata.GroupTitle, OfferKind: metadata.OfferKind,
		Highlights: metadata.Highlights, Features: metadata.Features,
	}
	var planCountBefore int64
	require.NoError(t, db.Model(&model.SubscriptionPlan{}).Count(&planCountBefore).Error)
	invalidCreate := call(AdminCreateSubscriptionPlan, http.MethodPost, "/api/subscription/admin/plans", map[string]any{
		"plan": map[string]any{
			"title": "Invalid display", "price_amount": 10, "currency": "USD",
			"duration_unit": "month", "duration_value": 1, "enabled": true,
			"max_purchase_per_user": 0, "total_amount": 500000,
			"quota_reset_period": "never", "xcode_display": map[string]any{
				"group_key": "invalid key", "group_title": "Invalid", "offer_kind": "standard",
				"highlights": []string{}, "features": []string{},
			},
		},
	})
	var rejected struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(invalidCreate.Body.Bytes(), &rejected))
	assert.False(t, rejected.Success)
	var planCountAfter int64
	require.NoError(t, db.Model(&model.SubscriptionPlan{}).Count(&planCountAfter).Error)
	assert.Equal(t, planCountBefore, planCountAfter)

	create := call(AdminCreateSubscriptionPlan, http.MethodPost, "/api/subscription/admin/plans", map[string]any{
		"plan": map[string]any{
			"title": "Display roundtrip", "price_amount": 10, "currency": "USD",
			"duration_unit": "month", "duration_value": 1, "enabled": true,
			"max_purchase_per_user": 0, "total_amount": 500000,
			"quota_reset_period": "never", "xcode_display": metadata,
		},
	})
	require.Equal(t, http.StatusOK, create.Code, create.Body.String())
	var created struct {
		Success bool                   `json:"success"`
		Data    model.SubscriptionPlan `json:"data"`
	}
	require.NoError(t, common.Unmarshal(create.Body.Bytes(), &created))
	require.True(t, created.Success, create.Body.String())
	require.NotZero(t, created.Data.Id)
	assert.Equal(t, metadata, created.Data.XCodeDisplay)

	stored, err := model.GetSubscriptionPlanById(created.Data.Id)
	require.NoError(t, err)
	assert.Equal(t, metadata, stored.XCodeDisplay)

	edited := *stored
	edited.XCodeDisplay = &model.XCodePlanDisplay{
		GroupKey: "pro-tier", GroupTitle: "Pro Plus", OfferKind: "trial",
		Highlights: []string{"Updated highlight"}, Features: []string{"Updated feature"},
	}
	update := call(AdminUpdateSubscriptionPlan, http.MethodPut,
		fmt.Sprintf("/api/subscription/admin/plans/%d", stored.Id), map[string]any{"plan": edited})
	require.Equal(t, http.StatusOK, update.Code, update.Body.String())
	reloaded, err := model.GetSubscriptionPlanById(stored.Id)
	require.NoError(t, err)
	assert.Equal(t, edited.XCodeDisplay, reloaded.XCodeDisplay)

	const exactPlanID = 9007199254740993
	exactPlan := model.SubscriptionPlan{
		Id: exactPlanID, Title: "Exact ID grantable", PriceAmount: 24, Currency: "USD",
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 3, Enabled: true,
		SortOrder: 20, MaxPurchasePerUser: 0, TotalAmount: 1500000,
		QuotaResetPeriod: model.SubscriptionResetMonthly, XCodeDisplay: metadata,
	}
	blockedPlan := model.SubscriptionPlan{
		Title: "Purchase-limited", PriceAmount: 10, Currency: "USD",
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
		SortOrder: 10, MaxPurchasePerUser: 1, TotalAmount: 500000,
		QuotaResetPeriod: model.SubscriptionResetMonthly, XCodeDisplay: metadata,
	}
	require.NoError(t, db.Create(&exactPlan).Error)
	require.NoError(t, db.Create(&blockedPlan).Error)
	const corruptPlanID = 9007199254740995
	corruptPlan := model.SubscriptionPlan{
		Id: corruptPlanID, Title: "Corrupt legacy display", PriceAmount: 12, Currency: "USD",
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
		SortOrder: 5, MaxPurchasePerUser: 0, TotalAmount: 500000,
		QuotaResetPeriod: model.SubscriptionResetMonthly,
	}
	require.NoError(t, db.Create(&corruptPlan).Error)
	require.NoError(t, db.Exec("UPDATE subscription_plans SET xcode_display = ? WHERE id = ?", `{"group_key":`, corruptPlanID).Error)
	user := model.User{Username: "display-viewer", AffCode: "display-viewer", Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)

	viewRecorder := httptest.NewRecorder()
	viewContext, _ := gin.CreateTestContext(viewRecorder)
	viewContext.Request = httptest.NewRequest(http.MethodGet, "/api/xcode/v1/web/view", nil)
	viewContext.Set("id", user.Id)
	GetXCodeWebView(viewContext)
	require.Equal(t, http.StatusOK, viewRecorder.Code, viewRecorder.Body.String())
	var view struct {
		Data struct {
			Plans             *dto.XCodePlansSnapshot `json:"plans"`
			PlanPresentations []struct {
				PlanID           string                `json:"plan_id"`
				Metadata         *dto.XCodePlanDisplay `json:"metadata"`
				Assignable       bool                  `json:"assignable"`
				AssignmentReason *string               `json:"assignment_reason"`
				DisplayIssue     *string               `json:"display_issue"`
			} `json:"plan_presentations"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(viewRecorder.Body.Bytes(), &view))
	require.NotEmpty(t, view.Data.PlanPresentations)
	require.NotNil(t, view.Data.Plans)
	assert.Contains(t, view.Data.Plans.Items, dto.XCodePlan{ID: strconv.FormatInt(corruptPlanID, 10), Title: corruptPlan.Title, Subtitle: corruptPlan.Subtitle, PriceAmount: "12", Currency: "USD", DurationUnit: "month", DurationValue: "1", DurationCustomSeconds: "0", TotalQuota: "500000", ResetPeriod: "monthly", ResetCustomSeconds: "0", Enabled: true})
	presentations := make(map[string]struct {
		metadata   *dto.XCodePlanDisplay
		assignable bool
		reason     *string
		issue      *string
	}, len(view.Data.PlanPresentations))
	for _, presentation := range view.Data.PlanPresentations {
		presentations[presentation.PlanID] = struct {
			metadata   *dto.XCodePlanDisplay
			assignable bool
			reason     *string
			issue      *string
		}{presentation.Metadata, presentation.Assignable, presentation.AssignmentReason, presentation.DisplayIssue}
	}
	exact := presentations[strconv.FormatInt(exactPlanID, 10)]
	assert.Equal(t, dtoMetadata, exact.metadata)
	assert.True(t, exact.assignable)
	assert.Nil(t, exact.reason)
	assert.Nil(t, exact.issue)
	blocked := presentations[strconv.Itoa(blockedPlan.Id)]
	assert.Equal(t, dtoMetadata, blocked.metadata)
	assert.False(t, blocked.assignable)
	require.NotNil(t, blocked.reason)
	assert.Equal(t, "template_config", *blocked.reason)
	assert.Nil(t, blocked.issue)
	corrupt := presentations[strconv.FormatInt(corruptPlanID, 10)]
	assert.Nil(t, corrupt.metadata)
	assert.False(t, corrupt.assignable)
	require.NotNil(t, corrupt.reason)
	assert.Equal(t, "template_config", *corrupt.reason)
	require.NotNil(t, corrupt.issue)
	assert.Equal(t, "invalid_display", *corrupt.issue)

	controlRecorder := httptest.NewRecorder()
	controlContext, _ := gin.CreateTestContext(controlRecorder)
	controlContext.Request = httptest.NewRequest(http.MethodGet, "/api/xcode/v1/plans", nil)
	GetXCodePlans(controlContext)
	require.Equal(t, http.StatusOK, controlRecorder.Code, controlRecorder.Body.String())
	var control struct {
		Data struct {
			Items []map[string]common.RawMessage `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(controlRecorder.Body.Bytes(), &control))
	require.NotEmpty(t, control.Data.Items)
	oldPlanKeys := []string{"id", "title", "subtitle", "price_amount", "currency", "duration_unit", "duration_value", "duration_custom_seconds", "total_quota", "reset_period", "reset_custom_seconds", "enabled"}
	for _, item := range control.Data.Items {
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		assert.ElementsMatch(t, oldPlanKeys, keys)
	}
	assert.NotContains(t, controlRecorder.Body.String(), "plan_presentations")

	cleared := *reloaded
	cleared.XCodeDisplay = nil
	clear := call(AdminUpdateSubscriptionPlan, http.MethodPut,
		fmt.Sprintf("/api/subscription/admin/plans/%d", reloaded.Id), map[string]any{"plan": cleared})
	require.Equal(t, http.StatusOK, clear.Code, clear.Body.String())
	clearedStored, err := model.GetSubscriptionPlanById(reloaded.Id)
	require.NoError(t, err)
	assert.Nil(t, clearedStored.XCodeDisplay)
}
