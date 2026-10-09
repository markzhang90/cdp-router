package biz

import (
	"context"
	"net/url"
	"strings"
)

type taskProcessingKey struct{}
type TaskProcessingLocker interface {
	WithTaskLock(context.Context, int64, func(context.Context) error) error
}

func taskProcessingLocked(ctx context.Context, id int64) bool {
	held, _ := ctx.Value(taskProcessingKey{}).(int64)
	return held == id
}
func (u *TaskUsecase) withTaskLock(ctx context.Context, id int64, work func(context.Context) error) error {
	if taskProcessingLocked(ctx, id) {
		return work(ctx)
	}
	run := func(ctx context.Context) error {
		return work(u.validationContext(context.WithValue(ctx, taskProcessingKey{}, id)))
	}
	if repo, ok := u.repo.(TaskProcessingLocker); ok {
		return repo.WithTaskLock(ctx, id, run)
	}
	return run(ctx)
}

// Only authentication query parameters are ignored. File selectors and source
// versions remain part of identity, so a changed content version is a new file.
func resultFileURLIdentity(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "x-amz-") || strings.HasPrefix(lower, "x-oss-") || lower == "ossaccesskeyid" || lower == "signature" || lower == "expires" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String()
}
func (u *TaskUsecase) resultFileImported(ctx context.Context, taskID int64, file ResultFileInput) (bool, error) {
	files, err := u.repo.ListTaskResultFiles(ctx, taskID)
	if err != nil {
		return false, err
	}
	for _, old := range files {
		if file.ObjectKey != "" && file.ObjectKey == old.ObjectKey {
			return true, nil
		}
		if file.ObjectKey == "" && file.FileURL != "" && old.FileURL != "" && resultFileURLIdentity(file.FileURL) == resultFileURLIdentity(old.FileURL) {
			return true, nil
		}
	}
	return false, nil
}

func (u *TaskUsecase) importProviderFileResult(ctx context.Context, task *Task, fileResult ProviderFilesResult, force bool, notifyCallback bool, triggerType string) (int, int, error) {
	if !taskProcessingLocked(ctx, task.ID) {
		var count, rows int
		err := u.withTaskLock(ctx, task.ID, func(ctx context.Context) error {
			var err error
			count, rows, err = u.importProviderFileResult(ctx, task, fileResult, force, notifyCallback, triggerType)
			return err
		})
		return count, rows, err
	}
	files, _ := u.pendingFilesFromProviderURLs(ctx, task, fileResult.FileURLs)
	pending := make([]ResultFileInput, 0, len(files))
	for _, file := range files {
		imported, err := u.resultFileImported(ctx, task.ID, file)
		if err != nil {
			return 0, 0, err
		}
		if !imported {
			pending = append(pending, file)
		}
	}
	if err := u.repo.UpsertPendingResultFiles(ctx, task.ID, pending); err != nil {
		return 0, 0, err
	}
	// Import in database registration order, including previously pending files.
	allPending, err := u.repo.ListPendingResultFiles(ctx, task.ID)
	if err != nil {
		return 0, 0, err
	}
	if len(allPending) > 0 {
		return u.importResultFileInputs(ctx, task, allPending, notifyCallback, triggerType)
	}
	updated, _, err := u.repo.CompleteResultImport(u.validationContext(ctx), task.ID, notifyCallback)
	if err != nil {
		return 0, 0, err
	}
	return len(files), updated.ResultRowCount, nil
}

func (u *TaskUsecase) unimportedResultFiles(ctx context.Context, taskID int64, files []ResultFileInput) ([]ResultFileInput, error) {
	pending := make([]ResultFileInput, 0, len(files))
	for _, file := range files {
		imported, err := u.resultFileImported(ctx, taskID, file)
		if err != nil {
			return nil, err
		}
		if !imported {
			pending = append(pending, file)
		}
	}
	return pending, nil
}
