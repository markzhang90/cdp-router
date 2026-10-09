package biz

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateAnswerAllRulesAndBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, text, refs, status string
		referenceCount, risks    int
	}{
		{"nineteen", strings.Repeat("字", 19), `[{"url":"https://example.test"}]`, ValidationInvalid, 1, 1},
		{"twenty", strings.Repeat("字", 20), `[{"title":"来源"}]`, ValidationPass, 1, 0},
		{"punctuation", strings.Repeat("字", 19) + " ，。😀 \n", `[{"source":"来源"}]`, ValidationInvalid, 1, 1},
		{"zero references", strings.Repeat("字", 20), `[]`, ValidationRisk, 0, 1},
		{"negative count", strings.Repeat("字", 20), `[]`, ValidationRisk, -1, 1},
		{"empty reference fields", strings.Repeat("字", 20), `[{}]`, ValidationPass, 1, 0},
		{"blank reference fields", strings.Repeat("字", 20), `[{"title":"  "}]`, ValidationPass, 1, 0},
		{"multiple references", strings.Repeat("字", 20), `[{},{}]`, ValidationPass, 2, 0},
		{"count is authoritative", strings.Repeat("字", 20), `[{"title":"来源"}]`, ValidationRisk, 0, 1},
		{"all findings", "禁止 ABC", `[]`, ValidationInvalid, 0, 3},
		{"normalized", strings.Repeat("字", 20) + "ＡＢＣ", `[{"url":"https://example.test"}]`, ValidationInvalid, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := TaskItemResult{AnswerText: tc.text, ReferencesJSON: json.RawMessage(tc.refs), ReferenceCount: tc.referenceCount, ParseStatus: "SUCCESS"}
			got := ValidateAnswer(answer, ResultValidationRules{Version: "3", Keywords: []string{"禁止", "abc"}})
			if got.Status != tc.status || len(got.RiskItems) != tc.risks || got.RuleVersion != "3" || got.ValidatedAt.IsZero() {
				t.Fatalf("validation=%+v", got)
			}
			if answer.ParseStatus != "SUCCESS" || answer.AnswerText != tc.text {
				t.Fatal("validator mutated source")
			}
		})
	}
}

type ruleCacheRepo struct {
	rules     ResultValidationRules
	err       error
	fullReads int
}

func (r *ruleCacheRepo) GetValidationRuleVersion(context.Context) (string, error) {
	return r.rules.Version, r.err
}
func (r *ruleCacheRepo) GetValidationRules(context.Context) (ResultValidationRules, error) {
	r.fullReads++
	return r.rules, r.err
}
func (r *ruleCacheRepo) SaveValidationRules(context.Context, string, []string, AdminAuditLog) (ResultValidationRules, error) {
	panic("unused")
}
func TestValidationCacheRetainsLastSuccessfulSnapshot(t *testing.T) {
	repo := &ruleCacheRepo{rules: ResultValidationRules{Version: "1", Keywords: []string{"ＡＢＣ"}}, err: errors.New("offline")}
	cache := NewValidationRuleCache(repo, time.Minute)
	if cache.Refresh(context.Background()) == nil || cache.snapshot().Version != "default" {
		t.Fatal("initial failure must retain fallback")
	}
	repo.err = nil
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	usecase := &TaskUsecase{validationRules: cache}
	importCtx := usecase.validationContext(context.Background())
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.fullReads != 1 {
		t.Fatal("unchanged version reloaded keywords")
	}
	repo.rules = ResultValidationRules{Version: "2", Keywords: []string{"new"}, RiskCallbackAllowedProviderCodes: []string{"vendor_a"}}
	repo.err = errors.New("offline")
	if cache.Refresh(context.Background()) == nil || cache.snapshot().Version != "1" {
		t.Fatal("failed refresh discarded old rules")
	}
	repo.err = nil
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	parsed := &ParsedResult{ItemResults: []TaskItemResult{{AnswerText: strings.Repeat("x", 20) + "abc", ReferenceCount: 1, ReferencesJSON: json.RawMessage(`[{"title":"source"}]`)}}}
	usecase.validateParsedResult(importCtx, parsed)
	if parsed.ItemResults[0].RuleVersion != "1" || parsed.ItemResults[0].Status != ValidationInvalid {
		t.Fatal("running import did not keep original snapshot")
	}
	if AllowsRiskCallback(importCtx, "vendor_a") || !AllowsRiskCallback(usecase.validationContext(context.Background()), "vendor_a") {
		t.Fatal("policy snapshot changed during operation")
	}
	usecase.validateParsedResult(context.Background(), parsed)
	if parsed.ItemResults[0].RuleVersion != "2" || parsed.ItemResults[0].Status != ValidationPass {
		t.Fatal("next import did not use latest successful snapshot")
	}
}
func TestResultFileIdentityPreservesVersion(t *testing.T) {
	a := resultFileURLIdentity("https://example.test/result.xlsx?version=2&Signature=old&Expires=1")
	b := resultFileURLIdentity("https://example.test/result.xlsx?Expires=2&Signature=new&version=2")
	c := resultFileURLIdentity("https://example.test/result.xlsx?version=3")
	if a != b || a == c {
		t.Fatalf("identities %s %s %s", a, b, c)
	}
}

type rotatingRuleRepo struct {
	next   atomic.Int64
	loaded chan struct{}
}

func (r *rotatingRuleRepo) GetValidationRuleVersion(context.Context) (string, error) {
	return "next", nil
}
func (r *rotatingRuleRepo) GetValidationRules(context.Context) (ResultValidationRules, error) {
	version := strconv.FormatInt(r.next.Add(1), 10)
	select {
	case r.loaded <- struct{}{}:
	default:
	}
	return ResultValidationRules{Version: version, Keywords: []string{version}}, nil
}
func (r *rotatingRuleRepo) SaveValidationRules(context.Context, string, []string, AdminAuditLog) (ResultValidationRules, error) {
	panic("unused")
}
func TestValidationCacheConcurrentSnapshotsAndPeriodicRefresh(t *testing.T) {
	repo := &rotatingRuleRepo{loaded: make(chan struct{}, 1)}
	cache := NewValidationRuleCache(repo, 5*time.Millisecond)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-repo.loaded
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { cache.Run(ctx); close(done) }()
	select {
	case <-repo.loaded:
	case <-time.After(time.Second):
		t.Fatal("periodic refresh did not load")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				snapshot := cache.snapshot()
				if len(snapshot.Keywords) != 1 || snapshot.Keywords[0] != snapshot.Version {
					t.Error("version/keywords from different snapshots")
					return
				}
				if j%100 == 0 {
					if err := cache.Refresh(ctx); err != nil {
						t.Error(err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh loop did not stop")
	}
}

func TestRiskPolicyNeverSkipsAnswerValidation(t *testing.T) {
	rules := ResultValidationRules{Version: "7", Keywords: []string{"禁止"}, RiskCallbackAllowedProviderCodes: []string{"vendor_a"}}
	ctx := context.WithValue(context.Background(), validationSnapshotKey{}, rules)
	for _, provider := range []string{"vendor_a", "vendor_b", "new_vendor"} {
		parsed := &ParsedResult{ItemResults: []TaskItemResult{{AnswerText: "禁止", ReferenceCount: 0}}}
		(&TaskUsecase{}).validateParsedResult(ctx, parsed)
		got := parsed.ItemResults[0].AnswerValidation
		if got.Status != ValidationInvalid || len(got.RiskItems) != 3 || got.RuleVersion != "7" {
			t.Fatalf("provider %s: %+v", provider, got)
		}
		if AllowsRiskCallback(ctx, provider) != (provider == "vendor_a") {
			t.Fatalf("policy for %s", provider)
		}
	}
	if AllowsRiskCallback(context.Background(), "vendor_a") {
		t.Fatal("missing snapshot must intercept risks")
	}
}

func TestQuestionTextDoesNotParticipateInAnswerValidation(t *testing.T) {
	answer := TaskItemResult{Question: "禁止", AnswerText: strings.Repeat("有效回答", 5), ReferenceCount: 1}
	got := ValidateAnswer(answer, ResultValidationRules{Keywords: []string{"禁止"}})
	if got.Status != ValidationPass || len(got.RiskItems) != 0 {
		t.Fatalf("question was validated: %+v", got)
	}
}
