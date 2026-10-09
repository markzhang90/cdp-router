# 强制导入回调技术方案

## 目标

强制导入回调用于处理供应商结果不完整、任务长时间未完全结束但业务需要先交付当前已有结果的场景。后台操作拆成两个业务步骤：

```text
1. 导入供应商新增或失败待重试的结果文件，跳过已成功导入文件
2. 不管供应商是否全部结束，按当前已成功导入的结果直接回调上游
```

该能力不是任务终态推进器。它只提交当前进度下可用的成功结果，不把任务改成 `DONE`，不写 `callback_sent_at`，也不阻止后续供应商 callback 或文件导入继续补齐结果。

## 核心语义

- 强制回调仍走现有 callback router。
- Yangshi 平台继续走 Yangshi 定制链路：重新生成结果 Excel、申请上游 OSS 上传地址、PUT 上传 Excel、调用完成通知接口。
- 普通 HTTP 回调直接在 callback payload 中携带当前 `parse_status=SUCCESS` 的 item 明细。
- 回调只选择当前数据库中 `parse_status=SUCCESS` 的 item。
- 未完成、解析失败的 item 不返回给上游；空回答由统一 validator 标为 INVALID，任何内容风险均阻止本入口回调。
- `seq` 只作为问答轮次/序号字段透传，不作为去重或范围过滤依据。
- 如果当前没有任何成功结果，拒绝回调，只写 `FORCE_IMPORT_CALLBACK` event。

## 强制导入

强制回调入口先刷新供应商状态和文件，再导入当前文件：

```text
获取供应商最新 status
  -> 保存 provider_response / item 进度
  -> 获取供应商最新 files_url 文件
  -> 跳过已成功文件，导入新增或失败文件
  -> 每个回答通过统一 validator 后保存
  -> 任意风险则拒绝发送，引导导入完成后使用页面强制回调
```

强制导入复用文件解析和 `MarkResultImported`，但不调用 `CompleteResultImport`。因此不会把任务状态推进到 `RESULT_IMPORTED` / `CALLBACK_PENDING`，也不会触发正常 callback worker。

如果供应商返回了文件但这些文件全部导入失败，强制回调直接失败并返回错误，不使用旧的成功结果继续回调。部分文件导入失败时，可以按已成功导入后的当前结果继续提交。

## 风险约束

本入口不绕过内容风险。若当前回答有风险则返回未发送，页面提示等待完整导入后使用正常回调入口的“强制回调”。该正常入口不重新导入、不筛掉风险回答，成功推进 DONE；与本文部分提交、不改终态的机制分开。

## 直接回调

导入完成后重新读取当前任务结果，并构造回调 payload：

- `ResultItems`：仅用于 Yangshi Excel，值为当前 `parse_status=SUCCESS` 的结果。
- `items`：仅用于普通 HTTP JSON 回调，值为当前 `parse_status=SUCCESS` 的结果。
- `counts.total`：本次直接提交的成功结果数。
- `counts.success`：本次直接提交的成功结果数。
- `counts.failed`：固定为 0；失败、空 answer、未完成 item 不作为回调结果返回。

未完成信息只进入后台返回和事件 payload：

- `taskItemCount`：任务 item 总数。
- `successCount`：当前成功结果数。
- `failedCount`：当前已有失败/空 answer 结果的 item 数。
- `unfinishedCount`：尚无成功或失败结果的 item 数。
- `partial`：`successCount < taskItemCount`。

## 数据写入约束

强制回调成功或失败后只能通过专用方法记录 callback：

```go
RecordForcedCallbackDone(...)
RecordForcedCallbackFailed(...)
```

这两个方法只写：

- `cdp_task_callbacks`
- `cdp_tasks.updated_at`

不得写：

- `cdp_tasks.status = DONE`
- `callback_sent_at`
- `cdp_tasks.last_error`
- 任何会阻止后续导入或正常 callback 的终态标记

## Event

只新增一个任务事件：

```text
FORCE_IMPORT_CALLBACK
```

payload 示例：

```json
{
  "reason": "数据不全，手动强制提交当前结果",
  "providerStatusFetched": true,
  "providerFilesFetched": true,
  "fileCount": 2,
  "importedFileCount": 2,
  "rowCount": 80,
  "taskItemCount": 100,
  "successCount": 80,
  "failedCount": 5,
  "unfinishedCount": 15,
  "callbackMode": "YANGSHI",
  "yangshiUploaded": true,
  "partial": true,
  "taskStatusUnchanged": true,
  "delivered": true,
  "targetUrl": "https://example.com/callback",
  "statusCode": 200
}
```

## 后台页面

任务详情页提供一个红色危险按钮：`强制导入回调`。

确认弹窗必须说明后果：

- 只导入供应商新文件或失败文件，已成功文件不重复导入。
- 不管供应商是否全部结束，都会按当前进度直接提交结果。
- 只提交当前 `parse_status=SUCCESS` 的 item。
- 未完成、失败、空 answer item 不返回上游。
- 不修改任务状态，不影响后续正常导入和 callback。

## 验收重点

- 强制回调成功后，`cdp_tasks.status` 不变。
- 强制回调成功后，`callback_sent_at` 不变。
- `cdp_task_callbacks` 新增记录。
- `cdp_task_events` 新增 `FORCE_IMPORT_CALLBACK`，且 payload 包含导入、回调和未完成统计。
- Yangshi Excel 只包含 `parse_status=SUCCESS` 的 item。
- 普通 HTTP callback payload 的 `items` 只包含 `parse_status=SUCCESS` 的 item。
- 后续供应商 callback / 文件导入仍可继续覆盖或补齐当前失败/空结果。


### 2026-10-09 风险策略调整

所有供应商的回答始终执行长度、关键词、引用数校验。原强制导入回调入口也遵循供应商“风险拦截”开关：开启时发现风险仍拒绝，需使用完成导入后的人工强制回调；关闭时允许包含风险的结果按原入口规则提交，保留风险记录。UNKNOWN 和旧 SKIPPED 均不能作为已校验结果提交。风险拦截开关不改变此入口的部分结果和主状态保持语义。架构事实见 `gateway-architecture.md` 8.5.2。
