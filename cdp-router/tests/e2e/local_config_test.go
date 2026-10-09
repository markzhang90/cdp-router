package e2e

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/GitMarkZhang/cdp-router/internal/data"
	"github.com/jackc/pgx/v5"
)

// Each E2E gets a migrated real local database. OSS uses a local HTTP boundary
// backed by files; no provider, callback, or object request uses production config.
func loadLocalE2EConfig(t *testing.T) (*config.Config, error) {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "cdp-router.conf"))
	if err != nil {
		return nil, err
	}
	if cfg.Postgres.Host != "127.0.0.1" && cfg.Postgres.Host != "localhost" && cfg.Postgres.Host != "::1" {
		return nil, fmt.Errorf("E2E requires loopback PostgreSQL")
	}
	host, _, err := net.SplitHostPort(cfg.Redis.Addr)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		return nil, fmt.Errorf("E2E requires loopback Redis")
	}
	cfg.Postgres.Database = fmt.Sprintf("cdp_e2e_%d", time.Now().UnixNano())
	database := cfg.Postgres.Database
	adminPG := cfg.Postgres
	adminPG.Database = "postgres"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		db, err := data.Open(ctx, adminPG)
		if err != nil {
			t.Errorf("open cleanup database: %v", err)
			return
		}
		defer db.Close()
		if _, err := db.Exec(ctx, "drop database if exists "+pgx.Identifier{database}.Sanitize()+" with (force)"); err != nil {
			t.Errorf("drop E2E database: %v", err)
		}
	})
	dir := t.TempDir()
	objects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hash object paths so untrusted object names cannot escape the temporary dir.
		name := filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256([]byte(r.URL.Path))))
		switch r.Method {
		case http.MethodPut:
			file, err := os.CreateTemp(dir, "upload-")
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			n, err := io.Copy(file, r.Body)
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(file.Name(), name)
			}
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Header().Set("ETag", fmt.Sprintf("\"%x\"", n))
			w.WriteHeader(http.StatusOK)
		case http.MethodGet, http.MethodHead:
			if _, err := os.Stat(name); err != nil {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>")
				return
			}
			http.ServeFile(w, r, name)
		case http.MethodDelete:
			_ = os.Remove(name)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unsupported local OSS operation", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(objects.Close)
	cfg.OSS = config.OSSConfig{Endpoint: objects.URL, Host: objects.URL, PublicHost: objects.URL + "/e2e-bucket", Bucket: "e2e-bucket", AccessKeyID: "local-e2e-key", AccessKeySecret: "local-e2e-secret", UploadDir: "e2e", ExpireSeconds: 3600, TempObjectRetentionDays: 7}
	cfg.Provider = map[string]config.ProviderConfig{"vendor_a": {Code: "vendor_a", DisplayName: "vendor_a", Token: "local-provider-token", ResultMode: "callback_upload", Timeout: 20 * time.Second, QPSLimit: 100, ConcurrencyLimit: 20, Enabled: true}}
	for _, code := range []string{"mpzdata", "liuandata"} {
		p := cfg.Provider["vendor_a"]
		p.Code, p.DisplayName = code, code
		cfg.Provider[code] = p
	}
	cfg.PlatformUsers = map[string]config.PlatformUserConfig{"mainland": {PlatformCode: "mainland", Name: "Local E2E", APIKey: "local-e2e-api-key", Status: "ACTIVE", DefaultProviderCode: "vendor_a", CallbackMode: "DEFAULT_HTTP", CallbackConfig: "{}"}}
	cfg.Admin.Bootstrap.Username = "local-admin"
	cfg.Admin.Bootstrap.Password = "local-e2e-password"
	cfg.Admin.Bootstrap.PasswordHash = ""
	cfg.Admin.Bootstrap.Role = "admin"
	cfg.Admin.Session.Secure = false
	cfg.Admin.Session.RedisKeyPrefix = database + ":"
	cfg.Server.PublicBaseURL = "http://127.0.0.1:1"
	cfg.Callback.MaxRetry = 3
	// Existing tests replace every provider endpoint with httptest URLs before use.
	for code, p := range cfg.Provider {
		p.SubmitURL = strings.TrimRight(cfg.Server.PublicBaseURL, "/") + "/unused"
		p.StatusURL = p.SubmitURL
		p.FilesURL = p.SubmitURL
		cfg.Provider[code] = p
	}
	return cfg, nil
}
