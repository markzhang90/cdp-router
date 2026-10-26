import { useEffect, useMemo, useState } from "react";
import { Button, DatePicker, Descriptions, Empty, Progress, Segmented, Space, Table, Tooltip, Typography, message } from "antd";
import type { ColumnsType } from "antd/es/table";
import dayjs from "dayjs";
import type { Dayjs } from "dayjs";
import { ArrowLeft, BarChart3, Clock3, Download, FileSpreadsheet, HelpCircle, RefreshCw, Search, Send, ServerCrash, Table2 } from "lucide-react";
import { useNavigate, useParams } from "react-router-dom";
import { exportPlatformUserReconciliationXlsx, getPlatformUserMetrics } from "../api/client";
import { PageHeader } from "../components/PageHeader";
import { TaskStatusTag } from "../components/StatusTag";
import type { MetricBreakdown, PlatformUserMetricsData, ProviderDailyMetric, TaskSummary } from "../types/admin";
import { compactText, formatDateTime, formatFullDateTime, formatRate, percent, precisePercent, rateFromCounts } from "../utils/format";

const { RangePicker } = DatePicker;

type DateRange = [Dayjs, Dayjs];
type RangeMode = "month" | "custom";

function monthRange(value: Dayjs): DateRange {
  return [value.startOf("month"), value.endOf("month")];
}

function defaultRange(): DateRange {
  return monthRange(dayjs());
}

function rangeQuery(range: DateRange) {
  return {
    createdFrom: range[0].startOf("day").format("YYYY-MM-DD 00:00:00"),
    createdTo: range[1].endOf("day").format("YYYY-MM-DD 23:59:59")
  };
}

function formatNumber(value: number): string {
  return new Intl.NumberFormat("zh-CN").format(value || 0);
}

function formatDecimal(value: number): string {
  if (!Number.isFinite(value)) return "0.0";
  return value.toFixed(1);
}

function MetricTile({
  label,
  value,
  sub,
  tone = "default"
}: {
  label: string;
  value: string;
  sub?: string;
  tone?: "default" | "success" | "warning" | "danger";
}) {
  return (
    <div className={`metric-tile metric-tile--${tone}`}>
      <span>{label}</span>
      <strong>{value}</strong>
      {sub ? <small>{sub}</small> : null}
    </div>
  );
}

function ProviderDailyChart({ rows }: { rows: ProviderDailyMetric[] }) {
  const maxValue = Math.max(
    1,
    ...rows.flatMap((row) => [row.taskCount, row.questionCount, row.successAnswerCount, row.failedAnswerCount])
  );

  return (
    <div className="provider-chart-scroll">
      <div className="provider-chart">
        {rows.map((row) => (
          <div className="provider-chart-group" key={`${row.metricDate}-${row.providerCode}`}>
            <div className="provider-chart-bars" aria-label={`${row.metricDate} ${row.providerCode}`}>
              {[
                { label: "任务", value: row.taskCount, tone: "task" },
                { label: "问答", value: row.questionCount, tone: "question" },
                { label: "成功", value: row.successAnswerCount, tone: "success" },
                { label: "失败", value: row.failedAnswerCount, tone: "failed" }
              ].map((bar) => (
                <Tooltip key={bar.label} title={`${bar.label}: ${formatNumber(bar.value)}`}>
                  <div className={`provider-chart-bar provider-chart-bar--${bar.tone}`}>
                    <span style={{ height: `${Math.max(6, (bar.value / maxValue) * 100)}%` }} />
                  </div>
                </Tooltip>
              ))}
            </div>
            <strong>{row.providerCode}</strong>
            <small>{row.metricDate}</small>
          </div>
        ))}
      </div>
      <div className="provider-chart-legend">
        <span className="provider-chart-legend__task">任务数</span>
        <span className="provider-chart-legend__question">问答数</span>
        <span className="provider-chart-legend__success">成功问答</span>
        <span className="provider-chart-legend__failed">失败/未回答</span>
      </div>
    </div>
  );
}

export function PlatformUserDetailPage() {
  const navigate = useNavigate();
  const params = useParams();
  const platformUserId = Number(params.id);
  const [rangeMode, setRangeMode] = useState<RangeMode>("month");
  const [month, setMonth] = useState<Dayjs>(() => dayjs());
  const [customRange, setCustomRange] = useState<DateRange>(() => defaultRange());
  const [loading, setLoading] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [metrics, setMetrics] = useState<PlatformUserMetricsData | null>(null);
  const [providerView, setProviderView] = useState<"table" | "chart">("table");
  const selectedRange = useMemo<DateRange>(() => (rangeMode === "month" ? monthRange(month) : customRange), [customRange, month, rangeMode]);

  const load = async (nextRange = selectedRange) => {
    if (!Number.isFinite(platformUserId)) return;
    setLoading(true);
    try {
      setMetrics(await getPlatformUserMetrics(platformUserId, rangeQuery(nextRange)));
    } catch (error) {
      message.error(error instanceof Error ? error.message : "加载接入方数据失败");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load(selectedRange);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [platformUserId]);

  const handleExportReconciliation = async () => {
    if (!Number.isFinite(platformUserId)) return;
    setExporting(true);
    try {
      const data = await exportPlatformUserReconciliationXlsx(platformUserId, rangeQuery(selectedRange));
      const link = document.createElement("a");
      link.href = data.downloadUrl;
      link.target = "_blank";
      link.rel = "noreferrer";
      link.click();
      message.success("对账单已生成");
    } catch (error) {
      message.error(error instanceof Error ? error.message : "导出对账单失败");
    } finally {
      setExporting(false);
    }
  };

  const statusColumns = useMemo<ColumnsType<MetricBreakdown>>(
    () => [
      {
        title: "状态",
        dataIndex: "label",
        width: 160,
        render: (value: string) => <TaskStatusTag status={value} />
      },
      { title: "任务数", dataIndex: "value", width: 100, render: formatNumber },
      {
        title: "占比",
        dataIndex: "ratio",
        render: (value: number) => <Progress percent={percent(value)} size="small" />
      }
    ],
    []
  );

  const failureColumns = useMemo<ColumnsType<MetricBreakdown>>(
    () => [
      {
        title: "失败原因",
        dataIndex: "label",
        render: (value: string) => (
          <Typography.Paragraph className="answer-preview" ellipsis={{ rows: 2 }}>
            {compactText(value)}
          </Typography.Paragraph>
        )
      },
      { title: "次数", dataIndex: "value", width: 90, render: formatNumber },
      { title: "占比", dataIndex: "ratio", width: 110, render: (value: number) => formatRate(value) }
    ],
    []
  );

  const providerDailyColumns = useMemo<ColumnsType<ProviderDailyMetric>>(
    () => [
      { title: "日期", dataIndex: "metricDate", width: 130 },
      {
        title: "供应商",
        dataIndex: "providerCode",
        width: 150,
        render: (value: string) => <Typography.Text strong>{value}</Typography.Text>
      },
      { title: "任务数", dataIndex: "taskCount", width: 110, render: formatNumber },
      { title: "问答数", dataIndex: "questionCount", width: 110, render: formatNumber },
      { title: "成功问答数量", dataIndex: "successAnswerCount", width: 140, render: formatNumber },
      { title: "失败/未回答数量", dataIndex: "failedAnswerCount", width: 150, render: formatNumber },
      {
        title: "成功率",
        width: 160,
        render: (_, row) => (
          <Progress percent={precisePercent(rateFromCounts(row.successAnswerCount, row.questionCount))} size="small" />
        )
      }
    ],
    []
  );

  const recentTaskColumns = useMemo<ColumnsType<TaskSummary>>(
    () => [
      {
        title: "任务名称",
        dataIndex: "taskName",
        width: 260,
        render: (_, task) => (
          <Button type="link" className="table-link" onClick={() => navigate(`/tasks/${task.taskId}`)}>
            {task.taskName || "-"}
          </Button>
        )
      },
      { title: "状态", dataIndex: "status", width: 140, render: (status: string) => <TaskStatusTag status={status} /> },
      { title: "问答", width: 110, render: (_, task) => `${task.completedCount}/${task.questionCount}` },
      { title: "耗时", dataIndex: "executionDurationText", width: 120, render: (value: string) => compactText(value) },
      { title: "创建时间", dataIndex: "createdAt", width: 150, render: formatDateTime }
    ],
    [navigate]
  );

  const user = metrics?.user;
  const task = metrics?.taskMetrics;
  const question = metrics?.questionMetrics;
  const result = metrics?.resultMetrics;
  const callback = metrics?.callbackMetrics;
  const providerDailyMetrics = metrics?.providerDailyMetrics ?? [];

  if (!metrics && !loading) {
    return (
      <section>
        <PageHeader title="接入方数据详情" extra={<Button icon={<ArrowLeft size={16} />} onClick={() => navigate("/platform-users")} />} />
        <Empty />
      </section>
    );
  }

  return (
    <section>
      <PageHeader
        title={user?.name ?? "接入方数据详情"}
        description={user?.platformCode}
        extra={
          <Space>
            <Button icon={<ArrowLeft size={16} />} onClick={() => navigate("/platform-users")}>
              返回
            </Button>
            <Button icon={<RefreshCw size={16} />} loading={loading} onClick={() => load(selectedRange)}>
              刷新
            </Button>
          </Space>
        }
      />

      <div className="detail-summary">
        <Descriptions bordered column={{ xs: 1, sm: 2, lg: 4 }} size="small">
          <Descriptions.Item label="接入方编码">{user?.platformCode ?? "-"}</Descriptions.Item>
          <Descriptions.Item label="状态">{user?.status === "ACTIVE" ? "启用" : "停用"}</Descriptions.Item>
          <Descriptions.Item label="Push 调度状态">{user?.dispatchProfile?.enabled ? "启用" : "停用"}</Descriptions.Item>
          <Descriptions.Item label="Push 调度策略">{user?.dispatchProfile?.strategy ?? "-"}</Descriptions.Item>
          <Descriptions.Item label="Push 供应商池">{user?.dispatchProfile?.providerCodes?.join(", ") || "-"}</Descriptions.Item>
          <Descriptions.Item label="Pull 调度状态">{user?.dispatchProfile?.pull?.enabled ? "启用" : "停用"}</Descriptions.Item>
          <Descriptions.Item label="Pull 调度策略">{user?.dispatchProfile?.pull?.strategy ?? "-"}</Descriptions.Item>
          <Descriptions.Item label="Pull 供应商池">{user?.dispatchProfile?.pull?.providerCodes?.join(", ") || "-"}</Descriptions.Item>
          <Descriptions.Item label="回调方式">{user?.callbackMode === "CUSTOM" ? "定制回调" : "默认 HTTP"}</Descriptions.Item>
          <Descriptions.Item label="API Key">
            <Typography.Text copyable={Boolean(user?.apiKey)} className="mono-text">
              {compactText(user?.apiKey ?? "")}
            </Typography.Text>
          </Descriptions.Item>
          <Descriptions.Item label="回调地址" span={2}>
            <Typography.Text copyable={Boolean(user?.callbackUrl)} className="mono-text">
              {compactText(user?.callbackUrl ?? "")}
            </Typography.Text>
          </Descriptions.Item>
          <Descriptions.Item label="更新时间">{formatFullDateTime(user?.updatedAt ?? "")}</Descriptions.Item>
        </Descriptions>
      </div>

      <div className="filter-bar platform-metrics-filter">
        <Space wrap>
          <Segmented
            size="small"
            value={rangeMode}
            onChange={(value) => setRangeMode(value as RangeMode)}
            options={[
              { label: "自然月", value: "month" },
              { label: "自定义", value: "custom" }
            ]}
          />
          {rangeMode === "month" ? (
            <DatePicker picker="month" allowClear={false} value={month} onChange={(value) => value && setMonth(value)} />
          ) : (
            <RangePicker
              value={customRange}
              onChange={(value) => {
                if (value?.[0] && value?.[1]) {
                  setCustomRange([value[0], value[1]]);
                }
              }}
            />
          )}
          <Button type="primary" icon={<Search size={16} />} loading={loading} onClick={() => load(selectedRange)}>
            查询
          </Button>
          <Button icon={<Download size={16} />} loading={exporting} onClick={handleExportReconciliation}>
            导出对账单
          </Button>
        </Space>
      </div>

      <div className="metrics-section">
        <div className="metrics-section__title">
          <Clock3 size={17} />
          <h2>任务维度</h2>
        </div>
        <div className="metric-grid">
          <MetricTile label="提交任务数" value={formatNumber(task?.submittedTaskCount ?? 0)} />
          <MetricTile label="完成任务数" value={formatNumber(task?.completedTaskCount ?? 0)} tone="success" />
          <MetricTile label="进行中任务数" value={formatNumber(task?.processingTaskCount ?? 0)} tone="warning" />
          <MetricTile label="失败任务数" value={formatNumber(task?.failedTaskCount ?? 0)} tone="danger" />
          <MetricTile label="任务成功率" value={formatRate(task?.taskSuccessRate ?? 0)} sub="DONE / (DONE + FAILED)" tone="success" />
          <MetricTile label="任务平均耗时" value={compactText(task?.averageDurationText ?? "")} />
          <MetricTile label="最长进行中耗时" value={compactText(task?.longestProcessingText ?? "")} tone="warning" />
        </div>
      </div>

      <div className="metrics-section">
        <div className="metrics-section__title">
          <HelpCircle size={17} />
          <h2>问题维度</h2>
        </div>
        <div className="metric-grid">
          <MetricTile label="提交问题数" value={formatNumber(question?.submittedQuestionCount ?? 0)} />
          <MetricTile label="完成问答数" value={formatNumber(question?.completedAnswerCount ?? 0)} tone="success" />
          <MetricTile label="供应商完成问题数" value={formatNumber(question?.providerDoneQuestionCount ?? 0)} />
          <MetricTile label="问题失败数" value={formatNumber(question?.failedQuestionCount ?? 0)} tone="danger" />
          <MetricTile label="问答完成率" value={formatRate(question?.answerCompletionRate ?? 0)} tone="success" />
          <MetricTile label="平均每任务问题数" value={formatDecimal(question?.averageQuestionsPerTask ?? 0)} />
          <MetricTile label="问题平台数" value={formatNumber(question?.activeQuestionPlatformCount ?? 0)} />
        </div>
      </div>

      <div className="metrics-section">
        <div className="metrics-section__header">
          <div className="metrics-section__title">
            <BarChart3 size={17} />
            <h2>供应商</h2>
          </div>
          <Segmented
            size="small"
            value={providerView}
            onChange={(value) => setProviderView(value as "table" | "chart")}
            options={[
              { label: "表格", value: "table", icon: <Table2 size={14} /> },
              { label: "柱状图", value: "chart", icon: <BarChart3 size={14} /> }
            ]}
          />
        </div>
        {providerView === "table" ? (
          <Table
            columns={providerDailyColumns}
            dataSource={providerDailyMetrics}
            loading={loading}
            pagination={false}
            rowKey={(row) => `${row.metricDate}-${row.providerCode}`}
            scroll={{ x: 940 }}
            size="small"
          />
        ) : providerDailyMetrics.length > 0 ? (
          <ProviderDailyChart rows={providerDailyMetrics} />
        ) : (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
        )}
      </div>

      <div className="metrics-section">
        <div className="metrics-section__title">
          <FileSpreadsheet size={17} />
          <h2>结果文件</h2>
        </div>
        <div className="metric-grid">
          <MetricTile label="结果文件数" value={formatNumber(result?.resultFileCount ?? 0)} />
          <MetricTile label="解析成功文件数" value={formatNumber(result?.parsedFileCount ?? 0)} tone="success" />
          <MetricTile label="解析失败文件数" value={formatNumber(result?.parseFailedFileCount ?? 0)} tone="danger" />
          <MetricTile label="结果总行数" value={formatNumber(result?.rowCount ?? 0)} />
          <MetricTile label="有效行数" value={formatNumber(result?.validRowCount ?? 0)} tone="success" />
          <MetricTile label="无效行数" value={formatNumber(result?.invalidRowCount ?? 0)} tone="danger" />
          <MetricTile label="有效行率" value={formatRate(result?.validRowRate ?? 0)} />
        </div>
      </div>

      <div className="metrics-section">
        <div className="metrics-section__title">
          <Send size={17} />
          <h2>回调健康度</h2>
        </div>
        <div className="metric-grid">
          <MetricTile label="回调次数" value={formatNumber(callback?.callbackCount ?? 0)} />
          <MetricTile label="回调成功数" value={formatNumber(callback?.callbackSuccessCount ?? 0)} tone="success" />
          <MetricTile label="回调失败数" value={formatNumber(callback?.callbackFailedCount ?? 0)} tone="danger" />
          <MetricTile label="回调成功率" value={formatRate(callback?.callbackSuccessRate ?? 0)} />
        </div>
      </div>

      <div className="platform-metrics-tables">
        <div className="platform-metrics-panel">
          <div className="metrics-section__title">
            <ServerCrash size={17} />
            <h2>状态分布</h2>
          </div>
          <Table
            columns={statusColumns}
            dataSource={metrics?.statusBreakdown ?? []}
            loading={loading}
            pagination={false}
            rowKey="label"
            size="small"
          />
        </div>
        <div className="platform-metrics-panel">
          <div className="metrics-section__title">
            <ServerCrash size={17} />
            <h2>失败原因</h2>
          </div>
          <Table
            columns={failureColumns}
            dataSource={metrics?.failureReasons ?? []}
            loading={loading}
            locale={{ emptyText: "暂无失败任务" }}
            pagination={false}
            rowKey="label"
            size="small"
          />
        </div>
      </div>

      <div className="platform-metrics-panel">
        <div className="metrics-section__title">
          <Tooltip title="最近创建的任务">
            <Clock3 size={17} />
          </Tooltip>
          <h2>最近任务</h2>
        </div>
        <Table
          columns={recentTaskColumns}
          dataSource={metrics?.recentTasks ?? []}
          loading={loading}
          pagination={false}
          rowKey="taskId"
          scroll={{ x: 820 }}
        />
      </div>
    </section>
  );
}
