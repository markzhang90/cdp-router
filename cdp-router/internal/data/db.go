package data

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, cfg config.PostgresConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	poolCfg.MaxConns = int32(cfg.PoolSize)
	poolCfg.MinConns = int32(cfg.MinPoolSize)
	if cfg.MaxConnLifetime > 0 {
		poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.HealthCheckPeriod > 0 {
		poolCfg.HealthCheckPeriod = cfg.HealthCheckPeriod
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

func EnsureDatabase(ctx context.Context, cfg config.PostgresConfig) error {
	adminCfg := cfg
	adminCfg.Database = "postgres"
	pool, err := Open(ctx, adminCfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	var exists bool
	if err := pool.QueryRow(ctx, `select exists(select 1 from pg_database where datname=$1)`, cfg.Database).Scan(&exists); err != nil {
		return fmt.Errorf("check database exists: %w", err)
	}
	if exists {
		return nil
	}
	_, err = pool.Exec(ctx, `create database "`+strings.ReplaceAll(cfg.Database, `"`, `""`)+`"`)
	if err != nil {
		return fmt.Errorf("create database %s: %w", cfg.Database, err)
	}
	return nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(files)
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", file, err)
		}
		if _, err := pool.Exec(ctx, string(content)); err != nil {
			return fmt.Errorf("execute migration %s: %w", file, err)
		}
	}
	return nil
}
