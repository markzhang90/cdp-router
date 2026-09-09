package callback

import (
	"context"
	"strings"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/config"
)

type Router struct {
	defaultClient *HTTPClient
	yangshi       *YangshiClient
	users         map[string]config.PlatformUserConfig
}

func NewClient(timeout time.Duration, uploadTimeout time.Duration, users map[string]config.PlatformUserConfig, store biz.ObjectStore, ownOSSHosts ...string) *Router {
	copied := make(map[string]config.PlatformUserConfig, len(users))
	for code, user := range users {
		copied[code] = user
	}
	return &Router{
		defaultClient: NewHTTPClient(timeout),
		yangshi:       NewYangshiClient(timeout, uploadTimeout, store, ownOSSHosts...),
		users:         copied,
	}
}

func (r *Router) Deliver(ctx context.Context, task *biz.Task, payload biz.CallbackDelivery) (biz.CallbackResult, error) {
	mode := r.ResolveMode(task)
	switch mode {
	case "YANGSHI":
		user := r.users[task.PlatformCode]
		return r.yangshi.Deliver(ctx, task, payload, user)
	default:
		return r.defaultClient.Deliver(ctx, task, payload)
	}
}

// ResolveMode returns the callback mode that Deliver would use for the task.
// It mirrors the selection rules from Deliver so callers can report the mode
// without duplicating the logic.
func (r *Router) ResolveMode(task *biz.Task) string {
	user := r.users[task.PlatformCode]
	mode := strings.ToUpper(strings.TrimSpace(user.CallbackMode))
	platformCode := strings.ToLower(strings.TrimSpace(task.PlatformCode))
	if matchYangshiKeyword(platformCode) || matchYangshiKeyword(mode) {
		return "YANGSHI"
	}
	return "DEFAULT_HTTP"
}

func matchYangshiKeyword(code string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(code)), "yangshi")
}

var _ biz.CallbackClient = (*Router)(nil)
