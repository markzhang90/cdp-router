package callback

import (
	"context"
	"fmt"
	"sync"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
)

type Adapter interface {
	Code() string
	Deliver(ctx context.Context, task *biz.Task, payload biz.CallbackDelivery) (biz.CallbackResult, error)
}

type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry(adapters ...Adapter) *Registry {
	registry := &Registry{adapters: map[string]Adapter{}}
	for _, adapter := range adapters {
		registry.Register(adapter)
	}
	return registry
}

func (r *Registry) Register(adapter Adapter) {
	if adapter == nil || adapter.Code() == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[adapter.Code()] = adapter
}

func (r *Registry) Get(code string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	adapter, ok := r.adapters[code]
	return adapter, ok
}

type UnsupportedAdapter struct {
	code string
}

func NewUnsupportedAdapter(code string) UnsupportedAdapter {
	return UnsupportedAdapter{code: code}
}

func (a UnsupportedAdapter) Code() string {
	return a.code
}

func (a UnsupportedAdapter) Deliver(context.Context, *biz.Task, biz.CallbackDelivery) (biz.CallbackResult, error) {
	return biz.CallbackResult{}, fmt.Errorf("callback adapter %s is not implemented", a.code)
}
