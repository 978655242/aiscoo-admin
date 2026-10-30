package common

import (
	"os"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskTimeoutEnvironment(t *testing.T) {
	previousTimeout := constant.TaskTimeoutMinutes
	t.Cleanup(func() { constant.TaskTimeoutMinutes = previousTimeout })
	for _, tc := range []struct {
		name  string
		value *string
		want  int
	}{
		{name: "unset", want: 1440},
		{name: "explicit 72 hours", value: GetPointer("4320"), want: 4320},
		{name: "disabled", value: GetPointer("0"), want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value == nil {
				previous, existed := os.LookupEnv("TASK_TIMEOUT_MINUTES")
				require.NoError(t, os.Unsetenv("TASK_TIMEOUT_MINUTES"))
				t.Cleanup(func() {
					if existed {
						require.NoError(t, os.Setenv("TASK_TIMEOUT_MINUTES", previous))
					}
				})
			} else {
				t.Setenv("TASK_TIMEOUT_MINUTES", *tc.value)
			}
			initConstantEnv()
			assert.Equal(t, tc.want, constant.TaskTimeoutMinutes)
		})
	}
}

func TestInitUserSessionSettingsUsesPositiveFallbacksAndClampsWindow(t *testing.T) {
	previousActiveLimit := UserSessionActiveLimit
	previousIssuanceLimit := UserSessionIssuanceLimit
	previousIssuanceWindow := UserSessionIssuanceWindowSeconds
	previousRevokedRetention := UserSessionRevokedRetentionDays
	previousAlertThreshold := UserSessionHourlyAlertThreshold
	t.Cleanup(func() {
		UserSessionActiveLimit = previousActiveLimit
		UserSessionIssuanceLimit = previousIssuanceLimit
		UserSessionIssuanceWindowSeconds = previousIssuanceWindow
		UserSessionRevokedRetentionDays = previousRevokedRetention
		UserSessionHourlyAlertThreshold = previousAlertThreshold
	})

	t.Setenv("USER_SESSION_ACTIVE_LIMIT", "0")
	t.Setenv("USER_SESSION_ISSUANCE_LIMIT", "-2")
	t.Setenv("USER_SESSION_ISSUANCE_WINDOW_SECONDS", "invalid")
	t.Setenv("USER_SESSION_REVOKED_RETENTION_DAYS", "0")
	t.Setenv("USER_SESSION_HOURLY_ALERT_THRESHOLD", "-1")
	initUserSessionSettings()

	assert.Equal(t, DefaultUserSessionActiveLimit, UserSessionActiveLimit)
	assert.Equal(t, DefaultUserSessionIssuanceLimit, UserSessionIssuanceLimit)
	assert.Equal(t, int64(DefaultUserSessionIssuanceWindowSeconds), UserSessionIssuanceWindowSeconds)
	assert.Equal(t, DefaultUserSessionRevokedRetentionDays, UserSessionRevokedRetentionDays)
	assert.Equal(t, DefaultUserSessionHourlyAlertThreshold, UserSessionHourlyAlertThreshold)

	t.Setenv("USER_SESSION_ACTIVE_LIMIT", "12")
	t.Setenv("USER_SESSION_ISSUANCE_LIMIT", "34")
	t.Setenv("USER_SESSION_ISSUANCE_WINDOW_SECONDS", "172800")
	t.Setenv("USER_SESSION_REVOKED_RETENTION_DAYS", "1")
	t.Setenv("USER_SESSION_HOURLY_ALERT_THRESHOLD", "56")
	initUserSessionSettings()

	assert.Equal(t, 12, UserSessionActiveLimit)
	assert.Equal(t, 34, UserSessionIssuanceLimit)
	assert.Equal(t, int64(24*60*60), UserSessionIssuanceWindowSeconds)
	assert.Equal(t, 1, UserSessionRevokedRetentionDays)
	assert.Equal(t, 56, UserSessionHourlyAlertThreshold)

	t.Setenv("USER_SESSION_ISSUANCE_WINDOW_SECONDS", "43200")
	initUserSessionSettings()
	assert.Equal(t, int64(12*60*60), UserSessionIssuanceWindowSeconds, "a window below retention remains unchanged")

	t.Setenv("USER_SESSION_ISSUANCE_WINDOW_SECONDS", "86400")
	initUserSessionSettings()
	assert.Equal(t, int64(24*60*60), UserSessionIssuanceWindowSeconds, "a window equal to retention remains unchanged")

	t.Setenv("USER_SESSION_REVOKED_RETENTION_DAYS", "9223372036854775807")
	initUserSessionSettings()
	assert.Equal(t, DefaultUserSessionRevokedRetentionDays, UserSessionRevokedRetentionDays)
}
