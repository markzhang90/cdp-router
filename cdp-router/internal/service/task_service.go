package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	taskv1 "github.com/GitMarkZhang/cdp-router/api/task/v1"
	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/trace"
	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type ProviderTokenLookup interface {
	Token(ctx context.Context, code string) (string, bool)
}

type PlatformUserLookup interface {
	GetByAPIKey(ctx context.Context, apiKey string) (biz.PlatformUser, error)
}

type TaskService struct {
	tasks         *biz.TaskUsecase
	tokens        ProviderTokenLookup
	platformUsers PlatformUserLookup
	bucket        string
}

func NewTaskService(tasks *biz.TaskUsecase, tokens ProviderTokenLookup, platformUsers PlatformUserLookup, bucket string) *TaskService {
	return &TaskService{tasks: tasks, tokens: tokens, platformUsers: platformUsers, bucket: bucket}
}

func (s *TaskService) CreateTask(c *gin.Context) {
	platformUser, ok := s.requirePlatformUser(c)
	if !ok {
		return
	}
	var req taskv1.CreateTaskRequest
	raw, err := bindProto(c, &req)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	items := make([]biz.TaskItem, 0, len(req.Items))
	var rawReq struct {
		Items []json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(raw, &rawReq)
	for i, item := range req.Items {
		var rawItem json.RawMessage
		if i < len(rawReq.Items) {
			rawItem = rawReq.Items[i]
		}
		items = append(items, biz.TaskItem{
			ItemID:       item.ItemId,
			PlatformCode: item.PlatformCode,
			Seq:          int(item.Seq),
			Question:     item.Question,
			Deepthink:    item.Deepthink,
			WebSearch:    item.WebSearch,
			RawItem:      rawItem,
		})
	}
	providerCode := platformUser.DefaultProviderCode
	callbackURL := req.CallbackUrl
	if callbackURL == "" {
		callbackURL = platformUser.CallbackURL
	}
	if callbackURL == "" && strings.ToUpper(platformUser.CallbackMode) != "DEFAULT_HTTP" {
		callbackURL = "custom://" + platformUser.PlatformCode
	}
	source := req.Source
	if source == "" {
		source = platformUser.PlatformCode
	}
	task, acceptedCount, err := s.tasks.CreateTask(c.Request.Context(), biz.CreateTaskInput{
		PlatformUserID: platformUser.ID,
		PlatformCode:   platformUser.PlatformCode,
		BatchNo:        req.BatchNo,
		ProviderCode:   providerCode,
		Source:         source,
		BizDate:        req.BizDate,
		CallbackURL:    callbackURL,
		Items:          items,
		RawRequest:     raw,
	})
	if err != nil {
		slog.WarnContext(c.Request.Context(), "create task failed",
			"trace_id", trace.RequestID(c.Request.Context()),
			"batch_no", req.BatchNo,
			"provider_code", providerCode,
			"platform_code", platformUser.PlatformCode,
			"item_count", len(req.Items),
			"error", err,
		)
		writeBizError(c, err)
		return
	}
	slog.InfoContext(c.Request.Context(), "create task accepted",
		"trace_id", trace.RequestID(c.Request.Context()),
		"task_id", task.ID,
		"job_id", task.JobID,
		"batch_no", task.BatchNo,
		"provider_code", task.ProviderCode,
		"platform_code", task.PlatformCode,
		"item_count", len(req.Items),
	)
	protoJSON(c, http.StatusOK, &taskv1.CreateTaskResponse{
		Code:    0,
		Message: "success",
		Data: &taskv1.CreateTaskData{
			ExternalBatchId: task.JobID,
			AcceptedCount:   int32(acceptedCount),
		},
	})
}

func (s *TaskService) GetTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	task, err := s.tasks.GetTask(c.Request.Context(), id)
	if err != nil {
		writeBizError(c, err)
		return
	}
	protoJSON(c, http.StatusOK, &taskv1.GetTaskResponse{
		Status: 0,
		Data: &taskv1.TaskData{
			TaskId:                  task.ID,
			BatchNo:                 task.BatchNo,
			ProviderCode:            task.ProviderCode,
			ExternalBatchId:         task.JobID,
			Status:                  string(task.Status),
			TotalCount:              int32(task.TotalCount),
			SuccessCount:            int32(task.SuccessCount),
			FailedCount:             int32(task.FailedCount),
			ResultObjectKey:         task.ResultObjectKey,
			LastError:               task.LastError,
			ResultRowCount:          int32(task.ResultRowCount),
			Source:                  task.Source,
			ProviderExternalBatchId: task.ProviderExternalBatchID,
			ResultUrl:               s.tasks.ResultURL(task.JobID),
		},
	})
}

func (s *TaskService) GetExternalTask(c *gin.Context) {
	platformUser, ok := s.requirePlatformUser(c)
	if !ok {
		return
	}
	task, err := s.tasks.GetExternalTask(c.Request.Context(), c.Param("externalBatchId"), platformUser.ID)
	if err != nil {
		writeBizError(c, err)
		return
	}
	protoJSON(c, http.StatusOK, &taskv1.GetTaskResponse{
		Status: 0,
		Data: &taskv1.TaskData{
			TaskId:                  task.ID,
			BatchNo:                 task.BatchNo,
			ProviderCode:            task.ProviderCode,
			ExternalBatchId:         task.JobID,
			Status:                  string(task.Status),
			TotalCount:              int32(task.TotalCount),
			SuccessCount:            int32(task.SuccessCount),
			FailedCount:             int32(task.FailedCount),
			ResultObjectKey:         task.ResultObjectKey,
			LastError:               task.LastError,
			ResultRowCount:          int32(task.ResultRowCount),
			Source:                  task.Source,
			ProviderExternalBatchId: task.ProviderExternalBatchID,
			ResultUrl:               s.tasks.ResultURL(task.JobID),
		},
	})
}

func (s *TaskService) GetExternalTaskResult(c *gin.Context) {
	platformUser, ok := s.requirePlatformUser(c)
	if !ok {
		return
	}
	task, items, err := s.tasks.GetExternalTaskResults(c.Request.Context(), c.Param("externalBatchId"), platformUser.ID)
	if err != nil {
		writeBizError(c, err)
		return
	}
	successOnly := strings.EqualFold(c.Query("successOnly"), "true") || c.Query("successOnly") == "1"
	resultURL := s.tasks.ResultURL(task.JobID)
	if successOnly {
		resultURL += "?successOnly=true"
	}
	resultItems := make([]*taskv1.ExternalTaskResultItem, 0, len(items))
	for _, item := range items {
		if successOnly && item.ParseStatus != "SUCCESS" {
			continue
		}
		resultItems = append(resultItems, &taskv1.ExternalTaskResultItem{
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
			RecommendedQuestionsJson: string(defaultJSON(item.RecommendedQuestions)),
			ReferencesJson:           string(defaultJSON(item.ReferencesJSON)),
			ReferenceCount:           int32(item.ReferenceCount),
			ParseStatus:              item.ParseStatus,
			ParseError:               item.ParseError,
		})
	}
	protoJSON(c, http.StatusOK, &taskv1.GetExternalTaskResultResponse{
		Status: 0,
		Data: &taskv1.ExternalTaskResultData{
			ExternalBatchId: task.JobID,
			BatchNo:         task.BatchNo,
			TaskStatus:      string(task.Status),
			ResultUrl:       resultURL,
			TotalCount:      int32(task.TotalCount),
			SuccessCount:    externalResultSuccessCount(task, resultItems, successOnly),
			FailedCount:     externalResultFailedCount(task, successOnly),
			Items:           resultItems,
		},
	})
}

func externalResultSuccessCount(task *biz.Task, items []*taskv1.ExternalTaskResultItem, successOnly bool) int32 {
	if successOnly {
		return int32(len(items))
	}
	return int32(task.SuccessCount)
}

func externalResultFailedCount(task *biz.Task, successOnly bool) int32 {
	if successOnly {
		return 0
	}
	return int32(task.FailedCount)
}

func (s *TaskService) GetProviderStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	result, err := s.tasks.GetProviderStatus(c.Request.Context(), id)
	if err != nil {
		writeBizError(c, err)
		return
	}
	questions := make([]*taskv1.ProviderQuestionStatus, 0, len(result.Questions))
	for _, question := range result.Questions {
		questions = append(questions, &taskv1.ProviderQuestionStatus{
			QuestionId: question.QuestionID,
			Status:     int32(question.Status),
		})
	}
	protoJSON(c, http.StatusOK, &taskv1.GetProviderStatusResponse{
		Status:  0,
		Message: providerStatusHint(),
		Data: &taskv1.ProviderStatusData{
			ExternalBatchId: result.ExternalBatchID,
			Questions:       questions,
		},
	})
}

func (s *TaskService) GetProviderResultFiles(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	result, err := s.tasks.GetProviderResultFiles(c.Request.Context(), id)
	if err != nil {
		writeBizError(c, err)
		return
	}
	protoJSON(c, http.StatusOK, &taskv1.GetProviderResultFilesResponse{
		Status:  0,
		Message: providerFilesMessage(len(result.FileURLs)),
		Data: &taskv1.ProviderResultFilesData{
			ExternalBatchId: result.ExternalBatchID,
			FileUrls:        result.FileURLs,
		},
	})
}

func (s *TaskService) RefetchTaskResult(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	var req taskv1.RefetchTaskResultRequest
	if _, err := bindOptionalProto(c, &req); err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	notifyCallback := req.NotifyCallback
	task, fileCount, rowCount, err := s.tasks.RefetchProviderResults(c.Request.Context(), id, req.Force, notifyCallback)
	if err != nil {
		slog.WarnContext(c.Request.Context(), "refetch task result failed",
			"trace_id", trace.RequestID(c.Request.Context()),
			"task_id", id,
			"force", req.Force,
			"notify_callback", notifyCallback,
			"error", err,
		)
		writeBizError(c, err)
		return
	}
	slog.InfoContext(c.Request.Context(), "refetch task result completed",
		"trace_id", trace.RequestID(c.Request.Context()),
		"task_id", task.ID,
		"batch_no", task.BatchNo,
		"file_count", fileCount,
		"row_count", rowCount,
		"notify_callback", notifyCallback,
	)
	protoJSON(c, http.StatusOK, &taskv1.RefetchTaskResultResponse{
		Status: 0,
		Data: &taskv1.RefetchTaskResultData{
			TaskId:    task.ID,
			BatchNo:   task.BatchNo,
			FileCount: int32(fileCount),
			RowCount:  int32(rowCount),
		},
	})
}

func (s *TaskService) SyncProviderResult(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	var req taskv1.SyncProviderResultRequest
	if _, err := bindOptionalProto(c, &req); err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	outcome, err := s.tasks.SyncProviderResult(c.Request.Context(), id, req.Force, req.NotifyCallback)
	if err != nil {
		slog.WarnContext(c.Request.Context(), "sync provider result failed",
			"trace_id", trace.RequestID(c.Request.Context()),
			"task_id", id,
			"force", req.Force,
			"notify_callback", req.NotifyCallback,
			"error", err,
		)
		writeBizError(c, err)
		return
	}
	task := outcome.Task
	slog.InfoContext(c.Request.Context(), "sync provider result completed",
		"trace_id", trace.RequestID(c.Request.Context()),
		"task_id", task.ID,
		"batch_no", task.BatchNo,
		"provider_done", outcome.ProviderDone,
		"file_count", outcome.FileCount,
		"row_count", outcome.RowCount,
		"msg", outcome.Msg,
		"notify_callback", req.NotifyCallback,
	)
	protoJSON(c, http.StatusOK, &taskv1.SyncProviderResultResponse{
		Status:  0,
		Message: outcome.Msg,
		Data: &taskv1.SyncProviderResultData{
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

func providerStatusHint() string {
	return "查询成功；供应商问题状态：0=待处理，1=处理中，2=处理完成。只有全部问题 status=2 后才会查询并导入结果文件。"
}

func providerFilesMessage(fileCount int) string {
	if fileCount == 0 {
		return "未查询到结果文件；供应商任务未全部完成时 fileUrls 为空属于预期，请先检查 provider-status 是否全部为 status=2。"
	}
	return "查询成功"
}

func (s *TaskService) GetUploadURL(c *gin.Context) {
	providerCode := c.Param("providerCode")
	if !s.checkProviderToken(c, providerCode) {
		return
	}
	var req taskv1.GetUploadURLRequest
	if _, err := bindProto(c, &req); err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	if req.ProviderCode == "" {
		req.ProviderCode = providerCode
	}
	upload, err := s.tasks.IssueUploadURL(c.Request.Context(), req.ProviderCode, req.BatchNo, req.FileName)
	if err != nil {
		slog.WarnContext(c.Request.Context(), "issue upload url failed",
			"trace_id", trace.RequestID(c.Request.Context()),
			"batch_no", req.BatchNo,
			"provider_code", req.ProviderCode,
			"file_name", req.FileName,
			"error", err,
		)
		writeBizError(c, err)
		return
	}
	slog.InfoContext(c.Request.Context(), "upload url issued",
		"trace_id", trace.RequestID(c.Request.Context()),
		"batch_no", req.BatchNo,
		"provider_code", req.ProviderCode,
		"file_name", req.FileName,
		"object_key", upload.Key,
		"expires_at", upload.ExpiresAt.Format(time.RFC3339),
	)
	protoJSON(c, http.StatusOK, &taskv1.GetUploadURLResponse{
		Status: 0,
		Data: &taskv1.UploadURLData{
			Bucket:    s.bucket,
			Key:       upload.Key,
			UploadUrl: upload.UploadURL,
			Method:    upload.Method,
			ExpiresAt: upload.ExpiresAt.Format(time.RFC3339),
			Headers:   upload.Headers,
		},
	})
}

func (s *TaskService) ProviderCallback(c *gin.Context) {
	providerCode := c.Param("providerCode")
	if !s.checkProviderToken(c, providerCode) {
		return
	}
	var req taskv1.ProviderCallbackRequest
	raw, err := bindProto(c, &req)
	if err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	if req.ProviderCode == "" {
		req.ProviderCode = providerCode
	}
	var finishedAt time.Time
	if req.FinishedAt != "" {
		finishedAt, _ = time.Parse(time.RFC3339, req.FinishedAt)
	}
	task, err := s.tasks.HandleProviderCallback(c.Request.Context(), biz.ProviderCallbackInput{
		ProviderCode:    req.ProviderCode,
		BatchNo:         req.BatchNo,
		ExternalBatchID: req.ExternalBatchId,
		Status:          req.Status,
		TotalCount:      int(req.TotalCount),
		SuccessCount:    int(req.SuccessCount),
		FailedCount:     int(req.FailedCount),
		FinishedAt:      finishedAt,
		RawRequest:      raw,
	})
	if err != nil {
		slog.WarnContext(c.Request.Context(), "provider callback failed",
			"trace_id", trace.RequestID(c.Request.Context()),
			"batch_no", req.BatchNo,
			"provider_code", req.ProviderCode,
			"external_batch_id", req.ExternalBatchId,
			"status", req.Status,
			"error", err,
		)
		writeBizError(c, err)
		return
	}
	slog.InfoContext(c.Request.Context(), "provider callback accepted",
		"trace_id", trace.RequestID(c.Request.Context()),
		"task_id", task.ID,
		"batch_no", task.BatchNo,
		"provider_code", task.ProviderCode,
		"external_batch_id", req.ExternalBatchId,
		"status", req.Status,
		"total_count", req.TotalCount,
		"success_count", req.SuccessCount,
		"failed_count", req.FailedCount,
	)
	protoJSON(c, http.StatusOK, &taskv1.ProviderCallbackResponse{
		Status: 0,
		Data: &taskv1.ProviderCallbackData{
			BatchNo:  task.BatchNo,
			Accepted: true,
		},
	})
}

func (s *TaskService) checkProviderToken(c *gin.Context, code string) bool {
	token, ok := s.tokens.Token(c.Request.Context(), code)
	if !ok || token == "" || c.GetHeader("X-Provider-Token") != token {
		errorJSON(c, http.StatusUnauthorized, errors.New("invalid provider token"))
		return false
	}
	return true
}

func (s *TaskService) requirePlatformUser(c *gin.Context) (biz.PlatformUser, bool) {
	if s.platformUsers == nil {
		errorJSON(c, http.StatusServiceUnavailable, errors.New("platform user registry unavailable"))
		return biz.PlatformUser{}, false
	}
	apiKey := platformAPIKey(c)
	if apiKey == "" {
		errorJSON(c, http.StatusUnauthorized, errors.New("missing X-API-Key or X-Provider-Token"))
		return biz.PlatformUser{}, false
	}
	user, err := s.platformUsers.GetByAPIKey(c.Request.Context(), apiKey)
	if err != nil {
		writeBizError(c, err)
		return biz.PlatformUser{}, false
	}
	return user, true
}

func platformAPIKey(c *gin.Context) string {
	if value := strings.TrimSpace(c.GetHeader("X-Provider-Token")); value != "" {
		return value
	}
	return strings.TrimSpace(c.GetHeader("X-API-Key"))
}

func bindProto(c *gin.Context, msg proto.Message) (json.RawMessage, error) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(body, msg); err != nil {
		return nil, err
	}
	return body, nil
}

func bindOptionalProto(c *gin.Context, msg proto.Message) (json.RawMessage, error) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return body, nil
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(body, msg); err != nil {
		return nil, err
	}
	return body, nil
}

func protoJSON(c *gin.Context, status int, msg proto.Message) {
	body, err := protojson.MarshalOptions{UseProtoNames: false, EmitUnpopulated: true}.Marshal(msg)
	if err != nil {
		errorJSON(c, http.StatusInternalServerError, err)
		return
	}
	c.Data(status, "application/json; charset=utf-8", body)
}

func writeBizError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, biz.ErrTaskNotFound):
		errorJSON(c, http.StatusNotFound, err)
	case errors.Is(err, biz.ErrProviderNotFound):
		errorJSON(c, http.StatusBadRequest, err)
	case errors.Is(err, biz.ErrPlatformUserNotFound), errors.Is(err, biz.ErrPlatformUserDisabled):
		errorJSON(c, http.StatusUnauthorized, err)
	case errors.Is(err, biz.ErrPlatformUserConflict):
		errorJSON(c, http.StatusConflict, err)
	case errors.Is(err, biz.ErrInvalidTaskRequest):
		errorJSON(c, http.StatusBadRequest, err)
	default:
		errorJSON(c, http.StatusInternalServerError, err)
	}
}

func errorJSON(c *gin.Context, status int, err error) {
	c.JSON(status, gin.H{
		"status":  status,
		"message": err.Error(),
		"data":    nil,
	})
}
