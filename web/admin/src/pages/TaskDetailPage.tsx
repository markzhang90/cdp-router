import { useEffect, useMemo, useState } from "react";
import { Alert, Button, Descriptions, Empty, Modal, Space, Table, Tabs, Tag, Typography, message } from "antd";
import type { ColumnsType } from "antd/es/table";
import { AlertTriangle, ArrowLeft, Download, ExternalLink, RefreshCw, RotateCcw, Send, Trash2 } from "lucide-react";
import { useNavigate, useParams } from "react-router-dom";
import { checkTaskResult, deleteTask, exportTaskItemsXlsx, forceImportCallback, getTaskDetail, retryTaskCallback } from "../api/client";
import { getStoredUser } from "../api/session";
import { AnswerRiskTags, TaskRiskSummary } from "../components/AnswerRiskTags";
import { JsonBlock } from "../components/JsonBlock";
import { PageHeader } from "../components/PageHeader";
import { ResultStatusTag, TaskStatusTag } from "../components/StatusTag";
import { TaskProgressBar } from "../components/TaskProgressBar";
import { TaskResultStats } from "../components/TaskResultStats";
import type { ResultFile, TaskCallback, TaskDetailData, TaskDetailQuery, TaskEvent, TaskItem, TaskSummary } from "../types/admin";
import { compactText, formatFullDateTime, positiveID } from "../utils/format";

export function TaskDetailPage() {
  const navigate = useNavigate();
  const params = useParams();
  const taskId = Number(params.id);
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [retryingCallback, setRetryingCallback] = useState(false);
  const [forcingRiskCallback, setForcingRiskCallback] = useState(false);
  const canWrite = ["admin", "operator"].includes(getStoredUser()?.role || "");
  const [forcingImportCallback, setForcingImportCallback] = useState(false);
  const [detail, setDetail] = useState<TaskDetailData | null>(null);
  const [sourceTask, setSourceTask] = useState<TaskSummary | null>(null);
  const [targetTask, setTargetTask] = useState<TaskSummary | null>(null);
  const [itemPage, setItemPage] = useState(1);
  const [itemPageSize, setItemPageSize] = useState(20);
  const [filePage, setFilePage] = useState(1);
  const [filePageSize, setFilePageSize] = useState(20);
  const [eventPage, setEventPage] = useState(1);
  const [eventPageSize, setEventPageSize] = useState(20);

  const load = async (nextQuery: TaskDetailQuery = {}) => {
    if (!Number.isFinite(taskId)) return;
    const query = {
      itemPage: nextQuery.itemPage ?? itemPage,
      itemPageSize: nextQuery.itemPageSize ?? itemPageSize,
      filePage: nextQuery.filePage ?? filePage,
      filePageSize: nextQuery.filePageSize ?? filePageSize,
      eventPage: nextQuery.eventPage ?? eventPage,
      eventPageSize: nextQuery.eventPageSize ?? eventPageSize
    };
    setLoading(true);
    try {
      const data = await getTaskDetail(taskId, query);
      setDetail(data);
      setItemPage(data.itemPage || query.itemPage);
      setItemPageSize(data.itemPageSize || query.itemPageSize);
      setFilePage(data.filePage || query.filePage);
      setFilePageSize(data.filePageSize || query.filePageSize);
      setEventPage(data.eventPage || query.eventPage);
      setEventPageSize(data.eventPageSize || query.eventPageSize);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "加载任务详情失败");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load({ itemPage: 1, filePage: 1, eventPage: 1 });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [taskId]);

  const sourceTaskId = positiveID(detail?.task.reassignedFromTaskId);
  const targetTaskId = positiveID(detail?.task.reassignedToTaskId);

  useEffect(() => {
    let active = true;
    setSourceTask(null);
    setTargetTask(null);
    const relatedIDs = [
      { id: sourceTaskId, setter: setSourceTask },
      { id: targetTaskId, setter: setTargetTask }
    ].filter((item) => item.id > 0 && item.id !== taskId);
    if (relatedIDs.length === 0) return;
    const relatedQuery: TaskDetailQuery = {
      itemPage: 1,
      itemPageSize: 1,
      filePage: 1,
      filePageSize: 1,
      eventPage: 1,
      eventPageSize: 1
    };
    void Promise.all(
      relatedIDs.map(async (item) => {
        const data = await getTaskDetail(item.id, relatedQuery);
        if (active) item.setter(data.task);
      })
    ).catch((error) => {
      if (active) message.warning(error instanceof Error ? error.message : "加载关联任务失败");
    });
    return () => {
      active = false;
    };
  }, [sourceTaskId, targetTaskId, taskId]);

  const handleCheck = async () => {
    setChecking(true);
    try {
      const data = await checkTaskResult(taskId);
      message.success(data.msg || `检查完成：文件 ${data.fileCount} 个，结果行 ${data.rowCount} 条`);
      await load();
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
              await load();
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
      await load();
    } catch (error) {
      message.error(error instanceof Error ? error.message : "重新回调失败");
    } finally {
      setRetryingCallback(false);
    }
  };

  const handleForceImportCallback = () => {
    Modal.confirm({
      title: "强制提交当前结果？",
      content: (
        <Space direction="vertical" size={10}>
          <Alert
            type="error"
            showIcon
            message="此操作只导入新文件或失败文件，已成功文件会跳过；结果存在风险时不会发送。无风险时按当前进度提交。"
          />
          <Typography.Text>
            回调只包含当前 parse_status=SUCCESS 的结果；未完成、失败或空答案 item 不会返回给上游。
          </Typography.Text>
          <Typography.Text type="secondary">
            任务状态和正常 callback 终态标记不会被修改，后续供应商文件导入仍可继续补齐结果。
          </Typography.Text>
        </Space>
      ),
      okText: "强制提交",
      cancelText: "取消",
      okButtonProps: { danger: true },
      onOk: async () => {
        setForcingImportCallback(true);
        try {
          const data = await forceImportCallback(taskId);
          if (data.rejected) {
            message.warning("结果存在风险、尚未校验或没有成功结果，已拒绝回调；请完成导入后查看风险提示。");
          } else if (data.delivered) {
            message.success(
              `强制回调已发送：成功 ${data.successCount}，未完成 ${data.unfinishedCount}${data.partial ? "（部分结果）" : ""}`
            );
          } else {
            message.error("强制回调失败，请查看回调记录");
          }
          await load();
        } catch (error) {
          message.error(error instanceof Error ? error.message : "强制回调失败");
        } finally {
          setForcingImportCallback(false);
        }
      }
    });
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

  const handleDeleteSubmitFailedTask = () => {
    if (!detail?.task) return;
    Modal.confirm({
      title: "删除提交失败任务？",
      content: `确认删除任务 ${detail.task.batchNo}？删除后将从后台列表隐藏，底层任务和关联数据会保留。`,
      okText: "删除",
      cancelText: "取消",
      okButtonProps: { danger: true },
      onOk: async () => {
        setDeleting(true);
        try {
          await deleteTask(taskId);
          message.success("任务已删除");
          navigate("/tasks", { replace: true });
        } catch (error) {
          message.error(error instanceof Error ? error.message : "删除任务失败");
        } finally {
          setDeleting(false);
        }
      }
    });
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
      { title: "质量校验", key: "validation", width: 190, render: (_, item) => <AnswerRiskTags item={item} /> },
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

  const handleForceRiskCallback = () => {
    if (!detail) return;
    Modal.confirm({
      title: "确认忽略本次风险并回调上游？",
      content: <Space direction="vertical"><TaskRiskSummary task={detail.task} /><Typography.Text>将发送当前已导入结果，保留风险标记。不会重新导入或剔除风险回答；发送失败后需再次手动操作。</Typography.Text></Space>,
      okText: "强制回调", okButtonProps: { danger: true },
      onOk: async () => {
        setForcingRiskCallback(true);
        try {
          const result = await retryTaskCallback(taskId, false, true);
          message[result.delivered ? "success" : "warning"](result.delivered ? "强制回调成功" : result.reason || "回调未成功，请查看回调记录");
          await load();
        } catch (error) { message.error(error instanceof Error ? error.message : "强制回调失败"); }
        finally { setForcingRiskCallback(false); }
      }
    });
  };

  const task = detail?.task;
  const renderRelatedBatchNo = (relatedTask: TaskSummary | null, relatedTaskId: number) => {
    if (!relatedTaskId) return "-";
    return (
      <Button className="related-batch-link" type="link" onClick={() => navigate(`/tasks/${relatedTaskId}`)}>
        <Typography.Text className="mono-text">{compactText(relatedTask?.batchNo ?? `#${relatedTaskId}`)}</Typography.Text>
      </Button>
    );
  };

  return (
    <section>
      <PageHeader
        title={task ? task.taskName || "-" : "任务详情"}
        description={task?.batchNo}
        extra={
          <Space>
            <Button icon={<ArrowLeft size={16} />} onClick={() => navigate("/tasks")}>
              返回
            </Button>
            <Button icon={<RefreshCw size={16} />} onClick={() => load()}>
              刷新
            </Button>
            <Button icon={<Send size={16} />} loading={retryingCallback} disabled={!canWrite} onClick={handleRetryCallback}>
              重新回调
            </Button>
            <Button
              danger
              type="primary"
              icon={<AlertTriangle size={16} />}
              loading={forcingImportCallback}
              disabled={!canWrite || task?.executionMode === "pull"}
              onClick={handleForceImportCallback}
            >
              强制导入回调
            </Button>
            {task?.submitFailed ? (
              <Button danger icon={<Trash2 size={16} />} loading={deleting} onClick={handleDeleteSubmitFailedTask}>
                删除
              </Button>
            ) : null}
            <Button icon={<RotateCcw size={16} />} loading={checking} disabled={task?.executionMode === "pull"} type="primary" onClick={handleCheck}>
              检查结果
            </Button>
          </Space>
        }
      />
      {task && task.riskCount > 0 && <Alert className="task-risk-alert" type="warning" showIcon
        message={task.status === "DONE" ? "任务已回调，结果风险标记保留" : "结果存在风险，回调遵循供应商风险拦截配置"}
        description={<TaskRiskSummary task={task} />}
        action={canWrite && <Button danger icon={<Send size={16} />} loading={forcingRiskCallback} disabled={!task.riskStatsUpdatedAt || checking || forcingImportCallback || task.status === "REASSIGNED"} onClick={handleForceRiskCallback}>强制回调</Button>}
      />}
      <div className="detail-summary">
        <Descriptions bordered column={{ xs: 1, sm: 2, lg: 4 }} size="small">
          <Descriptions.Item label="任务名称">
            <Space size={6} wrap>
              <span>{task?.taskName || "-"}</span>
              {sourceTaskId ? <Tag>转派</Tag> : null}
            </Space>
          </Descriptions.Item>
          <Descriptions.Item label="batchNo">
            <Typography.Text copyable={Boolean(task?.batchNo)} className="mono-text">
              {compactText(task?.batchNo ?? "") || "-"}
            </Typography.Text>
          </Descriptions.Item>
          <Descriptions.Item label="状态">{task ? <TaskStatusTag status={task.status} /> : "-"}</Descriptions.Item>
          {sourceTaskId ? (
            <Descriptions.Item label="来源 batchNo">
              {renderRelatedBatchNo(sourceTask, sourceTaskId)}
            </Descriptions.Item>
          ) : null}
          {task?.status === "REASSIGNED" ? (
            <Descriptions.Item label="转派到 batchNo">
              {renderRelatedBatchNo(targetTask, targetTaskId)}
            </Descriptions.Item>
          ) : null}
          <Descriptions.Item label="来源">{compactText(task?.source ?? "")}</Descriptions.Item>
          <Descriptions.Item label="设备 / 模式">{`${task?.device || "web"} / ${task?.executionMode || "push"}`}</Descriptions.Item>
          {task?.executionMode === "pull" && <Descriptions.Item label="分片 / 回传">{`分片完成 ${task.completedShardCount}/${task.shardCount}；待处理 ${task.pendingSubmissionCount}；处理失败 ${task.failedSubmissionCount}`}</Descriptions.Item>}
          <Descriptions.Item label="供应商">{task?.providerCode ?? "-"}</Descriptions.Item>
          <Descriptions.Item label="问答">{task ? `${task.completedCount}/${task.questionCount}` : "-"}</Descriptions.Item>
          <Descriptions.Item label="进度">
            {task ? (
              <TaskProgressBar
                successCount={task.successCount}
                failedCount={task.failedCount}
                totalCount={task.questionCount}
              />
            ) : (
              "-"
            )}
          </Descriptions.Item>
          <Descriptions.Item label="导入结果">
            {task ? (
              <TaskResultStats
                successCount={task.resultSuccessCount}
                failedCount={task.resultFailedCount}
                missingCount={task.resultMissingCount}
              />
            ) : (
              "-"
            )}
          </Descriptions.Item>
          <Descriptions.Item label="内容校验">{task ? <TaskRiskSummary task={task} /> : "—"}</Descriptions.Item>
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
                    void load({ itemPage: page, itemPageSize: pageSize });
                  }
                }}
                rowKey={(row) => `${row.taskItemId}-${row.seq}`}
                expandable={{
                  expandedRowRender: (item) => (
                    <div className="item-expanded">
                      <div>
                        <h3>质量校验</h3>
                        <AnswerRiskTags item={item} />
                        <div className="answer-risk-details">{item.riskItems?.map((risk) => <p key={risk.code}>{risk.message}{risk.code === "ANSWER_TOO_SHORT" ? `：${risk.actualLength} / ${risk.minLength} 字` : ""}{risk.matchedKeywords?.length ? `：${risk.matchedKeywords.join("、")}` : ""}</p>)}</div>
                        <Typography.Text type="secondary">规则版本 {item.validationRuleVersion || "—"} · {formatFullDateTime(item.validatedAt || "")}</Typography.Text>
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
                pagination={{
                  current: filePage,
                  pageSize: filePageSize,
                  total: detail?.fileTotal ?? 0,
                  showSizeChanger: true,
                  showTotal: (total) => `共 ${total} 条`,
                  onChange: (page, pageSize) => {
                    setFilePage(page);
                    setFilePageSize(pageSize);
                    void load({ filePage: page, filePageSize: pageSize });
                  }
                }}
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
                pagination={{
                  current: eventPage,
                  pageSize: eventPageSize,
                  total: detail?.eventTotal ?? 0,
                  showSizeChanger: true,
                  showTotal: (total) => `共 ${total} 条`,
                  onChange: (page, pageSize) => {
                    setEventPage(page);
                    setEventPageSize(pageSize);
                    void load({ eventPage: page, eventPageSize: pageSize });
                  }
                }}
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
