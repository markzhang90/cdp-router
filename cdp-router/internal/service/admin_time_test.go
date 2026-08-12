package service

import (
	"strings"
	"testing"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
)

func TestFormatTimeUsesAdminTimezone(t *testing.T) {
	value := time.Date(2026, 8, 3, 16, 20, 0, 0, time.UTC)

	if got, want := formatTime(value), "2026-08-04T00:20:00+08:00"; got != want {
		t.Fatalf("formatTime()=%q, want %q", got, want)
	}
}

func TestParseOptionalTimeUsesAdminTimezoneForPlainValues(t *testing.T) {
	value, err := parseOptionalTime("2026-08-04 00:20:00")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := value.Format(time.RFC3339), "2026-08-04T00:20:00+08:00"; got != want {
		t.Fatalf("parseOptionalTime()=%q, want %q", got, want)
	}
}

func TestTaskExecutionDurationUsesProviderFinishedAt(t *testing.T) {
	createdAt := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	seconds, text := taskExecutionDuration(biz.AdminTaskSummary{
		Status:           biz.TaskStatusCallbackPending,
		CreatedAt:        createdAt,
		UpdatedAt:        createdAt.Add(30 * time.Minute),
		ExecutionEndedAt: createdAt.Add(2 * time.Minute),
	})

	if seconds != 120 || text != "2分0秒" {
		t.Fatalf("duration seconds=%d text=%q, want 120 and 2分0秒", seconds, text)
	}
}

func TestTaskExecutionDurationDoesNotFallBackToUpdatedAtForFinishedTask(t *testing.T) {
	createdAt := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	seconds, text := taskExecutionDuration(biz.AdminTaskSummary{
		Status:    biz.TaskStatusDone,
		CreatedAt: createdAt,
		UpdatedAt: createdAt.Add(30 * time.Minute),
	})

	if seconds != 0 || text != "-" {
		t.Fatalf("duration seconds=%d text=%q, want 0 and -", seconds, text)
	}
}

func TestTaskExecutionDurationShowsRunningElapsedOnlyForProviderRunningTask(t *testing.T) {
	createdAt := time.Now().Add(-2 * time.Minute)
	seconds, text := taskExecutionDuration(biz.AdminTaskSummary{
		Status:    biz.TaskStatusSubmitted,
		CreatedAt: createdAt,
		UpdatedAt: createdAt.Add(time.Minute),
	})

	if seconds <= 0 || !strings.HasPrefix(text, "已执行 ") {
		t.Fatalf("duration seconds=%d text=%q, want running elapsed text", seconds, text)
	}
}
