# Provider 接入指南

本文档说明 CDP Router 后续新增供应商 provider 的接入方式。Provider 表示网关对下游供应商的适配配置和代码实现；业务接入方仍通过 `platform_users` 管理。

## 接入分级

### 1. 仅配置接入

如果新供应商的接口协议与当前通用 HTTP provider 一致，只需要在配置文件中增加 `[providers.<code>]`：

```ini
[providers.vendor_x]
code = vendor_x
display_name = vendor_x
company_name = Vendor X Co., Ltd.
settlement_start_at = 2026-01-01
token = <provider-token>
result_mode = callback_upload
submit_url = https://example.com/api/v1/ai/batch/create
status_url = https://example.com/api/v1/ai/batch/external/{externalBatchId}
files_url = https://example.com/api/v1/ai/batch/external/{externalBatchId}/files
bucket_path_prefix = cdp-router/q2a/vendor_x
timeout_seconds = 20
qps_limit = 10
concurrency_limit = 20
daily_question_quota = 0
enabled = true
```

`timeout_seconds` 可按供应商下调；未配置或配置超过 20 秒时，运行时会按 20 秒处理。

`company_name` 用于后台展示和接入方任务对账单导出；未配置时回退使用 `display_name`。`settlement_start_at` 表示供应商开始结算时间，建议使用 `YYYY-MM-DD`。

`daily_question_quota` 表示该供应商按提交当天 Asia/Shanghai 自然日可处理的 question 总额度。未配置、配置为 0 或负数时表示无上限；配置为正数时，任务调度基于 `cdp_tasks.quota_date + provider_code` 汇总 `cdp_tasks.total_count` 判断剩余额度，不扫描 `cdp_task_items` 明细表。

通用 HTTP provider 默认能力：

- 提交任务：`POST submit_url`，header 使用 `X-Provider-Token`。
- 获取进度：`GET status_url`，解析 `data.questions[]`。
- 获取文件：`GET files_url`，解析 `data.fileUrls[]`。
- 下载文件：直接 HTTP GET `fileUrl`。
- 完成回调后文件处理：`/external/callback/{provider}` 只做完成确认、`files_url` 校对和待导入文件登记，不下载或解析结果文件；下载和导入由 worker 扫描 `PROVIDER_DONE` 后异步执行。

配置变更后需要重启服务，配置会在启动时加载到内存。

新增 provider 只表示服务具备调用该供应商的能力，不会自动接收业务任务。需要在后台接入方页面或管理 API 中，把 provider code 加入目标接入方绑定的 `cdp_dispatch_profiles.config.providers`，并确保调度配置 `enabled=true`。当前调度粒度是整任务维度：一个任务被选中某个 provider 后，任务内全部 `items[]` 都提交给同一个 provider。接入方调度策略可选 `ROUND_ROBIN` 或 `RULE_BASED`；`ROUND_ROBIN` 下 `config.providers` 是默认供应商池，按顺序下发；`RULE_BASED` 下 `config.providers` 是 `FALLBACK` 供应商池，规则配置保存在 `cdp_dispatch_profiles.config.rules`，按 `taskName` 正则第一条命中规则确定标签并优先使用规则供应商池。规则供应商池可以不包含在 `FALLBACK` 池内；未命中规则或规则供应商池全部额度不足/不可用时进入 `FALLBACK` 池，其中规则池失败进入 fallback 时标签记录为 `fallback`。`FALLBACK` 池的选择方式由 `config.fallbackSelectionPolicy` 控制，可选 `ROUND_ROBIN` 或 `LOWEST_USAGE_RATE`，未配置时默认 `LOWEST_USAGE_RATE`。

调度可选 provider 来自 `GET /api/admin/v1/dispatch-options`。配置中未启用、缺少提交地址或缺少鉴权 token/API key 的 provider 不会进入可选列表，也不会在创建任务时被选中。

### 2. 完成文件策略差异

如果供应商完成回调后需要特殊文件处理，不要在 biz 层写 provider code 判断，应在 `internal/provider` 下为该供应商增加独立文件。

例如 `mpzdata`/`liuandata` 的规则是：收到完成回调且 counts 100% 后，先调用供应商 `files_url` 获取最终文件列表并登记待导入文件；worker 导入时优先消费待导入记录，如果列表为空再回退到 OSS 上传记录。实现位于：

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
- 上线时必须确认在途任务使用的 `cdp_tasks.provider_code` 仍存在于配置中；历史任务不会重新按 dispatch profile 选择供应商。
