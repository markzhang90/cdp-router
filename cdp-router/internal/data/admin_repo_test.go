package data

import (
	"strings"
	"testing"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
)

func TestAdminMetricWhereExcludesReassignedTasks(t *testing.T) {
	providerWhere, providerArgs := adminProviderMetricWhere("liuandata", biz.AdminProviderMetricsFilter{
		CreatedFrom: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		CreatedTo:   time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
	})
	if !strings.Contains(providerWhere, "t.status <> 'REASSIGNED'") {
		t.Fatalf("provider metric where=%q, want REASSIGNED excluded", providerWhere)
	}
	if len(providerArgs) != 3 {
		t.Fatalf("provider metric args len=%d, want 3", len(providerArgs))
	}

	platformWhere, platformArgs := adminPlatformUserMetricWhere(1, "mainland", biz.AdminPlatformUserMetricsFilter{
		CreatedFrom: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		CreatedTo:   time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
	})
	if !strings.Contains(platformWhere, "t.status <> 'REASSIGNED'") {
		t.Fatalf("platform metric where=%q, want REASSIGNED excluded", platformWhere)
	}
	if len(platformArgs) != 4 {
		t.Fatalf("platform metric args len=%d, want 4", len(platformArgs))
	}
}
