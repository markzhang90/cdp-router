package config

import (
	"testing"
	"time"
)

func TestNormalizeCallbackConfigSeparatesAPITimeoutAndUploadTimeout(t *testing.T) {
	cfg := NormalizeCallbackConfig(CallbackConfig{
		Timeout:       30 * time.Second,
		UploadTimeout: 5 * time.Minute,
		MaxRetry:      3,
	})
	if cfg.Timeout != 20*time.Second {
		t.Fatalf("callback API timeout = %s, want 20s", cfg.Timeout)
	}
	if cfg.UploadTimeout != 5*time.Minute {
		t.Fatalf("callback upload timeout = %s, want 5m", cfg.UploadTimeout)
	}
}

func TestNormalizeCallbackConfigDefaultsUploadTimeout(t *testing.T) {
	cfg := NormalizeCallbackConfig(CallbackConfig{MaxRetry: 3})
	if cfg.Timeout != 20*time.Second {
		t.Fatalf("callback API timeout = %s, want 20s", cfg.Timeout)
	}
	if cfg.UploadTimeout != 5*time.Minute {
		t.Fatalf("callback upload timeout = %s, want 5m", cfg.UploadTimeout)
	}
}
