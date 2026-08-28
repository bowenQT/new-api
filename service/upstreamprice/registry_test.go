package upstreamprice

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListAdaptersProjectsRegistryContract pins the adapter listing the admin
// UI reads instead of shipping its own adapter table: the declared role and
// scope sets, the pinned endpoint, and the channel requirement implied by the
// supplier_cost rule in ValidatePriceSourceForWrite.
func TestListAdaptersProjectsRegistryContract(t *testing.T) {
	cases := []struct {
		name    string
		adapter *fakeAdapter
		want    dto.UpstreamPriceAdapterView
	}{
		{
			name: "supplier cost adapter requires a channel",
			adapter: &fakeAdapter{
				key:      "list_adapters_test_cost",
				roles:    []PriceRole{RoleSupplierCost},
				scopes:   []PriceScope{ScopePublic},
				endpoint: "https://example.test/cost.json",
			},
			want: dto.UpstreamPriceAdapterView{
				Key:             "list_adapters_test_cost",
				AllowedRoles:    []string{string(RoleSupplierCost)},
				AllowedScopes:   []string{string(ScopePublic)},
				RequiresChannel: true,
				Endpoint:        "https://example.test/cost.json",
			},
		},
		{
			name: "reference adapter must not reference a channel",
			adapter: &fakeAdapter{
				key:      "list_adapters_test_reference",
				roles:    []PriceRole{RoleCuratedReference},
				scopes:   []PriceScope{ScopeUnknown},
				endpoint: "https://example.test/reference.json",
			},
			want: dto.UpstreamPriceAdapterView{
				Key:             "list_adapters_test_reference",
				AllowedRoles:    []string{string(RoleCuratedReference)},
				AllowedScopes:   []string{string(ScopeUnknown)},
				RequiresChannel: false,
				Endpoint:        "https://example.test/reference.json",
			},
		},
		{
			name: "an adapter allowing more than supplier_cost cannot demand a channel",
			adapter: &fakeAdapter{
				key:    "list_adapters_test_mixed",
				roles:  []PriceRole{RoleSupplierCost, RoleVendorList},
				scopes: []PriceScope{ScopeAccount, ScopeContract},
			},
			want: dto.UpstreamPriceAdapterView{
				Key:             "list_adapters_test_mixed",
				AllowedRoles:    []string{string(RoleSupplierCost), string(RoleVendorList)},
				AllowedScopes:   []string{string(ScopeAccount), string(ScopeContract)},
				RequiresChannel: false,
				Endpoint:        "",
			},
		},
	}

	for _, testCase := range cases {
		require.NoError(t, RegisterAdapter(testCase.adapter))
	}

	views := ListAdapters()
	byKey := make(map[string]dto.UpstreamPriceAdapterView, len(views))
	previousKey := ""
	for _, view := range views {
		assert.Greater(t, view.Key, previousKey, "adapters are listed in key order")
		previousKey = view.Key
		byKey[view.Key] = view
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			view, ok := byKey[testCase.adapter.key]
			require.True(t, ok, "registered adapter must be listed")
			assert.Equal(t, testCase.want, view)
		})
	}
}
