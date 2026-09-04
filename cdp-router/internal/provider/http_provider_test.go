package provider

import (
	"testing"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/config"
)

func TestNewRegistryNormalizesProviderTimeoutToTwentySeconds(t *testing.T) {
	registry := NewRegistry(map[string]config.ProviderConfig{
		"vendor_a": {
			Code:             "vendor_a",
			Timeout:          30 * time.Second,
			QPSLimit:         10,
			ConcurrencyLimit: 20,
		},
		"vendor_b": {
			Code:             "vendor_b",
			QPSLimit:         10,
			ConcurrencyLimit: 20,
		},
		"vendor_c": {
			Code:             "vendor_c",
			Timeout:          5 * time.Second,
			QPSLimit:         10,
			ConcurrencyLimit: 20,
		},
	})

	tests := map[string]time.Duration{
		"vendor_a": 20 * time.Second,
		"vendor_b": 20 * time.Second,
		"vendor_c": 5 * time.Second,
	}
	for code, want := range tests {
		client, ok := registry.Get(code)
		if !ok {
			t.Fatalf("provider %s not registered", code)
		}
		httpProvider, ok := client.(*HTTPProvider)
		if !ok {
			t.Fatalf("provider %s has type %T, want *HTTPProvider", code, client)
		}
		if got := httpProvider.cfg.Timeout; got != want {
			t.Fatalf("provider %s timeout = %s, want %s", code, got, want)
		}
	}
}
