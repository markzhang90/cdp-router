package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	adminv1 "github.com/GitMarkZhang/cdp-router/api/admin/v1"
	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/timezone"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/trace"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/xuri/excelize/v2"
	"golang.org/x/crypto/bcrypt"
)

const adminSessionContextKey = "admin_session"

type AdminService struct {
	cfg           config.AdminConfig
	redis         *redis.Client
	repo          biz.AdminRepo
	tasks         *biz.TaskUsecase
	providers     map[string]config.ProviderConfig
	ossPublicHost string
}

func NewAdminService(cfg config.AdminConfig, redisClient *redis.Client, repo biz.AdminRepo, tasks *biz.TaskUsecase, providers ...map[string]config.ProviderConfig) *AdminService {
	providerConfigs := map[string]config.ProviderConfig{}
	if len(providers) > 0 {
		for code, provider := range providers[0] {
			providerConfigs[code] = provider
		}
	}
	return &AdminService{cfg: cfg, redis: redisClient, repo: repo, tasks: tasks, providers: providerConfigs}
}

func (s *AdminService) SetOSSPublicHost(publicHost string) {
	s.ossPublicHost = strings.TrimRight(strings.TrimSpace(publicHost), "/")
}

func (s *AdminService) Login(c *gin.Context) {
	var req adminv1.LoginRequest
	if _, err := bindProto(c, &req); err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	if s.redis == nil {
		errorJSON(c, http.StatusServiceUnavailable, errors.New("admin session redis unavailable"))
		return
	}
	if !s.verifyPassword(req.Username, req.Password) {
		slog.WarnContext(c.Request.Context(), "admin login failed",
			"trace_id", trace.RequestID(c.Request.Context()),
			"username", req.Username,
		)
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return
	}
	session, err := s.createSession(c.Request.Context())
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, err)
		return
	}
	s.setCookie(c, session.SessionID, session.ExpiresAt)
	slog.InfoContext(c.Request.Context(), "admin login succeeded",
		"trace_id", trace.RequestID(c.Request.Context()),
		"user_id", session.User.UserID,
		"username", session.User.Username,
		"role", session.User.Role,
	)
	protoJSON(c, http.StatusOK, &adminv1.LoginResponse{
		Status: 0,
		Data: &adminv1.LoginData{
			User:      adminUserProto(session.User),
			CsrfToken: session.CSRFToken,
			ExpiresAt: formatTime(session.ExpiresAt),
		},
	})
}

func (s *AdminService) Logout(c *gin.Context) {
	session, ok := adminSessionFromContext(c)
	if !ok {
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return
	}
	if s.redis != nil {
		_ = s.redis.Del(c.Request.Context(), s.sessionKey(session.SessionID)).Err()
	}
	s.clearCookie(c)
	protoJSON(c, http.StatusOK, &adminv1.LogoutResponse{
		Status: 0,
		Data:   &adminv1.EmptyData{Ok: true},
	})
}

func (s *AdminService) Me(c *gin.Context) {
	session, ok := adminSessionFromContext(c)
	if !ok {
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return
	}
	protoJSON(c, http.StatusOK, &adminv1.MeResponse{
		Status: 0,
		Data: &adminv1.MeData{
			User:      adminUserProto(session.User),
			CsrfToken: session.CSRFToken,
		},
	})
}

func (s *AdminService) ListTasks(c *gin.Context) {
	filter, err := parseTaskFilter(c)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	result, err := s.repo.ListTasks(c.Request.Context(), filter)
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, err)
		return
	}
	tasks := make([]*adminv1.AdminTaskSummary, 0, len(result.Tasks))
	for _, task := range result.Tasks {
		tasks = append(tasks, adminTaskSummaryProto(task))
	}
	protoJSON(c, http.StatusOK, &adminv1.ListTasksResponse{
		Status: 0,
		Data: &adminv1.TaskListData{
			Tasks:    tasks,
			Total:    int32(result.Total),
			Page:     int32(result.Page),
			PageSize: int32(result.PageSize),
		},
	})
}

func (s *AdminService) GetTaskDetail(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	filter := biz.AdminTaskDetailFilter{
		Items: biz.AdminTaskItemFilter{
			Page:     parsePositiveInt(c.Query("itemPage"), 1),
			PageSize: parsePositiveInt(c.Query("itemPageSize"), 20),
		},
		Files: biz.AdminPaginationFilter{
			Page:     parsePositiveInt(c.Query("filePage"), 1),
			PageSize: parsePositiveInt(c.Query("filePageSize"), 20),
		},
		Events: biz.AdminPaginationFilter{
			Page:     parsePositiveInt(c.Query("eventPage"), 1),
			PageSize: parsePositiveInt(c.Query("eventPageSize"), 20),
		},
	}
	detail, err := s.repo.GetTaskDetail(c.Request.Context(), id, filter)
	if err != nil {
		writeBizError(c, err)
		return
	}
	items := make([]*adminv1.AdminTaskItem, 0, len(detail.Items))
	for _, item := range detail.Items {
		items = append(items, adminTaskItemProto(item))
	}
	files := make([]*adminv1.ResultFile, 0, len(detail.Files))
	for _, file := range detail.Files {
		files = append(files, s.adminResultFileProto(file))
	}
	events := make([]*adminv1.TaskEvent, 0, len(detail.Events))
	for _, event := range detail.Events {
		events = append(events, adminTaskEventProto(event))
	}
	callbacks := make([]*adminv1.TaskCallback, 0, len(detail.Callbacks))
	for _, callback := range detail.Callbacks {
		callbacks = append(callbacks, adminTaskCallbackProto(callback))
	}
	protoJSON(c, http.StatusOK, &adminv1.GetTaskDetailResponse{
		Status: 0,
		Data: &adminv1.TaskDetailData{
			Task:          adminTaskSummaryProto(detail.Task),
			Items:         items,
			Files:         files,
			Events:        events,
			Callbacks:     callbacks,
			ItemTotal:     int32(detail.ItemTotal),
			ItemPage:      int32(detail.ItemPage),
			ItemPageSize:  int32(detail.ItemSize),
			FileTotal:     int32(detail.FileTotal),
			FilePage:      int32(detail.FilePage),
			FilePageSize:  int32(detail.FileSize),
			EventTotal:    int32(detail.EventTotal),
			EventPage:     int32(detail.EventPage),
			EventPageSize: int32(detail.EventSize),
		},
	})
}

func (s *AdminService) ExportTaskItems(c *gin.Context) {
	session, ok := adminSessionFromContext(c)
	if !ok {
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	export, err := s.repo.ExportTaskItems(c.Request.Context(), id)
	if err != nil {
		writeBizError(c, err)
		return
	}
	workbook, err := buildTaskItemsWorkbook(export)
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, err)
		return
	}
	filename := taskItemsExportFilename(export.Task)
	download, err := s.tasks.CreateTemporaryDownloadFile(c.Request.Context(), filename, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", workbook)
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"objectKey": download.Key,
		"fileName":  filename,
		"expiresAt": formatTime(download.ExpiresAt),
	})
	_ = s.repo.AddAuditLog(c.Request.Context(), biz.AdminAuditLog{
		UserID:       session.User.UserID,
		Username:     session.User.Username,
		Role:         session.User.Role,
		Action:       "EXPORT_TASK_ITEMS",
		ResourceType: "TASK",
		ResourceID:   fmt.Sprint(id),
		RequestID:    trace.RequestID(c.Request.Context()),
		Payload:      payload,
	})
	protoJSON(c, http.StatusOK, &adminv1.ExportTaskItemsResponse{
		Status: 0,
		Data: &adminv1.ExportTaskItemsData{
			DownloadUrl: download.DownloadURL,
			ObjectKey:   download.Key,
			ExpiresAt:   formatTime(download.ExpiresAt),
		},
	})
}

func (s *AdminService) CheckTaskResult(c *gin.Context) {
	session, ok := adminSessionFromContext(c)
	if !ok {
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	req := adminv1.CheckTaskResultRequest{
		Id:             id,
		Force:          true,
		NotifyCallback: true,
	}
	raw, err := bindOptionalProto(c, &req)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	applyCheckResultDefaults(raw, &req)
	outcome, err := s.tasks.SyncProviderResult(c.Request.Context(), id, req.Force, req.NotifyCallback)
	if err != nil {
		writeBizError(c, err)
		return
	}
	task := outcome.Task
	payload, _ := json.Marshal(map[string]any{
		"request":        json.RawMessage(defaultJSON(raw)),
		"force":          req.Force,
		"notifyCallback": req.NotifyCallback,
		"providerDone":   outcome.ProviderDone,
		"fileCount":      outcome.FileCount,
		"rowCount":       outcome.RowCount,
		"msg":            outcome.Msg,
	})
	_ = s.repo.AddAuditLog(c.Request.Context(), biz.AdminAuditLog{
		UserID:       session.User.UserID,
		Username:     session.User.Username,
		Role:         session.User.Role,
		Action:       "CHECK_TASK_RESULT",
		ResourceType: "TASK",
		ResourceID:   strconv.FormatInt(id, 10),
		RequestID:    trace.RequestID(c.Request.Context()),
		Payload:      payload,
	})
	protoJSON(c, http.StatusOK, &adminv1.CheckTaskResultResponse{
		Status:  0,
		Message: outcome.Msg,
		Data: &adminv1.CheckTaskResultData{
			TaskId:       task.ID,
			BatchNo:      task.BatchNo,
			TaskStatus:   string(task.Status),
			ProviderDone: outcome.ProviderDone,
			FileCount:    int32(outcome.FileCount),
			RowCount:     int32(outcome.RowCount),
			Msg:          outcome.Msg,
		},
	})
}

func (s *AdminService) RetryTaskCallback(c *gin.Context) {
	session, ok := adminSessionFromContext(c)
	if !ok {
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	req := adminv1.RetryTaskCallbackRequest{Id: id}
	raw, err := bindOptionalProto(c, &req)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	task, outcome, err := s.tasks.RetryTaskCallback(c.Request.Context(), id, req.Force)
	if err != nil {
		writeBizError(c, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"request":       json.RawMessage(defaultJSON(raw)),
		"force":         req.Force,
		"delivered":     outcome.Delivered,
		"ready":         outcome.Readiness.Ready,
		"forceRequired": outcome.Readiness.ForceRequired,
		"reason":        outcome.Readiness.Reason,
		"successCount":  outcome.Readiness.SuccessCount,
		"totalCount":    outcome.Readiness.TotalCount,
	})
	_ = s.repo.AddAuditLog(c.Request.Context(), biz.AdminAuditLog{
		UserID:       session.User.UserID,
		Username:     session.User.Username,
		Role:         session.User.Role,
		Action:       "RETRY_TASK_CALLBACK",
		ResourceType: "TASK",
		ResourceID:   strconv.FormatInt(id, 10),
		RequestID:    trace.RequestID(c.Request.Context()),
		Payload:      payload,
	})
	protoJSON(c, http.StatusOK, &adminv1.RetryTaskCallbackResponse{
		Status:  0,
		Message: retryCallbackMessage(outcome),
		Data: &adminv1.RetryTaskCallbackData{
			TaskId:        task.ID,
			BatchNo:       task.BatchNo,
			TaskStatus:    string(task.Status),
			Delivered:     outcome.Delivered,
			Ready:         outcome.Readiness.Ready,
			ForceRequired: outcome.Readiness.ForceRequired,
			Reason:        outcome.Readiness.Reason,
			TotalCount:    int32(outcome.Readiness.TotalCount),
			SuccessCount:  int32(outcome.Readiness.SuccessCount),
			FailedCount:   int32(outcome.Readiness.FailedCount),
			ImportedCount: int32(outcome.Readiness.ImportedCount),
			TaskItemCount: int32(outcome.Readiness.TaskItemCount),
		},
	})
}

func (s *AdminService) ForceImportCallback(c *gin.Context) {
	session, ok := adminSessionFromContext(c)
	if !ok {
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	req := adminv1.ForceImportCallbackRequest{Id: id}
	raw, err := bindOptionalProto(c, &req)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	outcome, err := s.tasks.ForceImportCallback(c.Request.Context(), biz.ForceImportCallbackInput{
		TaskID:         id,
		Reason:         req.Reason,
		OperatorUserID: session.User.UserID,
	})
	if err != nil {
		writeBizError(c, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"request":           json.RawMessage(defaultJSON(raw)),
		"reason":            req.Reason,
		"delivered":         outcome.Delivered,
		"rejected":          outcome.Rejected,
		"fileCount":         outcome.FileCount,
		"importedFileCount": outcome.ImportedFileCount,
		"rowCount":          outcome.RowCount,
		"taskItemCount":     outcome.TaskItemCount,
		"successCount":      outcome.SuccessCount,
		"failedCount":       outcome.FailedCount,
		"unfinishedCount":   outcome.UnfinishedCount,
		"callbackMode":      outcome.CallbackMode,
		"partial":           outcome.Partial,
	})
	_ = s.repo.AddAuditLog(c.Request.Context(), biz.AdminAuditLog{
		UserID:       session.User.UserID,
		Username:     session.User.Username,
		Role:         session.User.Role,
		Action:       "FORCE_IMPORT_CALLBACK",
		ResourceType: "TASK",
		ResourceID:   strconv.FormatInt(id, 10),
		RequestID:    trace.RequestID(c.Request.Context()),
		Payload:      payload,
	})
	task := outcome.Task
	message := "强制回调已执行"
	if outcome.Rejected {
		message = "无有效成功结果，已拒绝回调"
	} else if !outcome.Delivered {
		message = "强制回调失败"
	}
	protoJSON(c, http.StatusOK, &adminv1.ForceImportCallbackResponse{
		Status:  0,
		Message: message,
		Data: &adminv1.ForceImportCallbackData{
			TaskId:              task.ID,
			BatchNo:             task.BatchNo,
			TaskStatus:          string(task.Status),
			Delivered:           outcome.Delivered,
			Rejected:            outcome.Rejected,
			FileCount:           int32(outcome.FileCount),
			ImportedFileCount:   int32(outcome.ImportedFileCount),
			RowCount:            int32(outcome.RowCount),
			TaskItemCount:       int32(outcome.TaskItemCount),
			SuccessCount:        int32(outcome.SuccessCount),
			FailedCount:         int32(outcome.FailedCount),
			UnfinishedCount:     int32(outcome.UnfinishedCount),
			CallbackMode:        outcome.CallbackMode,
			Partial:             outcome.Partial,
			TaskStatusUnchanged: outcome.TaskStatusUnchanged,
			Reason:              req.Reason,
		},
	})
}

func (s *AdminService) ReassignTask(c *gin.Context) {
	session, ok := adminSessionFromContext(c)
	if !ok {
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return
	}
	var req adminv1.ReassignTaskRequest
	raw, err := bindProto(c, &req)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	outcome, err := s.tasks.ReassignTask(c.Request.Context(), biz.ReassignTaskInput{
		BatchNo:        req.BatchNo,
		ProviderCode:   req.ProviderCode,
		Reason:         req.Reason,
		OperatorUserID: session.User.UserID,
	})
	if err != nil {
		payload, _ := json.Marshal(map[string]any{
			"request": json.RawMessage(defaultJSON(raw)),
			"error":   err.Error(),
		})
		_ = s.repo.AddAuditLog(c.Request.Context(), biz.AdminAuditLog{
			UserID:       session.User.UserID,
			Username:     session.User.Username,
			Role:         session.User.Role,
			Action:       "REASSIGN_TASK_FAILED",
			ResourceType: "TASK",
			ResourceID:   req.BatchNo,
			RequestID:    trace.RequestID(c.Request.Context()),
			Payload:      payload,
		})
		writeBizError(c, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"request":          json.RawMessage(defaultJSON(raw)),
		"sourceTaskId":     outcome.Source.ID,
		"targetTaskId":     outcome.Target.ID,
		"originalBatchNo":  outcome.Target.CallbackBatchNo,
		"targetBatchNo":    outcome.Target.BatchNo,
		"providerCode":     outcome.Target.ProviderCode,
		"acceptedCount":    outcome.AcceptedCount,
		"targetTaskStatus": outcome.Target.Status,
	})
	_ = s.repo.AddAuditLog(c.Request.Context(), biz.AdminAuditLog{
		UserID:       session.User.UserID,
		Username:     session.User.Username,
		Role:         session.User.Role,
		Action:       "REASSIGN_TASK",
		ResourceType: "TASK",
		ResourceID:   strconv.FormatInt(outcome.Source.ID, 10),
		RequestID:    trace.RequestID(c.Request.Context()),
		Payload:      payload,
	})
	protoJSON(c, http.StatusOK, &adminv1.ReassignTaskResponse{
		Status:  0,
		Message: "转派成功",
		Data: &adminv1.ReassignTaskData{
			SourceTaskId:    outcome.Source.ID,
			TargetTaskId:    outcome.Target.ID,
			OriginalBatchNo: outcome.Target.CallbackBatchNo,
			TargetBatchNo:   outcome.Target.BatchNo,
			ProviderCode:    outcome.Target.ProviderCode,
			TaskStatus:      string(outcome.Target.Status),
			AcceptedCount:   int32(outcome.AcceptedCount),
		},
	})
}

func (s *AdminService) DeleteTask(c *gin.Context) {
	session, ok := adminSessionFromContext(c)
	if !ok {
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	deletedTask, err := s.repo.SoftDeleteSubmitFailedTask(c.Request.Context(), id, session.User.UserID)
	if err != nil {
		writeBizError(c, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"taskId":        deletedTask.TaskID,
		"batchNo":       deletedTask.BatchNo,
		"status":        deletedTask.Status,
		"providerCode":  deletedTask.ProviderCode,
		"questionCount": deletedTask.QuestionCount,
		"submitFailed":  deletedTask.SubmitFailed,
	})
	_ = s.repo.AddAuditLog(c.Request.Context(), biz.AdminAuditLog{
		UserID:       session.User.UserID,
		Username:     session.User.Username,
		Role:         session.User.Role,
		Action:       "SOFT_DELETE_SUBMIT_FAILED_TASK",
		ResourceType: "TASK",
		ResourceID:   strconv.FormatInt(id, 10),
		RequestID:    trace.RequestID(c.Request.Context()),
		Payload:      payload,
	})
	protoJSON(c, http.StatusOK, &adminv1.DeleteTaskResponse{
		Status: 0,
		Data:   &adminv1.EmptyData{Ok: true},
	})
}

func (s *AdminService) ListProviders(c *gin.Context) {
	providers, err := s.providerList(c.Request.Context(), false)
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, err)
		return
	}
	protoJSON(c, http.StatusOK, &adminv1.ListProvidersResponse{
		Status: 0,
		Data: &adminv1.ProviderListData{
			Providers: providers,
		},
	})
}

func (s *AdminService) GetDispatchOptions(c *gin.Context) {
	providers, err := s.providerList(c.Request.Context(), true)
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, err)
		return
	}
	protoJSON(c, http.StatusOK, &adminv1.GetDispatchOptionsResponse{
		Status: 0,
		Data: &adminv1.DispatchOptionsData{
			Strategies: []*adminv1.DispatchStrategyOption{
				{Code: string(biz.DispatchStrategyRoundRobin), Name: "Round Robin"},
				{Code: string(biz.DispatchStrategyRuleBased), Name: "规则分配"},
			},
			Providers: providers,
		},
	})
}

func (s *AdminService) providerList(ctx context.Context, onlyDispatchAvailable bool) ([]*adminv1.Provider, error) {
	providers := make([]*adminv1.Provider, 0, len(s.providers))
	codes := make([]string, 0, len(s.providers))
	for code := range s.providers {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	usageByProvider, err := s.repo.ListProviderQuotaUsage(ctx, codes)
	if err != nil {
		return nil, err
	}
	for _, code := range codes {
		provider := s.providers[code]
		if onlyDispatchAvailable && !dispatchAvailableProvider(provider) {
			continue
		}
		providers = append(providers, providerProto(provider, usageByProvider[code]))
	}
	return providers, nil
}

func dispatchAvailableProvider(provider config.ProviderConfig) bool {
	return strings.TrimSpace(provider.Code) != "" &&
		strings.TrimSpace(provider.SubmitURL) != "" &&
		provider.QPSLimit > 0 &&
		provider.ConcurrencyLimit > 0
}

func (s *AdminService) providerCompanyName(providerCode string) string {
	provider, ok := s.providers[providerCode]
	if !ok {
		return ""
	}
	return providerCompanyName(provider)
}

func providerCompanyName(provider config.ProviderConfig) string {
	if name := strings.TrimSpace(provider.CompanyName); name != "" {
		return name
	}
	if name := strings.TrimSpace(provider.DisplayName); name != "" {
		return name
	}
	return provider.Code
}

func (s *AdminService) GetProviderMetrics(c *gin.Context) {
	providerCode := strings.TrimSpace(c.Param("providerCode"))
	if providerCode == "" {
		errorJSON(c, http.StatusBadRequest, fmt.Errorf("providerCode is required"))
		return
	}
	providerConfig, ok := s.providers[providerCode]
	if !ok {
		writeBizError(c, biz.ErrProviderNotFound)
		return
	}
	filter, err := parseProviderMetricsFilter(c)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	metrics, err := s.repo.GetProviderMetrics(c.Request.Context(), providerCode, filter)
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, err)
		return
	}
	quotaUsage, err := s.repo.ListProviderQuotaUsage(c.Request.Context(), []string{providerCode})
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, err)
		return
	}
	protoJSON(c, http.StatusOK, &adminv1.GetProviderMetricsResponse{
		Status: 0,
		Data:   providerMetricsProto(providerConfig, quotaUsage[providerCode], metrics),
	})
}

func (s *AdminService) ListPlatformUsers(c *gin.Context) {
	filter := parsePlatformUserFilter(c)
	result, err := s.repo.ListPlatformUsers(c.Request.Context(), filter)
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, err)
		return
	}
	users := make([]*adminv1.PlatformUser, 0, len(result.Users))
	for _, user := range result.Users {
		users = append(users, platformUserProto(user))
	}
	protoJSON(c, http.StatusOK, &adminv1.ListPlatformUsersResponse{
		Status: 0,
		Data: &adminv1.PlatformUserListData{
			Users:    users,
			Total:    int32(result.Total),
			Page:     int32(result.Page),
			PageSize: int32(result.PageSize),
		},
	})
}

func (s *AdminService) GetPlatformUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	user, err := s.repo.GetPlatformUser(c.Request.Context(), id)
	if err != nil {
		writeBizError(c, err)
		return
	}
	protoJSON(c, http.StatusOK, &adminv1.GetPlatformUserResponse{
		Status: 0,
		Data:   platformUserProto(user),
	})
}

func (s *AdminService) GetPlatformUserMetrics(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	filter, err := parsePlatformUserMetricsFilter(c)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	metrics, err := s.repo.GetPlatformUserMetrics(c.Request.Context(), id, filter)
	if err != nil {
		writeBizError(c, err)
		return
	}
	protoJSON(c, http.StatusOK, &adminv1.GetPlatformUserMetricsResponse{
		Status: 0,
		Data:   platformUserMetricsProto(metrics),
	})
}

func (s *AdminService) ExportPlatformUserReconciliation(c *gin.Context) {
	session, ok := adminSessionFromContext(c)
	if !ok {
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	req := adminv1.ExportPlatformUserReconciliationRequest{Id: id}
	raw, err := bindOptionalProto(c, &req)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	filter, err := parsePlatformUserReconciliationFilter(req.CreatedFrom, req.CreatedTo)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	export, err := s.repo.ExportPlatformUserReconciliation(c.Request.Context(), id, filter)
	if err != nil {
		writeBizError(c, err)
		return
	}
	for i := range export.Rows {
		export.Rows[i].ProviderCompanyName = s.providerCompanyName(export.Rows[i].ProviderCode)
	}
	workbook, err := buildPlatformUserReconciliationWorkbook(export)
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, err)
		return
	}
	filename := platformUserReconciliationFilename(export)
	download, err := s.tasks.CreateTemporaryDownloadFile(c.Request.Context(), filename, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", workbook)
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"request":     json.RawMessage(defaultJSON(raw)),
		"createdFrom": formatTime(filter.CreatedFrom),
		"createdTo":   formatTime(filter.CreatedTo),
		"objectKey":   download.Key,
		"fileName":    filename,
		"rowCount":    len(export.Rows),
		"expiresAt":   formatTime(download.ExpiresAt),
	})
	_ = s.repo.AddAuditLog(c.Request.Context(), biz.AdminAuditLog{
		UserID:       session.User.UserID,
		Username:     session.User.Username,
		Role:         session.User.Role,
		Action:       "EXPORT_PLATFORM_USER_RECONCILIATION",
		ResourceType: "PLATFORM_USER",
		ResourceID:   strconv.FormatInt(id, 10),
		RequestID:    trace.RequestID(c.Request.Context()),
		Payload:      payload,
	})
	protoJSON(c, http.StatusOK, &adminv1.ExportPlatformUserReconciliationResponse{
		Status: 0,
		Data: &adminv1.ExportTaskItemsData{
			DownloadUrl: download.DownloadURL,
			ObjectKey:   download.Key,
			ExpiresAt:   formatTime(download.ExpiresAt),
		},
	})
}

func (s *AdminService) CreatePlatformUser(c *gin.Context) {
	session, ok := adminSessionFromContext(c)
	if !ok {
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return
	}
	var req adminv1.CreatePlatformUserRequest
	raw, err := bindProto(c, &req)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	input, err := s.platformUserInput(req.PlatformCode, req.Name, req.Status, req.CallbackMode, req.CallbackUrl, req.DispatchProfile)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	user, err := s.repo.CreatePlatformUser(c.Request.Context(), input)
	if err != nil {
		writeBizError(c, err)
		return
	}
	_ = s.repo.AddAuditLog(c.Request.Context(), biz.AdminAuditLog{
		UserID:       session.User.UserID,
		Username:     session.User.Username,
		Role:         session.User.Role,
		Action:       "CREATE_PLATFORM_USER",
		ResourceType: "PLATFORM_USER",
		ResourceID:   strconv.FormatInt(user.ID, 10),
		RequestID:    trace.RequestID(c.Request.Context()),
		Payload:      json.RawMessage(defaultJSON(raw)),
	})
	protoJSON(c, http.StatusOK, &adminv1.CreatePlatformUserResponse{
		Status: 0,
		Data:   platformUserProto(user),
	})
}

func (s *AdminService) UpdatePlatformUser(c *gin.Context) {
	session, ok := adminSessionFromContext(c)
	if !ok {
		errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	var req adminv1.UpdatePlatformUserRequest
	raw, err := bindProto(c, &req)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	input, err := s.platformUserInput(req.PlatformCode, req.Name, req.Status, req.CallbackMode, req.CallbackUrl, req.DispatchProfile)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	user, err := s.repo.UpdatePlatformUser(c.Request.Context(), id, input)
	if err != nil {
		writeBizError(c, err)
		return
	}
	_ = s.repo.AddAuditLog(c.Request.Context(), biz.AdminAuditLog{
		UserID:       session.User.UserID,
		Username:     session.User.Username,
		Role:         session.User.Role,
		Action:       "UPDATE_PLATFORM_USER",
		ResourceType: "PLATFORM_USER",
		ResourceID:   strconv.FormatInt(user.ID, 10),
		RequestID:    trace.RequestID(c.Request.Context()),
		Payload:      json.RawMessage(defaultJSON(raw)),
	})
	protoJSON(c, http.StatusOK, &adminv1.UpdatePlatformUserResponse{
		Status: 0,
		Data:   platformUserProto(user),
	})
}

func retryCallbackMessage(outcome biz.RetryTaskCallbackOutcome) string {
	if outcome.Delivered {
		return "回调重试成功"
	}
	if outcome.Readiness.Reason != "" {
		return outcome.Readiness.Reason
	}
	return "回调重试已执行但未成功，请查看任务事件和 callback 记录"
}

func (s *AdminService) RequireAuth(requireCSRF bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := s.getSession(c.Request.Context(), c)
		if err != nil {
			errorJSON(c, http.StatusUnauthorized, biz.ErrAdminUnauthorized)
			c.Abort()
			return
		}
		if requireCSRF && !secureEqual(c.GetHeader(s.cfg.Session.CSRFHeader), session.CSRFToken) {
			errorJSON(c, http.StatusForbidden, biz.ErrAdminForbidden)
			c.Abort()
			return
		}
		c.Set(adminSessionContextKey, session)
		c.Next()
	}
}

func (s *AdminService) verifyPassword(username, password string) bool {
	bootstrap := s.cfg.Bootstrap
	if username != bootstrap.Username {
		return false
	}
	if bootstrap.PasswordHash != "" {
		return bcrypt.CompareHashAndPassword([]byte(bootstrap.PasswordHash), []byte(password)) == nil
	}
	return secureEqual(password, bootstrap.Password)
}

func (s *AdminService) createSession(ctx context.Context) (biz.AdminSession, error) {
	now := time.Now()
	session := biz.AdminSession{
		SessionID: randomHex(32),
		CSRFToken: randomHex(32),
		User: biz.AdminUser{
			UserID:   "bootstrap:" + s.cfg.Bootstrap.Username,
			Username: s.cfg.Bootstrap.Username,
			Role:     s.cfg.Bootstrap.Role,
		},
		CreatedAt: now,
		LastSeen:  now,
		ExpiresAt: now.Add(s.cfg.Session.AbsoluteTTL),
	}
	body, err := json.Marshal(session)
	if err != nil {
		return biz.AdminSession{}, err
	}
	if err := s.redis.Set(ctx, s.sessionKey(session.SessionID), body, minDuration(s.cfg.Session.IdleTTL, s.cfg.Session.AbsoluteTTL)).Err(); err != nil {
		return biz.AdminSession{}, err
	}
	return session, nil
}

func (s *AdminService) getSession(ctx context.Context, c *gin.Context) (biz.AdminSession, error) {
	if s.redis == nil {
		return biz.AdminSession{}, biz.ErrAdminUnauthorized
	}
	cookie, err := c.Cookie(s.cfg.Session.CookieName)
	if err != nil || cookie == "" {
		return biz.AdminSession{}, biz.ErrAdminUnauthorized
	}
	raw, err := s.redis.Get(ctx, s.sessionKey(cookie)).Bytes()
	if err != nil {
		return biz.AdminSession{}, biz.ErrAdminUnauthorized
	}
	var session biz.AdminSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return biz.AdminSession{}, biz.ErrAdminUnauthorized
	}
	now := time.Now()
	if session.ExpiresAt.Before(now) || session.LastSeen.Add(s.cfg.Session.IdleTTL).Before(now) {
		_ = s.redis.Del(ctx, s.sessionKey(cookie)).Err()
		return biz.AdminSession{}, biz.ErrAdminUnauthorized
	}
	session.LastSeen = now
	body, _ := json.Marshal(session)
	ttl := minDuration(s.cfg.Session.IdleTTL, time.Until(session.ExpiresAt))
	if ttl <= 0 {
		_ = s.redis.Del(ctx, s.sessionKey(cookie)).Err()
		return biz.AdminSession{}, biz.ErrAdminUnauthorized
	}
	if err := s.redis.Set(ctx, s.sessionKey(cookie), body, ttl).Err(); err != nil {
		return biz.AdminSession{}, biz.ErrAdminUnauthorized
	}
	return session, nil
}

func (s *AdminService) sessionKey(sessionID string) string {
	return s.cfg.Session.RedisKeyPrefix + sessionID
}

func (s *AdminService) setCookie(c *gin.Context, sessionID string, expiresAt time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     s.cfg.Session.CookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.Session.Secure,
		SameSite: sameSite(s.cfg.Session.SameSite),
	})
}

func (s *AdminService) clearCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     s.cfg.Session.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.Session.Secure,
		SameSite: sameSite(s.cfg.Session.SameSite),
	})
}

func parseTaskFilter(c *gin.Context) (biz.AdminTaskFilter, error) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", c.DefaultQuery("page_size", "20")))
	createdFrom, err := parseOptionalTime(c.Query("createdFrom"))
	if err != nil {
		return biz.AdminTaskFilter{}, err
	}
	createdTo, err := parseOptionalTime(c.Query("createdTo"))
	if err != nil {
		return biz.AdminTaskFilter{}, err
	}
	return biz.AdminTaskFilter{
		TaskName:     c.Query("taskName"),
		BatchNo:      c.Query("batchNo"),
		Source:       c.Query("source"),
		PlatformCode: c.Query("platformCode"),
		ProviderCode: c.Query("providerCode"),
		ResultStatus: c.Query("resultStatus"),
		CreatedFrom:  createdFrom,
		CreatedTo:    createdTo,
		Page:         page,
		PageSize:     pageSize,
	}, nil
}

func parsePlatformUserFilter(c *gin.Context) biz.AdminPlatformUserFilter {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", c.DefaultQuery("page_size", "20")))
	return biz.AdminPlatformUserFilter{
		Keyword:  c.Query("keyword"),
		Status:   c.Query("status"),
		Page:     page,
		PageSize: pageSize,
	}
}

func parsePlatformUserMetricsFilter(c *gin.Context) (biz.AdminPlatformUserMetricsFilter, error) {
	createdFrom, err := parseOptionalTime(firstQuery(c, "createdFrom", "created_from"))
	if err != nil {
		return biz.AdminPlatformUserMetricsFilter{}, err
	}
	createdTo, err := parseOptionalTime(firstQuery(c, "createdTo", "created_to"))
	if err != nil {
		return biz.AdminPlatformUserMetricsFilter{}, err
	}
	if createdFrom.IsZero() && createdTo.IsZero() {
		createdFrom, createdTo = currentAdminMonthRange()
	}
	if !createdFrom.IsZero() && !createdTo.IsZero() && createdTo.Before(createdFrom) {
		return biz.AdminPlatformUserMetricsFilter{}, fmt.Errorf("createdTo must be after createdFrom")
	}
	return biz.AdminPlatformUserMetricsFilter{
		CreatedFrom: createdFrom,
		CreatedTo:   createdTo,
	}, nil
}

func parsePlatformUserReconciliationFilter(createdFromValue, createdToValue string) (biz.AdminPlatformUserReconciliationFilter, error) {
	createdFrom, err := parseOptionalTime(createdFromValue)
	if err != nil {
		return biz.AdminPlatformUserReconciliationFilter{}, err
	}
	createdTo, err := parseOptionalTime(createdToValue)
	if err != nil {
		return biz.AdminPlatformUserReconciliationFilter{}, err
	}
	if createdFrom.IsZero() && createdTo.IsZero() {
		createdFrom, createdTo = currentAdminMonthRange()
	}
	if !createdFrom.IsZero() && !createdTo.IsZero() && createdTo.Before(createdFrom) {
		return biz.AdminPlatformUserReconciliationFilter{}, fmt.Errorf("createdTo must be after createdFrom")
	}
	return biz.AdminPlatformUserReconciliationFilter{
		CreatedFrom: createdFrom,
		CreatedTo:   createdTo,
	}, nil
}

func currentAdminMonthRange() (time.Time, time.Time) {
	now := time.Now().In(timezone.AdminLocation())
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, timezone.AdminLocation())
	end := start.AddDate(0, 1, 0).Add(-time.Second)
	return start, end
}

func parseProviderMetricsFilter(c *gin.Context) (biz.AdminProviderMetricsFilter, error) {
	createdFrom, err := parseOptionalTime(firstQuery(c, "createdFrom", "created_from"))
	if err != nil {
		return biz.AdminProviderMetricsFilter{}, err
	}
	createdTo, err := parseOptionalTime(firstQuery(c, "createdTo", "created_to"))
	if err != nil {
		return biz.AdminProviderMetricsFilter{}, err
	}
	if createdFrom.IsZero() && createdTo.IsZero() {
		createdTo = time.Now().In(timezone.AdminLocation())
		createdFrom = createdTo.Add(-24 * time.Hour)
	}
	if !createdFrom.IsZero() && !createdTo.IsZero() && createdTo.Before(createdFrom) {
		return biz.AdminProviderMetricsFilter{}, fmt.Errorf("createdTo must be after createdFrom")
	}
	return biz.AdminProviderMetricsFilter{
		CreatedFrom: createdFrom,
		CreatedTo:   createdTo,
	}, nil
}

func firstQuery(c *gin.Context, keys ...string) string {
	for _, key := range keys {
		if value := c.Query(key); value != "" {
			return value
		}
	}
	return ""
}

func parsePositiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func (s *AdminService) platformUserInput(platformCode string, name string, status string, callbackMode string, callbackURL string, dispatch *adminv1.DispatchProfileInput) (biz.AdminPlatformUserInput, error) {
	input := biz.AdminPlatformUserInput{
		PlatformCode: strings.TrimSpace(platformCode),
		Name:         strings.TrimSpace(name),
		Status:       strings.TrimSpace(strings.ToUpper(status)),
		CallbackMode: strings.TrimSpace(strings.ToUpper(callbackMode)),
		CallbackURL:  strings.TrimSpace(callbackURL),
	}
	input.DispatchProfile = biz.DispatchProfileInput{
		Strategy: biz.DispatchStrategyRoundRobin,
		Enabled:  true,
	}
	if dispatch != nil {
		input.DispatchProfile = biz.DispatchProfileInput{
			Code:                    strings.TrimSpace(dispatch.Code),
			Name:                    strings.TrimSpace(dispatch.Name),
			Strategy:                biz.DispatchStrategy(strings.TrimSpace(strings.ToUpper(dispatch.Strategy))),
			Enabled:                 dispatch.Enabled,
			ProviderCodes:           append([]string{}, dispatch.ProviderCodes...),
			FallbackSelectionPolicy: biz.DispatchFallbackSelectionPolicy(strings.TrimSpace(strings.ToUpper(dispatch.FallbackSelectionPolicy))),
			Rules:                   dispatchRuleInputs(dispatch.Rules),
		}
	}
	if input.Status == "" {
		input.Status = "ACTIVE"
	}
	if input.CallbackMode == "" {
		input.CallbackMode = "DEFAULT_HTTP"
	}
	if input.DispatchProfile.Strategy == "" {
		input.DispatchProfile.Strategy = biz.DispatchStrategyRoundRobin
	}
	if input.DispatchProfile.FallbackSelectionPolicy == "" {
		input.DispatchProfile.FallbackSelectionPolicy = defaultFallbackSelectionPolicy(input.DispatchProfile.Strategy)
	}
	assignDispatchRuleCodes(&input.DispatchProfile)
	if input.PlatformCode == "" || input.Name == "" {
		return biz.AdminPlatformUserInput{}, fmt.Errorf("platformCode/name are required")
	}
	if len(input.DispatchProfile.ProviderCodes) == 0 {
		return biz.AdminPlatformUserInput{}, fmt.Errorf("dispatchProfile.providerCodes are required")
	}
	for _, code := range input.DispatchProfile.ProviderCodes {
		provider, ok := s.providers[code]
		if !ok || !dispatchAvailableProvider(provider) {
			return biz.AdminPlatformUserInput{}, fmt.Errorf("dispatchProfile.providerCodes contains unavailable provider %s", code)
		}
	}
	switch input.Status {
	case "ACTIVE", "DISABLED":
	default:
		return biz.AdminPlatformUserInput{}, fmt.Errorf("status must be ACTIVE or DISABLED")
	}
	switch input.CallbackMode {
	case "DEFAULT_HTTP", "CUSTOM":
	default:
		return biz.AdminPlatformUserInput{}, fmt.Errorf("callbackMode must be DEFAULT_HTTP or CUSTOM")
	}
	switch input.DispatchProfile.Strategy {
	case biz.DispatchStrategyRoundRobin, biz.DispatchStrategyRuleBased:
	default:
		return biz.AdminPlatformUserInput{}, fmt.Errorf("dispatchProfile.strategy must be ROUND_ROBIN or RULE_BASED")
	}
	if err := validateFallbackSelectionPolicy(input.DispatchProfile); err != nil {
		return biz.AdminPlatformUserInput{}, err
	}
	if err := s.validateDispatchRules(input.DispatchProfile); err != nil {
		return biz.AdminPlatformUserInput{}, err
	}
	return input, nil
}

func dispatchRuleInputs(rules []*adminv1.DispatchRuleInput) []biz.DispatchRuleInput {
	out := make([]biz.DispatchRuleInput, 0, len(rules))
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		out = append(out, biz.DispatchRuleInput{
			Code:          strings.TrimSpace(rule.Code),
			Name:          strings.TrimSpace(rule.Name),
			SortOrder:     int(rule.SortOrder),
			TaskNameRegex: strings.TrimSpace(rule.TaskNameRegex),
			Labels:        append([]string{}, rule.Labels...),
			ProviderCodes: append([]string{}, rule.ProviderCodes...),
			Enabled:       rule.Enabled,
		})
	}
	return out
}

func (s *AdminService) validateDispatchRules(profile biz.DispatchProfileInput) error {
	if profile.Strategy != biz.DispatchStrategyRuleBased {
		return nil
	}
	seenCodes := map[string]bool{}
	for _, rule := range profile.Rules {
		if seenCodes[rule.Code] {
			return fmt.Errorf("dispatchProfile.rules contains duplicate code %s", rule.Code)
		}
		seenCodes[rule.Code] = true
		if strings.TrimSpace(rule.TaskNameRegex) == "" {
			return fmt.Errorf("dispatchProfile.rules.%s.taskNameRegex is required", rule.Code)
		}
		if _, err := regexp.Compile(rule.TaskNameRegex); err != nil {
			return fmt.Errorf("dispatchProfile.rules.%s.taskNameRegex is invalid: %w", rule.Code, err)
		}
		if len(rule.ProviderCodes) == 0 {
			return fmt.Errorf("dispatchProfile.rules.%s.providerCodes are required", rule.Code)
		}
		for _, code := range rule.ProviderCodes {
			code = strings.TrimSpace(code)
			provider, ok := s.providers[code]
			if !ok || !dispatchAvailableProvider(provider) {
				return fmt.Errorf("dispatchProfile.rules.%s.providerCodes contains unavailable provider %s", rule.Code, code)
			}
		}
	}
	return nil
}

func validateFallbackSelectionPolicy(profile biz.DispatchProfileInput) error {
	switch profile.FallbackSelectionPolicy {
	case "", biz.DispatchFallbackSelectionPolicyRoundRobin, biz.DispatchFallbackSelectionPolicyLowestUsageRate:
		return nil
	default:
		return fmt.Errorf("dispatchProfile.fallbackSelectionPolicy must be ROUND_ROBIN or LOWEST_USAGE_RATE")
	}
}

func defaultFallbackSelectionPolicy(strategy biz.DispatchStrategy) biz.DispatchFallbackSelectionPolicy {
	if strategy == biz.DispatchStrategyRoundRobin {
		return biz.DispatchFallbackSelectionPolicyRoundRobin
	}
	return biz.DispatchFallbackSelectionPolicyLowestUsageRate
}

func assignDispatchRuleCodes(profile *biz.DispatchProfileInput) {
	used := map[string]bool{}
	for _, rule := range profile.Rules {
		code := strings.TrimSpace(rule.Code)
		if code != "" {
			used[code] = true
		}
	}
	for i := range profile.Rules {
		if strings.TrimSpace(profile.Rules[i].Code) != "" {
			profile.Rules[i].Code = strings.TrimSpace(profile.Rules[i].Code)
			continue
		}
		for n := i + 1; ; n++ {
			code := fmt.Sprintf("rule_%d", n)
			if !used[code] {
				profile.Rules[i].Code = code
				used[code] = true
				break
			}
		}
	}
}

func parseOptionalTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		parsed, err := time.ParseInLocation(layout, value, timezone.AdminLocation())
		if err == nil {
			if layout == "2006-01-02" {
				return parsed, nil
			}
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time: %s", value)
}

func adminSessionFromContext(c *gin.Context) (biz.AdminSession, bool) {
	value, ok := c.Get(adminSessionContextKey)
	if !ok {
		return biz.AdminSession{}, false
	}
	session, ok := value.(biz.AdminSession)
	return session, ok
}

func adminUserProto(user biz.AdminUser) *adminv1.AdminUser {
	return &adminv1.AdminUser{
		UserId:   user.UserID,
		Username: user.Username,
		Role:     user.Role,
	}
}

func adminTaskSummaryProto(task biz.AdminTaskSummary) *adminv1.AdminTaskSummary {
	durationSeconds, durationText := taskExecutionDuration(task)
	return &adminv1.AdminTaskSummary{
		TaskId:                   task.TaskID,
		TaskName:                 task.TaskName,
		BatchNo:                  task.BatchNo,
		Source:                   task.Source,
		PlatformCode:             task.PlatformCode,
		ExternalBatchId:          task.ExternalBatchID,
		ProviderCode:             task.ProviderCode,
		Status:                   string(task.Status),
		QuestionCount:            int32(task.QuestionCount),
		CompletedCount:           int32(task.CompletedCount),
		SuccessCount:             int32(task.SuccessCount),
		FailedCount:              int32(task.FailedCount),
		ResultSuccessCount:       int32(task.ResultSuccessCount),
		ResultFailedCount:        int32(task.ResultFailedCount),
		ResultMissingCount:       int32(task.ResultMissingCount),
		Progress:                 task.Progress,
		CreatedAt:                formatTime(task.CreatedAt),
		UpdatedAt:                formatTime(task.UpdatedAt),
		ExecutionDurationSeconds: durationSeconds,
		ExecutionDurationText:    durationText,
		SubmitFailed:             task.SubmitFailed,
		DispatchLabels:           append([]string{}, task.DispatchLabels...),
		ReassignedFromTaskId:     task.ReassignedFromTaskID,
		ReassignedToTaskId:       task.ReassignedToTaskID,
	}
}

func taskExecutionDuration(task biz.AdminTaskSummary) (int32, string) {
	if task.CreatedAt.IsZero() {
		return 0, "-"
	}
	if task.ExecutionEndedAt.IsZero() {
		if !taskProviderExecutionRunning(task.Status) {
			return 0, "-"
		}
		return durationSince(task.CreatedAt)
	}
	endAt := task.ExecutionEndedAt
	if endAt.Before(task.CreatedAt) {
		endAt = task.CreatedAt
	}
	seconds := int64(endAt.Sub(task.CreatedAt).Seconds())
	return durationText(seconds)
}

func durationSince(start time.Time) (int32, string) {
	seconds, text := durationText(int64(time.Since(start).Seconds()))
	return seconds, "已执行 " + text
}

func durationText(seconds int64) (int32, string) {
	if seconds < 0 {
		seconds = 0
	}
	clampedSeconds := seconds
	if clampedSeconds > 2147483647 {
		clampedSeconds = 2147483647
	}
	text := humanDuration(seconds)
	return int32(clampedSeconds), text
}

func taskProviderExecutionRunning(status biz.TaskStatus) bool {
	switch status {
	case biz.TaskStatusCreated, biz.TaskStatusSubmitted:
		return true
	default:
		return false
	}
}

func humanDuration(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	days := seconds / 86400
	seconds %= 86400
	hours := seconds / 3600
	seconds %= 3600
	minutes := seconds / 60
	seconds %= 60

	switch {
	case days > 0:
		return fmt.Sprintf("%d天%d小时", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d小时%d分", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%d分%d秒", minutes, seconds)
	default:
		return fmt.Sprintf("%d秒", seconds)
	}
}

func metricDurationText(seconds int, hasValue bool) string {
	if !hasValue {
		return "-"
	}
	return humanDuration(int64(seconds))
}

func int32FromInt(value int) int32 {
	if value > 2147483647 {
		return 2147483647
	}
	if value < -2147483648 {
		return -2147483648
	}
	return int32(value)
}

func platformUserProto(user biz.PlatformUser) *adminv1.PlatformUser {
	return &adminv1.PlatformUser{
		Id:              user.ID,
		PlatformCode:    user.PlatformCode,
		Name:            user.Name,
		ApiKey:          user.APIKey,
		Status:          user.Status,
		CallbackMode:    user.CallbackMode,
		CallbackUrl:     user.CallbackURL,
		DispatchProfile: dispatchProfileProto(user.DispatchProfile),
		CreatedAt:       formatTime(user.CreatedAt),
		UpdatedAt:       formatTime(user.UpdatedAt),
	}
}

func dispatchProfileProto(profile biz.DispatchProfile) *adminv1.DispatchProfile {
	if profile.ID == 0 {
		return nil
	}
	return &adminv1.DispatchProfile{
		Id:                      profile.ID,
		Code:                    profile.Code,
		Name:                    profile.Name,
		Strategy:                string(profile.Strategy),
		Enabled:                 profile.Enabled,
		Cursor:                  profile.Cursor,
		LastProviderCode:        profile.LastProviderCode,
		ProviderCodes:           append([]string{}, profile.ProviderCodes...),
		FallbackSelectionPolicy: string(profile.FallbackSelectionPolicy),
		Rules:                   dispatchRulesProto(profile.Rules),
	}
}

func dispatchRulesProto(rules []biz.DispatchRule) []*adminv1.DispatchRule {
	out := make([]*adminv1.DispatchRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, &adminv1.DispatchRule{
			Code:          rule.Code,
			Name:          rule.Name,
			SortOrder:     int32FromInt(rule.SortOrder),
			TaskNameRegex: rule.TaskNameRegex,
			Labels:        append([]string{}, rule.Labels...),
			ProviderCodes: append([]string{}, rule.ProviderCodes...),
			Enabled:       rule.Enabled,
		})
	}
	return out
}

func providerProto(provider config.ProviderConfig, quotaUsage biz.AdminProviderQuotaUsage) *adminv1.Provider {
	unlimited := provider.DailyQuestionQuota <= 0
	remaining := provider.DailyQuestionQuota - quotaUsage.Used
	if unlimited {
		remaining = 0
	} else if remaining < 0 {
		remaining = 0
	}
	return &adminv1.Provider{
		ProviderCode:           provider.Code,
		DisplayName:            provider.DisplayName,
		Enabled:                provider.Enabled,
		ResultMode:             provider.ResultMode,
		TimeoutSeconds:         int32FromInt(int(provider.Timeout.Seconds())),
		QpsLimit:               int32FromInt(provider.QPSLimit),
		ConcurrencyLimit:       int32FromInt(provider.ConcurrencyLimit),
		BucketPathPrefix:       provider.BucketPathPrefix,
		SubmitUrlConfigured:    strings.TrimSpace(provider.SubmitURL) != "",
		StatusUrlConfigured:    strings.TrimSpace(provider.StatusURL) != "",
		FilesUrlConfigured:     strings.TrimSpace(provider.FilesURL) != "",
		DailyQuestionQuota:     int32FromInt(provider.DailyQuestionQuota),
		DailyQuestionUsed:      int32FromInt(quotaUsage.Used),
		DailyQuestionRemaining: int32FromInt(remaining),
		DailyQuestionUnlimited: unlimited,
		CompanyName:            providerCompanyName(provider),
		SettlementStartAt:      strings.TrimSpace(provider.SettlementStartAt),
	}
}

func providerMetricsProto(provider config.ProviderConfig, quotaUsage biz.AdminProviderQuotaUsage, metrics biz.AdminProviderMetrics) *adminv1.ProviderMetricsData {
	return &adminv1.ProviderMetricsData{
		Provider:    providerProto(provider, quotaUsage),
		CreatedFrom: formatTime(metrics.CreatedFrom),
		CreatedTo:   formatTime(metrics.CreatedTo),
		Summary: &adminv1.ProviderMetricsSummary{
			TaskCount:          int32FromInt(metrics.Summary.TaskCount),
			QuestionCount:      int32FromInt(metrics.Summary.QuestionCount),
			SuccessAnswerCount: int32FromInt(metrics.Summary.SuccessAnswerCount),
			FailedAnswerCount:  int32FromInt(metrics.Summary.FailedAnswerCount),
			AnswerSuccessRate:  metrics.Summary.AnswerSuccessRate,
		},
		DailyMetrics: providerDailyMetricsProto(metrics.Daily),
	}
}

func platformUserMetricsProto(metrics biz.AdminPlatformUserMetrics) *adminv1.PlatformUserMetricsData {
	recentTasks := make([]*adminv1.AdminTaskSummary, 0, len(metrics.RecentTasks))
	for _, task := range metrics.RecentTasks {
		recentTasks = append(recentTasks, adminTaskSummaryProto(task))
	}
	return &adminv1.PlatformUserMetricsData{
		User:                 platformUserProto(metrics.User),
		CreatedFrom:          formatTime(metrics.CreatedFrom),
		CreatedTo:            formatTime(metrics.CreatedTo),
		TaskMetrics:          platformTaskMetricsProto(metrics.TaskMetrics),
		QuestionMetrics:      platformQuestionMetricsProto(metrics.QuestionMetrics),
		ResultMetrics:        platformResultMetricsProto(metrics.ResultMetrics),
		CallbackMetrics:      platformCallbackMetricsProto(metrics.CallbackMetrics),
		StatusBreakdown:      metricBreakdownsProto(metrics.StatusBreakdown),
		FailureReasons:       metricBreakdownsProto(metrics.FailureReasons),
		RecentTasks:          recentTasks,
		ProviderDailyMetrics: providerDailyMetricsProto(metrics.ProviderDaily),
	}
}

func platformTaskMetricsProto(metrics biz.AdminPlatformTaskMetrics) *adminv1.PlatformTaskMetrics {
	return &adminv1.PlatformTaskMetrics{
		SubmittedTaskCount:       int32FromInt(metrics.SubmittedTaskCount),
		CompletedTaskCount:       int32FromInt(metrics.CompletedTaskCount),
		ProcessingTaskCount:      int32FromInt(metrics.ProcessingTaskCount),
		FailedTaskCount:          int32FromInt(metrics.FailedTaskCount),
		TaskSuccessRate:          metrics.TaskSuccessRate,
		AverageDurationSeconds:   int32FromInt(metrics.AverageDurationSeconds),
		AverageDurationText:      metricDurationText(metrics.AverageDurationSeconds, metrics.CompletedTaskCount+metrics.FailedTaskCount > 0),
		LongestProcessingSeconds: int32FromInt(metrics.LongestProcessingSeconds),
		LongestProcessingText:    metricDurationText(metrics.LongestProcessingSeconds, metrics.ProcessingTaskCount > 0),
	}
}

func platformQuestionMetricsProto(metrics biz.AdminPlatformQuestionMetrics) *adminv1.PlatformQuestionMetrics {
	return &adminv1.PlatformQuestionMetrics{
		SubmittedQuestionCount:      int32FromInt(metrics.SubmittedQuestionCount),
		CompletedAnswerCount:        int32FromInt(metrics.CompletedAnswerCount),
		ProviderDoneQuestionCount:   int32FromInt(metrics.ProviderDoneQuestionCount),
		FailedQuestionCount:         int32FromInt(metrics.FailedQuestionCount),
		AnswerCompletionRate:        metrics.AnswerCompletionRate,
		AverageQuestionsPerTask:     metrics.AverageQuestionsPerTask,
		ActiveQuestionPlatformCount: int32FromInt(metrics.ActiveQuestionPlatformCount),
	}
}

func platformResultMetricsProto(metrics biz.AdminPlatformResultMetrics) *adminv1.PlatformResultMetrics {
	return &adminv1.PlatformResultMetrics{
		ResultFileCount:      int32FromInt(metrics.ResultFileCount),
		ParsedFileCount:      int32FromInt(metrics.ParsedFileCount),
		ParseFailedFileCount: int32FromInt(metrics.ParseFailedFileCount),
		RowCount:             int32FromInt(metrics.RowCount),
		ValidRowCount:        int32FromInt(metrics.ValidRowCount),
		InvalidRowCount:      int32FromInt(metrics.InvalidRowCount),
		ValidRowRate:         metrics.ValidRowRate,
	}
}

func platformCallbackMetricsProto(metrics biz.AdminPlatformCallbackMetrics) *adminv1.PlatformCallbackMetrics {
	return &adminv1.PlatformCallbackMetrics{
		CallbackCount:        int32FromInt(metrics.CallbackCount),
		CallbackSuccessCount: int32FromInt(metrics.CallbackSuccessCount),
		CallbackFailedCount:  int32FromInt(metrics.CallbackFailedCount),
		CallbackSuccessRate:  metrics.CallbackSuccessRate,
	}
}

func metricBreakdownsProto(items []biz.AdminMetricBreakdown) []*adminv1.MetricBreakdown {
	out := make([]*adminv1.MetricBreakdown, 0, len(items))
	for _, item := range items {
		out = append(out, &adminv1.MetricBreakdown{
			Label: item.Label,
			Value: int32FromInt(item.Value),
			Ratio: item.Ratio,
		})
	}
	return out
}

func providerDailyMetricsProto(items []biz.AdminProviderDailyMetric) []*adminv1.ProviderDailyMetric {
	out := make([]*adminv1.ProviderDailyMetric, 0, len(items))
	for _, item := range items {
		out = append(out, &adminv1.ProviderDailyMetric{
			MetricDate:         item.MetricDate,
			ProviderCode:       item.ProviderCode,
			TaskCount:          int32FromInt(item.TaskCount),
			QuestionCount:      int32FromInt(item.QuestionCount),
			SuccessAnswerCount: int32FromInt(item.SuccessAnswerCount),
			FailedAnswerCount:  int32FromInt(item.FailedAnswerCount),
		})
	}
	return out
}

func adminTaskItemProto(item biz.AdminTaskItem) *adminv1.AdminTaskItem {
	return &adminv1.AdminTaskItem{
		TaskItemId:               item.TaskItemID,
		ItemId:                   item.ItemID,
		PlatformCode:             item.PlatformCode,
		Seq:                      int32(item.Seq),
		Question:                 item.Question,
		ProviderStatus:           int32(item.ProviderStatus),
		ResultStatus:             item.ResultStatus,
		AnswerText:               item.AnswerText,
		ThinkingProcess:          item.ThinkingProcess,
		GeneratedAt:              formatTime(item.GeneratedAt),
		GeneratedAtText:          item.GeneratedAtText,
		RecommendedQuestionsJson: string(defaultJSON(item.RecommendedJSON)),
		DeviceId:                 item.DeviceID,
		RecordingUrl:             item.RecordingURL,
		ReferencesJson:           string(defaultJSON(item.ReferencesJSON)),
		ReferenceCount:           int32(item.ReferenceCount),
		ParseStatus:              item.ParseStatus,
		ParseError:               item.ParseError,
		ResultImportedAt:         formatTime(item.ResultImportedAt),
	}
}

func buildTaskItemsWorkbook(export biz.AdminTaskItemsExport) ([]byte, error) {
	const (
		successSheet = "成功"
		failedSheet  = "失败／未执行"
	)
	file := excelize.NewFile()
	defer func() { _ = file.Close() }()
	if err := file.SetSheetName("Sheet1", successSheet); err != nil {
		return nil, err
	}
	if _, err := file.NewSheet(failedSheet); err != nil {
		return nil, err
	}
	if err := writeTaskItemsSheet(file, successSheet, filterTaskItems(export.Items, true)); err != nil {
		return nil, err
	}
	if err := writeTaskItemsSheet(file, failedSheet, filterTaskItems(export.Items, false)); err != nil {
		return nil, err
	}
	file.SetActiveSheet(0)
	var buf bytes.Buffer
	if err := file.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func buildPlatformUserReconciliationWorkbook(export biz.AdminPlatformUserReconciliationExport) ([]byte, error) {
	const sheet = "对账单"
	file := excelize.NewFile()
	defer func() { _ = file.Close() }()
	if err := file.SetSheetName("Sheet1", sheet); err != nil {
		return nil, err
	}
	if err := writePlatformUserReconciliationSheet(file, sheet, export.Rows); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := file.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writePlatformUserReconciliationSheet(file *excelize.File, sheet string, rows []biz.AdminPlatformUserReconciliationRow) error {
	headers := []any{"日期", "任务名称", "任务batchNo", "供应商code", "供应商公司名称", "问答数"}
	if err := file.SetSheetRow(sheet, "A1", &headers); err != nil {
		return err
	}
	for index, item := range rows {
		row := []any{
			item.MetricDate,
			item.TaskName,
			item.BatchNo,
			item.ProviderCode,
			item.ProviderCompanyName,
			item.QuestionCount,
		}
		cell, err := excelize.CoordinatesToCellName(1, index+2)
		if err != nil {
			return err
		}
		if err := file.SetSheetRow(sheet, cell, &row); err != nil {
			return err
		}
	}
	widths := []float64{14, 36, 34, 18, 28, 12}
	for i, width := range widths {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return err
		}
		if err := file.SetColWidth(sheet, col, col, width); err != nil {
			return err
		}
	}
	headerStyle, err := file.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "1D2925"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"F4F8F6"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center"},
	})
	if err != nil {
		return err
	}
	if err := file.SetCellStyle(sheet, "A1", "F1", headerStyle); err != nil {
		return err
	}
	if err := file.SetPanes(sheet, &excelize.Panes{
		Freeze:      true,
		Split:       false,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft",
	}); err != nil {
		return err
	}
	return file.AutoFilter(sheet, "A1:F1", nil)
}

func writeTaskItemsSheet(file *excelize.File, sheet string, items []biz.AdminTaskItem) error {
	headers := []any{
		"轮次",
		"itemId",
		"平台",
		"问题",
		"结果",
		"回答",
		"录屏",
		"引用数",
		"导入时间",
		"生成时间",
		"设备ID",
		"推荐问题JSON",
		"引用JSON",
		"解析状态",
		"解析错误",
		"思考过程",
	}
	if err := file.SetSheetRow(sheet, "A1", &headers); err != nil {
		return err
	}
	for index, item := range items {
		row := []any{
			item.Seq,
			item.ItemID,
			item.PlatformCode,
			item.Question,
			item.ResultStatus,
			item.AnswerText,
			item.RecordingURL,
			item.ReferenceCount,
			formatTime(item.ResultImportedAt),
			formatTime(item.GeneratedAt),
			item.DeviceID,
			string(defaultJSON(item.RecommendedJSON)),
			string(defaultJSON(item.ReferencesJSON)),
			item.ParseStatus,
			item.ParseError,
			item.ThinkingProcess,
		}
		for i, value := range row {
			if text, ok := value.(string); ok {
				row[i] = truncateExcelCell(text)
			}
		}
		cell, err := excelize.CoordinatesToCellName(1, index+2)
		if err != nil {
			return err
		}
		if err := file.SetSheetRow(sheet, cell, &row); err != nil {
			return err
		}
	}
	widths := []float64{10, 22, 14, 46, 16, 58, 34, 10, 22, 22, 20, 42, 42, 16, 36, 50}
	for i, width := range widths {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return err
		}
		if err := file.SetColWidth(sheet, col, col, width); err != nil {
			return err
		}
	}
	headerStyle, err := file.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "1D2925"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"F4F8F6"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center"},
	})
	if err != nil {
		return err
	}
	if err := file.SetCellStyle(sheet, "A1", "P1", headerStyle); err != nil {
		return err
	}
	if err := file.SetPanes(sheet, &excelize.Panes{
		Freeze:      true,
		Split:       false,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft",
	}); err != nil {
		return err
	}
	return file.AutoFilter(sheet, "A1:P1", nil)
}

func filterTaskItems(items []biz.AdminTaskItem, success bool) []biz.AdminTaskItem {
	out := make([]biz.AdminTaskItem, 0, len(items))
	for _, item := range items {
		if (item.ResultStatus == "IMPORTED") == success {
			out = append(out, item)
		}
	}
	return out
}

func truncateExcelCell(value string) string {
	const maxExcelCellChars = 32767
	if len([]rune(value)) <= maxExcelCellChars {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxExcelCellChars-16]) + "\n...[已截断]"
}

func taskItemsExportFilename(task biz.AdminTaskSummary) string {
	name := strings.TrimSpace(task.BatchNo)
	if name == "" {
		name = fmt.Sprintf("task-%d", task.TaskID)
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	return replacer.Replace(name) + "-问答记录.xlsx"
}

func platformUserReconciliationFilename(export biz.AdminPlatformUserReconciliationExport) string {
	name := strings.TrimSpace(export.User.PlatformCode)
	if name == "" {
		name = fmt.Sprintf("platform-user-%d", export.User.ID)
	}
	from := "start"
	if !export.CreatedFrom.IsZero() {
		from = timezone.InAdminLocation(export.CreatedFrom).Format("20060102")
	}
	to := "end"
	if !export.CreatedTo.IsZero() {
		to = timezone.InAdminLocation(export.CreatedTo).Format("20060102")
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	return fmt.Sprintf("%s-%s-%s-任务对账单.xlsx", replacer.Replace(name), from, to)
}

func (s *AdminService) adminResultFileProto(file biz.AdminResultFile) *adminv1.ResultFile {
	return &adminv1.ResultFile{
		Id:              file.ID,
		SourceType:      file.SourceType,
		ObjectKey:       file.ObjectKey,
		FileUrl:         s.resultFileURL(file),
		FileName:        file.FileName,
		ParseStatus:     file.ParseStatus,
		RowCount:        int32(file.RowCount),
		ValidRowCount:   int32(file.ValidRowCount),
		InvalidRowCount: int32(file.InvalidRowCount),
		CreatedAt:       formatTime(file.CreatedAt),
	}
}

func (s *AdminService) resultFileURL(file biz.AdminResultFile) string {
	if strings.TrimSpace(file.FileURL) != "" {
		return file.FileURL
	}
	if s.ossPublicHost == "" || strings.TrimSpace(file.ObjectKey) == "" {
		return ""
	}
	return s.ossPublicHost + "/" + strings.TrimLeft(file.ObjectKey, "/")
}

func adminTaskEventProto(event biz.AdminTaskEvent) *adminv1.TaskEvent {
	return &adminv1.TaskEvent{
		Id:          event.ID,
		EventType:   event.EventType,
		PayloadJson: string(defaultJSON(event.Payload)),
		CreatedAt:   formatTime(event.CreatedAt),
	}
}

func adminTaskCallbackProto(callback biz.AdminTaskCallback) *adminv1.TaskCallback {
	return &adminv1.TaskCallback{
		Id:                 callback.ID,
		TargetUrl:          callback.TargetURL,
		RequestPayloadJson: string(defaultJSON(callback.RequestPayload)),
		ResponseStatus:     int32(callback.ResponseStatus),
		ResponseBody:       callback.ResponseBody,
		Status:             callback.Status,
		RetryCount:         int32(callback.RetryCount),
		LastError:          callback.LastError,
		CreatedAt:          formatTime(callback.CreatedAt),
		UpdatedAt:          formatTime(callback.UpdatedAt),
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return timezone.InAdminLocation(value).Format(time.RFC3339)
}

func randomHex(size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func sameSite(value string) http.SameSite {
	switch strings.ToLower(value) {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

func defaultJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func applyCheckResultDefaults(raw json.RawMessage, req *adminv1.CheckTaskResultRequest) {
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	if _, ok := keys["force"]; !ok {
		req.Force = true
	}
	if _, ok := keys["notifyCallback"]; !ok {
		req.NotifyCallback = true
	}
}
