package biz

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestOwnOSSObjectKeyDecodesEncodedPath(t *testing.T) {
	usecase := NewTaskUsecase(nil, nil, nil, nil, nil, nil, TaskUsecaseOptions{
		OSSPublicHosts: []string{
			"https://opensource-man.oss-cn-shanghai.aliyuncs.com",
			"opensource-man.oss-cn-shanghai.aliyuncs.com",
		},
	})

	got, ok := usecase.ownOSSObjectKey("https://opensource-man.oss-cn-shanghai.aliyuncs.com/cdp-router%2Fq2a%2Fmpzdata%2FQ2A4-20260730-mpzdata2-2d1d0193%2F356a6b65-%E6%95%B0%E6%8D%AE%E9%87%87%E9%9B%86_2026-07-30T08-20-24.xlsx")
	if !ok {
		t.Fatal("expected URL to match own OSS host")
	}
	want := "cdp-router/q2a/mpzdata/Q2A4-20260730-mpzdata2-2d1d0193/356a6b65-数据采集_2026-07-30T08-20-24.xlsx"
	if got != want {
		t.Fatalf("object key=%q, want %q", got, want)
	}
}

func TestOwnOSSObjectKeyIgnoresExternalHost(t *testing.T) {
	usecase := NewTaskUsecase(nil, nil, nil, nil, nil, nil, TaskUsecaseOptions{
		OSSPublicHosts: []string{"https://opensource-man.oss-cn-shanghai.aliyuncs.com"},
	})

	if got, ok := usecase.ownOSSObjectKey("https://example.com/cdp-router%2Fq2a%2Fresult.xlsx"); ok {
		t.Fatalf("object key=%q, want external URL ignored", got)
	}
}

func TestMatchStoredProviderFilesReportsMissingObjectKeys(t *testing.T) {
	task := &Task{ProviderExternalBatchID: "external-1"}
	dbFiles := []ResultFileInput{
		{SourceType: "OSS_UPLOAD", ObjectKey: "cdp-router/q2a/mpzdata/batch/a.xlsx", FileName: "a.xlsx", ParseStatus: "PENDING"},
	}

	matched, missing := matchStoredProviderFiles(task, []string{
		"cdp-router/q2a/mpzdata/batch/a.xlsx",
		"cdp-router/q2a/mpzdata/batch/b.xlsx",
	}, dbFiles, false)

	if len(matched) != 1 {
		t.Fatalf("matched=%d, want 1", len(matched))
	}
	if matched[0].ObjectKey != "cdp-router/q2a/mpzdata/batch/a.xlsx" {
		t.Fatalf("matched object key=%q", matched[0].ObjectKey)
	}
	if len(missing) != 1 || missing[0] != "cdp-router/q2a/mpzdata/batch/b.xlsx" {
		t.Fatalf("missing=%v, want b.xlsx", missing)
	}
}

func TestMatchStoredProviderFilesSkipsSuccessUnlessForce(t *testing.T) {
	task := &Task{ProviderExternalBatchID: "external-1"}
	dbFiles := []ResultFileInput{
		{SourceType: "OSS_UPLOAD", ObjectKey: "cdp-router/q2a/mpzdata/batch/a.xlsx", FileName: "a.xlsx", ParseStatus: "SUCCESS"},
	}
	keys := []string{"cdp-router/q2a/mpzdata/batch/a.xlsx"}

	matched, missing := matchStoredProviderFiles(task, keys, dbFiles, false)
	if len(missing) != 0 {
		t.Fatalf("missing=%v, want none", missing)
	}
	if len(matched) != 0 {
		t.Fatalf("matched=%d, want 0 without force", len(matched))
	}

	matched, missing = matchStoredProviderFiles(task, keys, dbFiles, true)
	if len(missing) != 0 {
		t.Fatalf("missing=%v, want none", missing)
	}
	if len(matched) != 1 {
		t.Fatalf("matched=%d, want 1 with force", len(matched))
	}
}

func TestForcedCallbackResultItemsOnlyIncludeSuccessfulResults(t *testing.T) {
	items := []TaskItemResultView{
		{ItemID: "item-1", Seq: 1, ParseStatus: "SUCCESS", AnswerText: "answer"},
		{ItemID: "item-2", Seq: 1, ParseStatus: "FAILED", ParseError: "empty answer"},
		{ItemID: "item-3", Seq: 1, ParseStatus: ""},
	}

	successItems := successfulResultItems(items)
	payloadItems := callbackResultItems(successItems)

	if len(payloadItems) != 1 {
		t.Fatalf("payload item count=%d, want 1", len(payloadItems))
	}
	if payloadItems[0].ItemID != "item-1" || payloadItems[0].AnswerText != "answer" {
		t.Fatalf("payload item=%+v, want successful item-1", payloadItems[0])
	}
}

func TestForcedCallbackProgressCountsTreatSuccessAsFinalForItem(t *testing.T) {
	items := []TaskItemResultView{
		{ItemID: "item-1", Seq: 1, ParseStatus: "SUCCESS"},
		{ItemID: "item-1", Seq: 2, ParseStatus: "FAILED"},
		{ItemID: "item-2", Seq: 1, ParseStatus: "FAILED"},
		{ItemID: "item-3", Seq: 1, ParseStatus: ""},
	}

	successCount := len(successfulResultItems(items))
	failedCount := failedResultItemCount(items)
	unfinishedCount := maxInt(3-successCount-failedCount, 0)

	if successCount != 1 {
		t.Fatalf("successCount=%d, want 1", successCount)
	}
	if failedCount != 1 {
		t.Fatalf("failedCount=%d, want only item-2", failedCount)
	}
	if unfinishedCount != 1 {
		t.Fatalf("unfinishedCount=%d, want 1", unfinishedCount)
	}
}

func TestForceImportCallbackDeliversOnlySuccessfulItems(t *testing.T) {
	ctx := context.Background()
	task := &Task{
		ID:                      100,
		JobID:                   "job-force",
		BatchNo:                 "batch-force",
		ProviderCode:            "vendor_force",
		ProviderExternalBatchID: "provider-batch",
		CallbackURL:             "https://business.example.com/callback",
		Status:                  TaskStatusSubmitted,
	}
	repo := &forceImportCallbackRepo{task: task}
	callback := &captureCallbackClient{}
	usecase := NewTaskUsecase(
		repo,
		forceImportCallbackRegistry{provider: forceImportCallbackProvider{}},
		nil,
		nil,
		forceImportCallbackParser{},
		callback,
		TaskUsecaseOptions{PublicBaseURL: "https://gateway.example.com"},
	)

	outcome, err := usecase.ForceImportCallback(ctx, ForceImportCallbackInput{
		TaskID: 100,
		Reason: "submit current successful results",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Delivered || outcome.Rejected {
		t.Fatalf("outcome delivered=%v rejected=%v, want delivered", outcome.Delivered, outcome.Rejected)
	}
	if outcome.SuccessCount != 1 || outcome.FailedCount != 1 || outcome.UnfinishedCount != 1 {
		t.Fatalf("counts success=%d failed=%d unfinished=%d, want 1/1/1", outcome.SuccessCount, outcome.FailedCount, outcome.UnfinishedCount)
	}
	if callback.payload.Counts.Total != 1 || callback.payload.Counts.Success != 1 || callback.payload.Counts.Failed != 0 {
		t.Fatalf("callback counts=%+v, want submit only one successful result", callback.payload.Counts)
	}
	if len(callback.payload.Items) != 1 || callback.payload.Items[0].ItemID != "item-success" {
		t.Fatalf("callback items=%+v, want only successful item", callback.payload.Items)
	}
	if len(callback.payload.ResultItems) != 1 || callback.payload.ResultItems[0].ParseStatus != "SUCCESS" {
		t.Fatalf("yangshi result items=%+v, want only success", callback.payload.ResultItems)
	}
	if !repo.forcedDone || repo.markCallbackDoneCalled {
		t.Fatalf("forcedDone=%v markCallbackDoneCalled=%v, want forced callback recorder only", repo.forcedDone, repo.markCallbackDoneCalled)
	}
	if repo.markedResult.ItemResults[1].ParseStatus != "FAILED" {
		t.Fatalf("empty answer parse status=%q, want FAILED", repo.markedResult.ItemResults[1].ParseStatus)
	}
	if len(repo.eventTypes) == 0 || repo.eventTypes[len(repo.eventTypes)-1] != EventForceImportCallback {
		t.Fatalf("events=%v, want FORCE_IMPORT_CALLBACK", repo.eventTypes)
	}
}

func TestDeliverTaskCallbackUsesOriginalIdentifiersForReassignedTask(t *testing.T) {
	ctx := context.Background()
	callback := &captureCallbackClient{}
	usecase := NewTaskUsecase(
		callbackPayloadRepo{},
		nil,
		nil,
		nil,
		nil,
		callback,
		TaskUsecaseOptions{PublicBaseURL: "https://gateway.example.com"},
	)
	task := &Task{
		ID:                      22,
		JobID:                   "job-internal",
		BatchNo:                 "batch-internal",
		ProviderCode:            "vendor_b",
		ReassignedFromTaskID:    11,
		CallbackBatchNo:         "batch-original",
		CallbackExternalBatchID: "job-original",
		CallbackURL:             "https://business.example.com/callback",
		TotalCount:              2,
		SuccessCount:            2,
		Status:                  TaskStatusCallbackPending,
	}

	delivered, err := usecase.deliverTaskCallback(ctx, task, false)
	if err != nil {
		t.Fatal(err)
	}
	if !delivered {
		t.Fatal("expected callback delivered")
	}
	if callback.payload.BatchNo != "batch-original" {
		t.Fatalf("callback batchNo=%q, want original batch", callback.payload.BatchNo)
	}
	if callback.payload.ExternalBatchID != "job-original" {
		t.Fatalf("callback externalBatchId=%q, want original external id", callback.payload.ExternalBatchID)
	}
	if callback.payload.ResultURL != "https://gateway.example.com/api/v1/external-tasks/job-original/result" {
		t.Fatalf("callback resultURL=%q", callback.payload.ResultURL)
	}
}

func TestReassignTaskRejectsDisabledProvider(t *testing.T) {
	usecase := NewTaskUsecase(
		unsupportedTaskRepo{},
		callbackReconcileRegistry{provider: &callbackReconcileProvider{}},
		providerSettingsMap{
			"liuandata": {ProviderCode: "liuandata", Enabled: false},
		},
		nil,
		nil,
		nil,
		TaskUsecaseOptions{},
	)

	_, err := usecase.ReassignTask(context.Background(), ReassignTaskInput{
		BatchNo:      "batch-original",
		ProviderCode: "liuandata",
	})
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("reassign disabled provider err=%v, want provider unavailable", err)
	}
}

func TestProviderCallbackReconcilesAndImportsFilesWhenTaskNotDone(t *testing.T) {
	ctx := context.Background()
	task := &Task{
		ID:                      11,
		BatchNo:                 "batch-reconcile",
		ProviderCode:            "mpzdata",
		ProviderExternalBatchID: "external-1",
		Status:                  TaskStatusSubmitted,
	}
	objectKey := "cdp-router/q2a/mpzdata/batch-reconcile/result.xlsx"
	repo := &callbackReconcileRepo{
		task: task,
		uploaded: []ResultFileInput{{
			SourceType:  "OSS_UPLOAD",
			ObjectKey:   objectKey,
			FileName:    "result.xlsx",
			ParseStatus: "PENDING",
		}},
	}
	provider := &callbackReconcileProvider{
		files: ProviderCompletionFilesResult{
			Handled: true,
			Result: ProviderFilesResult{
				ExternalBatchID: "external-1",
				FileURLs: []string{
					"https://opensource-man.oss-cn-shanghai.aliyuncs.com/" + objectKey,
				},
			},
		},
	}
	usecase := NewTaskUsecase(
		repo,
		callbackReconcileRegistry{provider: provider},
		nil,
		callbackReconcileStore{content: []byte("xlsx"), exists: true},
		callbackReconcileParser{},
		nil,
		TaskUsecaseOptions{OSSPublicHosts: []string{"https://opensource-man.oss-cn-shanghai.aliyuncs.com"}},
	)

	updated, err := usecase.HandleProviderCallback(ctx, ProviderCallbackInput{
		ProviderCode:    "mpzdata",
		BatchNo:         task.BatchNo,
		ExternalBatchID: "external-1",
		Status:          "DONE",
		TotalCount:      1,
		SuccessCount:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != TaskStatusCallbackPending {
		t.Fatalf("updated status=%s, want %s", updated.Status, TaskStatusCallbackPending)
	}
	if provider.completionCalls != 1 {
		t.Fatalf("completion calls=%d, want 1", provider.completionCalls)
	}
	if repo.markImportedCalls != 1 {
		t.Fatalf("mark imported calls=%d, want 1", repo.markImportedCalls)
	}
	if repo.completeCalls != 1 {
		t.Fatalf("complete calls=%d, want 1", repo.completeCalls)
	}
	if len(repo.pendingFiles) != 1 {
		t.Fatalf("pending files=%d, want 1", len(repo.pendingFiles))
	}
	if repo.pendingFiles[0].ObjectKey != objectKey {
		t.Fatalf("pending object key=%q", repo.pendingFiles[0].ObjectKey)
	}
}

func TestProviderCallbackSkipsReconcileWhenTaskDone(t *testing.T) {
	ctx := context.Background()
	task := &Task{
		ID:                      12,
		BatchNo:                 "batch-done",
		ProviderCode:            "mpzdata",
		ProviderExternalBatchID: "external-1",
		Status:                  TaskStatusDone,
		TotalCount:              1,
		SuccessCount:            1,
	}
	repo := &callbackReconcileRepo{task: task}
	provider := &callbackReconcileProvider{}
	usecase := NewTaskUsecase(
		repo,
		callbackReconcileRegistry{provider: provider},
		nil,
		callbackReconcileStore{},
		callbackReconcileParser{},
		nil,
		TaskUsecaseOptions{OSSPublicHosts: []string{"https://opensource-man.oss-cn-shanghai.aliyuncs.com"}},
	)

	updated, err := usecase.HandleProviderCallback(ctx, ProviderCallbackInput{
		ProviderCode:    "mpzdata",
		BatchNo:         task.BatchNo,
		ExternalBatchID: "external-1",
		Status:          "DONE",
		TotalCount:      1,
		SuccessCount:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != TaskStatusDone {
		t.Fatalf("updated status=%s, want %s", updated.Status, TaskStatusDone)
	}
	if provider.completionCalls != 0 {
		t.Fatalf("completion calls=%d, want 0", provider.completionCalls)
	}
	if repo.markImportedCalls != 0 {
		t.Fatalf("mark imported calls=%d, want 0", repo.markImportedCalls)
	}
}

func TestProviderCallbackImportsPendingFilesWhenTaskAlreadyImported(t *testing.T) {
	ctx := context.Background()
	task := &Task{
		ID:                      14,
		BatchNo:                 "batch-already-imported",
		ProviderCode:            "mpzdata",
		ProviderExternalBatchID: "external-1",
		Status:                  TaskStatusResultImported,
		TotalCount:              1,
		SuccessCount:            0,
	}
	objectKey := "cdp-router/q2a/mpzdata/batch-already-imported/late.xlsx"
	repo := &callbackReconcileRepo{
		task: task,
		pendingFiles: []ResultFileInput{{
			SourceType:  "OSS_UPLOAD",
			ObjectKey:   objectKey,
			FileName:    "late.xlsx",
			ParseStatus: "PENDING",
		}},
	}
	provider := &callbackReconcileProvider{}
	usecase := NewTaskUsecase(
		repo,
		callbackReconcileRegistry{provider: provider},
		nil,
		callbackReconcileStore{content: []byte("xlsx"), exists: true},
		callbackReconcileParser{},
		nil,
		TaskUsecaseOptions{OSSPublicHosts: []string{"https://opensource-man.oss-cn-shanghai.aliyuncs.com"}},
	)

	updated, err := usecase.HandleProviderCallback(ctx, ProviderCallbackInput{
		ProviderCode:    "mpzdata",
		BatchNo:         task.BatchNo,
		ExternalBatchID: "external-1",
		Status:          "DONE",
		TotalCount:      1,
		SuccessCount:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != TaskStatusCallbackPending {
		t.Fatalf("updated status=%s, want %s", updated.Status, TaskStatusCallbackPending)
	}
	if provider.completionCalls != 0 {
		t.Fatalf("completion calls=%d, want 0", provider.completionCalls)
	}
	if repo.markImportedCalls != 1 {
		t.Fatalf("mark imported calls=%d, want 1", repo.markImportedCalls)
	}
	if repo.completeCalls != 1 {
		t.Fatalf("complete calls=%d, want 1", repo.completeCalls)
	}
	if len(repo.importedFiles) != 1 || repo.importedFiles[0].ObjectKey != objectKey {
		t.Fatalf("imported files=%v, want %s", repo.importedFiles, objectKey)
	}
}

func TestManualSyncReconcilesFilesWhenTaskDone(t *testing.T) {
	ctx := context.Background()
	task := &Task{
		ID:                      13,
		BatchNo:                 "batch-done-manual",
		ProviderCode:            "mpzdata",
		ProviderExternalBatchID: "external-1",
		Status:                  TaskStatusDone,
		TotalCount:              1,
		SuccessCount:            1,
	}
	objectKey := "cdp-router/q2a/mpzdata/batch-done-manual/result.xlsx"
	repo := &callbackReconcileRepo{
		task:      task,
		itemCount: 1,
		uploaded: []ResultFileInput{{
			SourceType:  "OSS_UPLOAD",
			ObjectKey:   objectKey,
			FileName:    "result.xlsx",
			ParseStatus: "SUCCESS",
		}},
	}
	provider := &callbackReconcileProvider{
		status: ProviderStatusResult{
			ExternalBatchID: "external-1",
			TotalCount:      1,
			SuccessCount:    1,
		},
		resultFiles: ProviderFilesResult{
			ExternalBatchID: "external-1",
			FileURLs: []string{
				"https://opensource-man.oss-cn-shanghai.aliyuncs.com/" + objectKey,
			},
		},
	}
	usecase := NewTaskUsecase(
		repo,
		callbackReconcileRegistry{provider: provider},
		nil,
		callbackReconcileStore{content: []byte("xlsx")},
		callbackReconcileParser{},
		nil,
		TaskUsecaseOptions{OSSPublicHosts: []string{"https://opensource-man.oss-cn-shanghai.aliyuncs.com"}},
	)

	outcome, err := usecase.SyncProviderResult(ctx, task.ID, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.ProviderDone {
		t.Fatal("providerDone=false, want true")
	}
	if outcome.Task.Status != TaskStatusDone {
		t.Fatalf("updated status=%s, want %s", outcome.Task.Status, TaskStatusDone)
	}
	if repo.markImportedCalls != 1 {
		t.Fatalf("mark imported calls=%d, want 1", repo.markImportedCalls)
	}
	if provider.statusCalls != 1 {
		t.Fatalf("status calls=%d, want 1", provider.statusCalls)
	}
	if provider.resultFileCalls != 1 {
		t.Fatalf("result file calls=%d, want 1", provider.resultFileCalls)
	}
}

type callbackReconcileRepo struct {
	unsupportedTaskRepo
	task              *Task
	itemCount         int
	uploaded          []ResultFileInput
	pendingFiles      []ResultFileInput
	importedFiles     []ResultFileInput
	markImportedCalls int
	completeCalls     int
}

func (r *callbackReconcileRepo) GetTask(ctx context.Context, id int64) (*Task, error) {
	if r.task == nil || r.task.ID != id {
		return nil, ErrTaskNotFound
	}
	return cloneTask(r.task), nil
}

func (r *callbackReconcileRepo) GetTaskByBatchNo(ctx context.Context, batchNo string) (*Task, error) {
	if r.task == nil || r.task.BatchNo != batchNo {
		return nil, ErrTaskNotFound
	}
	return cloneTask(r.task), nil
}

func (r *callbackReconcileRepo) ResolveProviderTask(ctx context.Context, providerCode, batchNo string) (*Task, error) {
	if r.task == nil || r.task.BatchNo != batchNo || r.task.ProviderCode != providerCode {
		return nil, ErrTaskNotFound
	}
	return cloneTask(r.task), nil
}

func (r *callbackReconcileRepo) CountTaskItems(ctx context.Context, taskID int64) (int, error) {
	if r.itemCount > 0 {
		return r.itemCount, nil
	}
	return 1, nil
}

func (r *callbackReconcileRepo) MarkProviderCallback(ctx context.Context, input ProviderCallbackInput) (*Task, error) {
	if r.task == nil || r.task.BatchNo != input.BatchNo {
		return nil, ErrTaskNotFound
	}
	if r.task.Status != TaskStatusResultImported && r.task.Status != TaskStatusCallbackPending && r.task.Status != TaskStatusDone {
		r.task.Status = TaskStatusProviderDone
		r.task.TotalCount = input.TotalCount
		r.task.SuccessCount = input.SuccessCount
		r.task.FailedCount = input.FailedCount
	}
	if input.ExternalBatchID != "" {
		r.task.ProviderExternalBatchID = input.ExternalBatchID
	}
	return cloneTask(r.task), nil
}

func (r *callbackReconcileRepo) MarkProviderCallbackProgress(ctx context.Context, input ProviderCallbackInput) (*Task, error) {
	return cloneTask(r.task), nil
}

func (r *callbackReconcileRepo) MarkProviderStatus(ctx context.Context, taskID int64, result ProviderStatusResult) error {
	return nil
}

func (r *callbackReconcileRepo) ListUploadedResultFiles(ctx context.Context, taskID int64, includeImported bool) ([]ResultFileInput, error) {
	return append([]ResultFileInput(nil), r.uploaded...), nil
}

func (r *callbackReconcileRepo) ListPendingResultFiles(ctx context.Context, taskID int64) ([]ResultFileInput, error) {
	return append([]ResultFileInput(nil), r.pendingFiles...), nil
}

func (r *callbackReconcileRepo) UpsertPendingResultFiles(ctx context.Context, taskID int64, files []ResultFileInput) error {
	r.pendingFiles = append(r.pendingFiles, files...)
	return nil
}

func (r *callbackReconcileRepo) MarkResultImported(ctx context.Context, taskID int64, file ResultFileInput, result ParsedResult) error {
	r.markImportedCalls++
	r.importedFiles = append(r.importedFiles, file)
	if r.task.Status != TaskStatusDone {
		r.task.Status = TaskStatusResultImported
	}
	r.task.ResultRowCount += result.RowCount
	r.task.SuccessCount = 1
	r.task.TotalCount = 1
	return nil
}

func (r *callbackReconcileRepo) CompleteResultImport(ctx context.Context, taskID int64, notifyCallback bool) (*Task, bool, error) {
	r.completeCalls++
	if r.task.Status != TaskStatusDone {
		r.task.Status = TaskStatusCallbackPending
	}
	return cloneTask(r.task), true, nil
}

func (r *callbackReconcileRepo) AddEvent(ctx context.Context, taskID int64, eventType EventType, payload any) error {
	return nil
}

type callbackReconcileProvider struct {
	files           ProviderCompletionFilesResult
	status          ProviderStatusResult
	resultFiles     ProviderFilesResult
	completionCalls int
	statusCalls     int
	resultFileCalls int
}

func (p *callbackReconcileProvider) SubmitTask(ctx context.Context, task *Task, payload SubmitTaskPayload) (SubmitResult, error) {
	return SubmitResult{}, nil
}

func (p *callbackReconcileProvider) FetchStatus(ctx context.Context, externalBatchID string) (ProviderStatusResult, error) {
	p.statusCalls++
	return p.status, nil
}

func (p *callbackReconcileProvider) FetchResultFiles(ctx context.Context, externalBatchID string) (ProviderFilesResult, error) {
	p.resultFileCalls++
	return p.resultFiles, nil
}

func (p *callbackReconcileProvider) CompletionFiles(ctx context.Context, task *Task) (ProviderCompletionFilesResult, error) {
	p.completionCalls++
	return p.files, nil
}

func (p *callbackReconcileProvider) DownloadResultFile(ctx context.Context, fileURL string) ([]byte, error) {
	return nil, errors.New("unexpected provider download")
}

type callbackReconcileRegistry struct {
	provider ProviderClient
}

func (r callbackReconcileRegistry) Get(code string) (ProviderClient, bool) {
	return r.provider, r.provider != nil
}

func (r callbackReconcileRegistry) Codes() []string {
	return []string{"mpzdata"}
}

type providerSettingsMap map[string]ProviderSettings

func (m providerSettingsMap) GetSettings(ctx context.Context, providerCode string) (ProviderSettings, bool) {
	settings, ok := m[providerCode]
	return settings, ok
}

type callbackReconcileStore struct {
	content []byte
	exists  bool
}

func (s callbackReconcileStore) SignedPutURL(ctx context.Context, key string, expires time.Duration) (string, time.Time, error) {
	return "", time.Time{}, errors.New("unexpected signed put")
}

func (s callbackReconcileStore) PutTemporaryObject(ctx context.Context, key string, contentType string, content []byte, retentionDays int) error {
	return errors.New("unexpected temporary put")
}

func (s callbackReconcileStore) SignedGetURL(ctx context.Context, key string, expires time.Duration) (string, time.Time, error) {
	return "", time.Time{}, errors.New("unexpected signed get")
}

func (s callbackReconcileStore) ReadObject(ctx context.Context, key string) ([]byte, error) {
	return append([]byte(nil), s.content...), nil
}

func (s callbackReconcileStore) ObjectExists(ctx context.Context, key string) (bool, error) {
	return s.exists, nil
}

type callbackReconcileParser struct{}

func (p callbackReconcileParser) Parse(ctx context.Context, fileName string, content []byte) (*ParsedResult, error) {
	return &ParsedResult{
		Payload:       json.RawMessage(`{"items":[]}`),
		RowCount:      1,
		ValidRowCount: 1,
		Rows: []ResultRow{{
			SheetName: "AI回答",
			RowIndex:  2,
			ItemID:    "9493375",
			Seq:       1,
			RowData:   json.RawMessage(`{"itemId":"9493375"}`),
			RowHash:   "hash",
		}},
		ItemResults: []TaskItemResult{{
			ItemID:               "9493375",
			Seq:                  1,
			RecommendedQuestions: json.RawMessage(`[]`),
			ReferencesJSON:       json.RawMessage(`[]`),
			CleanedPayload:       json.RawMessage(`{"itemId":"9493375"}`),
			ParseStatus:          "SUCCESS",
		}},
	}, nil
}

func cloneTask(task *Task) *Task {
	if task == nil {
		return nil
	}
	cloned := *task
	return &cloned
}

type unsupportedTaskRepo struct{}

func (unsupportedTaskRepo) CreateTask(ctx context.Context, input CreateTaskInput, providers []ProviderSettings, providerCallbackBaseURL string) (*Task, error) {
	return nil, errors.New("unexpected CreateTask")
}
func (unsupportedTaskRepo) CreateReassignedTask(ctx context.Context, input ReassignTaskRecordInput, providerCallbackBaseURL string) (*Task, error) {
	return nil, errors.New("unexpected CreateReassignedTask")
}
func (unsupportedTaskRepo) MarkTaskReassigned(ctx context.Context, sourceTaskID, targetTaskID int64, payload any) error {
	return errors.New("unexpected MarkTaskReassigned")
}
func (unsupportedTaskRepo) PrepareTaskForSubmit(ctx context.Context, taskID int64, providers []ProviderSettings, providerCallbackBaseURL string) (*Task, error) {
	return nil, errors.New("unexpected PrepareTaskForSubmit")
}
func (unsupportedTaskRepo) GetTask(ctx context.Context, id int64) (*Task, error) {
	return nil, errors.New("unexpected GetTask")
}
func (unsupportedTaskRepo) GetTaskByJobID(ctx context.Context, jobID string, platformUserID int64) (*Task, error) {
	return nil, errors.New("unexpected GetTaskByJobID")
}
func (unsupportedTaskRepo) GetTaskByBatchNo(ctx context.Context, batchNo string) (*Task, error) {
	return nil, errors.New("unexpected GetTaskByBatchNo")
}
func (unsupportedTaskRepo) ResolveProviderTask(ctx context.Context, providerCode, batchNo string) (*Task, error) {
	return nil, errors.New("unexpected ResolveProviderTask")
}
func (unsupportedTaskRepo) CountTaskItems(ctx context.Context, taskID int64) (int, error) {
	return 0, errors.New("unexpected CountTaskItems")
}
func (unsupportedTaskRepo) ListTaskItems(ctx context.Context, taskID int64) ([]TaskItem, error) {
	return nil, errors.New("unexpected ListTaskItems")
}
func (unsupportedTaskRepo) ListTasksByStatus(ctx context.Context, status TaskStatus, limit int) ([]*Task, error) {
	return nil, errors.New("unexpected ListTasksByStatus")
}
func (unsupportedTaskRepo) ListResultImportedTasksWithProviderCallback(ctx context.Context, limit int) ([]*Task, error) {
	return nil, errors.New("unexpected ListResultImportedTasksWithProviderCallback")
}
func (unsupportedTaskRepo) ListSubmittedTasksForProgress(ctx context.Context, limit int, submittedBefore time.Time) ([]*Task, error) {
	return nil, errors.New("unexpected ListSubmittedTasksForProgress")
}
func (unsupportedTaskRepo) ListTaskItemResults(ctx context.Context, taskID int64) ([]TaskItemResultView, error) {
	return nil, errors.New("unexpected ListTaskItemResults")
}
func (unsupportedTaskRepo) GetCallbackReadiness(ctx context.Context, taskID int64) (CallbackReadiness, error) {
	return CallbackReadiness{}, errors.New("unexpected GetCallbackReadiness")
}
func (unsupportedTaskRepo) ListTaskResultFiles(ctx context.Context, taskID int64) ([]CallbackResultFile, error) {
	return nil, errors.New("unexpected ListTaskResultFiles")
}
func (unsupportedTaskRepo) ListUploadedResultFiles(ctx context.Context, taskID int64, includeImported bool) ([]ResultFileInput, error) {
	return nil, errors.New("unexpected ListUploadedResultFiles")
}
func (unsupportedTaskRepo) CountSuccessfulResultFiles(ctx context.Context, taskID int64) (int, error) {
	return 0, errors.New("unexpected CountSuccessfulResultFiles")
}
func (unsupportedTaskRepo) MarkSubmitted(ctx context.Context, taskID int64, providerRequest json.RawMessage, result SubmitResult) error {
	return errors.New("unexpected MarkSubmitted")
}
func (unsupportedTaskRepo) MarkSubmitFailed(ctx context.Context, taskID int64, errMessage string) error {
	return errors.New("unexpected MarkSubmitFailed")
}
func (unsupportedTaskRepo) FailSubmittedOlderThan(ctx context.Context, cutoff time.Time, errMessage string, limit int) (int, error) {
	return 0, errors.New("unexpected FailSubmittedOlderThan")
}
func (unsupportedTaskRepo) SaveUploadObjectKey(ctx context.Context, taskID int64, key string, fileName string) error {
	return errors.New("unexpected SaveUploadObjectKey")
}
func (unsupportedTaskRepo) MarkProviderCallback(ctx context.Context, input ProviderCallbackInput) (*Task, error) {
	return nil, errors.New("unexpected MarkProviderCallback")
}
func (unsupportedTaskRepo) MarkProviderCallbackProgress(ctx context.Context, input ProviderCallbackInput) (*Task, error) {
	return nil, errors.New("unexpected MarkProviderCallbackProgress")
}
func (unsupportedTaskRepo) MarkProviderStatus(ctx context.Context, taskID int64, result ProviderStatusResult) error {
	return errors.New("unexpected MarkProviderStatus")
}
func (unsupportedTaskRepo) UpsertPendingResultFiles(ctx context.Context, taskID int64, files []ResultFileInput) error {
	return errors.New("unexpected UpsertPendingResultFiles")
}
func (unsupportedTaskRepo) ListPendingResultFiles(ctx context.Context, taskID int64) ([]ResultFileInput, error) {
	return nil, errors.New("unexpected ListPendingResultFiles")
}
func (unsupportedTaskRepo) MarkResultFileFailed(ctx context.Context, taskID int64, file ResultFileInput, errMessage string) error {
	return errors.New("unexpected MarkResultFileFailed")
}
func (unsupportedTaskRepo) MarkResultImported(ctx context.Context, taskID int64, file ResultFileInput, result ParsedResult) error {
	return errors.New("unexpected MarkResultImported")
}
func (unsupportedTaskRepo) CompleteResultImport(ctx context.Context, taskID int64, notifyCallback bool) (*Task, bool, error) {
	return nil, false, errors.New("unexpected CompleteResultImport")
}
func (unsupportedTaskRepo) MarkCallbackDone(ctx context.Context, taskID int64, targetURL string, statusCode int, responseBody string, payload json.RawMessage) error {
	return errors.New("unexpected MarkCallbackDone")
}
func (unsupportedTaskRepo) MarkCallbackFailed(ctx context.Context, taskID int64, targetURL string, errMessage string, payload json.RawMessage, maxRetry int) (int, bool, error) {
	return 0, false, errors.New("unexpected MarkCallbackFailed")
}
func (unsupportedTaskRepo) RecordForcedCallbackDone(ctx context.Context, taskID int64, targetURL string, statusCode int, responseBody string, payload json.RawMessage) error {
	return errors.New("unexpected RecordForcedCallbackDone")
}
func (unsupportedTaskRepo) RecordForcedCallbackFailed(ctx context.Context, taskID int64, targetURL string, errMessage string, payload json.RawMessage) error {
	return errors.New("unexpected RecordForcedCallbackFailed")
}
func (unsupportedTaskRepo) AddEvent(ctx context.Context, taskID int64, eventType EventType, payload any) error {
	return errors.New("unexpected AddEvent")
}

type callbackPayloadRepo struct {
	unsupportedTaskRepo
}

func (callbackPayloadRepo) ListTaskItemResults(ctx context.Context, taskID int64) ([]TaskItemResultView, error) {
	return nil, nil
}

func (callbackPayloadRepo) ListTaskResultFiles(ctx context.Context, taskID int64) ([]CallbackResultFile, error) {
	return nil, nil
}

func (callbackPayloadRepo) MarkCallbackDone(ctx context.Context, taskID int64, targetURL string, statusCode int, responseBody string, payload json.RawMessage) error {
	return nil
}

func (callbackPayloadRepo) AddEvent(ctx context.Context, taskID int64, eventType EventType, payload any) error {
	return nil
}

type captureCallbackClient struct {
	payload CallbackDelivery
}

func (c *captureCallbackClient) Deliver(ctx context.Context, task *Task, payload CallbackDelivery) (CallbackResult, error) {
	c.payload = payload
	return CallbackResult{TargetURL: task.CallbackURL, StatusCode: 200, ResponseBody: `{"ok":true}`}, nil
}

func (c *captureCallbackClient) ResolveMode(_ *Task) string {
	return "DEFAULT_HTTP"
}

type forceImportCallbackRepo struct {
	unsupportedTaskRepo
	task                   *Task
	markedResult           ParsedResult
	forcedDone             bool
	markCallbackDoneCalled bool
	eventTypes             []EventType
}

func (r *forceImportCallbackRepo) GetTask(ctx context.Context, id int64) (*Task, error) {
	return cloneTask(r.task), nil
}

func (r *forceImportCallbackRepo) CountTaskItems(ctx context.Context, taskID int64) (int, error) {
	return 3, nil
}

func (r *forceImportCallbackRepo) MarkProviderStatus(ctx context.Context, taskID int64, result ProviderStatusResult) error {
	return nil
}

func (r *forceImportCallbackRepo) ListUploadedResultFiles(ctx context.Context, taskID int64, includeImported bool) ([]ResultFileInput, error) {
	return nil, nil
}

func (r *forceImportCallbackRepo) MarkResultImported(ctx context.Context, taskID int64, file ResultFileInput, result ParsedResult) error {
	r.markedResult = result
	return nil
}

func (r *forceImportCallbackRepo) ListTaskItemResults(ctx context.Context, taskID int64) ([]TaskItemResultView, error) {
	return []TaskItemResultView{
		{ItemID: "item-success", Seq: 1, ParseStatus: r.markedResult.ItemResults[0].ParseStatus, AnswerText: r.markedResult.ItemResults[0].AnswerText},
		{ItemID: "item-empty", Seq: 1, ParseStatus: r.markedResult.ItemResults[1].ParseStatus, ParseError: r.markedResult.ItemResults[1].ParseError},
		{ItemID: "item-unfinished", Seq: 1, ParseStatus: ""},
	}, nil
}

func (r *forceImportCallbackRepo) ListTaskResultFiles(ctx context.Context, taskID int64) ([]CallbackResultFile, error) {
	return nil, nil
}

func (r *forceImportCallbackRepo) RecordForcedCallbackDone(ctx context.Context, taskID int64, targetURL string, statusCode int, responseBody string, payload json.RawMessage) error {
	r.forcedDone = true
	return nil
}

func (r *forceImportCallbackRepo) MarkCallbackDone(ctx context.Context, taskID int64, targetURL string, statusCode int, responseBody string, payload json.RawMessage) error {
	r.markCallbackDoneCalled = true
	return nil
}

func (r *forceImportCallbackRepo) AddEvent(ctx context.Context, taskID int64, eventType EventType, payload any) error {
	r.eventTypes = append(r.eventTypes, eventType)
	return nil
}

type forceImportCallbackRegistry struct {
	provider ProviderClient
}

func (r forceImportCallbackRegistry) Get(code string) (ProviderClient, bool) {
	return r.provider, r.provider != nil
}

func (r forceImportCallbackRegistry) Codes() []string {
	return []string{"vendor_force"}
}

type forceImportCallbackProvider struct{}

func (p forceImportCallbackProvider) SubmitTask(ctx context.Context, task *Task, payload SubmitTaskPayload) (SubmitResult, error) {
	return SubmitResult{}, errors.New("unexpected SubmitTask")
}

func (p forceImportCallbackProvider) FetchStatus(ctx context.Context, externalBatchID string) (ProviderStatusResult, error) {
	return ProviderStatusResult{
		ExternalBatchID: externalBatchID,
		Questions: []ProviderQuestionStatus{
			{QuestionID: "item-success", Status: 2},
			{QuestionID: "item-empty", Status: 2},
			{QuestionID: "item-unfinished", Status: 1},
		},
		TotalCount:   3,
		SuccessCount: 1,
		FailedCount:  0,
	}, nil
}

func (p forceImportCallbackProvider) FetchResultFiles(ctx context.Context, externalBatchID string) (ProviderFilesResult, error) {
	return ProviderFilesResult{
		ExternalBatchID: externalBatchID,
		FileURLs:        []string{"https://provider.example.com/result.xlsx"},
	}, nil
}

func (p forceImportCallbackProvider) CompletionFiles(ctx context.Context, task *Task) (ProviderCompletionFilesResult, error) {
	return ProviderCompletionFilesResult{}, errors.New("unexpected CompletionFiles")
}

func (p forceImportCallbackProvider) DownloadResultFile(ctx context.Context, fileURL string) ([]byte, error) {
	return []byte("xlsx-bytes"), nil
}

type forceImportCallbackParser struct{}

func (p forceImportCallbackParser) Parse(ctx context.Context, fileName string, content []byte) (*ParsedResult, error) {
	return &ParsedResult{
		RowCount:      2,
		ValidRowCount: 2,
		ItemResults: []TaskItemResult{
			{
				ItemID:               "item-success",
				Seq:                  1,
				AnswerText:           "usable answer",
				RecommendedQuestions: json.RawMessage(`[]`),
				ReferencesJSON:       json.RawMessage(`[]`),
				CleanedPayload:       json.RawMessage(`{}`),
				ParseStatus:          "SUCCESS",
			},
			{
				ItemID:               "item-empty",
				Seq:                  1,
				AnswerText:           "",
				RecommendedQuestions: json.RawMessage(`[]`),
				ReferencesJSON:       json.RawMessage(`[]`),
				CleanedPayload:       json.RawMessage(`{}`),
				ParseStatus:          "SUCCESS",
			},
		},
	}, nil
}
