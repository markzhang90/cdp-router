import { clearSession, getCSRFToken } from "./session";
import type {
  ApiResponse,
  CheckResultData,
  DispatchOptionsData,
  ExportTaskItemsData,
  ForceImportCallbackData,
  LoginData,
  MeData,
  ProviderListData,
  ProviderMetricsData,
  ProviderMetricsQuery,
  PlatformUser,
  PlatformUserListData,
  PlatformUserListQuery,
  PlatformUserMetricsData,
  PlatformUserMetricsQuery,
  PlatformUserReconciliationExportQuery,
  PlatformUserPayload,
  ReassignTaskData,
  ReassignTaskPayload,
  RetryCallbackData,
  TaskDetailData,
  TaskDetailQuery,
  TaskListData,
  TaskListQuery
} from "../types/admin";

const API_BASE = "/api/admin/v1";

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers);
  const method = (options.method ?? "GET").toUpperCase();
  if (method !== "GET" && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (method !== "GET") {
    const csrf = getCSRFToken();
    if (csrf) headers.set("X-CSRF-Token", csrf);
  }
  const response = await fetch(`${API_BASE}${path}`, {
    ...options,
    method,
    headers,
    credentials: "include"
  });
  const body = (await response.json().catch(() => null)) as ApiResponse<T> | null;
  if (!response.ok || !body || body.status !== 0) {
    if (response.status === 401) clearSession();
    throw new ApiError(response.status, body?.message || response.statusText || "请求失败");
  }
  return body.data;
}

export async function login(username: string, password: string): Promise<LoginData> {
  return request<LoginData>("/login", {
    method: "POST",
    body: JSON.stringify({ username, password })
  });
}

export async function logout(): Promise<void> {
  await request<{ ok: boolean }>("/logout", { method: "POST", body: "{}" });
  clearSession();
}

export function getMe(): Promise<MeData> {
  return request<MeData>("/me");
}

export function listTasks(query: TaskListQuery): Promise<TaskListData> {
  const params = new URLSearchParams();
  Object.entries(query).forEach(([key, value]) => {
    if (value !== undefined && value !== "") params.set(key, String(value));
  });
  return request<TaskListData>(`/tasks?${params.toString()}`);
}

export function getTaskDetail(id: number, query: TaskDetailQuery = {}): Promise<TaskDetailData> {
  const params = new URLSearchParams();
  Object.entries(query).forEach(([key, value]) => {
    if (value !== undefined && value !== "") params.set(key, String(value));
  });
  const search = params.toString();
  return request<TaskDetailData>(`/tasks/${id}${search ? `?${search}` : ""}`);
}

export function exportTaskItemsXlsx(id: number): Promise<ExportTaskItemsData> {
  return request<ExportTaskItemsData>(`/tasks/${id}/items-export`, {
    method: "POST",
    body: JSON.stringify({})
  });
}

export function checkTaskResult(id: number): Promise<CheckResultData> {
  return request<CheckResultData>(`/tasks/${id}/check-result`, {
    method: "POST",
    body: JSON.stringify({})
  });
}

export function retryTaskCallback(id: number, force = false): Promise<RetryCallbackData> {
  return request<RetryCallbackData>(`/tasks/${id}/retry-callback`, {
    method: "POST",
    body: JSON.stringify({ force })
  });
}

export function forceImportCallback(id: number, reason?: string): Promise<ForceImportCallbackData> {
  return request<ForceImportCallbackData>(`/tasks/${id}/force-import-callback`, {
    method: "POST",
    body: JSON.stringify({ reason })
  });
}

export function reassignTask(payload: ReassignTaskPayload): Promise<ReassignTaskData> {
  return request<ReassignTaskData>("/tasks/reassign", {
    method: "POST",
    body: JSON.stringify(payload)
  });
}

export async function deleteTask(id: number): Promise<void> {
  await request<{ ok: boolean }>(`/tasks/${id}`, {
    method: "DELETE"
  });
}

export function listProviders(): Promise<ProviderListData> {
  return request<ProviderListData>("/providers");
}

export function getDispatchOptions(): Promise<DispatchOptionsData> {
  return request<DispatchOptionsData>("/dispatch-options");
}

export function getProviderMetrics(providerCode: string, query: ProviderMetricsQuery): Promise<ProviderMetricsData> {
  const params = new URLSearchParams();
  Object.entries(query).forEach(([key, value]) => {
    if (value !== undefined && value !== "") params.set(key, String(value));
  });
  const search = params.toString();
  return request<ProviderMetricsData>(`/providers/${encodeURIComponent(providerCode)}/metrics${search ? `?${search}` : ""}`);
}

export function listPlatformUsers(query: PlatformUserListQuery): Promise<PlatformUserListData> {
  const params = new URLSearchParams();
  Object.entries(query).forEach(([key, value]) => {
    if (value !== undefined && value !== "") params.set(key, String(value));
  });
  return request<PlatformUserListData>(`/platform-users?${params.toString()}`);
}

export function getPlatformUser(id: number): Promise<PlatformUser> {
  return request<PlatformUser>(`/platform-users/${id}`);
}

export function getPlatformUserMetrics(id: number, query: PlatformUserMetricsQuery): Promise<PlatformUserMetricsData> {
  const params = new URLSearchParams();
  Object.entries(query).forEach(([key, value]) => {
    if (value !== undefined && value !== "") params.set(key, String(value));
  });
  const search = params.toString();
  return request<PlatformUserMetricsData>(`/platform-users/${id}/metrics${search ? `?${search}` : ""}`);
}

export function exportPlatformUserReconciliationXlsx(
  id: number,
  query: PlatformUserReconciliationExportQuery
): Promise<ExportTaskItemsData> {
  return request<ExportTaskItemsData>(`/platform-users/${id}/reconciliation-export`, {
    method: "POST",
    body: JSON.stringify(query)
  });
}

export function createPlatformUser(payload: PlatformUserPayload): Promise<PlatformUser> {
  return request<PlatformUser>("/platform-users", {
    method: "POST",
    body: JSON.stringify(payload)
  });
}

export function updatePlatformUser(id: number, payload: PlatformUserPayload): Promise<PlatformUser> {
  return request<PlatformUser>(`/platform-users/${id}`, {
    method: "PUT",
    body: JSON.stringify(payload)
  });
}
