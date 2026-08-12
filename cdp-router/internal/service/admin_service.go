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
	itemFilter := biz.AdminTaskItemFilter{
		Page:     parsePositiveInt(c.Query("itemPage"), 1),
		PageSize: parsePositiveInt(c.Query("itemPageSize"), 20),
	}
	detail, err := s.repo.GetTaskDetail(c.Request.Context(), id, itemFilter)
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
			Task:         adminTaskSummaryProto(detail.Task),
			Items:        items,
			Files:        files,
			Events:       events,
			Callbacks:    callbacks,
			ItemTotal:    int32(detail.ItemTotal),
			ItemPage:     int32(detail.ItemPage),
			ItemPageSize: int32(detail.ItemSize),
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

func (s *AdminService) ListProviders(c *gin.Context) {
	providers := s.providerList(false)
	protoJSON(c, http.StatusOK, &adminv1.ListProvidersResponse{
		Status: 0,
		Data: &adminv1.ProviderListData{
			Providers: providers,
		},
	})
}

func (s *AdminService) GetDispatchOptions(c *gin.Context) {
	protoJSON(c, http.StatusOK, &adminv1.GetDispatchOptionsResponse{
		Status: 0,
		Data: &adminv1.DispatchOptionsData{
			Strategies: []*adminv1.DispatchStrategyOption{
				{Code: string(biz.DispatchStrategyRoundRobin), Name: "Round Robin"},
			},
			Providers: s.providerList(true),
		},
	})
}

func (s *AdminService) providerList(onlyDispatchAvailable bool) []*adminv1.Provider {
	providers := make([]*adminv1.Provider, 0, len(s.providers))
	codes := make([]string, 0, len(s.providers))
	for code := range s.providers {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		provider := s.providers[code]
		if onlyDispatchAvailable && !dispatchAvailableProvider(provider) {
			continue
		}
		providers = append(providers, providerProto(provider))
	}
	return providers
}

func dispatchAvailableProvider(provider config.ProviderConfig) bool {
	return strings.TrimSpace(provider.Code) != "" &&
		strings.TrimSpace(provider.SubmitURL) != "" &&
		provider.QPSLimit > 0 &&
		provider.ConcurrencyLimit > 0
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
	protoJSON(c, http.StatusOK, &adminv1.GetProviderMetricsResponse{
		Status: 0,
		Data:   providerMetricsProto(providerConfig, metrics),
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
		BatchNo:      c.Query("batchNo"),
		Source:       c.Query("source"),
		PlatformCode: c.Query("platformCode"),
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
		createdTo = time.Now().In(timezone.AdminLocation())
		createdFrom = createdTo.Add(-24 * time.Hour)
	}
	if !createdFrom.IsZero() && !createdTo.IsZero() && createdTo.Before(createdFrom) {
		return biz.AdminPlatformUserMetricsFilter{}, fmt.Errorf("createdTo must be after createdFrom")
	}
	return biz.AdminPlatformUserMetricsFilter{
		CreatedFrom: createdFrom,
		CreatedTo:   createdTo,
	}, nil
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
			Code:          strings.TrimSpace(dispatch.Code),
			Name:          strings.TrimSpace(dispatch.Name),
			Strategy:      biz.DispatchStrategy(strings.TrimSpace(strings.ToUpper(dispatch.Strategy))),
			Enabled:       dispatch.Enabled,
			ProviderCodes: append([]string{}, dispatch.ProviderCodes...),
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
	if input.DispatchProfile.Strategy != biz.DispatchStrategyRoundRobin {
		return biz.AdminPlatformUserInput{}, fmt.Errorf("dispatchProfile.strategy must be ROUND_ROBIN")
	}
	return input, nil
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
		Progress:                 task.Progress,
		CreatedAt:                formatTime(task.CreatedAt),
		UpdatedAt:                formatTime(task.UpdatedAt),
		ExecutionDurationSeconds: durationSeconds,
		ExecutionDurationText:    durationText,
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
		Id:               profile.ID,
		Code:             profile.Code,
		Name:             profile.Name,
		Strategy:         string(profile.Strategy),
		Enabled:          profile.Enabled,
		Cursor:           profile.Cursor,
		LastProviderCode: profile.LastProviderCode,
		ProviderCodes:    append([]string{}, profile.ProviderCodes...),
	}
}

func providerProto(provider config.ProviderConfig) *adminv1.Provider {
	return &adminv1.Provider{
		ProviderCode:        provider.Code,
		DisplayName:         provider.DisplayName,
		Enabled:             provider.Enabled,
		ResultMode:          provider.ResultMode,
		TimeoutSeconds:      int32FromInt(int(provider.Timeout.Seconds())),
		QpsLimit:            int32FromInt(provider.QPSLimit),
		ConcurrencyLimit:    int32FromInt(provider.ConcurrencyLimit),
		BucketPathPrefix:    provider.BucketPathPrefix,
		SubmitUrlConfigured: strings.TrimSpace(provider.SubmitURL) != "",
		StatusUrlConfigured: strings.TrimSpace(provider.StatusURL) != "",
		FilesUrlConfigured:  strings.TrimSpace(provider.FilesURL) != "",
	}
}

func providerMetricsProto(provider config.ProviderConfig, metrics biz.AdminProviderMetrics) *adminv1.ProviderMetricsData {
	return &adminv1.ProviderMetricsData{
		Provider:    providerProto(provider),
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
