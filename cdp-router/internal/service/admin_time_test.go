package service

import (
	"bytes"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/GitMarkZhang/cdp-router/api/admin/v1"
	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/xuri/excelize/v2"
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

func TestParsePlatformUserReconciliationFilterUsesAdminDayBoundaries(t *testing.T) {
	filter, err := parsePlatformUserReconciliationFilter("2026-08-01 00:00:00", "2026-08-31 23:59:59")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := filter.CreatedFrom.Format(time.RFC3339), "2026-08-01T00:00:00+08:00"; got != want {
		t.Fatalf("createdFrom=%q, want %q", got, want)
	}
	if got, want := filter.CreatedTo.Format(time.RFC3339), "2026-08-31T23:59:59+08:00"; got != want {
		t.Fatalf("createdTo=%q, want %q", got, want)
	}
}

func TestPlatformUserReconciliationWorkbookIncludesTaskNameSecond(t *testing.T) {
	content, err := buildPlatformUserReconciliationWorkbook(biz.AdminPlatformUserReconciliationExport{
		Rows: []biz.AdminPlatformUserReconciliationRow{
			{
				MetricDate:          "2026-08-04",
				TaskName:            "测试任务",
				BatchNo:             "batch-001",
				ProviderCode:        "vendor_a",
				ProviderCompanyName: "Vendor A Co., Ltd.",
				QuestionCount:       2,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	book, err := excelize.OpenReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = book.Close() }()

	rows, err := book.GetRows("对账单")
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := []string{"日期", "任务名称", "任务batchNo", "供应商code", "供应商公司名称", "问答数"}
	for index, want := range wantHeader {
		if rows[0][index] != want {
			t.Fatalf("header[%d]=%q, want %q: %+v", index, rows[0][index], want, rows[0])
		}
	}
	if rows[1][1] != "测试任务" || rows[1][2] != "batch-001" || rows[1][5] != "2" {
		t.Fatalf("unexpected reconciliation row: %+v", rows[1])
	}
}

func TestProviderProtoIncludesCompanyAndSettlementFields(t *testing.T) {
	provider := providerProto(config.ProviderConfig{
		Code:              "vendor_a",
		DisplayName:       "Vendor A",
		CompanyName:       "Vendor A Co., Ltd.",
		SettlementStartAt: "2026-01-01",
		Timeout:           20 * time.Second,
		QPSLimit:          10,
		ConcurrencyLimit:  20,
	}, biz.AdminProviderQuotaUsage{})

	if provider.CompanyName != "Vendor A Co., Ltd." || provider.SettlementStartAt != "2026-01-01" {
		t.Fatalf("provider fields company=%q settlement=%q", provider.CompanyName, provider.SettlementStartAt)
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

func TestPlatformUserInputGeneratesMissingDispatchRuleCode(t *testing.T) {
	service := &AdminService{
		providers: map[string]config.ProviderConfig{
			"vendor_a": {
				Code:             "vendor_a",
				SubmitURL:        "https://provider.example.com/submit",
				Token:            "token",
				QPSLimit:         10,
				ConcurrencyLimit: 20,
				Enabled:          true,
			},
		},
	}

	input, err := service.platformUserInput("mainland", "Mainland", "ACTIVE", "DEFAULT_HTTP", "https://example.com/callback", &adminv1.DispatchProfileInput{
		Strategy:      string(biz.DispatchStrategyRuleBased),
		Enabled:       true,
		ProviderCodes: []string{"vendor_a"},
		Rules: []*adminv1.DispatchRuleInput{
			{
				Name:          "规则1",
				TaskNameRegex: "白酒",
				Labels:        []string{"白酒"},
				ProviderCodes: []string{"vendor_a"},
				Enabled:       true,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := input.DispatchProfile.Rules[0].Code, "rule_1"; got != want {
		t.Fatalf("rule code=%q, want %q", got, want)
	}
}
