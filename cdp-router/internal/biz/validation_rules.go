package biz

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type ValidationRuleRepo interface {
	GetValidationRules(context.Context) (ResultValidationRules, error)
	GetValidationRuleVersion(context.Context) (string, error)
	SaveValidationRules(context.Context, string, []string, AdminAuditLog) (ResultValidationRules, error)
}

// ValidationRuleCache publishes immutable snapshots. A failed load never
// replaces the last successful snapshot or blocks result imports.
type ValidationRuleCache struct {
	repo      ValidationRuleRepo
	interval  time.Duration
	current   atomic.Pointer[ResultValidationRules]
	refreshMu sync.Mutex
}

func NewValidationRuleCache(repo ValidationRuleRepo, interval time.Duration) *ValidationRuleCache {
	if interval <= 0 {
		interval = time.Minute
	}
	c := &ValidationRuleCache{repo: repo, interval: interval}
	c.current.Store(&ResultValidationRules{Version: "default", Keywords: []string{}})
	return c
}

// Snapshot is internal to the import usecase. Callers must never mutate it.
func (c *ValidationRuleCache) snapshot() ResultValidationRules {
	if c == nil {
		return ResultValidationRules{Version: "default"}
	}
	return *c.current.Load()
}

func (c *ValidationRuleCache) Refresh(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	version, err := c.repo.GetValidationRuleVersion(ctx)
	if err != nil {
		return err
	}
	if version == c.current.Load().Version {
		return nil
	}
	rules, err := c.repo.GetValidationRules(ctx)
	if err != nil {
		return err
	}
	rules.Keywords, err = NormalizeValidationKeywords(rules.Keywords)
	if err != nil {
		return err
	}
	c.current.Store(&rules)
	slog.InfoContext(ctx, "validation rules loaded", "version", rules.Version, "keyword_count", len(rules.Keywords), "risk_callback_allowed_provider_count", len(rules.RiskCallbackAllowedProviderCodes), "loaded_at", time.Now().UTC())
	return nil
}

// Run refreshes periodically; main explicitly performs the initial load before
// starting imports, including when the task worker is disabled.
func (c *ValidationRuleCache) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Refresh(ctx); err != nil {
				slog.WarnContext(ctx, "refresh validation rules failed; retaining previous version", "error", err)
			}
		}
	}
}

func (u *TaskUsecase) GetValidationRules(ctx context.Context) (ResultValidationRules, time.Duration, error) {
	if u.validationRules == nil {
		return ResultValidationRules{}, 0, ErrInvalidTaskRequest
	}
	rules, err := u.validationRules.repo.GetValidationRules(ctx)
	return rules, u.validationRules.interval, err
}

func (u *TaskUsecase) SaveValidationRules(ctx context.Context, expected string, keywords []string, audit AdminAuditLog) (ResultValidationRules, error) {
	if u.validationRules == nil {
		return ResultValidationRules{}, ErrInvalidTaskRequest
	}
	normalized, err := NormalizeValidationKeywords(keywords)
	if err != nil {
		return ResultValidationRules{}, err
	}
	return u.validationRules.repo.SaveValidationRules(ctx, expected, normalized, audit)
}

type validationSnapshotKey struct{}

func (u *TaskUsecase) validationContext(ctx context.Context) context.Context {
	if _, ok := ctx.Value(validationSnapshotKey{}).(ResultValidationRules); ok {
		return ctx
	}
	return context.WithValue(ctx, validationSnapshotKey{}, u.validationRules.snapshot())
}
func (u *TaskUsecase) validateParsedResult(ctx context.Context, parsed *ParsedResult) {
	rules, ok := ctx.Value(validationSnapshotKey{}).(ResultValidationRules)
	if !ok {
		rules = u.validationRules.snapshot()
	}
	for i := range parsed.ItemResults {
		parsed.ItemResults[i].AnswerValidation = ValidateAnswer(parsed.ItemResults[i], rules)
	}
}

func (u *TaskUsecase) ValidationRefreshInterval() time.Duration {
	if u.validationRules == nil {
		return time.Minute
	}
	return u.validationRules.interval
}

// Separate updates preserve keyword drafts and share optimistic concurrency control.
type ProviderValidationRuleRepo interface {
	SaveProviderValidationRule(context.Context, string, string, bool, AdminAuditLog) (ResultValidationRules, error)
}

func (u *TaskUsecase) SaveProviderValidationRule(ctx context.Context, expected, providerCode string, enabled bool, audit AdminAuditLog) (ResultValidationRules, error) {
	if u.validationRules == nil {
		return ResultValidationRules{}, ErrInvalidTaskRequest
	}
	if _, ok := u.providers.Get(providerCode); !ok {
		return ResultValidationRules{}, ErrProviderNotFound
	}
	repo, ok := u.validationRules.repo.(ProviderValidationRuleRepo)
	if !ok {
		return ResultValidationRules{}, ErrInvalidTaskRequest
	}
	return repo.SaveProviderValidationRule(ctx, expected, providerCode, enabled, audit)
}

// RiskCallbackProviderCodes reads the immutable operation snapshot. Without a
// successfully loaded policy all suppliers retain risk interception.
func RiskCallbackProviderCodes(ctx context.Context) []string {
	rules, _ := ctx.Value(validationSnapshotKey{}).(ResultValidationRules)
	return rules.RiskCallbackAllowedProviderCodes
}

func AllowsRiskCallback(ctx context.Context, providerCode string) bool {
	for _, code := range RiskCallbackProviderCodes(ctx) {
		if code == providerCode {
			return true
		}
	}
	return false
}
