# 数据来源 接口文档

## 1. 接入说明

本文档面向外部 Q2A 供应商，描述双方对接所需接口。

### 1.1 Base URL

| 环境 | baseURL |
| --- | --- |
| 测试环境 | `https://staging-api-growth.wiredcraft.cn/api2` |
| 生产环境 | `https://api-growth.wiredcraft.cn/api2` |

实际测试环境和生产环境域名由系统侧单独提供。

说明：

| 项 | 说明 |
| --- | --- |
| `providerCode` | 供应商编码，由系统侧单独提供 |
| `X-Provider-Token` | 接口鉴权 Token，由系统侧单独额外提供 |

### 1.2 调用链路

```text
系统批量提交问题给供应商
        |
        v
供应商处理并生成 Excel
        |
        v
供应商申请 OSS 上传地址
        |
        v
供应商 PUT 上传 Excel
        |
        v
供应商回调系统通知完成
```

### 1.3 通用鉴权

系统调用供应商接口、供应商调用系统接口时，均通过请求头 `X-Provider-Token` 做 Token 校验。

```http
X-Provider-Token: vendor-provider-token
```

实际 `providerCode` 和 `X-Provider-Token` 不写入本文档，由双方单独额外提供。

## 2. 系统调用供应商：批量提交问答任务

供应商需要提供该接口，系统按供应商提供的 `submitUrl` 调用。测试环境和生产环境的 `submitUrl`
可分别提供。

```http
POST {submitUrl}
Content-Type: application/json
X-Provider-Token: vendor-provider-token
```

### 2.1 请求体

```json
{
  "batchNo": "Q2A2-20260608-vendor_a-8f3b7e21",
  "providerCode": "vendor_a",
  "bizDate": "2026-06-08",
  "callbackUrl": "{baseURL}/external/callback/vendor_a",
  "items": [
    {
      "itemId": "123456",
      "platformCode": "yuanbao",
      "seq": 1,
      "question": "请评价某产品的优缺点",
      "deepthink": true,
      "webSearch": true
    }
  ]
}
```

### 2.2 字段说明

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `batchNo` | string | 是 | 系统生成的批次号 |
| `providerCode` | string | 是 | 供应商编码 |
| `bizDate` | string | 是 | 业务日期，格式 `yyyy-MM-dd` |
| `callbackUrl` | string | 是 | 供应商处理完成后的回调地址 |
| `items` | array | 是 | 问答任务列表 |
| `items.itemId` | string | 是 | 单条问答唯一标识，供应商回传 Excel 时需原样返回 |
| `items.platformCode` | string | 是 | 平台编码 |
| `items.seq` | number | 是 | 问答轮次 |
| `items.question` | string | 是 | 问题内容 |
| `items.deepthink` | boolean | 是 | 是否启用深度思考 |
| `items.webSearch` | boolean | 是 | 是否联网 |

### 2.3 platformCode 对应关系

| 平台名称 | platformCode |
| --- | --- |
| DeepSeek | `deepseek` |
| 通义千问 | `qwen` |
| Kimi | `kimi` |
| 豆包 | `doubao` |
| 腾讯元宝 | `yuanbao` |
| 蚂蚁阿福 | `antafu` |
| 智谱AI | `zhipu` |
| 文心一言 | `yiyan` |

### 2.4 响应体

```json
{
  "code": 0,
  "message": "success",
  "data": {
    "externalBatchId": "vendor-batch-001",
    "acceptedCount": 100
  }
}
```

### 2.5 响应字段说明

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `code` | number | 是 | `0` 表示成功，非 `0` 表示失败 |
| `message` | string | 否 | 响应说明 |
| `data.externalBatchId` | string | 否 | 供应商侧批次号 |
| `data.acceptedCount` | number | 否 | 供应商接收数量 |

## 3. 供应商调用系统：申请 OSS 上传地址

供应商处理完成后，先调用该接口申请短期有效的 OSS PUT 上传地址。

```http
POST {baseURL}/external/oss/{providerCode}/upload-url
Content-Type: application/json
X-Provider-Token: vendor-provider-token
```

上传地址固定有效 86400 秒（24 小时）。

### 3.1 路径参数

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `providerCode` | string | 是 | 供应商编码，由系统侧单独提供 |

### 3.2 请求体

```json
{
  "batchNo": "Q2A2-20260608-vendor_a-8f3b7e21",
  "fileName": "result.xlsx"
}
```

### 3.3 字段说明

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `batchNo` | string | 是 | 系统提交时传入的批次号 |
| `fileName` | string | 否 | 上传文件名，默认 `result.xlsx` |

### 3.4 响应体

```json
{
  "status": 0,
  "message": null,
  "data": {
    "bucket": "dvtp0",
    "key": "external/q2a/vendor_a/Q2A2-20260608-vendor_a-8f3b7e21/1a2b3c4d-result.xlsx",
    "uploadUrl": "https://...",
    "method": "PUT",
    "expiresAt": "2026-06-10T12:30:00",
    "headers": {}
  }
}
```

### 3.5 上传说明

供应商直接使用返回的 `uploadUrl` 做 HTTP PUT 上传 Excel 文件。

上传时不要额外设置 `Content-Type`。上传完成后调用回调接口即可，回调入参不需要再传 `oss`。

## 4. 供应商调用系统：结果完成通知

供应商处理完成并将 Excel 上传到 OSS 后，调用该接口通知系统。

```http
POST {baseURL}/external/callback/{providerCode}
Content-Type: application/json
X-Provider-Token: vendor-provider-token
```

### 4.1 路径参数

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `providerCode` | string | 是 | 供应商编码，由系统侧单独提供 |

### 4.2 校验规则

系统根据路径中的 `providerCode` 匹配供应商配置，并校验请求头 `X-Provider-Token` 是否正确。

### 4.3 请求体

```json
{
  "batchNo": "Q2A2-20260608-vendor_a-8f3b7e21",
  "externalBatchId": "vendor-batch-001",
  "status": "DONE",
  "totalCount": 100,
  "successCount": 98,
  "failedCount": 2,
  "finishedAt": "2026-06-08T16:30:00+08:00"
}
```

### 4.4 字段说明

| 字段 | 类型 | 必填 | 说明 |
| --- | --- |----| --- |
| `batchNo` | string | 是  | 系统提交时传入的批次号 |
| `externalBatchId` | string | 否  | 供应商侧批次号 |
| `status` | string | 否  | 建议固定传 `DONE` |
| `totalCount` | number | 否  | 总行数 |
| `successCount` | number | 否  | 成功行数 |
| `failedCount` | number | 否  | 失败行数 |
| `finishedAt` | string | 否  | 供应商完成时间，建议使用 ISO-8601 格式 |

### 4.5 响应体

```json
{
  "status": 0,
  "message": null,
  "data": {
    "batchNo": "Q2A2-20260608-vendor_a-8f3b7e21",
    "accepted": true
  }
}
```

### 4.6 回调幂等

系统按 `batchNo` 做幂等：

| 批次状态 | 处理方式 |
| --- | --- |
| 已导入完成 | 直接返回成功 |
| 正在导入中 | 直接返回成功 |
| 已提交或已回调 | 保存回调信息，等待结果导入定时任务处理 |
