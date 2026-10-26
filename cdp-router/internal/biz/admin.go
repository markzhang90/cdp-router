package biz

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrAdminUnauthorized = errors.New("admin unauthorized")
	ErrAdminForbidden    = errors.New("admin forbidden")
)

type AdminUser struct {
	UserID   string
	Username string
	Role     string
}

type AdminSession struct {
	SessionID string
	CSRFToken string
	User      AdminUser
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
}

type AdminTaskFilter struct {
	HasRisk      bool
	TaskName     string
	BatchNo      string
	Source       string
	PlatformCode string
	ProviderCode string
	ResultStatus string
	CreatedFrom  time.Time
	CreatedTo    time.Time
	Page         int
	PageSize     int
}

type AdminTaskList struct {
	Tasks    []AdminTaskSummary
	Total    int
	Page     int
	PageSize int
}

type AdminTaskItemFilter struct {
	Page     int
	PageSize int
}

type AdminPaginationFilter struct {
	Page     int
	PageSize int
}

type AdminTaskDetailFilter struct {
	Items  AdminTaskItemFilter
	Files  AdminPaginationFilter
	Events AdminPaginationFilter
}

type AdminTaskSummary struct {
	Device                 string
	ExecutionMode          string
	ShardCount             int
	CompletedShardCount    int
	PendingSubmissionCount int
	FailedSubmissionCount  int
	RiskCount              int
	InvalidAnswerCount     int
	RiskSummary            json.RawMessage
	RiskStatsUpdatedAt     *time.Time
	TaskID                 int64
	TaskName               string
	BatchNo                string
	Source                 string
	PlatformCode           string
	ExternalBatchID        string
	ProviderCode           string
	Status                 TaskStatus
	QuestionCount          int
	CompletedCount         int
	SuccessCount           int
	FailedCount            int
	ResultSuccessCount     int
	ResultFailedCount      int
	ResultMissingCount     int
	Progress               float64
	CreatedAt              time.Time
	UpdatedAt              time.Time
	ExecutionEndedAt       time.Time
	SubmitFailed           bool
	DispatchLabels         []string
	ReassignedFromTaskID   int64
	ReassignedToTaskID     int64
}

type AdminTaskDetail struct {
	Task       AdminTaskSummary
	Items      []AdminTaskItem
	ItemTotal  int
	ItemPage   int
	ItemSize   int
	Files      []AdminResultFile
	FileTotal  int
	FilePage   int
	FileSize   int
	Events     []AdminTaskEvent
	EventTotal int
	EventPage  int
	EventSize  int
	Callbacks  []AdminTaskCallback
}

type AdminTaskItemsExport struct {
	Task  AdminTaskSummary
	Items []AdminTaskItem
}

type AdminTaskItem struct {
	ProductCards []ProductCard
	AnswerValidation
	TaskItemID       int64
	ItemID           string
	PlatformCode     string
	Seq              int
	Question         string
	ProviderStatus   int
	ResultStatus     string
	AnswerText       string
	ThinkingProcess  string
	GeneratedAt      time.Time
	GeneratedAtText  string
	RecommendedJSON  json.RawMessage
	DeviceID         string
	RecordingURL     string
	ReferencesJSON   json.RawMessage
	ReferenceCount   int
	ParseStatus      string
	ParseError       string
	ResultImportedAt time.Time
}

type AdminResultFile struct {
	ID              int64
	SourceType      string
	ObjectKey       string
	FileURL         string
	FileName        string
	ParseStatus     string
	RowCount        int
	ValidRowCount   int
	InvalidRowCount int
	CreatedAt       time.Time
}

type AdminTaskEvent struct {
	ID        int64
	EventType string
	Payload   json.RawMessage
	CreatedAt time.Time
}

type AdminTaskCallback struct {
	ID             int64
	TargetURL      string
	RequestPayload json.RawMessage
	ResponseStatus int
	ResponseBody   string
	Status         string
	RetryCount     int
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type AdminAuditLog struct {
	UserID       string
	Username     string
	Role         string
	Action       string
	ResourceType string
	ResourceID   string
	RequestID    string
	Payload      json.RawMessage
}

type AdminPlatformUserFilter struct {
	Keyword  string
	Status   string
	Page     int
	PageSize int
}

type AdminPlatformUserList struct {
	Users    []PlatformUser
	Total    int
	Page     int
	PageSize int
}

type AdminPlatformUserInput struct {
	PlatformCode    string
	Name            string
	Status          string
	CallbackMode    string
	CallbackURL     string
	DispatchProfile DispatchProfileInput
}

type AdminPlatformUserMetricsFilter struct {
	CreatedFrom time.Time
	CreatedTo   time.Time
}

type AdminPlatformUserReconciliationFilter struct {
	CreatedFrom time.Time
	CreatedTo   time.Time
}

type AdminProviderMetricsFilter struct {
	CreatedFrom time.Time
	CreatedTo   time.Time
}

type AdminProviderMetrics struct {
	ProviderCode string
	CreatedFrom  time.Time
	CreatedTo    time.Time
	Summary      AdminProviderMetricsSummary
	Daily        []AdminProviderDailyMetric
}

type AdminProviderMetricsSummary struct {
	TaskCount          int
	QuestionCount      int
	SuccessAnswerCount int
	FailedAnswerCount  int
	AnswerSuccessRate  float64
}

type AdminPlatformUserMetrics struct {
	User            PlatformUser
	CreatedFrom     time.Time
	CreatedTo       time.Time
	TaskMetrics     AdminPlatformTaskMetrics
	QuestionMetrics AdminPlatformQuestionMetrics
	ResultMetrics   AdminPlatformResultMetrics
	CallbackMetrics AdminPlatformCallbackMetrics
	StatusBreakdown []AdminMetricBreakdown
	FailureReasons  []AdminMetricBreakdown
	RecentTasks     []AdminTaskSummary
	ProviderDaily   []AdminProviderDailyMetric
}

type AdminPlatformTaskMetrics struct {
	SubmittedTaskCount       int
	CompletedTaskCount       int
	ProcessingTaskCount      int
	FailedTaskCount          int
	TaskSuccessRate          float64
	AverageDurationSeconds   int
	LongestProcessingSeconds int
}

type AdminPlatformQuestionMetrics struct {
	SubmittedQuestionCount      int
	CompletedAnswerCount        int
	ProviderDoneQuestionCount   int
	FailedQuestionCount         int
	AnswerCompletionRate        float64
	AverageQuestionsPerTask     float64
	ActiveQuestionPlatformCount int
}

type AdminPlatformResultMetrics struct {
	ResultFileCount      int
	ParsedFileCount      int
	ParseFailedFileCount int
	RowCount             int
	ValidRowCount        int
	InvalidRowCount      int
	ValidRowRate         float64
}

type AdminPlatformCallbackMetrics struct {
	CallbackCount        int
	CallbackSuccessCount int
	CallbackFailedCount  int
	CallbackSuccessRate  float64
}

type AdminMetricBreakdown struct {
	Label string
	Value int
	Ratio float64
}

type AdminProviderDailyMetric struct {
	MetricDate         string
	ProviderCode       string
	TaskCount          int
	QuestionCount      int
	SuccessAnswerCount int
	FailedAnswerCount  int
}

type AdminPlatformUserReconciliationExport struct {
	User        PlatformUser
	CreatedFrom time.Time
	CreatedTo   time.Time
	Rows        []AdminPlatformUserReconciliationRow
}

type AdminPlatformUserReconciliationRow struct {
	MetricDate          string
	TaskName            string
	BatchNo             string
	ProviderCode        string
	ProviderCompanyName string
	QuestionCount       int
}

type AdminProviderQuotaUsage struct {
	ProviderCode string
	Used         int
}

type AdminRepo interface {
	ListTasks(ctx context.Context, filter AdminTaskFilter) (AdminTaskList, error)
	GetTaskDetail(ctx context.Context, id int64, filter AdminTaskDetailFilter) (AdminTaskDetail, error)
	ExportTaskItems(ctx context.Context, id int64) (AdminTaskItemsExport, error)
	SoftDeleteSubmitFailedTask(ctx context.Context, id int64, deletedBy string) (AdminTaskSummary, error)
	ListPlatformUsers(ctx context.Context, filter AdminPlatformUserFilter) (AdminPlatformUserList, error)
	GetPlatformUser(ctx context.Context, id int64) (PlatformUser, error)
	GetPlatformUserMetrics(ctx context.Context, id int64, filter AdminPlatformUserMetricsFilter) (AdminPlatformUserMetrics, error)
	ExportPlatformUserReconciliation(ctx context.Context, id int64, filter AdminPlatformUserReconciliationFilter) (AdminPlatformUserReconciliationExport, error)
	GetProviderMetrics(ctx context.Context, providerCode string, filter AdminProviderMetricsFilter) (AdminProviderMetrics, error)
	ListProviderQuotaUsage(ctx context.Context, providerCodes []string) (map[string]AdminProviderQuotaUsage, error)
	CreatePlatformUser(ctx context.Context, input AdminPlatformUserInput) (PlatformUser, error)
	UpdatePlatformUser(ctx context.Context, id int64, input AdminPlatformUserInput) (PlatformUser, error)
	AddAuditLog(ctx context.Context, log AdminAuditLog) error
}
