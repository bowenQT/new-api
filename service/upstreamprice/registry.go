package upstreamprice

import (
	"fmt"
	"sync"
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
