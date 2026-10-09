package biz

import (
	"context"
	"fmt"
	"log/slog"
)

type InitialAnswerValidation struct {
	ItemID string
	Seq    int
	AnswerValidation
}
type InitialValidationRepo interface {
	SaveInitialAnswerValidation(context.Context, int64, []InitialAnswerValidation) error
}

// InitializeTaskValidation is a one-off migration operation. It only validates
// legacy UNKNOWN/SKIPPED answers from the database; it never downloads files or sends.
func (u *TaskUsecase) InitializeTaskValidation(ctx context.Context, id int64) error {
	return u.initializeTaskValidation(ctx, id, false)
}

func (u *TaskUsecase) initializeTaskValidation(ctx context.Context, id int64, skippedOnly bool) error {
	return u.withTaskLock(ctx, id, func(ctx context.Context) error {
		task, err := u.repo.GetTask(ctx, id)
		if err != nil {
			return err
		}
		if (!skippedOnly && task.Status == TaskStatusDone) || task.Status == TaskStatusReassigned {
			return ErrTaskClosed
		}
		repo, ok := u.repo.(InitialValidationRepo)
		if !ok {
			return fmt.Errorf("initial validation repository unavailable")
		}
		items, err := u.repo.ListTaskItemResults(ctx, id)
		if err != nil {
			return err
		}
		rules := ctx.Value(validationSnapshotKey{}).(ResultValidationRules)
		updates := make([]InitialAnswerValidation, 0, len(items))
		for _, item := range items {
			if item.ParseStatus == "" || (item.AnswerValidation.Status != ValidationSkipped && (skippedOnly || item.AnswerValidation.Status != ValidationUnknown)) {
				continue
			}
			result := ValidateAnswer(TaskItemResult{AnswerText: item.AnswerText, ReferenceCount: item.ReferenceCount}, rules)
			updates = append(updates, InitialAnswerValidation{ItemID: item.ItemID, Seq: item.Seq, AnswerValidation: result})
		}
		if err = repo.SaveInitialAnswerValidation(ctx, id, updates); err != nil {
			return err
		}
		_, _, err = u.repo.CompleteResultImport(u.validationContext(ctx), id, true)
		return err
	})
}

// Only old SKIPPED rows are automatically repaired; existing findings remain unchanged.
func (u *TaskUsecase) initializeSkippedResults(ctx context.Context, limit int) error {
	repo, ok := u.repo.(interface {
		ListSkippedValidationTasks(context.Context, int) ([]int64, error)
	})
	if !ok {
		return nil
	}
	ids, err := repo.ListSkippedValidationTasks(ctx, limit)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := u.initializeTaskValidation(ctx, id, true); err != nil {
			slog.ErrorContext(ctx, "initialize skipped answer validation failed", "task_id", id, "error", err)
		}
	}
	return nil
}
