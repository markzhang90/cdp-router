import { useEffect, useMemo, useRef, useState } from "react";
import { Button, DatePicker, Descriptions, Empty, Progress, Segmented, Space, Table, Tag, Typography, message } from "antd";
import type { ColumnsType } from "antd/es/table";
import dayjs from "dayjs";
import type { Dayjs } from "dayjs";
import * as echarts from "echarts/core";
import { GridComponent, LegendComponent, TooltipComponent } from "echarts/components";
import type { EChartsCoreOption } from "echarts/core";
import { LineChart } from "echarts/charts";
import { CanvasRenderer } from "echarts/renderers";
import { ArrowLeft, BarChart3, RefreshCw, Search, Server, Table2 } from "lucide-react";
import { useNavigate, useParams } from "react-router-dom";
import { getProviderMetrics } from "../api/client";
import { PageHeader } from "../components/PageHeader";
import type { Provider, ProviderDailyMetric, ProviderMetricsData } from "../types/admin";
import { compactText, formatRateFromCounts, precisePercent, rateFromCounts } from "../utils/format";

const { RangePicker } = DatePicker;

echarts.use([GridComponent, LegendComponent, TooltipComponent, LineChart, CanvasRenderer]);

type DateRange = [Dayjs, Dayjs];

function defaultRange(): DateRange {
  return [dayjs().subtract(7, "day"), dayjs()];
}

function formatNumber(value: number): string {
  return new Intl.NumberFormat("zh-CN").format(value || 0);
}

function ProviderMetricTile({ label, value, tone = "default" }: { label: string; value: string; tone?: "default" | "success" | "danger" }) {
  return (
    <div className={`metric-tile metric-tile--${tone}`}>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function providerQuotaPercent(provider: Provider): number {
  if (provider.dailyQuestionUnlimited || provider.dailyQuestionQuota <= 0) return 100;
  return Math.round((Math.max(0, provider.dailyQuestionRemaining) / provider.dailyQuestionQuota) * 100);
}

function providerQuotaProgressColor(percent: number): string {
  if (percent <= 20) return "#d14343";
  if (percent <= 50) return "#c98912";
  return "#16776b";
}

function ProviderQuotaSummary({ provider }: { provider?: Provider }) {
  if (!provider) return <>-</>;
  if (provider.dailyQuestionUnlimited || provider.dailyQuestionQuota <= 0) {
    return (
      <div className="provider-quota-cell provider-quota-cell--detail">
        <div className="provider-quota-cell__meta">
          <strong>无上限</strong>
          <span>今日已用 {formatNumber(provider.dailyQuestionUsed)}</span>
        </div>
      </div>
    );
  }

  const percent = providerQuotaPercent(provider);
  return (
    <div className="provider-quota-cell provider-quota-cell--detail">
      <Progress
        percent={percent}
        showInfo={false}
        size="small"
        strokeColor={providerQuotaProgressColor(percent)}
        trailColor="#edf2ef"
      />
      <div className="provider-quota-cell__meta">
        <strong>剩 {formatNumber(provider.dailyQuestionRemaining)}</strong>
        <span>
          已用 {formatNumber(provider.dailyQuestionUsed)} / {formatNumber(provider.dailyQuestionQuota)}
        </span>
      </div>
    </div>
  );
}

function buildDailySeries(rows: ProviderDailyMetric[], range: DateRange): ProviderDailyMetric[] {
  const byDate = new Map(rows.map((row) => [row.metricDate, row]));
  const start = range[0].startOf("day");
  const end = range[1].startOf("day");
  const days = Math.max(0, end.diff(start, "day"));
  return Array.from({ length: days + 1 }, (_, index) => {
    const metricDate = start.add(index, "day").format("YYYY-MM-DD");
    return (
      byDate.get(metricDate) ?? {
        metricDate,
        providerCode: rows[0]?.providerCode ?? "",
        taskCount: 0,
        questionCount: 0,
        successAnswerCount: 0,
        failedAnswerCount: 0
      }
    );
  });
}

function ProviderDailyLineChart({ rows, range }: { rows: ProviderDailyMetric[]; range: DateRange }) {
  const chartRef = useRef<HTMLDivElement | null>(null);
  const chartRows = useMemo(() => buildDailySeries(rows, range), [range, rows]);

  useEffect(() => {
    if (!chartRef.current) return;
    const chart = echarts.init(chartRef.current);
    const dates = chartRows.map((row) => row.metricDate.slice(5));
    const option: EChartsCoreOption = {
      color: ["#16776b", "#496bd0", "#2d9a50", "#d14343"],
      tooltip: {
        trigger: "axis",
        valueFormatter: (value: unknown) => formatNumber(Number(value))
      },
      legend: {
        top: 0,
        right: 0,
        itemWidth: 10,
        itemHeight: 10,
        textStyle: {
          color: "#5e6b67"
        }
      },
      grid: {
        top: 46,
        right: 18,
        bottom: 32,
        left: 44,
        containLabel: true
      },
      xAxis: {
        type: "category",
        boundaryGap: false,
        data: dates,
        axisLine: { lineStyle: { color: "#d7e2de" } },
        axisTick: { show: false },
        axisLabel: { color: "#6c7975" }
      },
      yAxis: {
        type: "value",
        minInterval: 1,
        splitLine: { lineStyle: { color: "#edf3f0" } },
        axisLabel: { color: "#6c7975" }
      },
      series: [
        {
          name: "任务数",
          type: "line",
          smooth: true,
          symbolSize: 6,
          data: chartRows.map((row) => row.taskCount)
        },
        {
          name: "问答数",
          type: "line",
          smooth: true,
          symbolSize: 6,
          data: chartRows.map((row) => row.questionCount)
        },
        {
          name: "成功问答",
          type: "line",
          smooth: true,
          symbolSize: 6,
          data: chartRows.map((row) => row.successAnswerCount)
        },
        {
          name: "失败/未回答",
          type: "line",
          smooth: true,
          symbolSize: 6,
          data: chartRows.map((row) => row.failedAnswerCount)
        }
      ]
    };
    chart.setOption(option);
    const resize = () => chart.resize();
    window.addEventListener("resize", resize);
    return () => {
      window.removeEventListener("resize", resize);
      chart.dispose();
    };
  }, [chartRows]);

  return (
    <div className="provider-line-chart-wrap">
      <div ref={chartRef} className="provider-line-chart" />
    </div>
  );
}

export function ProviderDetailPage() {
  const navigate = useNavigate();
  const params = useParams();
  const providerCode = params.code ? decodeURIComponent(params.code) : "";
  const [range, setRange] = useState<DateRange>(() => defaultRange());
  const [loading, setLoading] = useState(false);
  const [detail, setDetail] = useState<ProviderMetricsData | null>(null);
  const [view, setView] = useState<"table" | "chart">("table");

  const load = async (nextRange = range) => {
    if (!providerCode) return;
    setLoading(true);
    try {
      setDetail(
        await getProviderMetrics(providerCode, {
          createdFrom: nextRange[0].toISOString(),
          createdTo: nextRange[1].toISOString()
        })
      );
    } catch (error) {
      message.error(error instanceof Error ? error.message : "加载供应商数据失败");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load(range);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [providerCode]);

  const setQuickRange = (amount: number, unit: "day") => {
    const nextRange: DateRange = [dayjs().subtract(amount, unit), dayjs()];
    setRange(nextRange);
    void load(nextRange);
  };

  const dailyColumns = useMemo<ColumnsType<ProviderDailyMetric>>(
    () => [
      { title: "日期", dataIndex: "metricDate", width: 130 },
      { title: "任务数", dataIndex: "taskCount", width: 110, render: formatNumber },
      { title: "问答数", dataIndex: "questionCount", width: 110, render: formatNumber },
      { title: "成功问答数量", dataIndex: "successAnswerCount", width: 140, render: formatNumber },
      { title: "失败/未回答数量", dataIndex: "failedAnswerCount", width: 150, render: formatNumber },
      {
        title: "成功率",
        render: (_, row) => <Progress percent={precisePercent(rateFromCounts(row.successAnswerCount, row.questionCount))} size="small" />
      }
    ],
    []
  );

  const provider = detail?.provider;
  const summary = detail?.summary;
  const rows = detail?.dailyMetrics ?? [];

  if (!detail && !loading) {
    return (
      <section>
        <PageHeader title="供应商详情" extra={<Button icon={<ArrowLeft size={16} />} onClick={() => navigate("/providers")} />} />
        <Empty />
      </section>
    );
  }

  return (
    <section>
      <PageHeader
        title={provider?.displayName ?? providerCode}
        description={provider?.providerCode}
        extra={
          <Space>
            <Button icon={<ArrowLeft size={16} />} onClick={() => navigate("/providers")}>
              返回
            </Button>
            <Button icon={<RefreshCw size={16} />} loading={loading} onClick={() => load(range)}>
              刷新
            </Button>
          </Space>
        }
      />

      <div className="detail-summary">
        <Descriptions bordered column={{ xs: 1, sm: 2, lg: 4 }} size="small">
          <Descriptions.Item label="供应商编码">{provider?.providerCode ?? "-"}</Descriptions.Item>
          <Descriptions.Item label="公司名称">{compactText(provider?.companyName ?? "")}</Descriptions.Item>
          <Descriptions.Item label="开始结算时间">{compactText(provider?.settlementStartAt ?? "")}</Descriptions.Item>
          <Descriptions.Item label="状态">{provider?.enabled ? <Tag color="success">启用</Tag> : <Tag>停用</Tag>}</Descriptions.Item>
          <Descriptions.Item label="结果模式">{provider?.resultMode ?? "-"}</Descriptions.Item>
          <Descriptions.Item label="超时">{provider ? `${provider.timeoutSeconds}s` : "-"}</Descriptions.Item>
          <Descriptions.Item label="QPS">{provider?.qpsLimit ?? "-"}</Descriptions.Item>
          <Descriptions.Item label="并发">{provider?.concurrencyLimit ?? "-"}</Descriptions.Item>
          <Descriptions.Item label="任务额度">
            {provider
              ? provider.dailyQuestionUnlimited || provider.dailyQuestionQuota <= 0
                ? "无上限"
                : `${formatNumber(provider.dailyQuestionQuota)}/日`
              : "-"}
          </Descriptions.Item>
          <Descriptions.Item label="今日剩余额度">
            <ProviderQuotaSummary provider={provider} />
          </Descriptions.Item>
          <Descriptions.Item label="OSS 前缀" span={2}>
            <Typography.Text copyable={Boolean(provider?.bucketPathPrefix)} className="mono-text">
              {compactText(provider?.bucketPathPrefix ?? "")}
            </Typography.Text>
          </Descriptions.Item>
        </Descriptions>
      </div>

      <div className="filter-bar platform-metrics-filter">
        <Space wrap>
          <RangePicker
            showTime
            value={range}
            onChange={(value) => {
              if (value?.[0] && value?.[1]) setRange([value[0], value[1]]);
            }}
          />
          <Button type="primary" icon={<Search size={16} />} loading={loading} onClick={() => load(range)}>
            查询
          </Button>
          <Button onClick={() => setQuickRange(7, "day")}>近 7 天</Button>
          <Button onClick={() => setQuickRange(30, "day")}>近 30 天</Button>
        </Space>
      </div>

      <div className="metrics-section">
        <div className="metrics-section__title">
          <Server size={17} />
          <h2>核心指标</h2>
        </div>
        <div className="metric-grid">
          <ProviderMetricTile label="任务数" value={formatNumber(summary?.taskCount ?? 0)} />
          <ProviderMetricTile label="问答数" value={formatNumber(summary?.questionCount ?? 0)} />
          <ProviderMetricTile label="成功问答数量" value={formatNumber(summary?.successAnswerCount ?? 0)} tone="success" />
          <ProviderMetricTile label="失败/未回答数量" value={formatNumber(summary?.failedAnswerCount ?? 0)} tone="danger" />
          <ProviderMetricTile
            label="问答成功率"
            value={formatRateFromCounts(summary?.successAnswerCount ?? 0, summary?.questionCount ?? 0)}
            tone="success"
          />
        </div>
      </div>

      <div className="metrics-section">
        <div className="metrics-section__header">
          <div className="metrics-section__title">
            <BarChart3 size={17} />
            <h2>每日数据</h2>
          </div>
          <Segmented
            size="small"
            value={view}
            onChange={(value) => setView(value as "table" | "chart")}
            options={[
              { label: "表格", value: "table", icon: <Table2 size={14} /> },
              { label: "折线图", value: "chart", icon: <BarChart3 size={14} /> }
            ]}
          />
        </div>
        {view === "table" ? (
          <Table
            columns={dailyColumns}
            dataSource={rows}
            loading={loading}
            pagination={false}
            rowKey={(row) => `${row.metricDate}-${row.providerCode}`}
            scroll={{ x: 780 }}
            size="small"
          />
        ) : (
          <ProviderDailyLineChart rows={rows} range={range} />
        )}
      </div>
    </section>
  );
}
