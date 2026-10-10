package controller

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestXCodeWebHandoffDatabaseMatrix(t *testing.T) {
	for index, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env)
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			require.NoError(t, db.AutoMigrate(&model.UserSession{}, &model.Token{}, &model.AuthFlow{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.XCodeAuthorization{}))
			t.Setenv("XCODE_INTEGRATION_ENABLED", "true")
			const origin = "http://127.0.0.1:33519"
			t.Setenv("XCODE_PUBLIC_ORIGIN", origin)
			previousSecret, previousUnit := common.SessionSecret, common.QuotaPerUnit
			previousPayment, previousGeneral := *operation_setting.GetPaymentSetting(), *operation_setting.GetGeneralSetting()
			common.SessionSecret, common.QuotaPerUnit = "owned-web-handoff-regression-secret", 500000
			operation_setting.GetPaymentSetting().ComplianceConfirmed = true
			operation_setting.GetPaymentSetting().ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
			operation_setting.GetGeneralSetting().QuotaDisplayType = "USD"
			t.Cleanup(func() {
				common.SessionSecret, common.QuotaPerUnit = previousSecret, previousUnit
				*operation_setting.GetPaymentSetting(), *operation_setting.GetGeneralSetting() = previousPayment, previousGeneral
			})
			user := model.User{Username: "web-owner", DisplayName: "Web owner", AffCode: "web-owner", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 1000}
			require.NoError(t, db.Create(&user).Error)
			other := model.User{Username: "web-other", AffCode: "web-other", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1}
			require.NoError(t, db.Create(&other).Error)
			now := time.Now().Unix()
			source := model.UserSession{SID: fmt.Sprintf("abcdef01-1234-4567-8900-%012d", index+1), UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: model.UserSessionStatusActive, ClientPurpose: model.ClientPurposeXCode, RefreshHash: strings.Repeat("a", 64), LoginMethod: "password", ExpiresAt: now + 3600}
			require.NoError(t, db.Create(&source).Error)
			identity := service.AuthIdentity{UserID: user.Id, SessionID: source.SID, UserAuthVersion: 1, SessionVersion: 1}
			control, _, err := service.IssueXCodeAccessToken(&source)
			require.NoError(t, err)
			plan := model.SubscriptionPlan{Title: "Actual EUR template", PriceAmount: 19.5, Currency: "EUR", DurationUnit: "month", DurationValue: 1, Enabled: true, TotalAmount: 0, QuotaResetPeriod: "never", StripePriceId: "owned-private-product"}
			require.NoError(t, db.Create(&plan).Error)
			sub := model.UserSubscription{UserId: user.Id, PlanId: plan.Id, UsageScope: model.SubscriptionUsageScopeXCode, Source: "admin", Status: "active", StartTime: now - 1, EndTime: now + 86400, AmountTotal: 0, AmountUsed: 42, QuotaResetPeriodSnapshot: "never", QuotaResetGeneration: 1}
			require.NoError(t, db.Create(&sub).Error)
			require.NoError(t, db.Create(&model.XCodeAuthorization{UserId: user.Id, SubscriptionId: sub.Id, Revision: 2}).Error)
			engine := gin.New()
			engine.POST("/api/xcode/v1/web-handoffs", middleware.XCodeControlAuth(), CreateXCodeWebHandoff)
			engine.GET("/xcode/plans", ConsumeXCodeWebHandoff)
			engine.GET("/api/xcode/v1/web/view", middleware.XCodeWebAuth(), GetXCodeWebView)
			engine.GET("/api/xcode/v1/me", middleware.XCodeControlAuth(), GetXCodeMe)
			engine.GET("/api/user/self", middleware.UserAuth(), GetSelf)
			request := func(method, path, bearer string, cookie *http.Cookie, requestOrigin string) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest(method, origin+path, nil)
				if requestOrigin != "" {
					req.Header.Set("Origin", requestOrigin)
				}
				if bearer != "" {
					req.Header.Set("Authorization", "Bearer "+bearer)
				}
				if cookie != nil {
					req.AddCookie(cookie)
				}
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, req)
				return response
			}
			create := request(http.MethodPost, "/api/xcode/v1/web-handoffs", control, nil, origin)
			require.Equal(t, http.StatusOK, create.Code, create.Body.String())
			var issued struct {
				Data dto.XCodeWebHandoffResponse `json:"data"`
			}
			require.NoError(t, common.Unmarshal(create.Body.Bytes(), &issued))
			target, err := url.Parse(issued.Data.URL)
			require.NoError(t, err)
			assert.Equal(t, origin, target.Scheme+"://"+target.Host)
			assert.Equal(t, "/xcode/plans", target.Path)
			assert.Greater(t, issued.Data.ExpiresAt, issued.Data.ServerTime)
			assert.LessOrEqual(t, issued.Data.ExpiresAt-issued.Data.ServerTime, int64(60000))
			assert.Equal(t, "no-store", create.Header().Get("Cache-Control"))
			assert.Equal(t, http.StatusForbidden, request(http.MethodPost, "/api/xcode/v1/web-handoffs", control, nil, "https://attacker.example").Code)
			// A normal refresh must not invalidate the already-issued SID-bound handoff.
			rotated, err := model.RotateUserSessionRefresh(user.Id, source.SID, source.RefreshHash, strings.Repeat("b", 64), time.Now().Unix(), time.Minute)
			require.NoError(t, err)
			require.Equal(t, strings.Repeat("b", 64), rotated.RefreshHash)
			consume := request(http.MethodGet, target.RequestURI(), "", nil, "")
			require.Equal(t, http.StatusSeeOther, consume.Code, consume.Body.String())
			assert.Equal(t, "/xcode/plans", consume.Header().Get("Location"))
			assert.Equal(t, "no-store", consume.Header().Get("Cache-Control"))
			assert.Equal(t, "no-referrer", consume.Header().Get("Referrer-Policy"))
			cookies := consume.Result().Cookies()
			require.Len(t, cookies, 1)
			cookie := cookies[0]
			assert.Equal(t, service.XCodeWebCookieName(origin), cookie.Name)
			assert.True(t, cookie.HttpOnly)
			assert.False(t, cookie.Secure)
			assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
			assert.Equal(t, "/api/xcode/v1/web", cookie.Path)
			assert.Empty(t, cookie.Domain)
			assert.Positive(t, cookie.MaxAge)
			assert.LessOrEqual(t, cookie.MaxAge, 900)
			webIdentity, err := service.ParseXCodeWebToken(cookie.Value)
			require.NoError(t, err)
			assert.Equal(t, user.Id, webIdentity.UserID)
			assert.Equal(t, source.SID, webIdentity.SessionID)
			assert.Equal(t, rotated.Version, webIdentity.SessionVersion)
			assert.Equal(t, http.StatusBadRequest, request(http.MethodGet, target.RequestURI(), "", nil, "").Code)
			assert.Equal(t, http.StatusForbidden, request(http.MethodGet, "/api/xcode/v1/web/view?user_id="+fmt.Sprint(other.Id), "", cookie, origin).Code)
			view := request(http.MethodGet, "/api/xcode/v1/web/view", "", cookie, origin)
			require.Equal(t, http.StatusOK, view.Code, view.Body.String())
			var visible struct {
				Data dto.XCodeWebView `json:"data"`
			}
			require.NoError(t, common.Unmarshal(view.Body.Bytes(), &visible))
			require.NotNil(t, visible.Data.Plans)
			require.NotNil(t, visible.Data.Entitlements)
			require.Len(t, visible.Data.Plans.Items, 1)
			assert.Equal(t, "EUR", visible.Data.Plans.Items[0].Currency)
			assert.Equal(t, "19.5", visible.Data.Plans.Items[0].PriceAmount)
			require.NotNil(t, visible.Data.Entitlements.Quota)
			assert.Equal(t, "42", visible.Data.Entitlements.Quota.Used)
			assert.True(t, visible.Data.Entitlements.Quota.Unlimited)
			assert.Nil(t, visible.Data.Entitlements.Quota.Total)
			assert.Nil(t, visible.Data.Entitlements.Quota.NextResetTime)
			for _, secret := range []string{cookie.Value, control, source.RefreshHash, "owned-private-product"} {
				assert.NotContains(t, view.Body.String(), secret)
			}
			assert.Equal(t, http.StatusForbidden, request(http.MethodGet, "/api/xcode/v1/web/view", control, nil, origin).Code)
			assert.Equal(t, http.StatusForbidden, request(http.MethodGet, "/api/xcode/v1/web/view", "", &http.Cookie{Name: cookie.Name, Value: control}, origin).Code)
			assert.Equal(t, http.StatusForbidden, request(http.MethodGet, "/api/xcode/v1/me", cookie.Value, nil, origin).Code)
			assert.Equal(t, http.StatusUnauthorized, request(http.MethodGet, "/api/user/self", "", cookie, origin).Code)
			assert.Equal(t, http.StatusForbidden, request(http.MethodGet, "/api/user/self", cookie.Value, nil, origin).Code)
			assert.Equal(t, http.StatusForbidden, request(http.MethodGet, "/api/xcode/v1/web/view", "", cookie, "https://attacker.example").Code)
			identity.SessionVersion = rotated.Version
			contested, err := service.CreateXCodeWebHandoff(identity)
			require.NoError(t, err)
			contestedURL, err := url.Parse(contested.URL)
			require.NoError(t, err)
			start := make(chan struct{})
			results := make(chan error, 2)
			var consumers sync.WaitGroup
			for range 2 {
				consumers.Go(func() {
					<-start
					_, _, err := service.ConsumeXCodeWebHandoff(contestedURL.Query().Get("handoff"))
					results <- err
				})
			}
			close(start)
			consumers.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				} else {
					assert.ErrorIs(t, err, service.ErrXCodeAuthCodeInvalid)
				}
			}
			assert.Equal(t, 1, successes, "concurrent consumption grants only one Web credential")
			t.Setenv("XCODE_PUBLIC_ORIGIN", "https://gateway.example")
			secure, err := service.CreateXCodeWebHandoff(identity)
			require.NoError(t, err)
			secureRequest := httptest.NewRequest(http.MethodGet, secure.URL, nil)
			secureResponse := httptest.NewRecorder()
			engine.ServeHTTP(secureResponse, secureRequest)
			require.Equal(t, http.StatusSeeOther, secureResponse.Code, secureResponse.Body.String())
			secureCookies := secureResponse.Result().Cookies()
			require.Len(t, secureCookies, 1)
			assert.True(t, secureCookies[0].Secure)
			assert.Equal(t, "__Secure-new_api_xcode_web", secureCookies[0].Name)
			t.Setenv("XCODE_PUBLIC_ORIGIN", origin)
			shortExpiry := time.Now().Unix() + 30
			require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", source.SID).Update("expires_at", shortExpiry).Error)
			short, err := service.CreateXCodeWebHandoff(identity)
			require.NoError(t, err)
			assert.LessOrEqual(t, short.ExpiresAt, shortExpiry*1000)
			shortURL, err := url.Parse(short.URL)
			require.NoError(t, err)
			shortResponse := request(http.MethodGet, shortURL.RequestURI(), "", nil, "")
			require.Equal(t, http.StatusSeeOther, shortResponse.Code, shortResponse.Body.String())
			shortCookies := shortResponse.Result().Cookies()
			require.Len(t, shortCookies, 1)
			assert.LessOrEqual(t, shortCookies[0].MaxAge, 30)
			_, err = service.ParseXCodeWebToken(shortCookies[0].Value)
			require.NoError(t, err)
			_, claimsPart, _ := strings.Cut(shortCookies[0].Value, ".")
			claimsPart, _, _ = strings.Cut(claimsPart, ".")
			claimsJSON, err := base64.RawURLEncoding.DecodeString(claimsPart)
			require.NoError(t, err)
			var shortClaims struct {
				ExpiresAt int64 `json:"exp"`
			}
			require.NoError(t, common.Unmarshal(claimsJSON, &shortClaims))
			assert.LessOrEqual(t, shortClaims.ExpiresAt, shortExpiry)
			require.NoError(t, db.Model(&model.UserSession{}).Where("sid = ?", source.SID).Update("expires_at", rotated.ExpiresAt).Error)
			crossPurpose, err := service.CreateXCodeWebHandoff(identity)
			require.NoError(t, err)
			crossPurposeURL, err := url.Parse(crossPurpose.URL)
			require.NoError(t, err)
			require.NoError(t, db.Model(&model.AuthFlow{}).Where("consumed_at IS NULL").Update("purpose", model.AuthFlowPurposeXCodeRequest).Error)
			deniedPurpose := request(http.MethodGet, crossPurposeURL.RequestURI(), "", nil, "")
			assert.Equal(t, http.StatusBadRequest, deniedPurpose.Code)
			assert.Empty(t, deniedPurpose.Result().Cookies())
			expired, err := service.CreateXCodeWebHandoff(identity)
			require.NoError(t, err)
			expiredURL, err := url.Parse(expired.URL)
			require.NoError(t, err)
			require.NoError(t, db.Model(&model.AuthFlow{}).Where("consumed_at IS NULL").Update("expires_at", time.Now().Add(-time.Minute)).Error)
			expiredResponse := request(http.MethodGet, expiredURL.RequestURI(), "", nil, "")
			assert.Equal(t, http.StatusBadRequest, expiredResponse.Code)
			assert.Empty(t, expiredResponse.Result().Cookies())
			for _, sourceCase := range []struct {
				sid     string
				userID  int
				purpose string
			}{{strings.ToUpper(source.SID), user.Id, model.ClientPurposeXCode}, {source.SID + " ", user.Id, model.ClientPurposeXCode}, {source.SID, other.Id, model.ClientPurposeXCode}, {source.SID, user.Id, model.ClientPurposeDashboard}} {
				err := db.Transaction(func(tx *gorm.DB) error {
					_, _, err := model.ValidateXCodeSourceSessionWithTx(tx, sourceCase.userID, sourceCase.sid, 1, 0, sourceCase.purpose)
					return err
				})
				require.Error(t, err, "must not accept a different owner, opaque SID or source purpose")
			}
			require.NoError(t, db.Migrator().DropTable(&model.SubscriptionPlan{}))
			unavailable := request(http.MethodGet, "/api/xcode/v1/web/view", "", cookie, origin)
			assert.Equal(t, http.StatusServiceUnavailable, unavailable.Code, unavailable.Body.String())
			pending, err := service.CreateXCodeWebHandoff(identity)
			require.NoError(t, err)
			pendingURL, err := url.Parse(pending.URL)
			require.NoError(t, err)
			revoked, err := model.RevokeUserSession(user.Id, source.SID, "owned-web-regression")
			require.NoError(t, err)
			require.True(t, revoked)
			assert.Equal(t, http.StatusUnauthorized, request(http.MethodGet, "/api/xcode/v1/web/view", "", cookie, origin).Code)
			revokedConsume := request(http.MethodGet, pendingURL.RequestURI(), "", nil, "")
			assert.Equal(t, http.StatusUnauthorized, revokedConsume.Code)
			assert.Empty(t, revokedConsume.Result().Cookies())
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, 1000, user.Quota)
			require.NoError(t, db.First(&sub, sub.Id).Error)
			assert.Equal(t, int64(42), sub.AmountUsed)
		})
	}
}
