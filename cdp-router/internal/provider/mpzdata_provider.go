package provider

import (
	"context"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
)

type mpzdataFileCompletion struct{}

func (mpzdataFileCompletion) CompletionFiles(ctx context.Context, provider *HTTPProvider, task *biz.Task) (biz.ProviderCompletionFilesResult, error) {
	result, err := provider.FetchResultFiles(ctx, task.ProviderExternalBatchID)
	if err != nil {
		return biz.ProviderCompletionFilesResult{}, err
	}
	return biz.ProviderCompletionFilesResult{
		Handled: true,
		Result:  result,
	}, nil
}
