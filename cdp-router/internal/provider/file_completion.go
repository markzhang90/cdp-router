package provider

import (
	"context"
	"strings"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
)

type fileCompletion interface {
	CompletionFiles(ctx context.Context, provider *HTTPProvider, task *biz.Task) (biz.ProviderCompletionFilesResult, error)
}

func newFileCompletion(providerCode string) fileCompletion {
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "mpzdata", "liuandata":
		return mpzdataFileCompletion{}
	default:
		return uploadedFileCompletion{}
	}
}

type uploadedFileCompletion struct{}

func (uploadedFileCompletion) CompletionFiles(context.Context, *HTTPProvider, *biz.Task) (biz.ProviderCompletionFilesResult, error) {
	return biz.ProviderCompletionFilesResult{Handled: false}, nil
}
