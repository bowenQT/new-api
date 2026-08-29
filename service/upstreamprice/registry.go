package upstreamprice

import (
	"fmt"
	"sort"
	"sync"

	"github.com/QuantumNous/new-api/dto"
)

var (
	registryMu sync.RWMutex
	registry   = make(map[string]Adapter)
)

// RegisterAdapter adds an adapter to the process-wide registry. Only
// registered adapters can serve price sources; duplicate keys are refused so
// an adapter cannot be silently replaced (spec §12).
func RegisterAdapter(adapter Adapter) error {
	if adapter == nil || adapter.Key() == "" {
		return fmt.Errorf("cannot register adapter without a key")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[adapter.Key()]; exists {
		return fmt.Errorf("adapter %q already registered", adapter.Key())
	}
	registry[adapter.Key()] = adapter
	return nil
}

// MustRegisterAdapter panics on registration failure; intended for package
// init of adapter implementations.
func MustRegisterAdapter(adapter Adapter) {
	if err := RegisterAdapter(adapter); err != nil {
		panic(err)
	}
}

// ListAdapters projects every registered adapter's non-secret contract,
// sorted by key. It is the authority the admin UI reads instead of shipping
// its own adapter table, so a newly registered adapter never drifts out of
// sync with the form that configures it.
func ListAdapters() []dto.UpstreamPriceAdapterView {
	registryMu.RLock()
	adapters := make([]Adapter, 0, len(registry))
	for _, adapter := range registry {
		adapters = append(adapters, adapter)
	}
	registryMu.RUnlock()

	views := make([]dto.UpstreamPriceAdapterView, 0, len(adapters))
	for _, adapter := range adapters {
		roleNames := make([]string, 0)
		for _, role := range adapter.AllowedRoles() {
			roleNames = append(roleNames, string(role))
		}
		scopeNames := make([]string, 0)
		for _, scope := range adapter.AllowedScopes() {
			scopeNames = append(scopeNames, string(scope))
		}
		endpoint := ""
		if reporter, ok := adapter.(EndpointReporter); ok {
			endpoint = reporter.Endpoint()
		}
		views = append(views, dto.UpstreamPriceAdapterView{
			Key:           adapter.Key(),
			AllowedRoles:  roleNames,
			AllowedScopes: scopeNames,
			Endpoint:      endpoint,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Key < views[j].Key })
	return views
}

func GetAdapter(key string) (Adapter, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	adapter, ok := registry[key]
	return adapter, ok
}

// FindAdapterForSource resolves the registered adapter serving a source and
// verifies the adapter accepts that source configuration.
func FindAdapterForSource(source SourceConfig) (Adapter, error) {
	adapter, ok := GetAdapter(source.AdapterKey)
	if !ok {
		return nil, fmt.Errorf("no registered adapter for key %q", source.AdapterKey)
	}
	if !adapter.Supports(source) {
		return nil, fmt.Errorf("adapter %q does not support this source configuration", source.AdapterKey)
	}
	return adapter, nil
}
