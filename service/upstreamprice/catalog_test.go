package upstreamprice

import (
	"slices"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func alertCodeList(alerts []dto.UpstreamPriceAlert) []string {
	codes := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		codes = append(codes, alert.Code)
	}
	return codes
}

// TestCatalogStaleBoundaryIsSharedByEntriesAndAlerts pins the staleness time
// basis of the two halves of the catalog projection against an injected `now`:
// a run finished exactly at the threshold is not stale, one second older is,
// and the entry label and the source alert answer the question identically.
// The comparison projects the entries and raises the alerts in two separate
// steps, so the two halves must keep agreeing when they are given the same
// timestamp.
func TestCatalogStaleBoundaryIsSharedByEntriesAndAlerts(t *testing.T) {
	cases := []struct {
		name      string
		age       int64
		wantStale bool
	}{
		{"exactly at the threshold", DefaultManualStaleThresholdSeconds, false},
		{"one second past the threshold", DefaultManualStaleThresholdSeconds + 1, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			db := setupCompareTestDB(t)
			now := common.GetTimestamp()
			channelId := 1
			require.NoError(t, db.Create(&model.Channel{Id: channelId, Name: "cost-channel"}).Error)
			seedCatalogSnapshot(t, db, "boundary-cost-source", RoleSupplierCost, &channelId,
				"boundary-model", `tier("base", p * 1 + c * 2)`, FormulaKindTokenExprV1, "", now-testCase.age)

			sources, err := model.GetAllPriceSources()
			require.NoError(t, err)

			entries, err := currentPriceEntries(sources, now)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, testCase.wantStale, entries[0].Stale)

			alerts, err := EvaluateSourceAlerts(sources, now)
			require.NoError(t, err)
			assert.Equal(t, testCase.wantStale, slices.Contains(alertCodeList(alerts), AlertSourceStale))
		})
	}
}
