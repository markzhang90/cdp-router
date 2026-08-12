### 1. 按 externalBatchId 查询问题状态

**基本信息**

| 项目   | 值                                                      |
|-------|---------------------------------------------------------|
| 接口名称 | 按 externalBatchId 查询问题状态                           |
| 请求方式 | `GET`                                                   |
| 接口路径 | `/api/v1/ai/batch/external/{externalBatchId}`           |
| 鉴权 | `X-Provider-Token`                                      |

**路径参数**

| 参数名            | 类型    | 必填 | 说明 |
|------------------|--------|------|------|
| externalBatchId  | string | 是   | 系统生成的批次编码（`batch_code`），创建批次接口返回的 `externalBatchId` |

**响应数据**

```json
{
  "success": true,
  "message": "查询成功",
  "data": {
    "externalBatchId": "8f3b7e21a3c94b5e9d6f2a1b3c4d5e6f",
    "questions": [
      {
        "questionId": "Q001",
        "status": 0
      },
      {
        "questionId": "Q002",
        "status": 1
      }
    ]
  },
  "code": 200
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| data.externalBatchId | string | 入参回显 |
| data.questions | array | 该编码下未删除问题列表，按 `id` 升序 |
| data.questions[].questionId | string | 问题ID |
| data.questions[].status | number | 状态：`0` 待处理 / `1` 处理中 / `2` 处理完成 |

> 查无数据时 `questions` 为空数组 `[]`。本接口只读，不改变问题状态。

---

### 2. 按 externalBatchId 查询处理结果文件

**基本信息**

| 项目   | 值                                                         |
|-------|------------------------------------------------------------|
| 接口名称 | 按 externalBatchId 查询处理结果文件                           |
| 请求方式 | `GET`                                                      |
| 接口路径 | `/api/v1/ai/batch/external/{externalBatchId}/files`        |
| 鉴权 | `X-Provider-Token`                                         |

**路径参数**

| 参数名            | 类型    | 必填 | 说明 |
|------------------|--------|------|------|
| externalBatchId  | string | 是   | 系统生成的批次编码（`batch_code`），创建批次接口返回的 `externalBatchId` |

**响应数据**

```json
{
  "success": true,
  "message": "查询成功",
  "data": {
    "externalBatchId": "8f3b7e21a3c94b5e9d6f2a1b3c4d5e6f",
    "fileUrls": [
      "https://oss.example.com/batch/BATCH202606090001/result1.xlsx",
      "https://oss.example.com/batch/BATCH202606090001/result2.xlsx"
    ]
  },
  "code": 200
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| data.externalBatchId | string | 入参回显 |
| data.fileUrls | string[] | 成功回调写入的文件地址列表，按 `ai_batch_callback.id` 升序；空串已过滤 |

> 查询流程：`externalBatchId` → `ai_question_batch.batch_id` → `ai_batch_callback.file_url`。  
> 批次不存在或尚无文件时，`fileUrls` 为空数组 `[]`。本接口只读。
