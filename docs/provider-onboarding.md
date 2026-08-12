# Provider 接入指南

本文档说明 CDP Router 后续新增供应商 provider 的接入方式。Provider 表示网关对下游供应商的适配配置和代码实现；业务接入方仍通过 `platform_users` 管理。

## 接入分级

### 1. 仅配置接入

如果新供应商的接口协议与当前通用 HTTP provider 一致，只需要在配置文件中增加 `[providers.<code>]`：

```ini
[providers.vendor_x]
code = vendor_x
display_name = vendor_x
token = <provider-token>
result_mode = callback_upload
submit_url = https://example.com/api/v1/ai/batch/create
status_url = https://example.com/api/v1/ai/batch/external/{externalBatchId}
files_url = https://example.com/api/v1/ai/batch/external/{externalBatchId}/files
bucket_path_prefix = cdp-router/q2a/vendor_x
timeout_seconds = 30
qps_limit = 10
concurrency_limit = 20
enabled = true
```

通用 HTTP provider 默认能力：

- 提交任务：`POST submit_url`，header 使用 `X-Provider-Token`。
- 获取进度：`GET status_url`，解析 `data.questions[]`。
- 获取文件：`GET files_url`，解析 `data.fileUrls[]`。
- 下载文件：直接 HTTP GET `fileUrl`。
- 完成回调后文件处理：默认处理供应商通过本网关 OSS 上传接口登记的文件。

配置变更后需要重启服务，配置会在启动时加载到内存。

### 2. 完成文件策略差异

如果供应商完成回调后需要特殊文件处理，不要在 biz 层写 provider code 判断，应在 `internal/provider` 下为该供应商增加独立文件。

例如 `mpzdata` 的规则是：收到完成回调后，先调用供应商 `files_url` 获取最终文件列表，按返回列表处理所有文件；如果列表为空，再回退到 OSS 上传记录。实现位于：

```text
internal/provider/mpzdata_provider.go
```

新增类似策略时：

1. 在 `internal/provider/<provider_code>_provider.go` 实现 `fileCompletion`。
2. 在 `newFileCompletion` 中按 provider code 注册。
3. 补充真实持久层 E2E，覆盖完成回调后的文件处理行为。

模板：

```go
type vendorXFileCompletion struct{}

func (vendorXFileCompletion) CompletionFiles(ctx context.Context, provider *HTTPProvider, task *biz.Task) (biz.ProviderCompletionFilesResult, error) {
	result, err := provider.FetchResultFiles(ctx, task.ProviderExternalBatchID)
	if err != nil {
		return biz.ProviderCompletionFilesResult{}, err
	}
	return biz.ProviderCompletionFilesResult{
		Handled: true,
		Result:  result,
	}, nil
}
```

`Handled=false` 表示 provider 没有特殊处理，biz 层会回退到 OSS 上传文件；`Handled=true` 表示 provider 已提供本次完成后的文件结果。

### 3. 协议差异较大

如果供应商的提交、状态、文件列表、下载响应结构与通用 HTTP provider 不一致，应新增独立 provider 实现，而不是把差异堆进 `HTTPProvider`。

建议文件：

```text
internal/provider/vendor_x_provider.go
```

实现接口：

```go
type VendorXProvider struct{}

func (p *VendorXProvider) SubmitTask(ctx context.Context, task *biz.Task, payload biz.SubmitTaskPayload) (biz.SubmitResult, error)
func (p *VendorXProvider) FetchStatus(ctx context.Context, externalBatchID string) (biz.ProviderStatusResult, error)
func (p *VendorXProvider) FetchResultFiles(ctx context.Context, externalBatchID string) (biz.ProviderFilesResult, error)
func (p *VendorXProvider) CompletionFiles(ctx context.Context, task *biz.Task) (biz.ProviderCompletionFilesResult, error)
func (p *VendorXProvider) DownloadResultFile(ctx context.Context, fileURL string) ([]byte, error)
```

然后在 provider registry 初始化时按 `code` 注册该实现。

## 必须遵守

- 不在 biz/service/httpapi 层硬编码 provider code。
- provider token、签名 URL、Authorization、Cookie 不允许明文打印到日志。
- 每个 provider 使用独立 HTTP client、连接池、QPS limiter 和并发 semaphore。
- 所有外部请求必须带 context timeout。
- 新增或修改 provider 行为必须补测试；业务接口链路需要真实 Postgres/Redis 持久层 E2E。
- 新增配置字段时同步更新配置示例、部署配置和本文档。
