import { Space, Tag, Tooltip, Typography } from "antd";
import type { AnswerRisk, TaskItem, TaskSummary } from "../types/admin";

const names: Record<string, string> = {
  ANSWER_TOO_SHORT: "回答过短",
  ANSWER_KEYWORD_MATCH: "命中关键词",
  ANSWER_NO_REFERENCE: "缺少引用"
};

function riskDescription(risk: AnswerRisk): string {
  if (risk.code === "ANSWER_TOO_SHORT") return `正文 ${risk.actualLength} 字，最低 ${risk.minLength} 字`;
  if (risk.code === "ANSWER_KEYWORD_MATCH") return `命中：${risk.matchedKeywords.join("、")}`;
  return risk.message;
}

export function AnswerRiskTags({ item }: { item: TaskItem }) {
  if (item.validationStatus === "SKIPPED") return <Tooltip title="历史回答曾跳过校验，等待补校验"><Tag color="warning">待补校验</Tag></Tooltip>;
  if (item.validationStatus === "PASS") return <Tag color="success">通过</Tag>;
  if (!item.riskItems?.length) return <Typography.Text type="secondary">未校验</Typography.Text>;
  return <Space size={[0, 4]} wrap>{item.riskItems.map((risk) => (
    <Tooltip key={risk.code} title={riskDescription(risk)}>
      <Tag color={risk.code === "ANSWER_NO_REFERENCE" ? "warning" : "error"}>{names[risk.code] || risk.code}</Tag>
    </Tooltip>
  ))}</Space>;
}

export function TaskRiskSummary({ task }: { task: TaskSummary }) {
  let counts: Record<string, number> = {};
  try { counts = JSON.parse(task.riskSummaryJson || "{}"); } catch { /* Display aggregate count even if a legacy summary is absent. */ }
  return <Space size={[0, 4]} wrap>
    {!task.riskStatsUpdatedAt && <Tag>统计待完成</Tag>}
    {task.riskCount > 0 ? <>
      <Tag color="error">风险回答 {task.riskCount} · 无效 {task.invalidAnswerCount}</Tag>
      {Object.entries(counts).map(([code, count]) => <Tag key={code}>{names[code] || code} {count}</Tag>)}
    </> : task.riskStatsUpdatedAt ? <Tag color="success">无风险</Tag> : null}
  </Space>;
}
