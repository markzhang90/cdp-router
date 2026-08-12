# 一次性任务前端接口文档

## 基础信息

- 服务上下文：`/api2`
- 接口前缀：`/v1/once/task`
- 请求方式：`POST`
- 请求格式：`application/json`
- 返回格式：统一 `Result<T>`
- 鉴权方式：请求 Header 必须传 `apiKey`

Base URL

| 环境   | baseURL                                         |
| ---- | ----------------------------------------------- |
| 测试环境 | `https://staging-api-growth.wiredcraft.cn/api2` |
| 生产环境 | `https://api-growth.wiredcraft.cn/api2`         |

请求 Header：

| Header | 必填 | 说明 |
| --- | --- | --- |
| apiKey | 是 | 接口访问密钥。 |

统一返回结构：

```json
{
  "status": 0,
  "message": null,
  "data": {}
}
```

## 1. 创建一次性任务

### 接口

`POST /v1/once/task/create`

### 请求 Header

```http
apiKey: {apiKey}
Content-Type: application/json
```

### 请求参数

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| product | object | 是 | 产品信息 |
| product.brand | string | 否 | 品牌 |
| product.model | string | 是 | 产品名 |
| product.model2 | string | 否 | 通用名 |
| competitors | array | 否 | 竞品信息 |
| competitors[].name | string | 否 | 竞品名称 |
| platformCodes | array<string> | 是 | 平台编码数组 |
| questions | array | 是 | 问题数组 |
| questions[].simulation | string | 是 | 关键字/场景 |
| questions[].name | string | 是 | 问题内容 |
| qaLoop | integer | 是 | 询问次数，必须大于 0 |

平台编码取值：

| 编码       | 说明       |
| -------- | -------- |
| deepseek | DeepSeek |
| qwen     | 通义千问     |
| kimi     | Kimi     |
| doubao   | 豆包       |
| yuanbao  | 元宝       |
| antafu   | antafu   |

### 请求示例

```json
{
  "product": {
    "brand": "万宝龙",
    "model": "万宝龙手表",
    "model2": "瑞士手表"
  },
  "competitors": [
    {
      "name": "帝舵"
    },
    {
      "name": "浪琴手表"
    }
  ],
  "platformCodes": [
    "deepseek",
    "qwen",
    "kimi",
    "doubao",
    "yuanbao",
    "antafu"
  ],
  "questions": [
    {
      "simulation": "瑞士手表推荐",
      "name": "15000-30000 元瑞士手表推荐"
    },
    {
      "simulation": "瑞士手表推荐",
      "name": "2 万左右浪琴、帝舵、豪雅、万宝龙怎么选"
    }
  ],
  "qaLoop": 2
}
```

### 返回参数

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| data.taskId | integer | 一次性任务 ID，用于获取导出数据 |

### 返回示例

```json
{
  "status": 0,
  "message": null,
  "data": {
    "taskId": 101
  }
}
```

## 2. 获取一次性任务导出数据

### 接口

`POST /v1/once/task/data`

### 请求 Header

```http
apiKey: {apiKey}
Content-Type: application/json
```

### 请求参数

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| taskId | integer | 是 | 创建任务接口返回的任务 ID |

### 请求示例

```json
{
  "taskId": 101
}
```

### 返回参数

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| data.stage | string | 当前阶段：`q2a` 表示 AI 问答，`analysis` 表示分析 |
| data.completedCount | integer | 当前阶段已结束任务数（执行完成或重试次数达到 4） |
| data.totalCount | integer | 当前阶段任务总数 |
| data.progress | integer | 当前阶段进度百分比，范围为 0-100 |
| data.url | string/null | 分析阶段全部结束时返回 OSS Excel 下载 URL，否则为 `null` |

### 已完成返回示例

```json
{
  "status": 0,
  "message": null,
  "data": {
    "stage": "analysis",
    "completedCount": 12,
    "totalCount": 12,
    "progress": 100,
    "url": "https://dvtp0.oss-cn-shanghai.aliyuncs.com/external/dev/onceTask/20260711/101-1783760000000-result.xlsx?Expires=..."
  }
}
```

### AI 问答进行中返回示例

```json
{
  "status": 0,
  "message": null,
  "data": {
    "stage": "q2a",
    "completedCount": 3,
    "totalCount": 12,
    "progress": 25,
    "url": null
  }
}
```

### 分析进行中返回示例

```json
{
  "status": 0,
  "message": null,
  "data": {
    "stage": "analysis",
    "completedCount": 6,
    "totalCount": 12,
    "progress": 50,
    "url": null
  }
}
```

### 前端处理

- `data.stage = q2a`：展示 AI 问答进度。
- `data.stage = analysis`：展示分析进度。
- `data.url = null`：任务结果暂未准备好，前端可继续轮询。
- `data.url` 有值：任务结果已准备好，前端可使用该 URL 下载 Excel。

### 鉴权失败返回

未传 `apiKey` 或 `apiKey` 不正确时，返回：

```json
{
  "status": 5002,
  "message": "没有权限",
  "data": null
}
```

### Excel 内容

| Sheet | 说明 |
| --- | --- |
| 提及 | 产品/竞品在不同日期、平台、问题下的提及数和 AI 回复数 |
| 溯源 | 引用链接明细、来源、发布日期、链接占比、采纳率、本品和竞品提及 |
| 情感 | 产品/竞品在不同日期、平台、问题下的正向、中性、负向数量和占比 |
| 回复快照 | 平台、问题、AI 答案、日期 |

## 3. 调用流程

1. 客户端调用创建接口 `/v1/once/task/create`。
2. 服务端在 `data.taskId` 中返回任务 ID。
3. 客户端轮询 `/v1/once/task/data`，请求体传入 `taskId`。
4. 根据 `data.stage` 和 `data.progress` 展示当前阶段进度。
5. 如果 `data.url = null`，说明任务结果暂未准备好。
6. 如果 `data.url` 有值，客户端使用该 URL 下载 Excel。
