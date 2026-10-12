package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestXCodePlanDisplayCorruptLegacyValues(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", `{"group_key":`} {
		t.Run(raw, func(t *testing.T) {
			var display XCodePlanDisplay
			require.NoError(t, display.Scan(raw))
			require.True(t, display.IsInvalid())
			encoded, err := common.Marshal(display)
			require.NoError(t, err)
			require.JSONEq(t, "null", string(encoded))
		})
	}
}
