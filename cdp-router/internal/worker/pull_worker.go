package worker

import (
	"context"
	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"log/slog"
	"time"
)

// PullResultWorker has its own bounded loop; retries never block Push polling.
type PullResultWorker struct {
	pull        *biz.PullUsecase
	concurrency int
}

func NewPull(pull *biz.PullUsecase, concurrency int) *PullResultWorker {
	if concurrency < 1 {
		concurrency = 1
	}
	return &PullResultWorker{pull, concurrency}
}
func (w *PullResultWorker) Run(ctx context.Context) {
	for i := 0; i < w.concurrency; i++ {
		go w.run(ctx)
	}
	<-ctx.Done()
}
func (w *PullResultWorker) run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i := 0; i < 20; i++ {
				processed, err := w.pull.ProcessOne(ctx)
				if err != nil {
					slog.ErrorContext(ctx, "pull result processing failed", "error", err)
					break
				}
				if !processed {
					break
				}
			}
		}
	}
}
