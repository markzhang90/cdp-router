package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/trace"
)

type Worker struct {
	tasks             *biz.TaskUsecase
	interval          time.Duration
	submittedInterval time.Duration
	submittedMaxAge   time.Duration
	limit             int
}

func New(tasks *biz.TaskUsecase) *Worker {
	return &Worker{
		tasks:             tasks,
		interval:          5 * time.Second,
		submittedInterval: 15 * time.Minute,
		submittedMaxAge:   24 * time.Hour,
		limit:             20,
	}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	submittedTicker := time.NewTicker(w.submittedInterval)
	defer submittedTicker.Stop()
	w.processSubmittedTasks(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-submittedTicker.C:
			w.processSubmittedTasks(ctx)
		case <-ticker.C:
			if err := w.tasks.ImportPendingResults(ctx, w.limit); err != nil {
				slog.Error("import pending results failed", "trace_id", trace.RequestID(ctx), "error", err)
			}
			if err := w.tasks.DeliverPendingCallbacks(ctx, w.limit); err != nil {
				slog.Error("deliver pending callbacks failed", "trace_id", trace.RequestID(ctx), "error", err)
			}
		}
	}
}

func (w *Worker) processSubmittedTasks(ctx context.Context) {
	if _, err := w.tasks.FailTimedOutSubmitted(ctx, w.submittedMaxAge, w.limit); err != nil {
		slog.Error("fail timed out submitted tasks failed", "trace_id", trace.RequestID(ctx), "error", err)
	}
	if _, err := w.tasks.RefreshSubmittedProgress(ctx, w.submittedInterval, w.limit); err != nil {
		slog.Error("refresh submitted task progress failed", "trace_id", trace.RequestID(ctx), "error", err)
	}
}
