package config

import (
	"os"
	"path/filepath"
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

func TestValidationRefreshConfig(t *testing.T) {
	for _, tc := range []struct {
		name, section string
		want          time.Duration
		invalid       bool
	}{
		{"default", "", time.Minute, false},
		{"configured", "[result_validation]\nkeyword_refresh_interval_seconds=12\n", 12 * time.Second, false},
		{"zero", "[result_validation]\nkeyword_refresh_interval_seconds=0\n", 0, true},
		{"unknown", "[result_validation]\nkeyword_refresh_interval_second=12\n", 0, true},
		{"overflow", "[result_validation]\nkeyword_refresh_interval_seconds=9223372036854775807\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.conf")
			if err := os.WriteFile(path, []byte(`[postgres]
user=local
database=local
[server]
public_base_url=http://127.0.0.1:5100
[oss]
access_key_id=local
access_key_secret=local
bucket=local
endpoint=http://127.0.0.1:9000
[platform_users.local]
api_key=local
[admin.bootstrap]
username=local
password=local
`+tc.section), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid refresh configuration accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ResultValidation.KeywordRefreshInterval != tc.want {
				t.Fatalf("interval=%s", cfg.ResultValidation.KeywordRefreshInterval)
			}
		})
	}
}
