import { useEffect, useMemo, useState } from "react";
import { Button, Descriptions, Empty, Modal, Progress, Space, Table, Tabs, Typography, message } from "antd";
import type { ColumnsType } from "antd/es/table";
import { ArrowLeft, Download, ExternalLink, RefreshCw, RotateCcw, Send } from "lucide-react";
import { useNavigate, useParams } from "react-router-dom";
import { checkTaskResult, exportTaskItemsXlsx, getTaskDetail, retryTaskCallback } from "../api/client";
import { JsonBlock } from "../components/JsonBlock";
import { PageHeader } from "../components/PageHeader";
import { ResultStatusTag, TaskStatusTag } from "../components/StatusTag";
import type { ResultFile, TaskCallback, TaskDetailData, TaskEvent, TaskItem } from "../types/admin";
import { compactText, formatFullDateTime, percent, taskProgress } from "../utils/format";

export function TaskDetailPage() {
  const navigate = useNavigate();
  const params = useParams();
  const taskId = Number(params.id);
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [retryingCallback, setRetryingCallback] = useState(false);
  const [detail, setDetail] = useState<TaskDetailData | null>(null);
  const [itemPage, setItemPage] = useState(1);
  const [itemPageSize, setItemPageSize] = useState(20);

  const load = async (nextItemPage = itemPage, nextItemPageSize = itemPageSize) => {
    if (!Number.isFinite(taskId)) return;
    setLoading(true);
    try {
      const data = await getTaskDetail(taskId, {
        itemPage: nextItemPage,
        itemPageSize: nextItemPageSize
      });
      setDetail(data);
      setItemPage(data.itemPage || nextItemPage);
      setItemPageSize(data.itemPageSize || nextItemPageSize);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "加载任务详情失败");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load(1, itemPageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [taskId]);

  const handleCheck = async () => {
    setChecking(true);
    try {
      const data = await checkTaskResult(taskId);
      message.success(data.msg || `检查完成：文件 ${data.fileCount} 个，结果行 ${data.rowCount} 条`);
      await load(itemPage, itemPageSize);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "检查结果失败");
    } finally {
      setChecking(false);
    }
  };

  const handleRetryCallback = async () => {
    setRetryingCallback(true);
    try {
      const data = await retryTaskCallback(taskId);
      if (data.forceRequired && !data.delivered) {
        setRetryingCallback(false);
        Modal.confirm({
          title: "数量不一致，确认强制回调？",
          content: (
            <Space direction="vertical" size={4}>
              <Typography.Text>{data.reason || "当前结果数量未通过校验。"}</Typography.Text>
              <Typography.Text type="secondary">
                当前成功 {data.successCount} / 任务总数 {data.totalCount}
              </Typography.Text>
            </Space>
          ),
          okText: "强制回调",
          cancelText: "取消",
          okButtonProps: { danger: true },
          onOk: async () => {
            setRetryingCallback(true);
            try {
              const forced = await retryTaskCallback(taskId, true);
              message[forced.delivered ? "success" : "warning"](
                forced.delivered ? "强制回调已发送" : forced.reason || "强制回调未成功"
              );
              await load(itemPage, itemPageSize);
            } finally {
              setRetryingCallback(false);
            }
          }
        });
        return;
      }
      message[data.delivered ? "success" : "warning"](
        data.delivered ? "回调重试成功" : data.reason || "回调重试未发送，请查看回调详情"
      );
      await load(itemPage, itemPageSize);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "重新回调失败");
    } finally {
      setRetryingCallback(false);
    }
  };

  const handleExportItems = async () => {
    if (!taskId || !detail?.task) return;
    setExporting(true);
    try {
      const data = await exportTaskItemsXlsx(taskId);
      const link = document.createElement("a");
      link.href = data.downloadUrl;
      link.rel = "noreferrer";
      link.target = "_blank";
      document.body.appendChild(link);
      link.click();
      link.remove();
      message.success("导出已开始");
    } catch (error) {
      message.error(error instanceof Error ? error.message : "导出问答记录失败");
    } finally {
      setExporting(false);
    }
  };

  const itemColumns = useMemo<ColumnsType<TaskItem>>(
    () => [
      { title: "轮次", dataIndex: "seq", width: 72 },
      { title: "itemId", dataIndex: "itemId", width: 220 },
      { title: "平台", dataIndex: "platformCode", width: 100 },
      {
        title: "问题",
        dataIndex: "question",
        width: 300,
        render: (value: string) => <Typography.Paragraph ellipsis={{ rows: 2 }}>{value}</Typography.Paragraph>
      },
      {
        title: "结果",
        dataIndex: "resultStatus",
        width: 120,
        render: (status: string) => <ResultStatusTag status={status} />
      },
      {
        title: "回答",
        dataIndex: "answerText",
        width: 360,
        render: (value: string) => (
          <Typography.Paragraph className="answer-preview" ellipsis={{ rows: 3 }}>
            {compactText(value)}
          </Typography.Paragraph>
        )
      },
      {
        title: "录屏",
        dataIndex: "recordingUrl",
        width: 110,
        render: (value: string) =>
          value ? (
            <Button
              href={value}
              icon={<ExternalLink size={15} />}
              rel="noreferrer"
              size="small"
              target="_blank"
              type="link"
            >
              打开
            </Button>
          ) : (
            "-"
          )
      },
      { title: "引用", dataIndex: "referenceCount", width: 80 },
      { title: "导入时间", dataIndex: "resultImportedAt", width: 170, render: formatFullDateTime }
    ],
    []
  );

  const fileColumns = useMemo<ColumnsType<ResultFile>>(
    () => [
      { title: "文件", dataIndex: "fileName", width: 220, render: (value: string) => compactText(value) },
      { title: "来源", dataIndex: "sourceType", width: 120 },
      { title: "状态", dataIndex: "parseStatus", width: 110, render: (status: string) => <ResultStatusTag status={status} /> },
      { title: "行数", dataIndex: "rowCount", width: 90 },
      { title: "有效", dataIndex: "validRowCount", width: 90 },
      { title: "无效", dataIndex: "invalidRowCount", width: 90 },
      {
        title: "地址",
        render: (_, file) => {
          const address = file.fileUrl || file.objectKey;
          if (!address) return "-";
          const canOpen = /^https?:\/\//i.test(address);
          return (
            <Space size={6}>
              <Typography.Text copyable={{ text: address }} className="mono-text">
                {address}
              </Typography.Text>
              {canOpen ? (
                <Button
                  href={address}
                  icon={<ExternalLink size={15} />}
                  size="small"
                  target="_blank"
                  type="text"
                />
              ) : null}
            </Space>
          );
        }
      },
      { title: "创建时间", dataIndex: "createdAt", width: 170, render: formatFullDateTime }
    ],
    []
  );

  const eventColumns = useMemo<ColumnsType<TaskEvent>>(
    () => [
      { title: "事件", dataIndex: "eventType", width: 220 },
      { title: "时间", dataIndex: "createdAt", width: 180, render: formatFullDateTime },
      { title: "内容", dataIndex: "payloadJson", render: (value: string) => <JsonBlock value={value} maxHeight={180} /> }
    ],
    []
  );

  const callbackColumns = useMemo<ColumnsType<TaskCallback>>(
    () => [
      {
        title: "状态",
        dataIndex: "status",
        width: 110,
        render: (status: string) => <TaskStatusTag status={status} />
      },
      {
        title: "HTTP",
        dataIndex: "responseStatus",
        width: 90,
        render: (value: number) => value || "-"
      },
      {
        title: "目标地址",
        dataIndex: "targetUrl",
        width: 320,
        render: (value: string) => (
          <Typography.Text copyable className="mono-text">
            {compactText(value)}
          </Typography.Text>
        )
      },
      { title: "重试次数", dataIndex: "retryCount", width: 100 },
      {
        title: "错误",
        dataIndex: "lastError",
        render: (value: string) => (
          <Typography.Paragraph className="answer-preview" ellipsis={{ rows: 2 }}>
            {compactText(value)}
          </Typography.Paragraph>
        )
      },
      { title: "创建时间", dataIndex: "createdAt", width: 180, render: formatFullDateTime },
      { title: "更新时间", dataIndex: "updatedAt", width: 180, render: formatFullDateTime }
    ],
    []
  );

  if (!detail && !loading) {
    return (
      <section>
        <PageHeader title="任务详情" extra={<Button icon={<ArrowLeft size={16} />} onClick={() => navigate("/tasks")} />} />
        <Empty />
      </section>
    );
  }

  const task = detail?.task;

  return (
    <section>
      <PageHeader
        title={task?.taskName ?? "任务详情"}
        description={task?.batchNo}
        extra={
          <Space>
            <Button icon={<ArrowLeft size={16} />} onClick={() => navigate("/tasks")}>
              返回
            </Button>
            <Button icon={<RefreshCw size={16} />} onClick={() => load(itemPage, itemPageSize)}>
              刷新
            </Button>
            <Button icon={<Send size={16} />} loading={retryingCallback} onClick={handleRetryCallback}>
              重新回调
            </Button>
            <Button icon={<RotateCcw size={16} />} loading={checking} type="primary" onClick={handleCheck}>
              检查结果
            </Button>
          </Space>
        }
      />
      <div className="detail-summary">
        <Descriptions bordered column={{ xs: 1, sm: 2, lg: 4 }} size="small">
          <Descriptions.Item label="状态">{task ? <TaskStatusTag status={task.status} /> : "-"}</Descriptions.Item>
          <Descriptions.Item label="来源">{compactText(task?.source ?? "")}</Descriptions.Item>
          <Descriptions.Item label="供应商">{task?.providerCode ?? "-"}</Descriptions.Item>
          <Descriptions.Item label="问答">{task ? `${task.completedCount}/${task.questionCount}` : "-"}</Descriptions.Item>
          <Descriptions.Item label="进度">
            <Progress percent={task ? percent(taskProgress(task)) : 0} size="small" />
          </Descriptions.Item>
          <Descriptions.Item label="执行耗时">{compactText(task?.executionDurationText ?? "")}</Descriptions.Item>
          <Descriptions.Item label="创建时间">{formatFullDateTime(task?.createdAt ?? "")}</Descriptions.Item>
          <Descriptions.Item label="更新时间">{formatFullDateTime(task?.updatedAt ?? "")}</Descriptions.Item>
        </Descriptions>
      </div>
      <Tabs
        className="detail-tabs"
        tabBarExtraContent={
          <Button icon={<Download size={16} />} loading={exporting} onClick={handleExportItems}>
            导出记录
          </Button>
        }
        items={[
          {
            key: "items",
            label: "问答结果",
            children: (
              <Table
                columns={itemColumns}
                dataSource={detail?.items ?? []}
                loading={loading}
                pagination={{
                  current: itemPage,
                  pageSize: itemPageSize,
                  total: detail?.itemTotal ?? 0,
                  showSizeChanger: true,
                  showTotal: (total) => `共 ${total} 条`,
                  onChange: (page, pageSize) => {
                    setItemPage(page);
                    setItemPageSize(pageSize);
                    void load(page, pageSize);
                  }
                }}
                rowKey={(row) => `${row.taskItemId}-${row.seq}`}
                expandable={{
                  expandedRowRender: (item) => (
                    <div className="item-expanded">
                      <div>
                        <h3>回答内容</h3>
                        <Typography.Paragraph>{compactText(item.answerText)}</Typography.Paragraph>
                      </div>
                      <div>
                        <h3>引用</h3>
                        <JsonBlock value={item.referencesJson || "[]"} />
                      </div>
                      <div>
                        <h3>录屏地址</h3>
                        <Typography.Text copyable={Boolean(item.recordingUrl)} className="mono-text">
                          {compactText(item.recordingUrl)}
                        </Typography.Text>
                      </div>
                    </div>
                  )
                }}
                scroll={{ x: 1420 }}
              />
            )
          },
          {
            key: "files",
            label: "结果文件",
            children: (
              <Table
                columns={fileColumns}
                dataSource={detail?.files ?? []}
                loading={loading}
                pagination={false}
                rowKey="id"
                scroll={{ x: 1100 }}
              />
            )
          },
          {
            key: "events",
            label: "任务事件",
            children: (
              <Table
                columns={eventColumns}
                dataSource={detail?.events ?? []}
                loading={loading}
                pagination={false}
                rowKey="id"
              />
            )
          },
          {
            key: "callbacks",
            label: "回调记录",
            children: (
              <Table
                columns={callbackColumns}
                dataSource={detail?.callbacks ?? []}
                loading={loading}
                pagination={false}
                rowKey="id"
                expandable={{
                  expandedRowRender: (callback) => (
                    <div className="callback-expanded">
                      <div>
                        <h3>请求内容</h3>
                        <JsonBlock value={callback.requestPayloadJson || "{}"} />
                      </div>
                      <div>
                        <h3>响应内容</h3>
                        <JsonBlock value={callback.responseBody || "{}"} />
                      </div>
                      <div>
                        <h3>错误详情</h3>
                        <JsonBlock value={callback.lastError || "-"} maxHeight={180} />
                      </div>
                    </div>
                  )
                }}
                scroll={{ x: 1200 }}
              />
            )
          }
        ]}
      />
    </section>
  );
}
