package biz

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/pkg/trace"
)

type TaskUsecase struct {
	validationRules         *ValidationRuleCache
	repo                    TaskRepo
	providers               ProviderRegistry
	providerConfig          ProviderSettingsRepo
	store                   ObjectStore
	parser                  ResultParser
	callback                CallbackClient
	publicBaseURL           string
	ossUploadDir            string
	ossPublicHosts          []string
	ossExpire               time.Duration
	tempObjectRetentionDays int
	callbackMaxTry          int
}

type TaskUsecaseOptions struct {
	ValidationRules         *ValidationRuleCache
	PublicBaseURL           string
	OSSUploadDir            string
	OSSPublicHosts          []string
	OSSExpire               time.Duration
	TempObjectRetentionDays int
	CallbackMaxTry          int
}

func NewTaskUsecase(repo TaskRepo, providers ProviderRegistry, providerConfig ProviderSettingsRepo, store ObjectStore, parser ResultParser, callback CallbackClient, opts TaskUsecaseOptions) *TaskUsecase {
	if opts.ValidationRules == nil {
		if rulesRepo, ok := repo.(ValidationRuleRepo); ok {
			opts.ValidationRules = NewValidationRuleCache(rulesRepo, time.Minute)
		}
	}
	retentionDays := opts.TempObjectRetentionDays
	if retentionDays <= 0 {
		retentionDays = 7
	}
	return &TaskUsecase{
		validationRules:         opts.ValidationRules,
		repo:                    repo,
		providers:               providers,
		providerConfig:          providerConfig,
		store:                   store,
		parser:                  parser,
		callback:                callback,
		publicBaseURL:           strings.TrimRight(opts.PublicBaseURL, "/"),
		ossUploadDir:            strings.Trim(opts.OSSUploadDir, "/"),
		ossPublicHosts:          normalizeURLHosts(opts.OSSPublicHosts),
		ossExpire:               opts.OSSExpire,
		tempObjectRetentionDays: retentionDays,
		callbackMaxTry:          opts.CallbackMaxTry,
	}
}

func (u *TaskUsecase) CreateTask(ctx context.Context, input CreateTaskInput) (*Task, int, error) {
	if input.BatchNo == "" || input.BizDate == "" || input.CallbackURL == "" || len(input.Items) == 0 {
		return nil, 0, ErrInvalidTaskRequest
	}
	if input.PlatformUserID <= 0 || input.PlatformCode == "" {
		return nil, 0, ErrPlatformUserNotFound
	}
	if input.JobID == "" {
		input.JobID = newJobID()
	}
	task, err := u.repo.CreateTask(ctx, input, u.providerDispatchSettings(ctx), u.publicBaseURL)
	if err != nil {
		return nil, 0, err
	}
	if task.Status != TaskStatusCreated || task.ProviderExternalBatchID != "" {
		return task, task.TotalCount, nil
	}
	result, err := u.submitOne(ctx, task)
	if err != nil {
		return nil, 0, err
	}
	updated, err := u.repo.GetTask(ctx, task.ID)
	if err != nil {
		return nil, 0, err
	}
	return updated, result.AcceptedCount, nil
}

func (u *TaskUsecase) ReassignTask(ctx context.Context, input ReassignTaskInput) (ReassignTaskOutcome, error) {
	input.BatchNo = strings.TrimSpace(input.BatchNo)
	input.ProviderCode = strings.TrimSpace(input.ProviderCode)
	if input.BatchNo == "" || input.ProviderCode == "" {
		return ReassignTaskOutcome{}, ErrInvalidTaskRequest
	}
	if _, ok := u.providers.Get(input.ProviderCode); !ok {
		return ReassignTaskOutcome{}, ErrProviderNotFound
	}
	if !u.providerReassignEnabled(ctx, input.ProviderCode) {
		return ReassignTaskOutcome{}, ErrProviderUnavailable
	}
	target, err := u.repo.CreateReassignedTask(ctx, ReassignTaskRecordInput{
		SourceBatchNo:  input.BatchNo,
		TargetBatchNo:  newReassignBatchNo(input.BatchNo),
		TargetJobID:    newJobID(),
		ProviderCode:   input.ProviderCode,
		Reason:         input.Reason,
		OperatorUserID: input.OperatorUserID,
	}, u.publicBaseURL)
	if err != nil {
		return ReassignTaskOutcome{}, err
	}
	result, err := u.submitOneWithBatchNo(ctx, target, target.CallbackBatchNo)
	if err != nil {
		_ = u.repo.AddEvent(ctx, target.ReassignedFromTaskID, EventTaskReassignFailed, map[string]any{
			"targetTaskId":       target.ID,
			"targetBatchNo":      target.BatchNo,
			"originalBatchNo":    target.CallbackBatchNo,
			"targetProviderCode": target.ProviderCode,
			"operatorUserId":     input.OperatorUserID,
			"reason":             input.Reason,
			"error":              err.Error(),
		})
		return ReassignTaskOutcome{Target: target}, err
	}
	payload := map[string]any{
		"sourceTaskId":            target.ReassignedFromTaskID,
		"targetTaskId":            target.ID,
		"targetBatchNo":           target.BatchNo,
		"originalBatchNo":         target.CallbackBatchNo,
		"callbackExternalBatchId": target.CallbackExternalBatchID,
		"targetProviderCode":      target.ProviderCode,
		"providerExternalBatchId": result.ExternalBatchID,
		"acceptedCount":           result.AcceptedCount,
		"operatorUserId":          input.OperatorUserID,
		"reason":                  input.Reason,
	}
	if err := u.repo.MarkTaskReassigned(ctx, target.ReassignedFromTaskID, target.ID, payload); err != nil {
		return ReassignTaskOutcome{Target: target, AcceptedCount: result.AcceptedCount}, err
	}
	source, err := u.repo.GetTask(ctx, target.ReassignedFromTaskID)
	if err != nil {
		return ReassignTaskOutcome{Target: target, AcceptedCount: result.AcceptedCount}, nil
	}
	updatedTarget, err := u.repo.GetTask(ctx, target.ID)
	if err != nil {
		updatedTarget = target
	}
	return ReassignTaskOutcome{Source: source, Target: updatedTarget, AcceptedCount: result.AcceptedCount}, nil
}

func (u *TaskUsecase) providerReassignEnabled(ctx context.Context, providerCode string) bool {
	if u.providerConfig == nil {
		return true
	}
	settings, ok := u.providerConfig.GetSettings(ctx, providerCode)
	if !ok {
		return true
	}
	return settings.Enabled
}

func (u *TaskUsecase) providerDispatchSettings(ctx context.Context) []ProviderSettings {
	codes := u.providers.Codes()
	settings := make([]ProviderSettings, 0, len(codes))
	for _, code := range codes {
		item := ProviderSettings{ProviderCode: code, Enabled: true}
		if u.providerConfig != nil {
			if providerSettings, ok := u.providerConfig.GetSettings(ctx, code); ok {
				item = providerSettings
			}
		}
		if item.ProviderCode == "" {
			item.ProviderCode = code
		}
		settings = append(settings, item)
	}
	return settings
}

func (u *TaskUsecase) GetTask(ctx context.Context, id int64) (*Task, error) {
	return u.repo.GetTask(ctx, id)
}

func (u *TaskUsecase) GetExternalTask(ctx context.Context, jobID string, platformUserID int64) (*Task, error) {
	return u.repo.GetTaskByJobID(ctx, jobID, platformUserID)
}

func (u *TaskUsecase) GetExternalTaskResults(ctx context.Context, jobID string, platformUserID int64) (*Task, []TaskItemResultView, error) {
	task, err := u.repo.GetTaskByJobID(ctx, jobID, platformUserID)
	if err != nil {
		return nil, nil, err
	}
	items, err := u.repo.ListTaskItemResults(ctx, task.ID)
	if err != nil {
		return nil, nil, err
	}
	return task, items, nil
}

func (u *TaskUsecase) IssueUploadURL(ctx context.Context, providerCode, batchNo, fileName string) (*UploadURL, error) {
	if fileName == "" {
		fileName = "result.xlsx"
	}
	task, err := u.repo.ResolveProviderTask(ctx, providerCode, batchNo)
	if err != nil {
		return nil, err
	}
	if task.ProviderCode != providerCode {
		return nil, ErrProviderNotFound
	}
	providerBatchNo := taskProviderBatchNo(task)
	key := u.resultUploadObjectKey(ctx, providerCode, providerBatchNo, fileName)
	uploadURL, expiresAt, err := u.store.SignedPutURL(ctx, key, u.ossExpire)
	if err != nil {
		return nil, err
	}
	if err := u.repo.SaveUploadObjectKey(ctx, task.ID, key, fileName); err != nil {
		return nil, err
	}
	_ = u.repo.AddEvent(ctx, task.ID, EventUploadURLIssued, map[string]any{
		"key":      key,
		"fileName": fileName,
	})
	slog.InfoContext(ctx, "task upload object key saved",
		"trace_id", trace.RequestID(ctx),
		"task_id", task.ID,
		"batch_no", providerBatchNo,
		"provider_code", providerCode,
		"object_key", key,
		"file_name", fileName,
	)
	return &UploadURL{
		Key:       key,
		UploadURL: uploadURL,
		Method:    "PUT",
		ExpiresAt: expiresAt,
		Headers:   map[string]string{},
	}, nil
}

func (u *TaskUsecase) CreateTemporaryDownloadFile(ctx context.Context, fileName string, contentType string, content []byte) (*TemporaryDownloadFile, error) {
	if strings.TrimSpace(fileName) == "" {
		fileName = "download.xlsx"
	}
	key := u.temporaryDownloadObjectKey(fileName)
	if err := u.store.PutTemporaryObject(ctx, key, contentType, content, u.tempObjectRetentionDays); err != nil {
		return nil, err
	}
	downloadURL, expiresAt, err := u.store.SignedGetURL(ctx, key, time.Duration(u.tempObjectRetentionDays)*24*time.Hour)
	if err != nil {
		return nil, err
	}
	return &TemporaryDownloadFile{
		Key:         key,
		DownloadURL: downloadURL,
		ExpiresAt:   expiresAt,
	}, nil
}

func (u *TaskUsecase) HandleProviderCallback(ctx context.Context, input ProviderCallbackInput) (*Task, error) {
	receivedTask, lookupErr := u.repo.ResolveProviderTask(ctx, input.ProviderCode, input.BatchNo)
	if lookupErr == nil && !taskProcessingLocked(ctx, receivedTask.ID) {
		var task *Task
		err := u.withTaskLock(ctx, receivedTask.ID, func(ctx context.Context) error {
			var err error
			task, err = u.HandleProviderCallback(ctx, input)
			return err
		})
		return task, err
	}
	receivedStatus := TaskStatus("")
	expectedCount := 0
	importedPendingFiles := 0
	if lookupErr == nil {
		input.TaskID = receivedTask.ID
		receivedStatus = receivedTask.Status
		_ = u.repo.AddEvent(ctx, receivedTask.ID, EventProviderCallbackRecv, providerCallbackEventPayload(input, nil))
		count, err := u.taskItemCount(ctx, receivedTask.ID)
		if err != nil {
			return nil, err
		}
		expectedCount = count
		successFiles, _, err := u.importCallbackPendingFiles(ctx, receivedTask)
		if err != nil {
			_ = u.repo.AddEvent(ctx, receivedTask.ID, EventProviderCallbackFail, providerCallbackEventPayload(input, map[string]any{
				"error": err.Error(),
			}))
			return nil, err
		}
		importedPendingFiles += successFiles
		if successFiles > 0 {
			if refreshedTask, err := u.repo.GetTask(ctx, receivedTask.ID); err == nil {
				receivedTask = refreshedTask
				receivedStatus = refreshedTask.Status
			}
		}
	}
	if lookupErr == nil && (receivedStatus == TaskStatusResultImported || receivedStatus == TaskStatusCallbackPending || receivedStatus == TaskStatusDone) {
		if err := u.reconcileProviderCallbackFiles(ctx, receivedTask, input); err != nil {
			return nil, err
		}

		task, err := u.repo.MarkProviderCallback(ctx, input)
		if err != nil {
			_ = u.repo.AddEvent(ctx, receivedTask.ID, EventProviderCallbackFail, providerCallbackEventPayload(input, map[string]any{
				"error": err.Error(),
			}))
			return nil, err
		}
		if importedPendingFiles > 0 {
			if updatedTask, _, err := u.repo.CompleteResultImport(u.validationContext(ctx), task.ID, true); err != nil {
				_ = u.repo.AddEvent(ctx, receivedTask.ID, EventProviderCallbackFail, providerCallbackEventPayload(input, map[string]any{
					"error": err.Error(),
				}))
				return nil, err
			} else {
				task = updatedTask
			}
		}
		_ = u.repo.AddEvent(ctx, task.ID, EventProviderCallback, providerCallbackEventPayload(input, map[string]any{
			"skipped":              "task already imported, callback pending, or done",
			"importedPendingFiles": importedPendingFiles,
		}))
		return task, nil
	}
	if !providerCallbackCountsComplete(input, expectedCount) {
		if lookupErr != nil {
			return nil, lookupErr
		}
		task, err := u.repo.MarkProviderCallbackProgress(ctx, input)
		if err != nil {
			_ = u.repo.AddEvent(ctx, receivedTask.ID, EventProviderCallbackFail, providerCallbackEventPayload(input, map[string]any{
				"error": err.Error(),
			}))
			return nil, err
		}
		_ = u.repo.AddEvent(ctx, task.ID, EventProviderCallbackProg, providerCallbackEventPayload(input, map[string]any{
			"expectedCount": expectedCount,
			"doneCount":     callbackDoneCount(input),
		}))
		return task, nil
	}
	if lookupErr == nil {
		if err := u.reconcileProviderCallbackFiles(ctx, receivedTask, input); err != nil {
			_ = u.repo.AddEvent(ctx, receivedTask.ID, EventProviderCallbackFail, providerCallbackEventPayload(input, map[string]any{
				"error": err.Error(),
			}))
			return nil, err
		}
		successFiles, _, err := u.importCallbackPendingFiles(ctx, receivedTask)
		if err != nil {
			_ = u.repo.AddEvent(ctx, receivedTask.ID, EventProviderCallbackFail, providerCallbackEventPayload(input, map[string]any{
				"error": err.Error(),
			}))
			return nil, err
		}
		importedPendingFiles += successFiles
	}
	task, err := u.repo.MarkProviderCallback(ctx, input)
	if err != nil {
		if lookupErr == nil {
			_ = u.repo.AddEvent(ctx, receivedTask.ID, EventProviderCallbackFail, providerCallbackEventPayload(input, map[string]any{
				"error": err.Error(),
			}))
		}
		return nil, err
	}
	if importedPendingFiles > 0 {
		if updatedTask, _, err := u.repo.CompleteResultImport(u.validationContext(ctx), task.ID, true); err != nil {
			_ = u.repo.AddEvent(ctx, task.ID, EventProviderCallbackFail, providerCallbackEventPayload(input, map[string]any{
				"error": err.Error(),
			}))
			return nil, err
		} else {
			task = updatedTask
		}
	}
	_ = u.repo.AddEvent(ctx, task.ID, EventProviderCallback, providerCallbackEventPayload(input, map[string]any{
		"importedPendingFiles": importedPendingFiles,
	}))
	slog.InfoContext(ctx, "task marked provider done",
		"trace_id", trace.RequestID(ctx),
		"task_id", task.ID,
		"batch_no", task.BatchNo,
		"provider_code", input.ProviderCode,
		"external_batch_id", input.ExternalBatchID,
		"status", input.Status,
		"total_count", input.TotalCount,
		"success_count", input.SuccessCount,
		"failed_count", input.FailedCount,
	)
	return task, nil
}

func (u *TaskUsecase) importCallbackPendingFiles(ctx context.Context, task *Task) (int, int, error) {
	pendingFiles, err := u.repo.ListPendingResultFiles(ctx, task.ID)
	if err != nil {
		return 0, 0, err
	}
	if len(pendingFiles) == 0 {
		return 0, 0, nil
	}
	_, successFiles, totalRows, err := u.importResultFileInputsOnly(ctx, task, pendingFiles, true, "PROVIDER_CALLBACK_PENDING_FILE")
	return successFiles, totalRows, err
}

func (u *TaskUsecase) RecordProviderCallbackRejected(ctx context.Context, input ProviderCallbackInput, errMessage string, extra map[string]any) error {
	task, err := u.repo.ResolveProviderTask(ctx, input.ProviderCode, input.BatchNo)
	if err != nil {
		return err
	}
	input.TaskID = task.ID
	payloadExtra := map[string]any{
		"error":    errMessage,
		"rejected": true,
	}
	for key, value := range extra {
		payloadExtra[key] = value
	}
	if err := u.repo.AddEvent(ctx, task.ID, EventProviderCallbackRecv, providerCallbackEventPayload(input, payloadExtra)); err != nil {
		return err
	}
	return u.repo.AddEvent(ctx, task.ID, EventProviderCallbackFail, providerCallbackEventPayload(input, payloadExtra))
}

func providerCallbackCountsComplete(input ProviderCallbackInput, expectedCount int) bool {
	if expectedCount <= 0 {
		return false
	}
	totalCount := input.TotalCount
	if totalCount <= 0 {
		totalCount = expectedCount
	}
	if totalCount != expectedCount {
		return false
	}
	return callbackDoneCount(input) == expectedCount
}

func callbackDoneCount(input ProviderCallbackInput) int {
	successCount := input.SuccessCount
	if successCount < 0 {
		successCount = 0
	}
	failedCount := input.FailedCount
	if failedCount < 0 {
		failedCount = 0
	}
	return successCount + failedCount
}

func (u *TaskUsecase) reconcileProviderCallbackFiles(ctx context.Context, task *Task, input ProviderCallbackInput) error {
	provider, ok := u.providers.Get(task.ProviderCode)
	if !ok {
		return ErrProviderNotFound
	}
	completion, err := provider.CompletionFiles(ctx, task)
	if err != nil {
		return err
	}
	if !completion.Handled {
		_ = u.repo.AddEvent(ctx, task.ID, EventProviderFilesFetched, map[string]any{
			"triggerType":     "PROVIDER_CALLBACK_RECONCILE",
			"externalBatchId": input.ExternalBatchID,
			"fileCount":       0,
			"handled":         false,
		})
		return nil
	}
	files, payload := u.pendingFilesFromProviderURLs(ctx, task, completion.Result.FileURLs)
	payload["triggerType"] = "PROVIDER_CALLBACK_RECONCILE"
	payload["externalBatchId"] = completion.Result.ExternalBatchID
	payload["fileCount"] = len(completion.Result.FileURLs)
	payload["handled"] = true
	files, err = u.unimportedResultFiles(ctx, task.ID, files)
	if err != nil {
		return err
	}
	if err := u.repo.UpsertPendingResultFiles(ctx, task.ID, files); err != nil {
		return err
	}
	_ = u.repo.AddEvent(ctx, task.ID, EventProviderFilesFetched, payload)
	return nil
}

func (u *TaskUsecase) pendingFilesFromProviderURLs(ctx context.Context, task *Task, fileURLs []string) ([]ResultFileInput, map[string]any) {
	localUploads, _ := u.repo.ListUploadedResultFiles(ctx, task.ID, true)
	localKeys := map[string]struct{}{}
	for _, file := range localUploads {
		if file.ObjectKey != "" {
			localKeys[file.ObjectKey] = struct{}{}
		}
	}
	providerOwnKeys := map[string]struct{}{}
	matchedKeys := 0
	ossMissingKeys := []string{}
	ossHeadErrors := []string{}
	files := make([]ResultFileInput, 0, len(fileURLs))
	seenObjectKeys := map[string]struct{}{}
	seenFileURLs := map[string]struct{}{}
	for _, fileURL := range fileURLs {
		fileURL = strings.TrimSpace(fileURL)
		if fileURL == "" {
			continue
		}
		if objectKey, ok := u.ownOSSObjectKey(fileURL); ok {
			if _, exists := seenObjectKeys[objectKey]; exists {
				continue
			}
			seenObjectKeys[objectKey] = struct{}{}
			providerOwnKeys[objectKey] = struct{}{}
			if _, ok := localKeys[objectKey]; ok {
				matchedKeys++
			}
			if exists, err := u.store.ObjectExists(ctx, objectKey); err != nil {
				ossHeadErrors = append(ossHeadErrors, objectKey)
			} else if !exists {
				ossMissingKeys = append(ossMissingKeys, objectKey)
			}
			files = append(files, ResultFileInput{
				SourceType:      "OSS",
				ObjectKey:       objectKey,
				FileName:        path.Base(objectKey),
				ExternalBatchID: task.ProviderExternalBatchID,
				ParseStatus:     "PENDING",
			})
			continue
		}
		if _, exists := seenFileURLs[fileURL]; exists {
			continue
		}
		seenFileURLs[fileURL] = struct{}{}
		files = append(files, ResultFileInput{
			SourceType:      "PROVIDER_FILE",
			FileURL:         fileURL,
			FileName:        fileNameFromURL(fileURL),
			ExternalBatchID: task.ProviderExternalBatchID,
			ParseStatus:     "PENDING",
		})
	}
	localMissingFromProvider := 0
	for key := range localKeys {
		if _, ok := providerOwnKeys[key]; !ok {
			localMissingFromProvider++
		}
	}
	payload := map[string]any{
		"pendingFileCount":           len(files),
		"ownOSSFileCount":            len(providerOwnKeys),
		"externalFileCount":          len(seenFileURLs),
		"localUploadCount":           len(localKeys),
		"matchedLocalUploadCount":    matchedKeys,
		"localMissingFromFilesURL":   localMissingFromProvider,
		"ossMissingCount":            len(ossMissingKeys),
		"ossHeadErrorCount":          len(ossHeadErrors),
		"firstOSSMissingKey":         firstString(ossMissingKeys),
		"firstOSSHeadErrorKey":       firstString(ossHeadErrors),
		"filesURLPreferredForImport": true,
	}
	return files, payload
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func providerCallbackEventPayload(input ProviderCallbackInput, extra map[string]any) map[string]any {
	payload := map[string]any{
		"providerCode":    input.ProviderCode,
		"batchNo":         input.BatchNo,
		"externalBatchId": input.ExternalBatchID,
		"status":          input.Status,
		"totalCount":      input.TotalCount,
		"successCount":    input.SuccessCount,
		"failedCount":     input.FailedCount,
		"rawRequest":      input.RawRequest,
	}
	if !input.FinishedAt.IsZero() {
		payload["finishedAt"] = input.FinishedAt.Format(time.RFC3339)
	}
	for key, value := range extra {
		payload[key] = value
	}
	return payload
}

func (u *TaskUsecase) SubmitPending(ctx context.Context, limit int) error {
	tasks, err := u.repo.ListTasksByStatus(ctx, TaskStatusCreated, limit)
	if err != nil {
		return err
	}
	if len(tasks) > 0 {
		slog.InfoContext(ctx, "submit pending tasks picked", "trace_id", trace.RequestID(ctx), "count", len(tasks), "limit", limit)
	}
	for _, task := range tasks {
		if _, err := u.submitOne(ctx, task); err != nil {
			slog.ErrorContext(ctx, "submit pending task failed",
				"trace_id", trace.RequestID(ctx),
				"task_id", task.ID,
				"batch_no", task.BatchNo,
				"provider_code", task.ProviderCode,
				"error", err,
			)
			continue
		}
	}
	return nil
}

func (u *TaskUsecase) ImportPendingResults(ctx context.Context, limit int) error {
	if err := u.initializeSkippedResults(ctx, limit); err != nil {
		return err
	}
	tasks, err := u.repo.ListTasksByStatus(ctx, TaskStatusProviderDone, limit)
	if err != nil {
		return err
	}
	if repo, ok := u.repo.(interface {
		ListTasksWithPendingResultFiles(context.Context, int) ([]*Task, error)
	}); ok {
		extra, err := repo.ListTasksWithPendingResultFiles(ctx, limit)
		if err != nil {
			return err
		}
		seen := map[int64]bool{}
		for _, t := range tasks {
			seen[t.ID] = true
		}
		for _, t := range extra {
			if !seen[t.ID] {
				tasks = append(tasks, t)
				seen[t.ID] = true
			}
		}
	}
	if len(tasks) > 0 {
		slog.InfoContext(ctx, "import pending results picked", "trace_id", trace.RequestID(ctx), "count", len(tasks), "limit", limit)
	}
	for _, task := range tasks {
		if _, _, err := u.importCompletionFiles(ctx, task, false, true); err != nil {
			slog.ErrorContext(ctx, "import pending task result failed",
				"trace_id", trace.RequestID(ctx),
				"task_id", task.ID,
				"batch_no", task.BatchNo,
				"provider_code", task.ProviderCode,
				"error", err,
			)
			continue
		}
	}
	return u.queueImportedCallbacks(ctx, limit)
}

func (u *TaskUsecase) queueImportedCallbacks(ctx context.Context, limit int) error {
	ctx = u.validationContext(ctx)
	tasks, err := u.repo.ListResultImportedTasksWithProviderCallback(ctx, limit)
	if err != nil {
		return err
	}
	if len(tasks) > 0 {
		slog.InfoContext(ctx, "queue imported callbacks picked", "trace_id", trace.RequestID(ctx), "count", len(tasks), "limit", limit)
	}
	for _, task := range tasks {
		updatedTask, ready, err := u.repo.CompleteResultImport(u.validationContext(ctx), task.ID, true)
		if err != nil {
			slog.ErrorContext(ctx, "queue imported callback failed",
				"trace_id", trace.RequestID(ctx),
				"task_id", task.ID,
				"batch_no", task.BatchNo,
				"provider_code", task.ProviderCode,
				"error", err,
			)
			continue
		}
		if !ready {
			slog.WarnContext(ctx, "imported task not ready for callback",
				"trace_id", trace.RequestID(ctx),
				"task_id", task.ID,
				"batch_no", task.BatchNo,
				"provider_code", task.ProviderCode,
				"status", updatedTask.Status,
				"total_count", updatedTask.TotalCount,
				"success_count", updatedTask.SuccessCount,
				"failed_count", updatedTask.FailedCount,
			)
		}
	}
	return nil
}

func (u *TaskUsecase) FailTimedOutSubmitted(ctx context.Context, maxAge time.Duration, limit int) (int, error) {
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	if limit <= 0 {
		limit = 20
	}
	count, err := u.repo.FailSubmittedOlderThan(ctx, time.Now().Add(-maxAge), "执行超过24小时", limit)
	if err != nil {
		return count, err
	}
	if count > 0 {
		slog.WarnContext(ctx, "submitted tasks timed out",
			"trace_id", trace.RequestID(ctx),
			"count", count,
			"max_age", maxAge.String(),
		)
	}
	return count, nil
}

func (u *TaskUsecase) RefreshSubmittedProgress(ctx context.Context, interval time.Duration, limit int) (int, error) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	if limit <= 0 {
		limit = 20
	}
	tasks, err := u.repo.ListSubmittedTasksForProgress(ctx, limit, time.Now().Add(-interval))
	if err != nil {
		return 0, err
	}
	for _, task := range tasks {
		if task.ProviderExternalBatchID == "" {
			continue
		}
		if _, err := u.refreshSubmittedTaskProgress(ctx, task); err != nil {
			slog.WarnContext(ctx, "refresh submitted task progress failed",
				"trace_id", trace.RequestID(ctx),
				"task_id", task.ID,
				"batch_no", task.BatchNo,
				"provider_code", task.ProviderCode,
				"error", err,
			)
			continue
		}
	}
	return len(tasks), nil
}

func (u *TaskUsecase) refreshSubmittedTaskProgress(ctx context.Context, task *Task) (ProviderStatusResult, error) {
	provider, ok := u.providers.Get(task.ProviderCode)
	if !ok {
		return ProviderStatusResult{}, ErrProviderNotFound
	}
	result, err := provider.FetchStatus(ctx, task.ProviderExternalBatchID)
	if err != nil {
		return ProviderStatusResult{}, err
	}
	if err := u.repo.MarkProviderStatus(ctx, task.ID, result); err != nil {
		return ProviderStatusResult{}, err
	}
	_ = u.repo.AddEvent(ctx, task.ID, EventProviderStatusPolled, map[string]any{
		"triggerType":     "SCHEDULED_PROGRESS",
		"externalBatchId": result.ExternalBatchID,
		"questionCount":   len(result.Questions),
	})
	return result, nil
}

func (u *TaskUsecase) importStoredResultFiles(ctx context.Context, task *Task, files []ResultFileInput, notifyCallback bool, triggerType string) (int, int, error) {
	successFiles := 0
	totalRows := 0
	for _, file := range files {
		imported, checkErr := u.resultFileImported(ctx, task.ID, file)
		if checkErr != nil {
			return successFiles, totalRows, checkErr
		}
		if imported {
			successFiles++
			continue
		}

		if file.SourceType == "" {
			file.SourceType = "OSS_UPLOAD"
		}
		if file.FileName == "" {
			file.FileName = path.Base(file.ObjectKey)
		}
		startedAt := time.Now()
		slog.InfoContext(ctx, "import uploaded result started",
			"trace_id", trace.RequestID(ctx),
			"task_id", task.ID,
			"batch_no", task.BatchNo,
			"provider_code", task.ProviderCode,
			"trigger_type", triggerType,
			"object_key", file.ObjectKey,
			"file_name", file.FileName,
		)
		content, err := u.store.ReadObject(ctx, file.ObjectKey)
		if err != nil {
			errMessage := fmt.Sprintf("read oss object: %v", err)
			_ = u.repo.MarkResultFileFailed(ctx, task.ID, file, errMessage)
			slog.WarnContext(ctx, "import uploaded result read object failed",
				"trace_id", trace.RequestID(ctx),
				"task_id", task.ID,
				"batch_no", task.BatchNo,
				"provider_code", task.ProviderCode,
				"trigger_type", triggerType,
				"object_key", file.ObjectKey,
				"error", err,
			)
			continue
		}
		parsed, err := u.parser.Parse(ctx, file.FileName, content)
		if err != nil {
			errMessage := fmt.Sprintf("parse result: %v", err)
			_ = u.repo.MarkResultFileFailed(ctx, task.ID, file, errMessage)
			slog.WarnContext(ctx, "import uploaded result parse failed",
				"trace_id", trace.RequestID(ctx),
				"task_id", task.ID,
				"batch_no", task.BatchNo,
				"provider_code", task.ProviderCode,
				"trigger_type", triggerType,
				"file_name", file.FileName,
				"content_bytes", len(content),
				"error", err,
			)
			continue
		}
		file.Checksum = checksum(content)
		file.ExternalBatchID = task.ProviderExternalBatchID
		file.NotifyCallback = notifyCallback
		u.validateParsedResult(ctx, parsed)
		if err := u.repo.MarkResultImported(ctx, task.ID, file, *parsed); err != nil {
			return successFiles, totalRows, err
		}
		successFiles++
		totalRows += parsed.RowCount
		_ = u.repo.AddEvent(ctx, task.ID, EventResultImported, map[string]any{
			"objectKey": file.ObjectKey,
			"rowCount":  parsed.RowCount,
		})
		slog.InfoContext(ctx, "import uploaded result completed",
			"trace_id", trace.RequestID(ctx),
			"task_id", task.ID,
			"batch_no", task.BatchNo,
			"provider_code", task.ProviderCode,
			"trigger_type", triggerType,
			"row_count", parsed.RowCount,
			"valid_row_count", parsed.ValidRowCount,
			"invalid_row_count", parsed.InvalidRowCount,
			"content_bytes", len(content),
			"latency_ms", time.Since(startedAt).Milliseconds(),
		)
	}
	return successFiles, totalRows, nil
}

func (u *TaskUsecase) importUploadedFiles(ctx context.Context, task *Task, force bool, notifyCallback bool) (int, int, error) {
	if !taskProcessingLocked(ctx, task.ID) {
		var a, b int
		err := u.withTaskLock(ctx, task.ID, func(ctx context.Context) error {
			var err error
			a, b, err = u.importUploadedFiles(ctx, task, force, notifyCallback)
			return err
		})
		return a, b, err
	}
	files, err := u.uploadedResultFiles(ctx, task, force)
	if err != nil {
		return 0, 0, err
	}
	if len(files) == 0 {
		successfulFiles, countErr := u.repo.CountSuccessfulResultFiles(ctx, task.ID)
		if countErr != nil {
			return 0, 0, countErr
		}
		if successfulFiles > 0 {
			updatedTask, _, err := u.repo.CompleteResultImport(u.validationContext(ctx), task.ID, notifyCallback)
			if err != nil {
				return 0, 0, err
			}
			return successfulFiles, updatedTask.ResultRowCount, nil
		}
		_ = u.repo.MarkSubmitFailed(ctx, task.ID, "provider callback received before upload url was issued")
		slog.WarnContext(ctx, "import result skipped without upload files",
			"trace_id", trace.RequestID(ctx),
			"task_id", task.ID,
			"batch_no", task.BatchNo,
			"provider_code", task.ProviderCode,
		)
		return 0, 0, nil
	}

	successFiles, totalRows, err := u.importStoredResultFiles(ctx, task, files, notifyCallback, "OSS_UPLOAD")
	if err != nil {
		return len(files), totalRows, err
	}
	if successFiles == 0 {
		_ = u.repo.MarkSubmitFailed(ctx, task.ID, "all uploaded result files failed to import")
		return len(files), totalRows, nil
	}
	if _, _, err := u.repo.CompleteResultImport(u.validationContext(ctx), task.ID, notifyCallback); err != nil {
		return len(files), totalRows, err
	}
	return len(files), totalRows, nil
}

func (u *TaskUsecase) uploadedResultFiles(ctx context.Context, task *Task, force bool) ([]ResultFileInput, error) {
	files, err := u.repo.ListUploadedResultFiles(ctx, task.ID, false)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 && task.ResultObjectKey != "" {
		files = append(files, ResultFileInput{
			SourceType:      "OSS_UPLOAD",
			ObjectKey:       task.ResultObjectKey,
			FileName:        task.ResultFileName,
			ExternalBatchID: task.ProviderExternalBatchID,
		})
	}
	return files, nil
}

func (u *TaskUsecase) importCompletionFiles(ctx context.Context, task *Task, force bool, notifyCallback bool) (int, int, error) {
	if !taskProcessingLocked(ctx, task.ID) {
		var a, b int
		err := u.withTaskLock(ctx, task.ID, func(ctx context.Context) error {
			var err error
			a, b, err = u.importCompletionFiles(ctx, task, force, notifyCallback)
			return err
		})
		return a, b, err
	}
	pendingFiles, err := u.repo.ListPendingResultFiles(ctx, task.ID)
	if err != nil {
		return 0, 0, err
	}
	if len(pendingFiles) > 0 {
		return u.importResultFileInputs(ctx, task, pendingFiles, notifyCallback, "PENDING_RESULT_FILE")
	}
	provider, ok := u.providers.Get(task.ProviderCode)
	if !ok {
		return 0, 0, ErrProviderNotFound
	}
	completion, err := provider.CompletionFiles(ctx, task)
	if err != nil {
		return 0, 0, err
	}
	if completion.Handled {
		_ = u.repo.AddEvent(ctx, task.ID, EventProviderFilesFetched, map[string]any{
			"triggerType":     "PROVIDER_CALLBACK_RECONCILE",
			"externalBatchId": completion.Result.ExternalBatchID,
			"fileCount":       len(completion.Result.FileURLs),
		})
		if len(completion.Result.FileURLs) > 0 {
			return u.importProviderFileResult(ctx, task, completion.Result, force, notifyCallback, "PROVIDER_CALLBACK_RECONCILE")
		}
		slog.WarnContext(ctx, "provider callback reconcile returned empty files; fallback to uploaded files",
			"trace_id", trace.RequestID(ctx),
			"task_id", task.ID,
			"batch_no", task.BatchNo,
			"provider_code", task.ProviderCode,
			"external_batch_id", task.ProviderExternalBatchID,
		)
	}
	return u.importUploadedFiles(ctx, task, force, notifyCallback)
}

func (u *TaskUsecase) importResultFileInputs(ctx context.Context, task *Task, files []ResultFileInput, notifyCallback bool, triggerType string) (int, int, error) {
	if !taskProcessingLocked(ctx, task.ID) {
		var a, b int
		err := u.withTaskLock(ctx, task.ID, func(ctx context.Context) error {
			var err error
			a, b, err = u.importResultFileInputs(ctx, task, files, notifyCallback, triggerType)
			return err
		})
		return a, b, err
	}
	fileCount, successFiles, totalRows, err := u.importResultFileInputsOnly(ctx, task, files, notifyCallback, triggerType)
	if err != nil {
		return fileCount, totalRows, err
	}
	if successFiles == 0 {
		_ = u.repo.MarkSubmitFailed(ctx, task.ID, "all pending result files failed to import")
		return fileCount, totalRows, nil
	}
	if _, _, err := u.repo.CompleteResultImport(u.validationContext(ctx), task.ID, notifyCallback); err != nil {
		return fileCount, totalRows, err
	}
	return fileCount, totalRows, nil
}

func (u *TaskUsecase) importResultFileInputsOnly(ctx context.Context, task *Task, files []ResultFileInput, notifyCallback bool, triggerType string) (int, int, int, error) {
	totalRows, successFiles := 0, 0
	var provider ProviderClient
	// Keep database registration order across both OSS and external files.
	for _, file := range files {
		if file.ObjectKey != "" {
			imported, rows, err := u.importStoredResultFiles(ctx, task, []ResultFileInput{file}, notifyCallback, triggerType)
			if err != nil {
				return len(files), successFiles, totalRows, err
			}
			successFiles += imported
			totalRows += rows
			continue
		}
		if file.FileURL == "" {
			continue
		}
		if provider == nil {
			var ok bool
			provider, ok = u.providers.Get(task.ProviderCode)
			if !ok {
				return len(files), successFiles, totalRows, ErrProviderNotFound
			}
		}
		imported, checkErr := u.resultFileImported(ctx, task.ID, file)
		if checkErr != nil {
			return len(files), successFiles, totalRows, checkErr
		}
		if imported {
			successFiles++
			continue
		}

		startedAt := time.Now()
		content, err := provider.DownloadResultFile(ctx, file.FileURL)
		if err != nil {
			_ = u.repo.MarkResultFileFailed(ctx, task.ID, file, err.Error())
			return len(files), successFiles, totalRows, err
		}
		if file.FileName == "" {
			file.FileName = fileNameFromURL(file.FileURL)
		}
		if file.SourceType == "" {
			file.SourceType = "PROVIDER_FILE"
		}
		file.Checksum = checksum(content)
		file.ExternalBatchID = task.ProviderExternalBatchID
		file.NotifyCallback = notifyCallback
		parsed, err := u.parser.Parse(ctx, file.FileName, content)
		if err != nil {
			errMessage := fmt.Sprintf("parse result: %v", err)
			_ = u.repo.MarkResultFileFailed(ctx, task.ID, file, errMessage)
			return len(files), successFiles, totalRows, err
		}
		u.validateParsedResult(ctx, parsed)
		if err := u.repo.MarkResultImported(ctx, task.ID, file, *parsed); err != nil {
			return len(files), successFiles, totalRows, err
		}
		successFiles++
		totalRows += parsed.RowCount
		slog.InfoContext(ctx, "pending provider result file imported",
			"trace_id", trace.RequestID(ctx),
			"task_id", task.ID,
			"batch_no", task.BatchNo,
			"provider_code", task.ProviderCode,
			"trigger_type", triggerType,
			"file_name", file.FileName,
			"row_count", parsed.RowCount,
			"content_bytes", len(content),
			"latency_ms", time.Since(startedAt).Milliseconds(),
		)
	}
	if successFiles > 0 {
		_ = u.repo.AddEvent(ctx, task.ID, EventResultFetchCompleted, map[string]any{
			"triggerType": triggerType,
			"fileCount":   len(files),
			"rowCount":    totalRows,
		})
	}
	return len(files), successFiles, totalRows, nil
}

func (u *TaskUsecase) GetProviderStatus(ctx context.Context, id int64) (ProviderStatusResult, error) {
	task, err := u.repo.GetTask(ctx, id)
	if err != nil {
		return ProviderStatusResult{}, err
	}
	if task.ProviderExternalBatchID == "" {
		return ProviderStatusResult{}, fmt.Errorf("%w: external batch id is empty", ErrInvalidTaskRequest)
	}
	provider, ok := u.providers.Get(task.ProviderCode)
	if !ok {
		return ProviderStatusResult{}, ErrProviderNotFound
	}
	result, err := provider.FetchStatus(ctx, task.ProviderExternalBatchID)
	if err != nil {
		return ProviderStatusResult{}, err
	}
	if err := u.repo.MarkProviderStatus(ctx, task.ID, result); err != nil {
		return ProviderStatusResult{}, err
	}
	_ = u.repo.AddEvent(ctx, task.ID, EventProviderStatusPolled, map[string]any{
		"externalBatchId": result.ExternalBatchID,
		"questionCount":   len(result.Questions),
	})
	return result, nil
}

func (u *TaskUsecase) GetProviderResultFiles(ctx context.Context, id int64) (ProviderFilesResult, error) {
	task, err := u.repo.GetTask(ctx, id)
	if err != nil {
		return ProviderFilesResult{}, err
	}
	result, err := u.fetchProviderResultFiles(ctx, task)
	if err != nil {
		return ProviderFilesResult{}, err
	}
	_ = u.repo.AddEvent(ctx, task.ID, EventProviderFilesFetched, map[string]any{
		"externalBatchId": result.ExternalBatchID,
		"fileCount":       len(result.FileURLs),
	})
	return result, nil
}

func (u *TaskUsecase) RefetchProviderResults(ctx context.Context, id int64, force bool, notifyCallback bool) (*Task, int, int, error) {
	task, err := u.repo.GetTask(ctx, id)
	if err != nil {
		return nil, 0, 0, err
	}
	fileCount, rowCount, err := u.importProviderFiles(ctx, task, force, notifyCallback)
	if err != nil {
		return nil, fileCount, rowCount, err
	}
	updatedTask, err := u.repo.GetTask(ctx, id)
	if err != nil {
		return nil, fileCount, rowCount, err
	}
	return updatedTask, fileCount, rowCount, nil
}

func (u *TaskUsecase) SyncProviderResult(ctx context.Context, id int64, force bool, notifyCallback bool) (SyncProviderResultOutcome, error) {
	if !taskProcessingLocked(ctx, id) {
		var out SyncProviderResultOutcome
		err := u.withTaskLock(ctx, id, func(ctx context.Context) error {
			var err error
			out, err = u.SyncProviderResult(ctx, id, force, notifyCallback)
			return err
		})
		return out, err
	}
	task, err := u.repo.GetTask(ctx, id)
	if err != nil {
		return SyncProviderResultOutcome{}, err
	}
	expectedCount, err := u.taskItemCount(ctx, task.ID)
	if err != nil {
		return SyncProviderResultOutcome{}, err
	}
	status, err := u.GetProviderStatus(ctx, id)
	if err != nil {
		return SyncProviderResultOutcome{}, err
	}
	providerDone := providerStatusDone(status, expectedCount)
	if !providerDone {
		updatedTask, err := u.repo.GetTask(ctx, id)
		if err != nil {
			return SyncProviderResultOutcome{}, err
		}
		return SyncProviderResultOutcome{
			Task:         updatedTask,
			ProviderDone: false,
			FileCount:    0,
			RowCount:     0,
			Msg:          providerSyncResultMsg(task.ProviderCode, status, false, 0),
		}, nil
	}
	if providerStatusCountDone(status, expectedCount) {
		task, err = u.markProviderDoneFromStatus(ctx, task, status, expectedCount)
		if err != nil {
			return SyncProviderResultOutcome{}, err
		}
	}
	fileCount, rowCount, err := u.importProviderFiles(ctx, task, force, notifyCallback)
	if err != nil {
		return SyncProviderResultOutcome{}, err
	}
	updatedTask, err := u.repo.GetTask(ctx, id)
	if err != nil {
		return SyncProviderResultOutcome{}, err
	}
	return SyncProviderResultOutcome{
		Task:         updatedTask,
		ProviderDone: true,
		FileCount:    fileCount,
		RowCount:     rowCount,
		Msg:          providerSyncResultMsg(task.ProviderCode, status, true, fileCount),
	}, nil
}

func (u *TaskUsecase) taskItemCount(ctx context.Context, taskID int64) (int, error) {
	return u.repo.CountTaskItems(ctx, taskID)
}

func (u *TaskUsecase) markProviderDoneFromStatus(ctx context.Context, task *Task, status ProviderStatusResult, expectedCount int) (*Task, error) {
	successCount, failedCount, totalCount := providerCompletionCounts(status, expectedCount)
	providerStatus := status.RawResponse
	if len(providerStatus) == 0 {
		providerStatus = json.RawMessage(`{}`)
	}
	rawRequest, err := json.Marshal(map[string]any{
		"triggerType":     "MANUAL_STATUS_SYNC",
		"externalBatchId": status.ExternalBatchID,
		"totalCount":      totalCount,
		"successCount":    successCount,
		"failedCount":     failedCount,
		"providerStatus":  providerStatus,
	})
	if err != nil {
		return nil, err
	}
	input := ProviderCallbackInput{
		TaskID:          task.ID,
		ProviderCode:    task.ProviderCode,
		BatchNo:         taskProviderBatchNo(task),
		ExternalBatchID: status.ExternalBatchID,
		Status:          "DONE",
		TotalCount:      totalCount,
		SuccessCount:    successCount,
		FailedCount:     failedCount,
		FinishedAt:      time.Now(),
		RawRequest:      rawRequest,
	}
	updatedTask, err := u.repo.MarkProviderCallback(ctx, input)
	if err != nil {
		_ = u.repo.AddEvent(ctx, task.ID, EventProviderCallbackFail, providerCallbackEventPayload(input, map[string]any{
			"triggerType": "MANUAL_STATUS_SYNC",
			"error":       err.Error(),
		}))
		return nil, err
	}
	_ = u.repo.AddEvent(ctx, updatedTask.ID, EventProviderCallback, providerCallbackEventPayload(input, map[string]any{
		"triggerType": "MANUAL_STATUS_SYNC",
	}))
	slog.InfoContext(ctx, "task provider done synthesized from status counts",
		"trace_id", trace.RequestID(ctx),
		"task_id", updatedTask.ID,
		"batch_no", updatedTask.BatchNo,
		"provider_code", updatedTask.ProviderCode,
		"external_batch_id", input.ExternalBatchID,
		"total_count", totalCount,
		"success_count", successCount,
		"failed_count", failedCount,
	)
	return updatedTask, nil
}

func (u *TaskUsecase) importProviderFiles(ctx context.Context, task *Task, force bool, notifyCallback bool) (int, int, error) {
	fileResult, err := u.fetchProviderResultFiles(ctx, task)
	if err != nil {
		return 0, 0, err
	}
	return u.importProviderFileResult(ctx, task, fileResult, force, notifyCallback, "MANUAL")
}

func (u *TaskUsecase) matchProviderOwnOSSFiles(ctx context.Context, task *Task, fileURLs []string, force bool) ([]ResultFileInput, []string, []string, error) {
	var ownObjectKeys []string
	var externalURLs []string
	seenOwnKeys := map[string]struct{}{}
	for _, fileURL := range fileURLs {
		objectKey, ok := u.ownOSSObjectKey(fileURL)
		if !ok {
			externalURLs = append(externalURLs, fileURL)
			continue
		}
		if _, exists := seenOwnKeys[objectKey]; exists {
			continue
		}
		seenOwnKeys[objectKey] = struct{}{}
		ownObjectKeys = append(ownObjectKeys, objectKey)
	}
	if len(ownObjectKeys) == 0 {
		return nil, externalURLs, nil, nil
	}

	dbFiles, err := u.repo.ListUploadedResultFiles(ctx, task.ID, true)
	if err != nil {
		return nil, nil, nil, err
	}
	matchedFiles, missingObjectKeys := matchStoredProviderFiles(task, ownObjectKeys, dbFiles, force)
	return matchedFiles, externalURLs, missingObjectKeys, nil
}

func matchStoredProviderFiles(task *Task, ownObjectKeys []string, dbFiles []ResultFileInput, force bool) ([]ResultFileInput, []string) {
	filesByKey := make(map[string]ResultFileInput, len(dbFiles))
	for _, file := range dbFiles {
		if file.ObjectKey == "" {
			continue
		}
		filesByKey[file.ObjectKey] = file
	}

	var matchedFiles []ResultFileInput
	var missingObjectKeys []string
	for _, objectKey := range ownObjectKeys {
		file, ok := filesByKey[objectKey]
		if !ok {
			missingObjectKeys = append(missingObjectKeys, objectKey)
			continue
		}
		if file.ParseStatus == "SUCCESS" {
			continue
		}
		if file.SourceType == "" {
			file.SourceType = "OSS_UPLOAD"
		}
		if file.FileName == "" {
			file.FileName = path.Base(file.ObjectKey)
		}
		file.ExternalBatchID = task.ProviderExternalBatchID
		matchedFiles = append(matchedFiles, file)
	}
	return matchedFiles, missingObjectKeys
}

func shouldReconcileProviderFilesOnCallback(providerCode string) bool {
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "mpzdata":
		return true
	default:
		return false
	}
}

func providerStatusDone(status ProviderStatusResult, expectedCount int) bool {
	if providerStatusCountDone(status, expectedCount) {
		return true
	}
	if len(status.Questions) == 0 {
		return false
	}
	for _, question := range status.Questions {
		if question.Status != 2 {
			return false
		}
	}
	return true
}

func providerStatusCountDone(status ProviderStatusResult, expectedCount int) bool {
	if expectedCount <= 0 {
		return false
	}
	if status.TotalCount > 0 && status.TotalCount != expectedCount {
		return false
	}
	if status.SuccessCount < 0 || status.FailedCount < 0 {
		return false
	}
	return status.SuccessCount+status.FailedCount == expectedCount
}

func providerCompletionCounts(status ProviderStatusResult, expectedCount int) (int, int, int) {
	totalCount := expectedCount
	if status.TotalCount > 0 {
		totalCount = status.TotalCount
	}
	return status.SuccessCount, status.FailedCount, totalCount
}

func providerSyncResultMsg(providerCode string, status ProviderStatusResult, providerDone bool, fileCount int) string {
	doneCount, totalCount := providerProgressCount(status)
	fileDesc := "结果文件未生成"
	if fileCount > 0 {
		fileDesc = fmt.Sprintf("结果文件已生成 %d 个", fileCount)
	}

	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "mpzdata":
		if providerDone {
			return fmt.Sprintf("mpzdata 处理进度：已处理 %d/%d，%s。", doneCount, totalCount, fileDesc)
		}
		return fmt.Sprintf("mpzdata 处理进度：已处理 %d/%d，%s；仍有问题处理中，请稍后重试检查结果。", doneCount, totalCount, fileDesc)
	default:
		if providerDone {
			return fmt.Sprintf("供应商任务已全部完成，已处理 %d/%d，%s。", doneCount, totalCount, fileDesc)
		}
		return fmt.Sprintf("供应商任务仍在处理中，已处理 %d/%d，%s；只有全部问题 status=2 或成功数+失败数等于任务数后才会查询并导入结果文件。", doneCount, totalCount, fileDesc)
	}
}

func providerProgressCount(status ProviderStatusResult) (int, int) {
	if status.TotalCount > 0 || status.SuccessCount > 0 || status.FailedCount > 0 {
		doneCount := status.SuccessCount + status.FailedCount
		totalCount := status.TotalCount
		if totalCount <= 0 {
			totalCount = doneCount
		}
		return doneCount, totalCount
	}
	doneCount := 0
	for _, question := range status.Questions {
		if question.Status == 2 {
			doneCount++
		}
	}
	return doneCount, len(status.Questions)
}

func (u *TaskUsecase) DeliverPendingCallbacks(ctx context.Context, limit int) error {
	ctx = u.validationContext(ctx)
	tasks, err := u.repo.ListTasksByStatus(ctx, TaskStatusCallbackPending, limit)
	if err != nil {
		return err
	}
	if len(tasks) > 0 {
		slog.InfoContext(ctx, "deliver pending callbacks picked", "trace_id", trace.RequestID(ctx), "count", len(tasks), "limit", limit)
	}
	for _, task := range tasks {
		updatedTask, ready, err := u.repo.CompleteResultImport(u.validationContext(ctx), task.ID, true)
		if err != nil {
			slog.ErrorContext(ctx, "complete result import before callback failed",
				"trace_id", trace.RequestID(ctx),
				"task_id", task.ID,
				"batch_no", task.BatchNo,
				"provider_code", task.ProviderCode,
				"error", err,
			)
			continue
		}
		if !ready {
			slog.WarnContext(ctx, "business callback skipped because result import is not ready",
				"trace_id", trace.RequestID(ctx),
				"task_id", task.ID,
				"batch_no", task.BatchNo,
				"provider_code", task.ProviderCode,
				"status", updatedTask.Status,
				"total_count", updatedTask.TotalCount,
				"success_count", updatedTask.SuccessCount,
				"failed_count", updatedTask.FailedCount,
			)
			continue
		}
		task = updatedTask
		if _, err := u.deliverTaskCallback(ctx, task, false); err != nil {
			slog.ErrorContext(ctx, "deliver pending callback failed",
				"trace_id", trace.RequestID(ctx),
				"task_id", task.ID,
				"batch_no", task.BatchNo,
				"provider_code", task.ProviderCode,
				"error", err,
			)
			continue
		}
	}
	return nil
}

func (u *TaskUsecase) RetryTaskCallback(ctx context.Context, id int64, force bool, ignoreRisk ...bool) (*Task, RetryTaskCallbackOutcome, error) {
	if !taskProcessingLocked(ctx, id) {
		var task *Task
		var outcome RetryTaskCallbackOutcome
		err := u.withTaskLock(ctx, id, func(ctx context.Context) error {
			var err error
			task, outcome, err = u.RetryTaskCallback(ctx, id, force, ignoreRisk...)
			return err
		})
		return task, outcome, err
	}
	bypassRisk := len(ignoreRisk) > 0 && ignoreRisk[0]
	if bypassRisk && force {
		return nil, RetryTaskCallbackOutcome{}, ErrInvalidTaskRequest
	}
	task, err := u.repo.GetTask(ctx, id)
	if err != nil {
		return nil, RetryTaskCallbackOutcome{}, err
	}
	readiness, err := u.repo.GetCallbackReadiness(ctx, task.ID)
	if err != nil {
		return task, RetryTaskCallbackOutcome{}, err
	}
	outcome := RetryTaskCallbackOutcome{Readiness: readiness}
	if task.Status == TaskStatusReassigned {
		return task, outcome, ErrTaskClosed
	}
	if !readiness.RiskStatsReady || (readiness.RiskCount > 0 && !bypassRisk && !AllowsRiskCallback(ctx, task.ProviderCode)) {
		outcome.Readiness.Ready = false
		outcome.Readiness.ForceRequired = false
		outcome.Readiness.Reason = "结果风险统计未完成"
		if readiness.RiskStatsReady {
			outcome.Readiness.Reason = "结果存在风险，请使用页面强制回调"
		}
		return task, outcome, nil
	}
	if !readiness.Ready && !force {
		return task, outcome, nil
	}
	if !readiness.Ready && force {
		taskForCallback := *task
		taskForCallback.TotalCount = readiness.TotalCount
		taskForCallback.SuccessCount = readiness.SuccessCount
		taskForCallback.FailedCount = readiness.FailedCount
		task = &taskForCallback
	}
	ctx = context.WithValue(ctx, manualCallbackKey{}, true)
	delivered, err := u.deliverTaskCallback(ctx, task, force && !readiness.Ready, bypassRisk)
	if err != nil {
		return task, outcome, err
	}
	outcome.Delivered = delivered
	if bypassRisk {
		_ = u.repo.AddEvent(ctx, task.ID, EventType("BUSINESS_CALLBACK_RISK_FORCED"), map[string]any{"riskCount": task.RiskCount, "riskSummary": task.RiskSummary, "delivered": delivered})
	}
	updated, err := u.repo.GetTask(ctx, id)
	if err != nil {
		return task, outcome, nil
	}
	return updated, outcome, nil
}

// ForceImportCallback fetches the provider's latest status and result files,
// imports unseen files with quality validation, then delivers a
// callback using only the currently imported results. It must NOT change the
// task status or callback_sent_at, and must not block subsequent imports.
func (u *TaskUsecase) ForceImportCallback(ctx context.Context, input ForceImportCallbackInput) (ForceImportCallbackOutcome, error) {
	if !taskProcessingLocked(ctx, input.TaskID) {
		var out ForceImportCallbackOutcome
		err := u.withTaskLock(ctx, input.TaskID, func(ctx context.Context) error {
			var err error
			out, err = u.ForceImportCallback(ctx, input)
			return err
		})
		return out, err
	}
	outcome := ForceImportCallbackOutcome{
		TaskStatusUnchanged: true,
	}
	task, err := u.repo.GetTask(ctx, input.TaskID)
	if err != nil {
		return outcome, err
	}
	outcome.Task = task
	taskItemCount, err := u.taskItemCount(ctx, task.ID)
	if err != nil {
		return outcome, err
	}
	outcome.TaskItemCount = taskItemCount

	if err := u.refreshForcedCallbackResults(ctx, task, &outcome); err != nil {
		return outcome, err
	}

	allItems, err := u.repo.ListTaskItemResults(ctx, task.ID)
	if err != nil {
		return outcome, err
	}
	for _, item := range allItems {
		if item.ParseStatus != "" && (item.AnswerValidation.Status != ValidationPass && !(AllowsRiskCallback(ctx, task.ProviderCode) && (item.AnswerValidation.Status == ValidationRisk || item.AnswerValidation.Status == ValidationInvalid))) {
			outcome.Rejected = true
			_ = u.repo.AddEvent(ctx, task.ID, EventForceImportCallback, map[string]any{"reason": "结果存在风险或尚未校验，请完成导入后使用页面强制回调", "delivered": false})
			return outcome, nil
		}
	}
	successItems := successfulResultItems(allItems)
	outcome.SuccessCount = len(successItems)
	outcome.FailedCount = failedResultItemCount(allItems)
	outcome.UnfinishedCount = maxInt(outcome.TaskItemCount-outcome.SuccessCount-outcome.FailedCount, 0)
	outcome.Partial = outcome.SuccessCount < outcome.TaskItemCount

	if outcome.SuccessCount == 0 {
		outcome.Rejected = true
		_ = u.repo.AddEvent(ctx, task.ID, EventForceImportCallback, u.forceImportCallbackEventPayload(input.Reason, outcome, "", 0, false, nil, ""))
		return outcome, nil
	}

	callbackExternalBatchID := taskCallbackExternalBatchID(task)
	resultURL := u.resultURL(callbackExternalBatchID) + "?successOnly=true"
	payload := CallbackDelivery{
		ExternalBatchID: callbackExternalBatchID,
		BatchNo:         taskCallbackBatchNo(task),
		Status:          TaskStatusDone,
		ResultURL:       resultURL,
		Counts: CallbackCounts{
			Total:   outcome.SuccessCount,
			Success: outcome.SuccessCount,
			Failed:  0,
		},
		ResultItems: successItems,
		Items:       callbackResultItems(successItems),
	}
	payload.ResultFiles = u.callbackResultFiles(ctx, task)

	taskCopy := *task
	taskCopy.TotalCount = outcome.SuccessCount
	taskCopy.SuccessCount = outcome.SuccessCount
	taskCopy.FailedCount = 0

	callbackMode := u.callback.ResolveMode(task)
	outcome.CallbackMode = callbackMode

	result, err := u.callback.Deliver(ctx, &taskCopy, payload)
	payloadBytes, _ := json.Marshal(payload)
	if len(result.RequestPayload) > 0 {
		payloadBytes = result.RequestPayload
	}
	targetURL := task.CallbackURL
	if result.TargetURL != "" {
		targetURL = result.TargetURL
	}
	if err != nil {
		if markErr := u.repo.RecordForcedCallbackFailed(ctx, task.ID, targetURL, err.Error(), payloadBytes); markErr != nil {
			return outcome, markErr
		}
		_ = u.repo.AddEvent(ctx, task.ID, EventForceImportCallback, u.forceImportCallbackEventPayload(input.Reason, outcome, targetURL, result.StatusCode, false, result.RequestPayload, result.ResponseBody))
		slog.WarnContext(ctx, "forced import callback failed",
			"trace_id", trace.RequestID(ctx),
			"task_id", task.ID,
			"batch_no", task.BatchNo,
			"provider_code", task.ProviderCode,
			"target_url", targetURL,
			"success_count", outcome.SuccessCount,
			"failed_count", outcome.FailedCount,
			"error", err,
		)
		return outcome, nil
	}
	if err := u.repo.RecordForcedCallbackDone(ctx, task.ID, targetURL, result.StatusCode, result.ResponseBody, payloadBytes); err != nil {
		return outcome, err
	}
	outcome.Delivered = true
	if callbackMode == "YANGSHI" {
		outcome.YangshiUploaded = true
	}
	_ = u.repo.AddEvent(ctx, task.ID, EventForceImportCallback, u.forceImportCallbackEventPayload(input.Reason, outcome, targetURL, result.StatusCode, true, result.RequestPayload, result.ResponseBody))
	slog.InfoContext(ctx, "forced import callback delivered",
		"trace_id", trace.RequestID(ctx),
		"task_id", task.ID,
		"batch_no", task.BatchNo,
		"provider_code", task.ProviderCode,
		"target_url", targetURL,
		"status_code", result.StatusCode,
		"success_count", outcome.SuccessCount,
		"failed_count", outcome.FailedCount,
		"partial", outcome.Partial,
	)
	updated, err := u.repo.GetTask(ctx, task.ID)
	if err == nil {
		outcome.Task = updated
	}
	return outcome, nil
}

func (u *TaskUsecase) refreshForcedCallbackResults(ctx context.Context, task *Task, outcome *ForceImportCallbackOutcome) error {
	if _, err := u.GetProviderStatus(ctx, task.ID); err != nil {
		return err
	}
	outcome.ProviderStatusFetched = true

	fileResult, err := u.fetchProviderResultFiles(ctx, task)
	if err != nil {
		return err
	}
	outcome.ProviderFilesFetched = true
	outcome.FileCount = len(fileResult.FileURLs)

	importedFileCount, rowCount, err := u.forceImportProviderFiles(ctx, task, fileResult.FileURLs)
	if err != nil {
		return err
	}
	outcome.ImportedFileCount = importedFileCount
	outcome.RowCount = rowCount
	if repo, ok := u.repo.(interface {
		RefreshTaskValidation(context.Context, int64) error
	}); ok {
		return repo.RefreshTaskValidation(ctx, task.ID)
	}
	return nil
}

// forceImportCallbackEventPayload builds the FORCE_IMPORT_CALLBACK event payload.
func (u *TaskUsecase) forceImportCallbackEventPayload(reason string, outcome ForceImportCallbackOutcome, targetURL string, statusCode int, delivered bool, requestPayload json.RawMessage, responseBody string) map[string]any {
	return map[string]any{
		"reason":                reason,
		"providerStatusFetched": outcome.ProviderStatusFetched,
		"providerFilesFetched":  outcome.ProviderFilesFetched,
		"fileCount":             outcome.FileCount,
		"importedFileCount":     outcome.ImportedFileCount,
		"rowCount":              outcome.RowCount,
		"taskItemCount":         outcome.TaskItemCount,
		"successCount":          outcome.SuccessCount,
		"failedCount":           outcome.FailedCount,
		"unfinishedCount":       outcome.UnfinishedCount,
		"callbackMode":          outcome.CallbackMode,
		"yangshiUploaded":       outcome.YangshiUploaded,
		"partial":               outcome.Partial,
		"taskStatusUnchanged":   outcome.TaskStatusUnchanged,
		"delivered":             delivered,
		"targetUrl":             targetURL,
		"statusCode":            statusCode,
		"requestPayload":        json.RawMessage(requestPayload),
		"responseBody":          responseBody,
	}
}

// forceImportProviderFiles imports the provider's latest result files without
// changing the task status. Content quality is validated and aggregated separately.
func (u *TaskUsecase) forceImportProviderFiles(ctx context.Context, task *Task, fileURLs []string) (int, int, error) {
	storedFiles, externalURLs, missingObjectKeys, err := u.matchProviderOwnOSSFiles(ctx, task, fileURLs, true)
	if err != nil {
		return 0, 0, err
	}
	if len(missingObjectKeys) > 0 {
		return 0, 0, fmt.Errorf("provider result files missing from database: %d object keys, first=%s", len(missingObjectKeys), missingObjectKeys[0])
	}
	importedFileCount := 0
	totalRows := 0
	var firstImportErr error
	for _, file := range storedFiles {
		rows, ok, err := u.forceImportStoredFile(ctx, task, file)
		if err != nil {
			if firstImportErr == nil {
				firstImportErr = err
			}
			slog.WarnContext(ctx, "force import stored file failed",
				"trace_id", trace.RequestID(ctx),
				"task_id", task.ID,
				"object_key", file.ObjectKey,
				"error", err,
			)
			continue
		}
		if ok {
			importedFileCount++
			totalRows += rows
		}
	}
	for _, fileURL := range externalURLs {
		rows, ok, err := u.forceImportExternalFile(ctx, task, fileURL)
		if err != nil {
			if firstImportErr == nil {
				firstImportErr = err
			}
			slog.WarnContext(ctx, "force import external file failed",
				"trace_id", trace.RequestID(ctx),
				"task_id", task.ID,
				"file_url", fileURL,
				"error", err,
			)
			continue
		}
		if ok {
			importedFileCount++
			totalRows += rows
		}
	}
	if len(fileURLs) > 0 && importedFileCount == 0 && firstImportErr != nil {
		return 0, 0, fmt.Errorf("all forced result files failed to import: %w", firstImportErr)
	}
	return importedFileCount, totalRows, nil
}

func (u *TaskUsecase) forceImportStoredFile(ctx context.Context, task *Task, file ResultFileInput) (int, bool, error) {
	imported, err := u.resultFileImported(ctx, task.ID, file)
	if err != nil {
		return 0, false, err
	}
	if imported {
		return 0, false, nil
	}
	if file.SourceType == "" {
		file.SourceType = "OSS_UPLOAD"
	}
	if file.FileName == "" {
		file.FileName = path.Base(file.ObjectKey)
	}
	content, err := u.store.ReadObject(ctx, file.ObjectKey)
	if err != nil {
		return 0, false, fmt.Errorf("read oss object: %w", err)
	}
	parsed, err := u.parser.Parse(ctx, file.FileName, content)
	if err != nil {
		return 0, false, fmt.Errorf("parse result: %w", err)
	}
	file.Checksum = checksum(content)
	file.ExternalBatchID = task.ProviderExternalBatchID
	file.NotifyCallback = false
	u.validateParsedResult(ctx, parsed)
	if err := u.repo.MarkResultImported(ctx, task.ID, file, *parsed); err != nil {
		return 0, false, err
	}
	return parsed.RowCount, true, nil
}

func (u *TaskUsecase) forceImportExternalFile(ctx context.Context, task *Task, fileURL string) (int, bool, error) {
	imported, checkErr := u.resultFileImported(ctx, task.ID, ResultFileInput{FileURL: fileURL})
	if checkErr != nil {
		return 0, false, checkErr
	}
	if imported {
		return 0, false, nil
	}
	provider, ok := u.providers.Get(task.ProviderCode)
	if !ok {
		return 0, false, ErrProviderNotFound
	}
	content, err := provider.DownloadResultFile(ctx, fileURL)
	if err != nil {
		return 0, false, err
	}
	fileName := fileNameFromURL(fileURL)
	parsed, err := u.parser.Parse(ctx, fileName, content)
	if err != nil {
		return 0, false, err
	}
	file := ResultFileInput{
		SourceType:      "PROVIDER_FILE",
		FileURL:         fileURL,
		FileName:        fileName,
		Checksum:        checksum(content),
		ExternalBatchID: task.ProviderExternalBatchID,
		NotifyCallback:  false,
	}
	u.validateParsedResult(ctx, parsed)
	if err := u.repo.MarkResultImported(ctx, task.ID, file, *parsed); err != nil {
		return 0, false, err
	}
	return parsed.RowCount, true, nil
}

type manualCallbackKey struct{}

func (u *TaskUsecase) deliverTaskCallback(ctx context.Context, task *Task, successOnly bool, ignoreRisk ...bool) (bool, error) {
	if !taskProcessingLocked(ctx, task.ID) {
		var sent bool
		err := u.withTaskLock(ctx, task.ID, func(ctx context.Context) error {
			var err error
			sent, err = u.deliverTaskCallback(ctx, task, successOnly, ignoreRisk...)
			return err
		})
		return sent, err
	}
	current, err := u.repo.GetTask(ctx, task.ID)
	if err != nil {
		return false, err
	}
	if current.Status == TaskStatusReassigned {
		return false, ErrTaskClosed
	}
	bypassRisk := len(ignoreRisk) > 0 && ignoreRisk[0]
	if current.RiskStatsUpdatedAt == nil || (current.RiskCount > 0 && !bypassRisk && !AllowsRiskCallback(ctx, current.ProviderCode)) {
		return false, nil
	}
	manual, _ := ctx.Value(manualCallbackKey{}).(bool)
	if !manual && current.Status != TaskStatusCallbackPending {
		return false, nil
	}
	if successOnly {
		current.TotalCount, current.SuccessCount, current.FailedCount = task.TotalCount, task.SuccessCount, task.FailedCount
	}
	task = current
	startedAt := time.Now()
	callbackExternalBatchID := taskCallbackExternalBatchID(task)
	resultURL := u.resultURL(callbackExternalBatchID)
	if successOnly {
		resultURL += "?successOnly=true"
	}
	payload := CallbackDelivery{
		ExternalBatchID: callbackExternalBatchID,
		BatchNo:         taskCallbackBatchNo(task),
		Status:          TaskStatusDone,
		ResultURL:       resultURL,
		Counts: CallbackCounts{
			Total:   task.TotalCount,
			Success: task.SuccessCount,
			Failed:  task.FailedCount,
		},
	}
	payload.ResultFiles = u.callbackResultFiles(ctx, task)
	if items, err := u.repo.ListTaskItemResults(ctx, task.ID); err == nil {
		if successOnly {
			items = successfulResultItems(items)
		}
		payload.ResultItems = items
	} else {
		return false, err
	}
	result, err := u.callback.Deliver(ctx, task, payload)
	payloadBytes, _ := json.Marshal(payload)
	if len(result.RequestPayload) > 0 {
		payloadBytes = result.RequestPayload
	}
	targetURL := task.CallbackURL
	if result.TargetURL != "" {
		targetURL = result.TargetURL
	}
	if err != nil {
		attemptCount, stopped, markErr := u.repo.MarkCallbackFailed(ctx, task.ID, targetURL, err.Error(), payloadBytes, u.callbackMaxTry)
		if markErr != nil {
			return false, markErr
		}
		_ = u.repo.AddEvent(ctx, task.ID, EventBusinessCallbackFail, callbackEventPayload(result, targetURL, map[string]any{
			"error":        err.Error(),
			"attemptCount": attemptCount,
			"stopped":      stopped,
		}))
		slog.WarnContext(ctx, "business callback failed",
			"trace_id", trace.RequestID(ctx),
			"task_id", task.ID,
			"job_id", task.JobID,
			"batch_no", task.BatchNo,
			"provider_code", task.ProviderCode,
			"target_url", targetURL,
			"attempt_count", attemptCount,
			"max_retry", u.callbackMaxTry,
			"stopped", stopped,
			"latency_ms", time.Since(startedAt).Milliseconds(),
			"error", err,
		)
		return false, nil
	}
	if err := u.repo.MarkCallbackDone(ctx, task.ID, targetURL, result.StatusCode, result.ResponseBody, payloadBytes); err != nil {
		return false, err
	}
	_ = u.repo.AddEvent(ctx, task.ID, EventBusinessCallbackOK, callbackEventPayload(result, targetURL, nil))
	slog.InfoContext(ctx, "business callback delivered",
		"trace_id", trace.RequestID(ctx),
		"task_id", task.ID,
		"job_id", task.JobID,
		"batch_no", task.BatchNo,
		"provider_code", task.ProviderCode,
		"target_url", targetURL,
		"status_code", result.StatusCode,
		"latency_ms", time.Since(startedAt).Milliseconds(),
	)
	return true, nil
}

func successfulResultItems(items []TaskItemResultView) []TaskItemResultView {
	successful := make([]TaskItemResultView, 0, len(items))
	for _, item := range items {
		if item.ParseStatus == "SUCCESS" {
			successful = append(successful, item)
		}
	}
	return successful
}

func failedResultItemCount(items []TaskItemResultView) int {
	statusByItem := make(map[string]string, len(items))
	for _, item := range items {
		switch item.ParseStatus {
		case "SUCCESS":
			statusByItem[item.ItemID] = "SUCCESS"
		case "":
			continue
		default:
			if statusByItem[item.ItemID] != "SUCCESS" {
				statusByItem[item.ItemID] = "FAILED"
			}
		}
	}
	failed := 0
	for _, status := range statusByItem {
		if status == "FAILED" {
			failed++
		}
	}
	return failed
}

func callbackResultItems(items []TaskItemResultView) []CallbackResultItem {
	out := make([]CallbackResultItem, 0, len(items))
	for _, item := range items {
		out = append(out, CallbackResultItem{
			ItemID:                   item.ItemID,
			PlatformCode:             item.PlatformCode,
			Seq:                      item.Seq,
			Question:                 item.Question,
			AnswerText:               item.AnswerText,
			ThinkingProcess:          item.ThinkingProcess,
			GeneratedAt:              callbackResultTime(item.GeneratedAt),
			GeneratedAtText:          item.GeneratedAtText,
			RecommendedQuestionsJSON: item.RecommendedQuestions,
			ReferencesJSON:           item.ReferencesJSON,
			ReferenceCount:           item.ReferenceCount,
			ParseStatus:              item.ParseStatus,
		})
	}
	return out
}

func callbackResultTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func callbackEventPayload(result CallbackResult, targetURL string, extra map[string]any) map[string]any {
	payload := map[string]any{
		"targetUrl":  targetURL,
		"statusCode": result.StatusCode,
	}
	for key, value := range result.EventPayload {
		payload[key] = value
	}
	for key, value := range extra {
		payload[key] = value
	}
	return payload
}

func (u *TaskUsecase) callbackResultFiles(ctx context.Context, task *Task) []CallbackResultFile {
	files := make([]CallbackResultFile, 0, 4)
	if task.ResultObjectKey != "" {
		files = append(files, CallbackResultFile{
			SourceType: "OSS_UPLOAD",
			ObjectKey:  task.ResultObjectKey,
			FileName:   task.ResultFileName,
		})
	}
	stored, err := u.repo.ListTaskResultFiles(ctx, task.ID)
	if err != nil {
		slog.WarnContext(ctx, "list callback result files failed",
			"trace_id", trace.RequestID(ctx),
			"task_id", task.ID,
			"batch_no", task.BatchNo,
			"error", err,
		)
		return files
	}
	seen := map[string]bool{}
	for _, file := range files {
		seen[file.ObjectKey+"|"+file.FileURL] = true
	}
	for _, file := range stored {
		key := file.ObjectKey + "|" + file.FileURL
		if seen[key] {
			continue
		}
		seen[key] = true
		files = append(files, file)
	}
	return files
}

func (u *TaskUsecase) submitOne(ctx context.Context, task *Task) (SubmitResult, error) {
	return u.submitOneWithBatchNo(ctx, task, "")
}

func (u *TaskUsecase) submitOneWithBatchNo(ctx context.Context, task *Task, batchNo string) (SubmitResult, error) {
	preparedTask, err := u.repo.PrepareTaskForSubmit(ctx, task.ID, u.providerDispatchSettings(ctx), u.publicBaseURL)
	if err != nil {
		return SubmitResult{}, err
	}
	task = preparedTask
	provider, ok := u.providers.Get(task.ProviderCode)
	if !ok {
		if err := u.repo.MarkSubmitFailed(ctx, task.ID, ErrProviderNotFound.Error()); err != nil {
			return SubmitResult{}, err
		}
		return SubmitResult{}, ErrProviderNotFound
	}
	items, err := u.repo.ListTaskItems(ctx, task.ID)
	if err != nil {
		errMessage := fmt.Sprintf("load task items: %v", err)
		if markErr := u.repo.MarkSubmitFailed(ctx, task.ID, errMessage); markErr != nil {
			return SubmitResult{}, markErr
		}
		return SubmitResult{}, fmt.Errorf("%s", errMessage)
	}
	if len(items) == 0 {
		if markErr := u.repo.MarkSubmitFailed(ctx, task.ID, ErrInvalidTaskRequest.Error()); markErr != nil {
			return SubmitResult{}, markErr
		}
		return SubmitResult{}, ErrInvalidTaskRequest
	}
	if strings.TrimSpace(batchNo) == "" {
		batchNo = taskProviderBatchNo(task)
	}
	payload := SubmitTaskPayload{
		BatchNo:      batchNo,
		ProviderCode: task.ProviderCode,
		BizDate:      task.BizDate.Format("2006-01-02"),
		CallbackURL:  task.ProviderCallbackURL,
		Items:        items,
	}
	payload = adaptCustomerSubmitRequest(task, payload)
	providerRequest, _ := json.Marshal(payload)
	result, err := provider.SubmitTask(ctx, task, payload)
	if err != nil {
		_ = u.repo.AddEvent(ctx, task.ID, EventProviderSubmitFailed, map[string]any{"error": err.Error()})
		slog.WarnContext(ctx, "provider submit failed",
			"trace_id", trace.RequestID(ctx),
			"task_id", task.ID,
			"batch_no", task.BatchNo,
			"provider_code", task.ProviderCode,
			"item_count", len(items),
			"error", err,
		)
		if markErr := u.repo.MarkSubmitFailed(ctx, task.ID, err.Error()); markErr != nil {
			return SubmitResult{}, markErr
		}
		return SubmitResult{}, err
	}
	if result.AcceptedCount > 0 && result.AcceptedCount != len(items) {
		err := fmt.Errorf("provider accepted count mismatch: accepted=%d task_items=%d", result.AcceptedCount, len(items))
		_ = u.repo.AddEvent(ctx, task.ID, EventProviderSubmitFailed, map[string]any{
			"error":         err.Error(),
			"acceptedCount": result.AcceptedCount,
			"taskItemCount": len(items),
		})
		if markErr := u.repo.MarkSubmitFailed(ctx, task.ID, err.Error()); markErr != nil {
			return SubmitResult{}, markErr
		}
		return SubmitResult{}, err
	}
	if err := u.repo.MarkSubmitted(ctx, task.ID, providerRequest, result); err != nil {
		return SubmitResult{}, err
	}
	slog.InfoContext(ctx, "provider submit completed",
		"trace_id", trace.RequestID(ctx),
		"task_id", task.ID,
		"batch_no", task.BatchNo,
		"provider_code", task.ProviderCode,
		"external_batch_id", result.ExternalBatchID,
		"accepted_count", result.AcceptedCount,
	)
	if err := u.repo.AddEvent(ctx, task.ID, EventProviderSubmitted, map[string]any{
		"providerExternalBatchId": result.ExternalBatchID,
		"acceptedCount":           result.AcceptedCount,
	}); err != nil {
		return SubmitResult{}, err
	}
	return result, nil
}

func taskProviderBatchNo(task *Task) string {
	if task != nil && task.ReassignedFromTaskID > 0 && strings.TrimSpace(task.CallbackBatchNo) != "" {
		return task.CallbackBatchNo
	}
	if task == nil {
		return ""
	}
	return task.BatchNo
}

func taskCallbackBatchNo(task *Task) string {
	if task != nil && strings.TrimSpace(task.CallbackBatchNo) != "" {
		return task.CallbackBatchNo
	}
	if task == nil {
		return ""
	}
	return task.BatchNo
}

func taskCallbackExternalBatchID(task *Task) string {
	if task != nil && strings.TrimSpace(task.CallbackExternalBatchID) != "" {
		return task.CallbackExternalBatchID
	}
	if task == nil {
		return ""
	}
	return task.JobID
}

func newReassignBatchNo(batchNo string) string {
	suffix := shortRandomHex(4)
	prefix := strings.TrimSpace(batchNo)
	maxPrefixLen := 128 - len("-REASSIGN-") - len(suffix)
	if maxPrefixLen < 1 {
		maxPrefixLen = 1
	}
	if len(prefix) > maxPrefixLen {
		prefix = prefix[:maxPrefixLen]
	}
	return prefix + "-REASSIGN-" + suffix
}

func shortRandomHex(size int) string {
	random := make([]byte, size)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(random)
}

func (u *TaskUsecase) fetchProviderResultFiles(ctx context.Context, task *Task) (ProviderFilesResult, error) {
	if task.ProviderExternalBatchID == "" {
		return ProviderFilesResult{}, fmt.Errorf("%w: external batch id is empty", ErrInvalidTaskRequest)
	}
	provider, ok := u.providers.Get(task.ProviderCode)
	if !ok {
		return ProviderFilesResult{}, ErrProviderNotFound
	}
	return provider.FetchResultFiles(ctx, task.ProviderExternalBatchID)
}

func (u *TaskUsecase) resultURL(jobID string) string {
	return fmt.Sprintf("%s/api/v1/external-tasks/%s/result", u.publicBaseURL, jobID)
}

func (u *TaskUsecase) ResultURL(jobID string) string {
	return u.resultURL(jobID)
}

func newJobID() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("job_%d", time.Now().UnixNano())
	}
	return "job_" + hex.EncodeToString(random)
}

func checksum(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func fileNameFromURL(fileURL string) string {
	parsed, err := url.Parse(fileURL)
	if err != nil {
		return path.Base(fileURL)
	}
	name := path.Base(parsed.Path)
	if name == "." || name == "/" || name == "" {
		return "result.xlsx"
	}
	return name
}

func (u *TaskUsecase) ownOSSObjectKey(rawURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		host = strings.ToLower(parsed.Host)
	}
	matched := false
	for _, ownHost := range u.ossPublicHosts {
		if host == ownHost {
			matched = true
			break
		}
	}
	if !matched {
		return "", false
	}
	objectKey, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		objectKey = parsed.Path
	}
	objectKey = strings.TrimLeft(objectKey, "/")
	if objectKey == "" {
		return "", false
	}
	return objectKey, true
}

func normalizeURLHosts(values []string) []string {
	seen := map[string]bool{}
	var hosts []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		host := value
		if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
			host = parsed.Hostname()
		}
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	return hosts
}

func (u *TaskUsecase) resultUploadObjectKey(ctx context.Context, providerCode, batchNo, fileName string) string {
	name := path.Base(fileName)
	random := make([]byte, 4)
	_, _ = rand.Read(random)
	prefix := u.ossUploadDir
	if u.providerConfig != nil {
		if settings, ok := u.providerConfig.GetSettings(ctx, providerCode); ok && settings.BucketPathPrefix != "" {
			prefix = settings.BucketPathPrefix
		}
	}
	parts := []string{}
	if prefix != "" {
		parts = append(parts, strings.Trim(prefix, "/"))
	}
	parts = append(parts, batchNo, hex.EncodeToString(random)+"-"+name)
	return strings.Join(parts, "/")
}

func (u *TaskUsecase) temporaryDownloadObjectKey(fileName string) string {
	name := path.Base(fileName)
	random := make([]byte, 8)
	_, _ = rand.Read(random)
	parts := []string{}
	if u.ossUploadDir != "" {
		parts = append(parts, strings.Trim(u.ossUploadDir, "/"))
	}
	parts = append(parts, "tmp", "downloads", time.Now().UTC().Format("20060102"), hex.EncodeToString(random)+"-"+name)
	return strings.Join(parts, "/")
}
