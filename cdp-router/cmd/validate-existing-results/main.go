// validate-existing-results initializes UNKNOWN legacy answers once. The default
// is a read-only preview; operators explicitly opt into writes with -apply.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/GitMarkZhang/cdp-router/internal/data"
)

func main() {
	configPath := flag.String("config", "configs/cdp-router.conf", "configuration file")
	apply := flag.Bool("apply", false, "persist initial validation; default previews task IDs")
	limit := flag.Int("limit", 100, "maximum tasks in this invocation")
	taskID := flag.Int64("task-id", 0, "optional single task ID")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	fail := func(err error) { slog.Error("initial validation failed", "error", err); os.Exit(1) }
	if *limit <= 0 || *taskID < 0 {
		slog.Error("limit must be positive and task-id nonnegative")
		os.Exit(1)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fail(err)
	}
	db, err := data.Open(ctx, cfg.Postgres)
	if err != nil {
		fail(err)
	}
	defer db.Close()
	repo := data.NewTaskRepo(db)
	ids, err := repo.ListInitialValidationTasks(ctx, *limit, *taskID)
	if err != nil {
		fail(err)
	}
	slog.Info("initial validation candidates", "postgres_host", cfg.Postgres.Host, "database", cfg.Postgres.Database, "task_ids", ids, "apply", *apply)
	if !*apply {
		return
	}
	cache := biz.NewValidationRuleCache(repo, cfg.ResultValidation.KeywordRefreshInterval)
	if err = cache.Refresh(ctx); err != nil {
		fail(err)
	}
	usecase := biz.NewTaskUsecase(repo, nil, nil, nil, nil, nil, biz.TaskUsecaseOptions{ValidationRules: cache})
	for _, id := range ids {
		if err = usecase.InitializeTaskValidation(ctx, id); err != nil {
			fail(err)
		}
		slog.Info("initial validation completed", "task_id", id)
	}
}
