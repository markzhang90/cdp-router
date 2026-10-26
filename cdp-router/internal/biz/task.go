package biz

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type TaskStatus string

const (
	TaskStatusCreated         TaskStatus = "CREATED"
	TaskStatusSubmitted       TaskStatus = "SUBMITTED"
	TaskStatusProviderDone    TaskStatus = "PROVIDER_DONE"
	TaskStatusResultImported  TaskStatus = "RESULT_IMPORTED"
	TaskStatusCallbackPending TaskStatus = "CALLBACK_PENDING"
	TaskStatusDone            TaskStatus = "DONE"
	TaskStatusFailed          TaskStatus = "FAILED"
	TaskStatusReassigned      TaskStatus = "REASSIGNED"
)

type EventType string

const (
	EventTaskCreated           EventType = "TASK_CREATED"
	EventTaskDispatched        EventType = "TASK_DISPATCHED"
	EventTaskDispatchRuleHit   EventType = "TASK_DISPATCH_RULE_MATCHED"
	EventTaskDispatchQuota     EventType = "TASK_DISPATCH_QUOTA_CHECKED"
	EventProviderSubmitted     EventType = "PROVIDER_SUBMITTED"
	EventProviderSubmitFailed  EventType = "PROVIDER_SUBMIT_FAILED"
	EventUploadURLIssued       EventType = "UPLOAD_URL_ISSUED"
	EventProviderCallbackRecv  EventType = "PROVIDER_CALLBACK_RECEIVED"
	EventProviderCallbackProg  EventType = "PROVIDER_CALLBACK_PROGRESS"
	EventProviderCallback      EventType = "PROVIDER_CALLBACK"
	EventProviderCallbackFail  EventType = "PROVIDER_CALLBACK_FAILED"
	EventProviderStatusPolled  EventType = "PROVIDER_STATUS_POLLED"
	EventProviderFilesFetched  EventType = "PROVIDER_FILES_FETCHED"
	EventTaskTimedOut          EventType = "TASK_TIMED_OUT"
	EventResultImported        EventType = "RESULT_IMPORTED"
	EventResultFetchCompleted  EventType = "RESULT_FETCH_COMPLETED"
	EventBusinessCallbackOK    EventType = "BUSINESS_CALLBACK_OK"
	EventBusinessCallbackFail  EventType = "BUSINESS_CALLBACK_FAIL"
	EventTaskReassignRequested EventType = "TASK_REASSIGN_REQUESTED"
	EventTaskReassignSubmitted EventType = "TASK_REASSIGN_SUBMITTED"
	EventTaskReassigned        EventType = "TASK_REASSIGNED"
	EventTaskReassignFailed    EventType = "TASK_REASSIGN_FAILED"
	EventForceImportCallback   EventType = "FORCE_IMPORT_CALLBACK"
)

var (
	ErrTaskNotFound         = errors.New("task not found")
	ErrProviderNotFound     = errors.New("provider not found")
	ErrDispatchDisabled     = errors.New("任务创建失败，任务调度未启用")
	ErrProviderUnavailable  = errors.New("任务创建失败，供应商服务不可用")
	ErrPlatformUserNotFound = errors.New("platform user not found")
	ErrPlatformUserDisabled = errors.New("platform user disabled")
	ErrPlatformUserConflict = errors.New("platform user conflict")
	ErrInvalidTaskRequest   = errors.New("invalid task request")
	ErrTaskClosed           = errors.New("task is closed")
)

type PlatformUser struct {
	ID              int64
	PlatformCode    string
	Name            string
	APIKey          string
	Status          string
	CallbackMode    string
	CallbackURL     string
	CallbackConfig  json.RawMessage
	DispatchProfile DispatchProfile
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type DispatchStrategy string

const (
	DispatchStrategyRoundRobin DispatchStrategy = "ROUND_ROBIN"
	DispatchStrategyRuleBased  DispatchStrategy = "RULE_BASED"
)

type DispatchFallbackSelectionPolicy string

const (
	DispatchFallbackSelectionPolicyRoundRobin      DispatchFallbackSelectionPolicy = "ROUND_ROBIN"
	DispatchFallbackSelectionPolicyLowestUsageRate DispatchFallbackSelectionPolicy = "LOWEST_USAGE_RATE"
)

type DispatchProfile struct {
	Pull                    DispatchMode
	ID                      int64
	PlatformUserID          int64
	Code                    string
	Name                    string
	Strategy                DispatchStrategy
	Enabled                 bool
	Cursor                  int64
	LastProviderCode        string
	Config                  json.RawMessage
	ProviderCodes           []string
	FallbackSelectionPolicy DispatchFallbackSelectionPolicy
	Rules                   []DispatchRule
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type DispatchProfileInput struct {
	Pull                    *DispatchModeInput
	Code                    string
	Name                    string
	Strategy                DispatchStrategy
	Enabled                 bool
	ProviderCodes           []string
	FallbackSelectionPolicy DispatchFallbackSelectionPolicy
	Rules                   []DispatchRuleInput
}

type DispatchMode struct {
	Strategy                DispatchStrategy
	Enabled                 bool
	ProviderCodes           []string
	FallbackSelectionPolicy DispatchFallbackSelectionPolicy
	Rules                   []DispatchRule
	Cursor                  int64
	LastProviderCode        string
}

type DispatchModeInput struct {
	Strategy                DispatchStrategy
	Enabled                 bool
	ProviderCodes           []string
	FallbackSelectionPolicy DispatchFallbackSelectionPolicy
	Rules                   []DispatchRuleInput
}

type DispatchRule struct {
	Code          string
	Name          string
	SortOrder     int
	TaskNameRegex string
	Labels        []string
	ProviderCodes []string
	Enabled       bool
}

type DispatchRuleInput struct {
	Code          string
	Name          string
	SortOrder     int
	TaskNameRegex string
	Labels        []string
	ProviderCodes []string
	Enabled       bool
}

type Task struct {
	Device                  string
	ExecutionMode           string
	RiskCount               int
	InvalidAnswerCount      int
	RiskSummary             json.RawMessage
	RiskStatsUpdatedAt      *time.Time
	ID                      int64
	JobID                   string
	PlatformUserID          int64
	PlatformCode            string
	BatchNo                 string
	TaskName                string
	ProviderCode            string
	GatewayCode             string
	Source                  string
	ExternalBatchID         string
	ProviderExternalBatchID string
	ReassignedFromTaskID    int64
	CallbackBatchNo         string
	CallbackExternalBatchID string
	DispatchProfileID       int64
	DispatchProfileCode     string
	DispatchStrategy        DispatchStrategy
	BizDate                 time.Time
	QuotaDate               time.Time
	Status                  TaskStatus
	CallbackURL             string
	ProviderCallbackURL     string
	RawRequest              json.RawMessage
	ProviderRequest         json.RawMessage
	ProviderResponse        json.RawMessage
	ResultPayload           json.RawMessage // Full retained result; omitted by ordinary repository reads.
	ResultObjectKey         string
	ResultFileName          string
	ResultRowCount          int
	TotalCount              int
	SuccessCount            int
	FailedCount             int
	LastError               string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type TaskItem struct {
	ItemID       string          `json:"itemId"`
	PlatformCode string          `json:"platformCode"`
	Seq          int             `json:"seq"`
	Question     string          `json:"question"`
	Deepthink    bool            `json:"deepthink"`
	WebSearch    bool            `json:"webSearch"`
	RawItem      json.RawMessage `json:"-"`
}

type CreateTaskInput struct {
	ExecutionMode  string
	Device         string
	JobID          string
	PlatformUserID int64
	PlatformCode   string
	BatchNo        string
	TaskName       string
	GatewayCode    string
	Source         string
	BizDate        string
	CallbackURL    string
	Items          []TaskItem
	RawRequest     json.RawMessage
}

type SubmitTaskPayload struct {
	Device       string     `json:"device,omitempty"`
	BatchNo      string     `json:"batchNo"`
	ProviderCode string     `json:"providerCode"`
	BizDate      string     `json:"bizDate"`
	CallbackURL  string     `json:"callbackUrl"`
	Items        []TaskItem `json:"items"`
}

type SubmitResult struct {
	ExternalBatchID string
	AcceptedCount   int
	RawResponse     json.RawMessage
}

type ProviderQuestionStatus struct {
	QuestionID string `json:"questionId"`
	Status     int    `json:"status"`
}

type ProviderStatusResult struct {
	ExternalBatchID string                   `json:"externalBatchId"`
	Questions       []ProviderQuestionStatus `json:"questions"`
	TotalCount      int                      `json:"totalCount"`
	SuccessCount    int                      `json:"successCount"`
	FailedCount     int                      `json:"failedCount"`
	RawResponse     json.RawMessage          `json:"-"`
}

type ProviderFilesResult struct {
	ExternalBatchID string          `json:"externalBatchId"`
	FileURLs        []string        `json:"fileUrls"`
	RawResponse     json.RawMessage `json:"-"`
}

type ProviderCompletionFilesResult struct {
	Handled bool
	Result  ProviderFilesResult
}

type SyncProviderResultOutcome struct {
	Task         *Task
	ProviderDone bool
	FileCount    int
	RowCount     int
	Msg          string
}

type UploadURL struct {
	Bucket    string
	Key       string
	UploadURL string
	Method    string
	ExpiresAt time.Time
	Headers   map[string]string
}

type TemporaryDownloadFile struct {
	Key         string
	DownloadURL string
	ExpiresAt   time.Time
}

type ProviderCallbackInput struct {
	TaskID          int64
	ProviderCode    string
	BatchNo         string
	ExternalBatchID string
	Status          string
	TotalCount      int
	SuccessCount    int
	FailedCount     int
	FinishedAt      time.Time
	RawRequest      json.RawMessage
}

type CallbackDelivery struct {
	ExternalBatchID string               `json:"externalBatchId"`
	BatchNo         string               `json:"batchNo"`
	Status          TaskStatus           `json:"status"`
	ResultURL       string               `json:"resultUrl"`
	Counts          CallbackCounts       `json:"counts"`
	Items           []CallbackResultItem `json:"items,omitempty"`
	ResultFiles     []CallbackResultFile `json:"-"`
	ResultItems     []TaskItemResultView `json:"-"`
}

type CallbackResultItem struct {
	ProductCards             []ProductCard   `json:"productCards,omitempty"`
	ItemID                   string          `json:"itemId"`
	PlatformCode             string          `json:"platformCode,omitempty"`
	Seq                      int             `json:"seq,omitempty"`
	Question                 string          `json:"question,omitempty"`
	AnswerText               string          `json:"answerText,omitempty"`
	ThinkingProcess          string          `json:"thinkingProcess,omitempty"`
	GeneratedAt              string          `json:"generatedAt,omitempty"`
	GeneratedAtText          string          `json:"generatedAtText,omitempty"`
	RecommendedQuestionsJSON json.RawMessage `json:"recommendedQuestionsJson,omitempty"`
	ReferencesJSON           json.RawMessage `json:"referencesJson,omitempty"`
	ReferenceCount           int             `json:"referenceCount,omitempty"`
	ParseStatus              string          `json:"parseStatus,omitempty"`
}

type RetryTaskCallbackOutcome struct {
	Delivered bool
	Readiness CallbackReadiness
}

type ForceImportCallbackInput struct {
	TaskID         int64
	Reason         string
	OperatorUserID string
}

type ForceImportCallbackOutcome struct {
	Task                  *Task
	Delivered             bool
	Rejected              bool
	ProviderStatusFetched bool
	ProviderFilesFetched  bool
	FileCount             int
	ImportedFileCount     int
	RowCount              int
	TaskItemCount         int
	SuccessCount          int
	FailedCount           int
	UnfinishedCount       int
	CallbackMode          string
	YangshiUploaded       bool
	Partial               bool
	TaskStatusUnchanged   bool
}

type ReassignTaskInput struct {
	BatchNo        string
	ProviderCode   string
	Reason         string
	OperatorUserID string
}

type ReassignTaskRecordInput struct {
	ExecutionMode  string
	SourceBatchNo  string
	TargetBatchNo  string
	TargetJobID    string
	ProviderCode   string
	Reason         string
	OperatorUserID string
}

type ReassignTaskOutcome struct {
	Source        *Task
	Target        *Task
	AcceptedCount int
}

type CallbackCounts struct {
	Total   int `json:"total"`
	Success int `json:"success"`
	Failed  int `json:"failed"`
}

type CallbackResultFile struct {
	SourceType string `json:"sourceType,omitempty"`
	ObjectKey  string `json:"objectKey,omitempty"`
	FileURL    string `json:"fileUrl,omitempty"`
	FileName   string `json:"fileName,omitempty"`
}

type CallbackResult struct {
	TargetURL      string
	StatusCode     int
	ResponseBody   string
	RequestPayload json.RawMessage
	EventPayload   map[string]any
}

type ParsedResult struct {
	Payload         json.RawMessage
	Rows            []ResultRow
	ItemResults     []TaskItemResult
	RowCount        int
	ValidRowCount   int
	InvalidRowCount int
}

type ResultRow struct {
	SheetName  string          `json:"sheetName"`
	RowIndex   int             `json:"rowIndex"`
	ItemID     string          `json:"itemId,omitempty"`
	Seq        int             `json:"seq,omitempty"`
	RowData    json.RawMessage `json:"rowData"`
	RowHash    string          `json:"rowHash"`
	ParseError string          `json:"parseError,omitempty"`
}

type ResultFileInput struct {
	SourceType      string
	ObjectKey       string
	FileURL         string
	FileName        string
	ParseStatus     string
	Checksum        string
	ExternalBatchID string
	NotifyCallback  bool
}

// ProductCard preserves the provider's display values and order within an answer.
type ProductCard struct {
	ProductID     string `json:"productId,omitempty"`
	ProductName   string `json:"productName,omitempty"`
	Price         string `json:"price,omitempty"`
	ShopName      string `json:"shopName,omitempty"`
	ShopID        string `json:"shopId,omitempty"`
	SellingPoints string `json:"sellingPoints,omitempty"`
	ImageURL      string `json:"imageUrl,omitempty"`
	SalesVolume   string `json:"salesVolume,omitempty"`
}

type TaskItemResult struct {
	ProductCards []ProductCard `json:"productCards,omitempty"`
	AnswerValidation
	ItemID               string          `json:"itemId"`
	Seq                  int             `json:"seq"`
	PlatformCode         string          `json:"platformCode,omitempty"`
	Question             string          `json:"question,omitempty"`
	AnswerText           string          `json:"answerText,omitempty"`
	ThinkingProcess      string          `json:"thinkingProcess,omitempty"`
	GeneratedAt          time.Time       `json:"generatedAt,omitempty"`
	GeneratedAtText      string          `json:"generatedAtText,omitempty"`
	RecommendedQuestions json.RawMessage `json:"recommendedQuestions"`
	DeviceID             string          `json:"deviceId,omitempty"`
	RecordingURL         string          `json:"recordingUrl,omitempty"`
	ReferencesJSON       json.RawMessage `json:"references"`
	CleanedPayload       json.RawMessage `json:"cleanedPayload"`
	ParseStatus          string          `json:"parseStatus"`
	ParseError           string          `json:"parseError,omitempty"`
	AnswerSheetRowIndex  int             `json:"answerSheetRowIndex,omitempty"`
	ReferenceCount       int             `json:"referenceCount"`
	RowHash              string          `json:"rowHash,omitempty"`
}

type TaskItemResultView struct {
	ProductCards []ProductCard
	AnswerValidation
	ItemID               string
	PlatformCode         string
	Seq                  int
	Question             string
	ProviderStatus       int
	ResultStatus         string
	AnswerText           string
	ThinkingProcess      string
	GeneratedAt          time.Time
	GeneratedAtText      string
	RecommendedQuestions json.RawMessage
	DeviceID             string
	RecordingURL         string
	ReferencesJSON       json.RawMessage
	ReferenceCount       int
	ParseStatus          string
	ParseError           string
}

type CallbackReadiness struct {
	RiskCount      int
	RiskStatsReady bool
	Ready          bool
	ForceRequired  bool
	Reason         string
	TotalCount     int
	SuccessCount   int
	FailedCount    int
	ImportedCount  int
	TaskItemCount  int
	ProviderDone   bool
	ProviderTotal  int
	ProviderDoneAt time.Time
}

type TaskRepo interface {
	CreateTask(ctx context.Context, input CreateTaskInput, providers []ProviderSettings, providerCallbackBaseURL string) (*Task, error)
	CreateReassignedTask(ctx context.Context, input ReassignTaskRecordInput, providerCallbackBaseURL string) (*Task, error)
	MarkTaskReassigned(ctx context.Context, sourceTaskID, targetTaskID int64, payload any) error
	PrepareTaskForSubmit(ctx context.Context, taskID int64, providers []ProviderSettings, providerCallbackBaseURL string) (*Task, error)
	GetTask(ctx context.Context, id int64) (*Task, error)
	GetTaskByJobID(ctx context.Context, jobID string, platformUserID int64) (*Task, error)
	GetTaskByBatchNo(ctx context.Context, batchNo string) (*Task, error)
	ResolveProviderTask(ctx context.Context, providerCode, batchNo string) (*Task, error)
	CountTaskItems(ctx context.Context, taskID int64) (int, error)
	ListTaskItems(ctx context.Context, taskID int64) ([]TaskItem, error)
	ListTasksByStatus(ctx context.Context, status TaskStatus, limit int) ([]*Task, error)
	ListResultImportedTasksWithProviderCallback(ctx context.Context, limit int) ([]*Task, error)
	ListSubmittedTasksForProgress(ctx context.Context, limit int, submittedBefore time.Time) ([]*Task, error)
	ListTaskItemResults(ctx context.Context, taskID int64) ([]TaskItemResultView, error)
	GetCallbackReadiness(ctx context.Context, taskID int64) (CallbackReadiness, error)
	ListTaskResultFiles(ctx context.Context, taskID int64) ([]CallbackResultFile, error)
	ListUploadedResultFiles(ctx context.Context, taskID int64, includeImported bool) ([]ResultFileInput, error)
	ListPendingResultFiles(ctx context.Context, taskID int64) ([]ResultFileInput, error)
	CountSuccessfulResultFiles(ctx context.Context, taskID int64) (int, error)
	MarkSubmitted(ctx context.Context, taskID int64, providerRequest json.RawMessage, result SubmitResult) error
	MarkSubmitFailed(ctx context.Context, taskID int64, errMessage string) error
	FailSubmittedOlderThan(ctx context.Context, cutoff time.Time, errMessage string, limit int) (int, error)
	SaveUploadObjectKey(ctx context.Context, taskID int64, key string, fileName string) error
	MarkProviderCallback(ctx context.Context, input ProviderCallbackInput) (*Task, error)
	MarkProviderCallbackProgress(ctx context.Context, input ProviderCallbackInput) (*Task, error)
	MarkProviderStatus(ctx context.Context, taskID int64, result ProviderStatusResult) error
	UpsertPendingResultFiles(ctx context.Context, taskID int64, files []ResultFileInput) error
	MarkResultFileFailed(ctx context.Context, taskID int64, file ResultFileInput, errMessage string) error
	MarkResultImported(ctx context.Context, taskID int64, file ResultFileInput, result ParsedResult) error
	CompleteResultImport(ctx context.Context, taskID int64, notifyCallback bool) (*Task, bool, error)
	MarkCallbackDone(ctx context.Context, taskID int64, targetURL string, statusCode int, responseBody string, payload json.RawMessage) error
	MarkCallbackFailed(ctx context.Context, taskID int64, targetURL string, errMessage string, payload json.RawMessage, maxRetry int) (attemptCount int, stopped bool, err error)
	RecordForcedCallbackDone(ctx context.Context, taskID int64, targetURL string, statusCode int, responseBody string, payload json.RawMessage) error
	RecordForcedCallbackFailed(ctx context.Context, taskID int64, targetURL string, errMessage string, payload json.RawMessage) error
	AddEvent(ctx context.Context, taskID int64, eventType EventType, payload any) error
}

type PlatformUserRepo interface {
	GetByAPIKey(ctx context.Context, apiKey string) (PlatformUser, error)
}

type ProviderClient interface {
	SubmitTask(ctx context.Context, task *Task, payload SubmitTaskPayload) (SubmitResult, error)
	FetchStatus(ctx context.Context, externalBatchID string) (ProviderStatusResult, error)
	FetchResultFiles(ctx context.Context, externalBatchID string) (ProviderFilesResult, error)
	CompletionFiles(ctx context.Context, task *Task) (ProviderCompletionFilesResult, error)
	DownloadResultFile(ctx context.Context, fileURL string) ([]byte, error)
}

type ProviderRegistry interface {
	Get(code string) (ProviderClient, bool)
	Codes() []string
}

type ProviderSettings struct {
	InteractionMode    string
	SupportedDevices   []string
	DeviceChannels     map[string][]string
	ProviderCode       string
	BucketPathPrefix   string
	DailyQuestionQuota int
	Enabled            bool
}

type ProviderSettingsRepo interface {
	GetSettings(ctx context.Context, providerCode string) (ProviderSettings, bool)
}

type ObjectStore interface {
	SignedPutURL(ctx context.Context, key string, expires time.Duration) (string, time.Time, error)
	PutTemporaryObject(ctx context.Context, key string, contentType string, content []byte, retentionDays int) error
	SignedGetURL(ctx context.Context, key string, expires time.Duration) (string, time.Time, error)
	ObjectExists(ctx context.Context, key string) (bool, error)
	ReadObject(ctx context.Context, key string) ([]byte, error)
}

type ResultParser interface {
	Parse(ctx context.Context, fileName string, content []byte) (*ParsedResult, error)
}

type CallbackClient interface {
	Deliver(ctx context.Context, task *Task, payload CallbackDelivery) (CallbackResult, error)
	// ResolveMode returns the callback mode that Deliver would use for the
	// given task (e.g. "DEFAULT_HTTP" or "YANGSHI"). It must match the
	// routing logic inside Deliver exactly, so callers can report the mode
	// without duplicating the selection rules.
	ResolveMode(task *Task) string
}
