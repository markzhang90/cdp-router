import { useEffect, useMemo, useState } from "react";
import { Button, DatePicker, Form, Input, Modal, Select, Space, Table, Tag, Tooltip, Typography, message } from "antd";
import type { ColumnsType } from "antd/es/table";
import dayjs from "dayjs";
import type { Dayjs } from "dayjs";
import { Copy, ExternalLink, GitBranch, RefreshCw, RotateCcw, Search, Trash2 } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { checkTaskResult, deleteTask, listPlatformUsers, listProviders, listTasks, reassignTask } from "../api/client";
import { PageHeader } from "../components/PageHeader";
import { TaskStatusTag } from "../components/StatusTag";
import { TaskProgressBar } from "../components/TaskProgressBar";
import { TaskResultStats } from "../components/TaskResultStats";
import type { PlatformUser, Provider, TaskListQuery, TaskSummary } from "../types/admin";
import { compactText, formatDateTime, positiveID } from "../utils/format";

const { RangePicker } = DatePicker;
const CLOSED_REASSIGN_STATUSES = new Set(["CALLBACK_PENDING", "DONE", "REASSIGNED"]);
const TASK_LIST_FILTER_CACHE_KEY = "cdp-router:admin:task-list-filter:v1";

type TaskListFormValues = {
  taskName?: string;
  batchNo?: string;
  platformCode?: string;
  providerCode?: string;
  resultStatus?: string;
  createdRange?: [Dayjs, Dayjs];
};

function cleanCachedText(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function cleanCachedNumber(value: unknown, fallback: number): number {
  if (typeof value !== "number" || !Number.isFinite(value)) return fallback;
  return value > 0 ? Math.floor(value) : fallback;
}

function readCachedTaskListQuery(): TaskListQuery {
  try {
    const raw = window.localStorage.getItem(TASK_LIST_FILTER_CACHE_KEY);
    if (!raw) return { page: 1, pageSize: 20 };
    const value = JSON.parse(raw) as Record<string, unknown>;
    return {
      taskName: cleanCachedText(value.taskName),
      batchNo: cleanCachedText(value.batchNo),
      source: cleanCachedText(value.source),
      platformCode: cleanCachedText(value.platformCode),
      providerCode: cleanCachedText(value.providerCode),
      resultStatus: cleanCachedText(value.resultStatus),
      createdFrom: cleanCachedText(value.createdFrom),
      createdTo: cleanCachedText(value.createdTo),
      page: cleanCachedNumber(value.page, 1),
      pageSize: cleanCachedNumber(value.pageSize, 20)
    };
  } catch {
    return { page: 1, pageSize: 20 };
  }
}

function cacheTaskListQuery(query: TaskListQuery) {
  try {
    window.localStorage.setItem(TASK_LIST_FILTER_CACHE_KEY, JSON.stringify(query));
  } catch {
    // localStorage can be unavailable in private browsing; filtering should still work.
  }
}

function taskListFormValues(query: TaskListQuery): TaskListFormValues {
  const createdFrom = query.createdFrom ? dayjs(query.createdFrom) : null;
  const createdTo = query.createdTo ? dayjs(query.createdTo) : null;
  return {
    taskName: query.taskName,
    batchNo: query.batchNo,
    platformCode: query.platformCode,
    providerCode: query.providerCode,
    resultStatus: query.resultStatus,
    createdRange: createdFrom?.isValid() && createdTo?.isValid() ? [createdFrom, createdTo] : undefined
  };
}

function DispatchLabelPill({ label }: { label: string }) {
  const normalized = label.trim().toLowerCase();
  const tone = normalized === "fallback" ? "fallback" : "rule";

  return (
    <span className={`dispatch-label-pill dispatch-label-pill--${tone}`} title={label}>
      <span className="dispatch-label-pill__text">{label}</span>
    </span>
  );
}

function canReassignTask(task: TaskSummary): boolean {
  return !CLOSED_REASSIGN_STATUSES.has(task.status?.trim().toUpperCase() ?? "");
}

function isReassignedTask(task: TaskSummary): boolean {
  return positiveID(task.reassignedFromTaskId) > 0;
}

export function TaskListPage() {
  const navigate = useNavigate();
  const [form] = Form.useForm<TaskListFormValues>();
  const [reassignForm] = Form.useForm<{ providerCode: string; reason?: string }>();
  const [loading, setLoading] = useState(false);
  const [checkingId, setCheckingId] = useState<number | null>(null);
  const [deletingId, setDeletingId] = useState<number | null>(null);
  const [reassigningId, setReassigningId] = useState<number | null>(null);
  const [reassignTaskRow, setReassignTaskRow] = useState<TaskSummary | null>(null);
  const [query, setQuery] = useState<TaskListQuery>(() => readCachedTaskListQuery());
  const [rows, setRows] = useState<TaskSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [platformUsers, setPlatformUsers] = useState<PlatformUser[]>([]);
  const [platformLoading, setPlatformLoading] = useState(false);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [providerLoading, setProviderLoading] = useState(false);

  const load = async (nextQuery = query) => {
    setLoading(true);
    try {
      const data = await listTasks(nextQuery);
      setRows(data.tasks);
      setTotal(data.total);
      const loadedQuery = { ...nextQuery, page: data.page, pageSize: data.pageSize };
      setQuery(loadedQuery);
      cacheTaskListQuery(loadedQuery);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "加载任务失败");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    form.setFieldsValue(taskListFormValues(query));
    void load(query);
    void loadPlatformUsers();
    void loadProviders();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const loadPlatformUsers = async (keyword = "") => {
    setPlatformLoading(true);
    try {
      const data = await listPlatformUsers({ keyword, status: "ACTIVE", page: 1, pageSize: 50 });
      setPlatformUsers(data.users);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "加载接入方失败");
    } finally {
      setPlatformLoading(false);
    }
  };

  const handleSearch = () => {
    const values = form.getFieldsValue();
    const range = values.createdRange as [unknown, unknown] | undefined;
    const nextQuery: TaskListQuery = {
      taskName: values.taskName,
      batchNo: values.batchNo,
      platformCode: values.platformCode,
      providerCode: values.providerCode,
      resultStatus: values.resultStatus,
      createdFrom: range?.[0] ? String((range[0] as { toISOString: () => string }).toISOString()) : undefined,
      createdTo: range?.[1] ? String((range[1] as { toISOString: () => string }).toISOString()) : undefined,
      page: 1,
      pageSize: query.pageSize
    };
    void load(nextQuery);
  };

  const handleCheck = async (task: TaskSummary) => {
    setCheckingId(task.taskId);
    try {
      const data = await checkTaskResult(task.taskId);
      message.success(data.msg || `检查完成：文件 ${data.fileCount} 个，结果行 ${data.rowCount} 条`);
      void load(query);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "检查结果失败");
    } finally {
      setCheckingId(null);
    }
  };

  const handleDeleteSubmitFailedTask = (task: TaskSummary) => {
    Modal.confirm({
      title: "删除提交失败任务？",
      content: `确认删除任务 ${task.batchNo}？删除后将从后台列表隐藏，底层任务和关联数据会保留。`,
      okText: "删除",
      cancelText: "取消",
      okButtonProps: { danger: true },
      onOk: async () => {
        setDeletingId(task.taskId);
        try {
          await deleteTask(task.taskId);
          message.success("任务已删除");
          await load(query);
        } catch (error) {
          message.error(error instanceof Error ? error.message : "删除任务失败");
        } finally {
          setDeletingId(null);
        }
      }
    });
  };

  const loadProviders = async () => {
    setProviderLoading(true);
    try {
      const data = await listProviders();
      setProviders(data.providers);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "加载供应商失败");
    } finally {
      setProviderLoading(false);
    }
  };

  const handleOpenReassign = (task: TaskSummary) => {
    setReassignTaskRow(task);
    reassignForm.resetFields();
    void loadProviders();
  };

  const handleConfirmReassign = async () => {
    if (!reassignTaskRow) return;
    const values = await reassignForm.validateFields();
    Modal.confirm({
      title: "确认转派任务？",
      content: `确认将任务 ${reassignTaskRow.batchNo} 转派给 ${values.providerCode}？`,
      okText: "确认转派",
      cancelText: "取消",
      onOk: async () => {
        setReassigningId(reassignTaskRow.taskId);
        try {
          const data = await reassignTask({
            batchNo: reassignTaskRow.batchNo,
            providerCode: values.providerCode,
            reason: values.reason
          });
          message.success(`任务已转派到 ${data.providerCode}`);
          setReassignTaskRow(null);
          await load(query);
          navigate(`/tasks/${data.targetTaskId}`);
        } catch (error) {
          message.error(error instanceof Error ? error.message : "转派失败");
        } finally {
          setReassigningId(null);
        }
      }
    });
  };

  const handleCopyBatchNo = async (batchNo: string) => {
    try {
      await navigator.clipboard.writeText(batchNo);
      message.success("batchNo 已复制");
    } catch {
      message.error("复制 batchNo 失败");
    }
  };

  const columns = useMemo<ColumnsType<TaskSummary>>(
    () => [
      {
        title: "任务名称",
        dataIndex: "taskName",
        fixed: "left",
        width: 240,
        render: (_, task) => (
          <div className="task-name-cell">
            <Button type="link" className="table-link" onClick={() => navigate(`/tasks/${task.taskId}`)}>
              {task.taskName || "-"}
            </Button>
            {isReassignedTask(task) ? <Tag>转派</Tag> : null}
          </div>
        )
      },
      {
        title: "batchNo",
        dataIndex: "batchNo",
        width: 280,
        render: (value: string) => (
          <div className="batch-no-cell">
            <Typography.Text className="mono-text">{compactText(value)}</Typography.Text>
            <Tooltip title="复制 batchNo">
              <Button
                aria-label="复制 batchNo"
                className="task-name-copy"
                icon={<Copy size={14} />}
                size="small"
                type="text"
                onClick={() => handleCopyBatchNo(value)}
              />
            </Tooltip>
          </div>
        )
      },
      {
        title: "externalBatchId",
        dataIndex: "externalBatchId",
        width: 260,
        render: (value: string) => (
          <Typography.Text copyable={Boolean(value)} className="mono-text">
            {compactText(value)}
          </Typography.Text>
        )
      },
      {
        title: "来源",
        dataIndex: "source",
        width: 150,
        render: (value: string) => compactText(value)
      },
      {
        title: "接入方",
        dataIndex: "platformCode",
        width: 130,
        render: (value: string) => compactText(value)
      },
      {
        title: "问答",
        width: 110,
        render: (_, task) => `${task.completedCount}/${task.questionCount}`
      },
      {
        title: "进度",
        width: 170,
        render: (_, task) => (
          <TaskProgressBar
            compact
            successCount={task.successCount}
            failedCount={task.failedCount}
            totalCount={task.questionCount}
          />
        )
      },
      {
        title: "导入结果",
        width: 190,
        render: (_, task) => (
          <TaskResultStats
            compact
            successCount={task.resultSuccessCount}
            failedCount={task.resultFailedCount}
            missingCount={task.resultMissingCount}
          />
        )
      },
      {
        title: "供应商",
        dataIndex: "providerCode",
        width: 190,
        render: (value: string, task) => (
          <Space size={[4, 4]} wrap className="provider-label-cell">
            <Typography.Text>{value || "-"}</Typography.Text>
            {(task.dispatchLabels ?? []).map((label) => (
              <DispatchLabelPill key={label} label={label} />
            ))}
          </Space>
        )
      },
      {
        title: "状态",
        dataIndex: "status",
        width: 150,
        render: (status: string) => <TaskStatusTag status={status} />
      },
      {
        title: "执行耗时",
        dataIndex: "executionDurationText",
        width: 130,
        render: (value: string) => compactText(value)
      },
      {
        title: "创建时间",
        dataIndex: "createdAt",
        width: 150,
        render: formatDateTime
      },
      {
        title: "更新时间",
        dataIndex: "updatedAt",
        width: 150,
        render: formatDateTime
      },
      {
        title: "操作",
        fixed: "right",
        width: 212,
        render: (_, task) => (
          <Space>
            <Tooltip title="检查结果">
              <Button
                icon={<RotateCcw size={16} />}
                loading={checkingId === task.taskId}
                onClick={() => handleCheck(task)}
              />
            </Tooltip>
            {(() => {
              const reassignedToTaskId = positiveID(task.reassignedToTaskId);
              if (task.status === "REASSIGNED") {
                return (
                  <Tooltip title={reassignedToTaskId ? "查看转派任务" : "未找到转派任务"}>
                    <Button
                      disabled={!reassignedToTaskId}
                      icon={<ExternalLink size={16} />}
                      onClick={() => navigate(`/tasks/${reassignedToTaskId}`)}
                    />
                  </Tooltip>
                );
              }
              if (!canReassignTask(task)) return null;
              return (
                <Tooltip title="转派">
                  <Button
                    icon={<GitBranch size={16} />}
                    loading={reassigningId === task.taskId}
                    onClick={() => handleOpenReassign(task)}
                  />
                </Tooltip>
              );
            })()}
            {task.submitFailed ? (
              <Tooltip title="删除提交失败任务">
                <Button
                  danger
                  icon={<Trash2 size={16} />}
                  loading={deletingId === task.taskId}
                  onClick={() => handleDeleteSubmitFailedTask(task)}
                />
              </Tooltip>
            ) : null}
          </Space>
        )
      }
    ],
    [checkingId, deletingId, navigate, query, reassigningId]
  );

  const reassignProviderOptions = providers
    .filter((provider) => provider.enabled && provider.submitUrlConfigured && provider.providerCode !== reassignTaskRow?.providerCode)
    .map((provider) => ({
      label: `${provider.providerCode}${provider.displayName ? ` · ${provider.displayName}` : ""}`,
      value: provider.providerCode
    }));

  const providerOptions = providers.map((provider) => ({
    label: `${provider.providerCode}${provider.displayName ? ` · ${provider.displayName}` : ""}`,
    value: provider.providerCode
  }));

  return (
    <section>
      <PageHeader
        title="任务列表"
        description="按批次、接入方和执行状态定位任务，查看供应商结果导入情况。"
        extra={
          <Button icon={<RefreshCw size={16} />} onClick={() => load(query)}>
            刷新
          </Button>
        }
      />
      <div className="filter-bar">
        <Form form={form} layout="inline">
          <Form.Item name="taskName">
            <Input allowClear placeholder="任务名称" prefix={<Search size={15} />} />
          </Form.Item>
          <Form.Item name="batchNo">
            <Input allowClear placeholder="batchNo" prefix={<Search size={15} />} />
          </Form.Item>
          <Form.Item name="platformCode">
            <Select
              allowClear
              showSearch
              filterOption={false}
              loading={platformLoading}
              optionFilterProp="label"
              placeholder="接入方"
              style={{ width: 180 }}
              onFocus={() => loadPlatformUsers()}
              onSearch={(value) => loadPlatformUsers(value)}
              options={platformUsers.map((user) => ({
                label: `${user.platformCode} · ${user.name}`,
                value: user.platformCode
              }))}
            />
          </Form.Item>
          <Form.Item name="providerCode">
            <Select
              allowClear
              showSearch
              loading={providerLoading}
              optionFilterProp="label"
              placeholder="执行供应商"
              style={{ width: 180 }}
              onFocus={() => loadProviders()}
              options={providerOptions}
            />
          </Form.Item>
          <Form.Item name="resultStatus">
            <Select
              allowClear
              placeholder="执行结果"
              style={{ width: 150 }}
              options={[
                { label: "处理中", value: "PROCESSING" },
                { label: "已完成", value: "DONE" },
                { label: "失败", value: "FAILED" },
                { label: "已提交", value: "SUBMITTED" },
                { label: "待回调", value: "CALLBACK_PENDING" }
              ]}
            />
          </Form.Item>
          <Form.Item name="createdRange">
            <RangePicker showTime />
          </Form.Item>
          <Form.Item>
            <Button type="primary" icon={<Search size={16} />} onClick={handleSearch}>
              查询
            </Button>
          </Form.Item>
        </Form>
      </div>
      <Table
        className="task-table"
        columns={columns}
        dataSource={rows}
        loading={loading}
        pagination={{
          current: query.page,
          pageSize: query.pageSize,
          total,
          showSizeChanger: true,
          onChange: (page, pageSize) => load({ ...query, page, pageSize })
        }}
        rowKey="taskId"
        scroll={{ x: 1960 }}
      />
      <Modal
        title="任务转派"
        open={Boolean(reassignTaskRow)}
        okText="转派"
        cancelText="取消"
        confirmLoading={reassigningId === reassignTaskRow?.taskId}
        onCancel={() => setReassignTaskRow(null)}
        onOk={() => void handleConfirmReassign()}
      >
        <Form form={reassignForm} layout="vertical">
          <Form.Item label="原 batchNo">
            <Typography.Text className="mono-text">{reassignTaskRow?.batchNo ?? "-"}</Typography.Text>
          </Form.Item>
          <Form.Item
            label="目标供应商"
            name="providerCode"
            rules={[{ required: true, message: "请选择目标供应商" }]}
          >
            <Select
              showSearch
              loading={providerLoading}
              optionFilterProp="label"
              placeholder="选择启用的供应商"
              options={reassignProviderOptions}
            />
          </Form.Item>
          <Form.Item label="转派原因" name="reason">
            <Input.TextArea rows={3} maxLength={200} showCount placeholder="可选" />
          </Form.Item>
        </Form>
      </Modal>
    </section>
  );
}
