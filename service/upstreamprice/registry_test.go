package upstreamprice

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/dto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// endpointlessAdapter is an adapter without a pinned public URL: it does not
// implement EndpointReporter at all, which is what ListAdapters must tolerate
// now that Endpoint is an optional capability rather than an interface method.
type endpointlessAdapter struct {
	key    string
	roles  []PriceRole
	scopes []PriceScope
}

func (f *endpointlessAdapter) Key() string                 { return f.key }
func (f *endpointlessAdapter) Supports(SourceConfig) bool  { return true }
func (f *endpointlessAdapter) AllowedRoles() []PriceRole   { return f.roles }
func (f *endpointlessAdapter) AllowedScopes() []PriceScope { return f.scopes }
func (f *endpointlessAdapter) Fetch(context.Context, SourceConfig) ([]Observation, FetchMeta, error) {
	return nil, FetchMeta{}, nil
}

// TestListAdaptersProjectsRegistryContract pins the adapter listing the admin
// UI reads instead of shipping its own adapter table: the declared role and
// scope sets, key ordering, and the pinned endpoint — empty for an adapter
// that does not implement EndpointReporter. The listing deliberately says
// nothing about channels; that requirement follows from the selected role
// alone (TestValidatePriceSourceRoleChannelRules).
func TestListAdaptersProjectsRegistryContract(t *testing.T) {
	cases := []struct {
		name    string
		adapter Adapter
		want    dto.UpstreamPriceAdapterView
	}{
		{
			name: "supplier cost adapter reports its pinned endpoint",
			adapter: &fakeAdapter{
				key:      "list_adapters_test_cost",
				roles:    []PriceRole{RoleSupplierCost},
				scopes:   []PriceScope{ScopePublic},
				endpoint: "https://example.test/cost.json",
			},
			want: dto.UpstreamPriceAdapterView{
				Key:           "list_adapters_test_cost",
				AllowedRoles:  []string{string(RoleSupplierCost)},
				AllowedScopes: []string{string(ScopePublic)},
				Endpoint:      "https://example.test/cost.json",
			},
		},
		{
			name: "reference adapter reports its pinned endpoint",
			adapter: &fakeAdapter{
				key:      "list_adapters_test_reference",
				roles:    []PriceRole{RoleCuratedReference},
				scopes:   []PriceScope{ScopeUnknown},
				endpoint: "https://example.test/reference.json",
			},
			want: dto.UpstreamPriceAdapterView{
				Key:           "list_adapters_test_reference",
				AllowedRoles:  []string{string(RoleCuratedReference)},
				AllowedScopes: []string{string(ScopeUnknown)},
				Endpoint:      "https://example.test/reference.json",
			},
		},
		{
			name: "an adapter admitting several roles lists all of them",
			adapter: &fakeAdapter{
				key:    "list_adapters_test_mixed",
				roles:  []PriceRole{RoleSupplierCost, RoleVendorList},
				scopes: []PriceScope{ScopeAccount, ScopeContract},
			},
			want: dto.UpstreamPriceAdapterView{
				Key:           "list_adapters_test_mixed",
				AllowedRoles:  []string{string(RoleSupplierCost), string(RoleVendorList)},
				AllowedScopes: []string{string(ScopeAccount), string(ScopeContract)},
				Endpoint:      "",
			},
		},
		{
			name: "an adapter without EndpointReporter is listed with an empty endpoint",
			adapter: &endpointlessAdapter{
				key:    "list_adapters_test_no_endpoint",
				roles:  []PriceRole{RoleVendorList},
				scopes: []PriceScope{ScopeContract},
			},
			want: dto.UpstreamPriceAdapterView{
				Key:           "list_adapters_test_no_endpoint",
				AllowedRoles:  []string{string(RoleVendorList)},
				AllowedScopes: []string{string(ScopeContract)},
				Endpoint:      "",
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
			view, ok := byKey[testCase.adapter.Key()]
			require.True(t, ok, "registered adapter must be listed")
			assert.Equal(t, testCase.want, view)
		})
	}
}
