package config

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"gopkg.in/ini.v1"
)

const (
	ProviderRequestTimeout = 20 * time.Second
	CallbackAPITimeout     = 20 * time.Second
	CallbackUploadTimeout  = 5 * time.Minute
)

type Config struct {
	ResultValidation ResultValidationConfig
	Server           ServerConfig
	Postgres         PostgresConfig
	Redis            RedisConfig
	OSS              OSSConfig
	Provider         map[string]ProviderConfig
	PlatformUsers    map[string]PlatformUserConfig
	Callback         CallbackConfig
	Admin            AdminConfig
}

type ResultValidationConfig struct {
	KeywordRefreshInterval time.Duration
}

type ServerConfig struct {
	Host          string
	Port          int
	Debug         bool
	PublicBaseURL string
	WorkerEnabled bool
}

func (c ServerConfig) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

type PostgresConfig struct {
	Host              string
	Port              int
	User              string
	Password          string
	Database          string
	PoolSize          int
	MinPoolSize       int
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
	SSLMode           string
}

func (c PostgresConfig) DSN() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Host, c.Port),
		Path:   c.Database,
	}
	q := u.Query()
	q.Set("sslmode", c.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

func (c PostgresConfig) AdminDSN() string {
	cp := c
	cp.Database = "postgres"
	return cp.DSN()
}

type RedisConfig struct {
	Enabled      bool
	Addr         string
	Password     string
	DB           int
	PoolSize     int
	MinIdleConns int
	MaxIdleConns int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	DialTimeout  time.Duration
}

type OSSConfig struct {
	AccessKeyID             string
	AccessKeySecret         string
	STSRoleARN              string
	Bucket                  string
	Region                  string
	Endpoint                string
	PublicHost              string
	Host                    string
	UploadDir               string
	ExpireSeconds           int
	TempObjectRetentionDays int
}

type ProviderConfig struct {
	Code               string
	DisplayName        string
	CompanyName        string
	SettlementStartAt  string
	BucketPathPrefix   string
	SubmitURL          string
	StatusURL          string
	FilesURL           string
	Token              string
	APIKey             string
	ResultMode         string
	Timeout            time.Duration
	QPSLimit           int
	ConcurrencyLimit   int
	DailyQuestionQuota int
	Enabled            bool
}

func NormalizeProviderConfig(cfg ProviderConfig) ProviderConfig {
	if cfg.Timeout <= 0 || cfg.Timeout > ProviderRequestTimeout {
		cfg.Timeout = ProviderRequestTimeout
	}
	return cfg
}

type PlatformUserConfig struct {
	PlatformCode        string
	Name                string
	APIKey              string
	Status              string
	DefaultProviderCode string
	CallbackMode        string
	CallbackURL         string
	CallbackConfig      string
}

type CallbackConfig struct {
	Timeout       time.Duration
	UploadTimeout time.Duration
	MaxRetry      int
}

func NormalizeCallbackConfig(cfg CallbackConfig) CallbackConfig {
	if cfg.Timeout <= 0 || cfg.Timeout > CallbackAPITimeout {
		cfg.Timeout = CallbackAPITimeout
	}
	if cfg.UploadTimeout <= 0 {
		cfg.UploadTimeout = CallbackUploadTimeout
	}
	return cfg
}

type AdminConfig struct {
	Bootstrap AdminBootstrapConfig
	Session   AdminSessionConfig
}

type AdminBootstrapConfig struct {
	Username     string
	Password     string
	PasswordHash string
	Role         string
}

type AdminSessionConfig struct {
	CookieName     string
	IdleTTL        time.Duration
	AbsoluteTTL    time.Duration
	Secure         bool
	SameSite       string
	CSRFHeader     string
	RedisKeyPrefix string
}

func Load(path string) (*Config, error) {
	f, err := ini.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	cfg := &Config{
		Provider:      map[string]ProviderConfig{},
		PlatformUsers: map[string]PlatformUserConfig{},
	}

	validation := f.Section("result_validation")
	for _, key := range validation.Keys() {
		if key.Name() != "keyword_refresh_interval_seconds" {
			return nil, fmt.Errorf("unknown result_validation field %s", key.Name())
		}
	}
	seconds := 60
	if validation.HasKey("keyword_refresh_interval_seconds") {
		seconds, err = validation.Key("keyword_refresh_interval_seconds").Int()
	}
	if err != nil || seconds <= 0 || int64(seconds) > int64((1<<63-1)/time.Second) {
		return nil, fmt.Errorf("result_validation.keyword_refresh_interval_seconds must be positive")
	}
	cfg.ResultValidation.KeywordRefreshInterval = time.Duration(seconds) * time.Second

	server := f.Section("server")
	cfg.Server = ServerConfig{
		Host:          server.Key("host").MustString("0.0.0.0"),
		Port:          server.Key("port").MustInt(8080),
		Debug:         server.Key("debug").MustBool(false),
		PublicBaseURL: server.Key("public_base_url").MustString("http://127.0.0.1:8080"),
		WorkerEnabled: server.Key("worker_enabled").MustBool(true),
	}

	pg := f.Section("postgres")
	cfg.Postgres = PostgresConfig{
		Host:              pg.Key("host").MustString("127.0.0.1"),
		Port:              pg.Key("port").MustInt(5432),
		User:              pg.Key("user").String(),
		Password:          pg.Key("password").String(),
		Database:          pg.Key("database").MustString("cdp-admin"),
		PoolSize:          pg.Key("pool_size").MustInt(10),
		MinPoolSize:       pg.Key("min_pool_size").MustInt(2),
		MaxConnLifetime:   time.Duration(pg.Key("max_conn_lifetime_seconds").MustInt(1800)) * time.Second,
		MaxConnIdleTime:   time.Duration(pg.Key("max_conn_idle_seconds").MustInt(300)) * time.Second,
		HealthCheckPeriod: time.Duration(pg.Key("health_check_period_seconds").MustInt(30)) * time.Second,
		SSLMode:           pg.Key("ssl_mode").MustString("disable"),
	}

	redis := f.Section("redis")
	cfg.Redis = RedisConfig{
		Enabled:      redis.Key("enabled").MustBool(false),
		Addr:         redis.Key("addr").MustString("127.0.0.1:6379"),
		Password:     redis.Key("password").String(),
		DB:           redis.Key("db").MustInt(0),
		PoolSize:     redis.Key("pool_size").MustInt(10),
		MinIdleConns: redis.Key("min_idle_conns").MustInt(2),
		MaxIdleConns: redis.Key("max_idle_conns").MustInt(10),
		ReadTimeout:  time.Duration(redis.Key("read_timeout_seconds").MustInt(3)) * time.Second,
		WriteTimeout: time.Duration(redis.Key("write_timeout_seconds").MustInt(3)) * time.Second,
		DialTimeout:  time.Duration(redis.Key("dial_timeout_seconds").MustInt(5)) * time.Second,
	}

	oss := f.Section("oss")
	cfg.OSS = OSSConfig{
		AccessKeyID:             oss.Key("access_key_id").String(),
		AccessKeySecret:         oss.Key("access_key_secret").String(),
		STSRoleARN:              oss.Key("sts_role_arn").String(),
		Bucket:                  oss.Key("bucket").String(),
		Region:                  oss.Key("region").String(),
		Endpoint:                oss.Key("endpoint").String(),
		PublicHost:              oss.Key("public_host").String(),
		Host:                    oss.Key("host").String(),
		UploadDir:               oss.Key("upload_dir").MustString("cdp-router/"),
		ExpireSeconds:           oss.Key("expire_seconds").MustInt(86400),
		TempObjectRetentionDays: oss.Key("temporary_object_retention_days").MustInt(7),
	}

	for _, section := range f.Sections() {
		name := section.Name()
		const prefix = "providers."
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		code := section.Key("code").MustString(name[len(prefix):])
		timeoutSeconds := section.Key("timeout_seconds").MustInt(int(ProviderRequestTimeout / time.Second))
		cfg.Provider[code] = NormalizeProviderConfig(ProviderConfig{
			Code:               code,
			DisplayName:        section.Key("display_name").MustString(code),
			CompanyName:        section.Key("company_name").String(),
			SettlementStartAt:  section.Key("settlement_start_at").String(),
			BucketPathPrefix:   section.Key("bucket_path_prefix").MustString("cdp-router/q2a/" + code),
			SubmitURL:          section.Key("submit_url").String(),
			StatusURL:          section.Key("status_url").String(),
			FilesURL:           section.Key("files_url").String(),
			Token:              section.Key("token").String(),
			APIKey:             section.Key("api_key").String(),
			ResultMode:         section.Key("result_mode").MustString("callback_upload"),
			Timeout:            time.Duration(timeoutSeconds) * time.Second,
			QPSLimit:           section.Key("qps_limit").MustInt(10),
			ConcurrencyLimit:   section.Key("concurrency_limit").MustInt(20),
			DailyQuestionQuota: section.Key("daily_question_quota").MustInt(0),
			Enabled:            section.Key("enabled").MustBool(true),
		})
	}

	for _, section := range f.Sections() {
		name := section.Name()
		const prefix = "platform_users."
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		code := section.Key("platform_code").MustString(name[len(prefix):])
		cfg.PlatformUsers[code] = PlatformUserConfig{
			PlatformCode:        code,
			Name:                section.Key("name").MustString(code),
			APIKey:              section.Key("api_key").String(),
			Status:              section.Key("status").MustString("ACTIVE"),
			DefaultProviderCode: section.Key("default_provider_code").String(),
			CallbackMode:        section.Key("callback_mode").MustString("DEFAULT_HTTP"),
			CallbackURL:         section.Key("callback_url").String(),
			CallbackConfig:      section.Key("callback_config").MustString("{}"),
		}
	}

	callback := f.Section("callbacks.http")
	cfg.Callback = NormalizeCallbackConfig(CallbackConfig{
		Timeout:       time.Duration(callback.Key("timeout_seconds").MustInt(int(CallbackAPITimeout/time.Second))) * time.Second,
		UploadTimeout: time.Duration(callback.Key("upload_timeout_seconds").MustInt(int(CallbackUploadTimeout/time.Second))) * time.Second,
		MaxRetry:      callback.Key("max_retry").MustInt(3),
	})

	adminBootstrap := f.Section("admin.bootstrap")
	adminSession := f.Section("admin.session")
	cfg.Admin = AdminConfig{
		Bootstrap: AdminBootstrapConfig{
			Username:     adminBootstrap.Key("username").MustString("admin"),
			Password:     adminBootstrap.Key("password").MustString("admin123"),
			PasswordHash: adminBootstrap.Key("password_hash").String(),
			Role:         adminBootstrap.Key("role").MustString("admin"),
		},
		Session: AdminSessionConfig{
			CookieName:     adminSession.Key("cookie_name").MustString("cdp_admin_session"),
			IdleTTL:        time.Duration(adminSession.Key("idle_ttl_seconds").MustInt(604800)) * time.Second,
			AbsoluteTTL:    time.Duration(adminSession.Key("absolute_ttl_seconds").MustInt(2592000)) * time.Second,
			Secure:         adminSession.Key("secure").MustBool(false),
			SameSite:       adminSession.Key("same_site").MustString("lax"),
			CSRFHeader:     adminSession.Key("csrf_header").MustString("X-CSRF-Token"),
			RedisKeyPrefix: adminSession.Key("redis_key_prefix").MustString("cdp-router:admin:session:"),
		},
	}

	return cfg, cfg.Validate()
}

func (c *Config) Validate() error {
	if c.Postgres.User == "" {
		return fmt.Errorf("postgres.user is required")
	}
	if c.Postgres.Database == "" {
		return fmt.Errorf("postgres.database is required")
	}
	if c.Postgres.PoolSize <= 0 || c.Postgres.MinPoolSize < 0 {
		return fmt.Errorf("postgres pool_size must be positive and min_pool_size cannot be negative")
	}
	if c.Postgres.MinPoolSize > c.Postgres.PoolSize {
		return fmt.Errorf("postgres min_pool_size cannot be greater than pool_size")
	}
	if c.Redis.Enabled {
		if c.Redis.Addr == "" {
			return fmt.Errorf("redis.addr is required when redis is enabled")
		}
		if c.Redis.PoolSize <= 0 || c.Redis.MinIdleConns < 0 || c.Redis.MaxIdleConns < 0 {
			return fmt.Errorf("redis pool_size must be positive and idle conns cannot be negative")
		}
		if c.Redis.MinIdleConns > c.Redis.PoolSize || c.Redis.MaxIdleConns > c.Redis.PoolSize {
			return fmt.Errorf("redis idle conns cannot be greater than pool_size")
		}
	}
	if c.Server.PublicBaseURL == "" {
		return fmt.Errorf("server.public_base_url is required")
	}
	if _, err := url.ParseRequestURI(c.Server.PublicBaseURL); err != nil {
		return fmt.Errorf("server.public_base_url is invalid: %w", err)
	}
	if c.OSS.AccessKeyID == "" || c.OSS.AccessKeySecret == "" || c.OSS.Bucket == "" {
		return fmt.Errorf("oss access_key_id/access_key_secret/bucket are required")
	}
	if c.OSS.Endpoint == "" && c.OSS.Host == "" && c.OSS.Region == "" {
		return fmt.Errorf("oss endpoint/host/region is required")
	}
	if c.OSS.ExpireSeconds <= 0 {
		return fmt.Errorf("oss.expire_seconds must be positive")
	}
	if c.OSS.TempObjectRetentionDays <= 0 {
		return fmt.Errorf("oss.temporary_object_retention_days must be positive")
	}
	for code, provider := range c.Provider {
		if !provider.Enabled {
			continue
		}
		if provider.SubmitURL == "" {
			return fmt.Errorf("providers.%s.submit_url is required", code)
		}
		if _, err := url.ParseRequestURI(provider.SubmitURL); err != nil {
			return fmt.Errorf("providers.%s.submit_url is invalid: %w", code, err)
		}
		if provider.Token == "" && provider.APIKey == "" {
			return fmt.Errorf("providers.%s token or api_key is required", code)
		}
		if provider.ResultMode == "" {
			return fmt.Errorf("providers.%s.result_mode is required", code)
		}
		if provider.StatusURL != "" {
			if _, err := url.ParseRequestURI(provider.StatusURL); err != nil {
				return fmt.Errorf("providers.%s.status_url is invalid: %w", code, err)
			}
		}
		if provider.FilesURL != "" {
			if _, err := url.ParseRequestURI(provider.FilesURL); err != nil {
				return fmt.Errorf("providers.%s.files_url is invalid: %w", code, err)
			}
		}
		if provider.QPSLimit <= 0 || provider.ConcurrencyLimit <= 0 {
			return fmt.Errorf("providers.%s qps_limit and concurrency_limit must be positive", code)
		}
	}
	if len(c.PlatformUsers) == 0 {
		return fmt.Errorf("at least one platform_user is required")
	}
	seenAPIKeys := map[string]string{}
	for code, user := range c.PlatformUsers {
		if user.PlatformCode == "" {
			return fmt.Errorf("platform_users.%s.platform_code is required", code)
		}
		if user.APIKey == "" {
			return fmt.Errorf("platform_users.%s.api_key is required", code)
		}
		if existing := seenAPIKeys[user.APIKey]; existing != "" {
			return fmt.Errorf("platform_users.%s.api_key duplicates platform_users.%s.api_key", code, existing)
		}
		seenAPIKeys[user.APIKey] = code
		if user.Status == "" {
			return fmt.Errorf("platform_users.%s.status is required", code)
		}
		if user.CallbackMode == "" {
			return fmt.Errorf("platform_users.%s.callback_mode is required", code)
		}
	}
	for code, user := range c.PlatformUsers {
		if user.DefaultProviderCode == "" {
			continue
		}
		if _, ok := c.Provider[user.DefaultProviderCode]; !ok {
			return fmt.Errorf("platform_users.%s.default_provider_code references unknown provider %s", code, user.DefaultProviderCode)
		}
	}
	if c.Callback.MaxRetry <= 0 {
		return fmt.Errorf("callbacks.http.max_retry must be positive")
	}
	if c.Callback.Timeout <= 0 || c.Callback.UploadTimeout <= 0 {
		return fmt.Errorf("callbacks.http timeout_seconds and upload_timeout_seconds must be positive")
	}
	if c.Admin.Bootstrap.Username == "" {
		return fmt.Errorf("admin.bootstrap.username is required")
	}
	if c.Admin.Bootstrap.Password == "" && c.Admin.Bootstrap.PasswordHash == "" {
		return fmt.Errorf("admin.bootstrap.password or password_hash is required")
	}
	if c.Admin.Bootstrap.Role == "" {
		return fmt.Errorf("admin.bootstrap.role is required")
	}
	if c.Admin.Session.CookieName == "" || c.Admin.Session.CSRFHeader == "" || c.Admin.Session.RedisKeyPrefix == "" {
		return fmt.Errorf("admin.session cookie_name/csrf_header/redis_key_prefix are required")
	}
	if c.Admin.Session.IdleTTL <= 0 || c.Admin.Session.AbsoluteTTL <= 0 {
		return fmt.Errorf("admin.session ttl values must be positive")
	}
	return nil
}

func (c ServerConfig) PortString() string {
	return strconv.Itoa(c.Port)
}
