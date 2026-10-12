package biz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrPullConflict = errors.New("requestId already used with different content")

type PullClaimInput struct {
	RequestID string `json:"requestId"`
	Device    string `json:"device"`
	Channel   string `json:"channel"`
	Limit     int    `json:"limit"`
}
type PullShard struct {
	ID      int64      `json:"shardId,string"`
	TaskID  int64      `json:"taskId,string"`
	BatchNo string     `json:"batchNo"`
	Device  string     `json:"device"`
	Channel string     `json:"channel"`
	Status  string     `json:"status"`
	Items   []TaskItem `json:"items"`
}
type PullAnswer struct {
	ItemID               string          `json:"itemId"`
	AnswerSeq            int             `json:"answerSeq"`
	ExecutionStatus      string          `json:"executionStatus"`
	ErrorCode            string          `json:"errorCode"`
	ErrorMessage         string          `json:"errorMessage"`
	AnswerText           string          `json:"answerText"`
	ThinkingProcess      string          `json:"thinkingProcess"`
	GeneratedAt          string          `json:"generatedAt"`
	RecommendedQuestions json.RawMessage `json:"recommendedQuestions"`
	References           json.RawMessage `json:"references"`
	DeviceID             string          `json:"deviceId"`
	RecordingURL         string          `json:"recordingUrl"`
}
type PullSubmissionInput struct {
	RequestID     string       `json:"requestId"`
	BatchNo       string       `json:"batchNo"`
	ShardID       int64        `json:"shardId,string"`
	SchemaVersion string       `json:"schemaVersion"`
	Finished      bool         `json:"finished"`
	Results       []PullAnswer `json:"results"`
}
type PullSubmission struct {
	ID         int64
	TaskID     int64
	ShardID    int64
	LeaseToken string
	Input      PullSubmissionInput
}
type PullProgress struct {
	TaskID             int64
	Device             string
	ExecutionMode      string
	Status             TaskStatus
	Total              int
	Completed          int
	Success            int
	Failed             int
	Unclaimed          int
	Shards             int
	CompletedShards    int
	PendingSubmissions int
	FailedSubmissions  int
}
type PullRepo interface {
	Claim(context.Context, string, PullClaimInput, string) (PullShard, error)
	Accept(context.Context, string, PullSubmissionInput, string) (int64, error)
	Lease(context.Context) (*PullSubmission, error)
	WithTaskLock(context.Context, int64, func(context.Context) error) error
	Active(context.Context, int64) error
	ApplyAnswer(context.Context, *PullSubmission, PullAnswer, TaskItemResult) error
	Finish(context.Context, *PullSubmission) error
	Retry(context.Context, *PullSubmission, error) error
	Reconcile(context.Context, int64) error
	Progress(context.Context, int64) (PullProgress, error)
	ReconcileReady(context.Context) error
}
type PullUsecase struct {
	repo  PullRepo
	rules *ValidationRuleCache
}

func NewPullUsecase(repo PullRepo, rules *ValidationRuleCache) *PullUsecase {
	return &PullUsecase{repo: repo, rules: rules}
}
func pullHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func validRequestID(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 128 }
func (u *PullUsecase) Claim(ctx context.Context, provider ProviderSettings, in PullClaimInput, max int) (PullShard, error) {
	if in.Device == "" {
		in.Device = "web"
	}
	if strings.TrimSpace(in.Channel) == "" || len(in.Channel) > 64 || !validRequestID(in.RequestID) || in.Limit <= 0 || in.Limit > max || !provider.Supports(in.Device, in.Channel) || provider.InteractionMode != "pull" || !provider.Enabled {
		return PullShard{}, ErrInvalidTaskRequest
	}
	return u.repo.Claim(ctx, provider.ProviderCode, in, pullHash(in))
}
func (u *PullUsecase) Accept(ctx context.Context, provider string, in PullSubmissionInput, max int) (int64, error) {
	if in.Results == nil {
		in.Results = []PullAnswer{}
	}
	if in.SchemaVersion == "" {
		in.SchemaVersion = "1"
	}
	if !validRequestID(in.RequestID) || in.ShardID <= 0 || in.BatchNo == "" || in.SchemaVersion != "1" || len(in.Results) > max || (!in.Finished && len(in.Results) == 0) {
		return 0, ErrInvalidTaskRequest
	}
	keys := map[string]bool{}
	for i := range in.Results {
		a := &in.Results[i]
		key := fmt.Sprintf("%s/%d", a.ItemID, a.AnswerSeq)
		if a.ItemID == "" || a.AnswerSeq <= 0 || keys[key] {
			return 0, ErrInvalidTaskRequest
		}
		keys[key] = true
		switch a.ExecutionStatus {
		case "SUCCESS":
			if strings.TrimSpace(a.AnswerText) == "" {
				return 0, ErrInvalidTaskRequest
			}
		case "FAILED":
			if strings.TrimSpace(a.ErrorCode) == "" && strings.TrimSpace(a.ErrorMessage) == "" {
				return 0, ErrInvalidTaskRequest
			}
		default:
			return 0, ErrInvalidTaskRequest
		}
		if a.GeneratedAt != "" {
			if _, err := time.Parse(time.RFC3339, a.GeneratedAt); err != nil {
				return 0, ErrInvalidTaskRequest
			}
		}
		for _, raw := range []*json.RawMessage{&a.References, &a.RecommendedQuestions} {
			if len(*raw) == 0 || string(*raw) == "null" {
				*raw = json.RawMessage(`[]`)
			}
			var values []json.RawMessage
			if json.Unmarshal(*raw, &values) != nil {
				return 0, ErrInvalidTaskRequest
			}
		}
	}
	return u.repo.Accept(ctx, provider, in, pullHash(in))
}
func (u *PullUsecase) ProcessOne(ctx context.Context) (bool, error) {
	rules := u.rules.snapshot()
	ctx = context.WithValue(ctx, validationSnapshotKey{}, rules)
	sub, err := u.repo.Lease(ctx)
	if err != nil {
		return false, err
	}
	if sub == nil {
		return false, u.repo.ReconcileReady(ctx)
	}
	err = u.repo.WithTaskLock(ctx, sub.TaskID, func(ctx context.Context) error {
		if err := u.repo.Active(ctx, sub.TaskID); err != nil {
			return err
		}
		for _, a := range sub.Input.Results {
			var refs []json.RawMessage
			_ = json.Unmarshal(a.References, &refs)
			result := TaskItemResult{ItemID: a.ItemID, Seq: a.AnswerSeq, AnswerText: a.AnswerText, ThinkingProcess: a.ThinkingProcess, GeneratedAtText: a.GeneratedAt, RecommendedQuestions: a.RecommendedQuestions, DeviceID: a.DeviceID, RecordingURL: a.RecordingURL, ReferencesJSON: a.References, ReferenceCount: len(refs), ParseStatus: "SUCCESS"}
			if a.GeneratedAt != "" {
				result.GeneratedAt, _ = time.Parse(time.RFC3339, a.GeneratedAt)
			}
			result.CleanedPayload, _ = json.Marshal(a)
			if a.ExecutionStatus == "SUCCESS" {
				result.AnswerValidation = ValidateAnswer(result, rules)
			} else {
				result.ParseStatus = "EXECUTION_FAILED"
				result.ParseError = strings.TrimSpace(a.ErrorCode + " " + a.ErrorMessage)
				result.AnswerValidation = AnswerValidation{Status: ValidationSkipped, RuleVersion: rules.Version, ValidatedAt: time.Now(), RiskItems: []AnswerRisk{}}
				if strings.TrimSpace(a.AnswerText) != "" {
					result.AnswerValidation = ValidateAnswer(result, rules)
				}
			}
			if err := u.repo.ApplyAnswer(ctx, sub, a, result); err != nil {
				return err
			}
		}
		return u.repo.Finish(ctx, sub)
	})
	if err != nil {
		if retryErr := u.repo.Retry(ctx, sub, err); retryErr != nil {
			return true, retryErr
		}
	}
	if errors.Is(err, ErrTaskClosed) || errors.Is(err, ErrTaskNotFound) {
		return true, nil
	}
	return true, err
}
func (u *PullUsecase) Progress(ctx context.Context, id int64) (PullProgress, error) {
	return u.repo.Progress(ctx, id)
}

// Pull readiness is independent of file callbacks. A completed task stays DONE
// when later submissions repair individual answers.
func PullCompletionStatus(current TaskStatus, complete, riskAllowed bool) TaskStatus {
	if !complete || current == TaskStatusDone || current == TaskStatusCallbackPending || current == TaskStatusFailed || current == TaskStatusReassigned {
		return current
	}
	if riskAllowed {
		return TaskStatusCallbackPending
	}
	return TaskStatusResultImported
}
