package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/callback"
	"github.com/GitMarkZhang/cdp-router/internal/cleaner"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/GitMarkZhang/cdp-router/internal/data"
	"github.com/GitMarkZhang/cdp-router/internal/httpapi"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/logger"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/ossutil"
	"github.com/GitMarkZhang/cdp-router/internal/provider"
	"github.com/GitMarkZhang/cdp-router/internal/service"
	"github.com/GitMarkZhang/cdp-router/internal/worker"
	"github.com/redis/go-redis/v9"
)

func main() {
	configPath := flag.String("config", "configs/cdp-router.conf", "config file path")
	migrate := flag.Bool("migrate", true, "run database migrations before serving")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		panic(err)
	}
	logger.Configure(cfg.Server.Debug)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *migrate {
		if err := data.EnsureDatabase(ctx, cfg.Postgres); err != nil {
			slog.Error("ensure database failed", "error", err)
			os.Exit(1)
		}
	}
	db, err := data.Open(ctx, cfg.Postgres)
	if err != nil {
		slog.Error("open database failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	slog.Info("postgres pool opened",
		"host", cfg.Postgres.Host,
		"database", cfg.Postgres.Database,
		"max_conns", cfg.Postgres.PoolSize,
		"min_conns", cfg.Postgres.MinPoolSize,
		"max_conn_lifetime", cfg.Postgres.MaxConnLifetime.String(),
		"max_conn_idle_time", cfg.Postgres.MaxConnIdleTime.String(),
		"health_check_period", cfg.Postgres.HealthCheckPeriod.String(),
	)
	if *migrate {
		if err := data.Migrate(ctx, db, "migrations"); err != nil {
			slog.Error("migrate database failed", "error", err)
			os.Exit(1)
		}
	}

	var redisClient *redis.Client
	if cfg.Redis.Enabled {
		var err error
		redisClient, err = data.OpenRedis(ctx, cfg.Redis)
		if err != nil {
			slog.Error("open redis failed", "addr", cfg.Redis.Addr, "error", err)
			os.Exit(1)
		}
		defer redisClient.Close()
		slog.Info("redis pool opened",
			"addr", cfg.Redis.Addr,
			"db", cfg.Redis.DB,
			"pool_size", cfg.Redis.PoolSize,
			"min_idle_conns", cfg.Redis.MinIdleConns,
			"max_idle_conns", cfg.Redis.MaxIdleConns,
		)
	}

	store, err := ossutil.New(cfg.OSS)
	if err != nil {
		slog.Error("open oss failed", "error", err)
		os.Exit(1)
	}

	repo := data.NewTaskRepo(db)
	platformUsers := data.NewPlatformUserRepo(db)
	if err := platformUsers.SyncFromConfig(ctx, cfg.PlatformUsers); err != nil {
		slog.Error("sync platform users failed", "error", err)
		os.Exit(1)
	}
	providers := provider.NewRegistry(cfg.Provider)
	parser := cleaner.NewExcelParser()
	callbackClient := callback.NewClient(cfg.Callback.Timeout, cfg.Callback.UploadTimeout, cfg.PlatformUsers, store, cfg.OSS.PublicHost, cfg.OSS.Host)
	usecase := biz.NewTaskUsecase(repo, providers, providers, store, parser, callbackClient, biz.TaskUsecaseOptions{
		PublicBaseURL:           cfg.Server.PublicBaseURL,
		OSSUploadDir:            cfg.OSS.UploadDir,
		OSSPublicHosts:          []string{cfg.OSS.PublicHost, cfg.OSS.Host},
		OSSExpire:               time.Duration(cfg.OSS.ExpireSeconds) * time.Second,
		TempObjectRetentionDays: cfg.OSS.TempObjectRetentionDays,
		CallbackMaxTry:          cfg.Callback.MaxRetry,
	})
	taskService := service.NewTaskService(usecase, providers, platformUsers, cfg.OSS.Bucket)
	adminService := service.NewAdminService(cfg.Admin, redisClient, data.NewAdminRepo(db), usecase, cfg.Provider)
	adminService.SetOSSPublicHost(firstNonEmpty(cfg.OSS.PublicHost, cfg.OSS.Host))
	server := httpapi.New(taskService, adminService)

	if cfg.Server.WorkerEnabled {
		go worker.New(usecase).Run(ctx)
	}

	httpServer := &http.Server{
		Addr:              cfg.Server.Addr(),
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		slog.Info("cdp-router started", "addr", cfg.Server.Addr())
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown failed", "error", err)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
