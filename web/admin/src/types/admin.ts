export interface ApiResponse<T> {
  status: number;
  message: string;
  data: T;
}

export interface AdminUser {
  userId: string;
  username: string;
  role: string;
}

export interface LoginData {
  user: AdminUser;
  csrfToken: string;
  expiresAt: string;
}

export interface MeData {
  user: AdminUser;
  csrfToken: string;
}

export interface TaskSummary {
  device: string;
  executionMode: string;
  shardCount: number;
  completedShardCount: number;
  pendingSubmissionCount: number;
  failedSubmissionCount: number;
  riskCount: number;
  invalidAnswerCount: number;
  riskSummaryJson: string;
  riskStatsUpdatedAt: string;
  taskId: number;
  taskName: string;
  batchNo: string;
  source: string;
  platformCode: string;
  externalBatchId: string;
  providerCode: string;
  status: string;
  questionCount: number;
  completedCount: number;
  successCount: number;
  failedCount: number;
  resultSuccessCount: number;
  resultFailedCount: number;
  resultMissingCount: number;
  progress: number;
  createdAt: string;
  updatedAt: string;
  executionDurationSeconds: number;
  executionDurationText: string;
  submitFailed: boolean;
  dispatchLabels: string[];
  reassignedFromTaskId: number | string;
  reassignedToTaskId: number | string;
}

export interface TaskListData {
  tasks: TaskSummary[];
  total: number;
  page: number;
  pageSize: number;
}

export interface TaskListQuery {
  hasRisk?: boolean;
  taskName?: string;
  batchNo?: string;
  source?: string;
  platformCode?: string;
  providerCode?: string;
  resultStatus?: string;
  createdFrom?: string;
  createdTo?: string;
  page?: number;
  pageSize?: number;
}

export interface ProductCard {
  productId: string;
  productName: string;
  price: string;
  shopName: string;
  shopId: string;
  sellingPoints: string;
  imageUrl: string;
  salesVolume: string;
}

export interface TaskItem {
  productCards?: ProductCard[];
  validationStatus: string;
  riskItems: AnswerRisk[];
  validationRuleVersion: string;
  validatedAt: string;
  taskItemId: number;
  itemId: string;
  platformCode: string;
  seq: number;
  question: string;
  providerStatus: number;
  resultStatus: string;
  answerText: string;
  thinkingProcess: string;
  generatedAt: string;
  generatedAtText: string;
  recommendedQuestionsJson: string;
  deviceId: string;
  recordingUrl: string;
  referencesJson: string;
  referenceCount: number;
  parseStatus: string;
  parseError: string;
  resultImportedAt: string;
}

export interface ResultFile {
  id: number;
  sourceType: string;
  objectKey: string;
  fileUrl: string;
  fileName: string;
  parseStatus: string;
  rowCount: number;
  validRowCount: number;
  invalidRowCount: number;
  createdAt: string;
}

export interface TaskEvent {
  id: number;
  eventType: string;
  payloadJson: string;
  createdAt: string;
}

export interface TaskCallback {
  id: number;
  targetUrl: string;
  requestPayloadJson: string;
  responseStatus: number;
  responseBody: string;
  status: string;
  retryCount: number;
  lastError: string;
  createdAt: string;
  updatedAt: string;
}

export interface TaskDetailData {
  task: TaskSummary;
  items: TaskItem[];
  itemTotal: number;
  itemPage: number;
  itemPageSize: number;
  files: ResultFile[];
  fileTotal: number;
  filePage: number;
  filePageSize: number;
  events: TaskEvent[];
  eventTotal: number;
  eventPage: number;
  eventPageSize: number;
  callbacks: TaskCallback[];
}

export interface TaskDetailQuery {
  itemPage?: number;
  itemPageSize?: number;
  filePage?: number;
  filePageSize?: number;
  eventPage?: number;
  eventPageSize?: number;
}

export interface ExportTaskItemsData {
  downloadUrl: string;
  objectKey: string;
  expiresAt: string;
}

export interface CheckResultData {
  taskId: number;
  batchNo: string;
  taskStatus: string;
  providerDone: boolean;
  fileCount: number;
  rowCount: number;
  msg: string;
}

export interface RetryCallbackData {
  riskCount: number;
  riskStatsReady: boolean;
  taskId: number;
  batchNo: string;
  taskStatus: string;
  delivered: boolean;
  ready: boolean;
  forceRequired: boolean;
  reason: string;
  totalCount: number;
  successCount: number;
  failedCount: number;
  importedCount: number;
  taskItemCount: number;
}

export interface ForceImportCallbackData {
  taskId: number;
  batchNo: string;
  taskStatus: string;
  delivered: boolean;
  rejected: boolean;
  fileCount: number;
  importedFileCount: number;
  rowCount: number;
  taskItemCount: number;
  successCount: number;
  failedCount: number;
  unfinishedCount: number;
  callbackMode: string;
  partial: boolean;
  taskStatusUnchanged: boolean;
  reason: string;
}

export interface ReassignTaskPayload {
  batchNo: string;
  providerCode: string;
  reason?: string;
}

export interface ReassignTaskData {
  sourceTaskId: number;
  targetTaskId: number;
  originalBatchNo: string;
  targetBatchNo: string;
  providerCode: string;
  taskStatus: string;
  acceptedCount: number;
}

export interface Provider {
  interactionMode: "push" | "pull";
  supportedDevices: string[];
  providerCode: string;
  displayName: string;
  companyName: string;
  settlementStartAt: string;
  enabled: boolean;
  resultMode: string;
  timeoutSeconds: number;
  qpsLimit: number;
  concurrencyLimit: number;
  bucketPathPrefix: string;
  submitUrlConfigured: boolean;
  statusUrlConfigured: boolean;
  filesUrlConfigured: boolean;
  dailyQuestionQuota: number;
  dailyQuestionUsed: number;
  dailyQuestionRemaining: number;
  dailyQuestionUnlimited: boolean;
}

export interface ProviderListData {
  providers: Provider[];
}

export interface DispatchStrategyOption {
  code: string;
  name: string;
}

export interface DispatchOptionsData {
  strategies: DispatchStrategyOption[];
  providers: Provider[];
}

export interface ProviderMetricsQuery {
  createdFrom?: string;
  createdTo?: string;
}

export interface ProviderMetricsSummary {
  taskCount: number;
  questionCount: number;
  successAnswerCount: number;
  failedAnswerCount: number;
  answerSuccessRate: number;
}

export interface ProviderMetricsData {
  provider: Provider;
  createdFrom: string;
  createdTo: string;
  summary: ProviderMetricsSummary;
  dailyMetrics: ProviderDailyMetric[];
}

export interface PlatformUser {
  id: number;
  platformCode: string;
  name: string;
  apiKey: string;
  status: string;
  callbackMode: string;
  callbackUrl: string;
  dispatchProfile?: DispatchProfile;
  createdAt: string;
  updatedAt: string;
}

export interface DispatchProfile extends DispatchMode {
  id: number;
  code: string;
  name: string;
  pull?: DispatchMode;
}

export interface DispatchMode {
  strategy: string;
  enabled: boolean;
  cursor: number;
  lastProviderCode: string;
  providerCodes: string[];
  fallbackSelectionPolicy?: string;
  rules: DispatchRule[];
}

export interface DispatchRule {
  code: string;
  name: string;
  sortOrder: number;
  taskNameRegex: string;
  labels: string[];
  providerCodes: string[];
  enabled: boolean;
}

export interface PlatformUserListData {
  users: PlatformUser[];
  total: number;
  page: number;
  pageSize: number;
}

export interface PlatformUserListQuery {
  keyword?: string;
  status?: string;
  page?: number;
  pageSize?: number;
}

export interface PlatformUserMetricsQuery {
  createdFrom?: string;
  createdTo?: string;
}

export interface PlatformUserReconciliationExportQuery {
  createdFrom?: string;
  createdTo?: string;
}

export interface PlatformTaskMetrics {
  submittedTaskCount: number;
  completedTaskCount: number;
  processingTaskCount: number;
  failedTaskCount: number;
  taskSuccessRate: number;
  averageDurationSeconds: number;
  averageDurationText: string;
  longestProcessingSeconds: number;
  longestProcessingText: string;
}

export interface PlatformQuestionMetrics {
  submittedQuestionCount: number;
  completedAnswerCount: number;
  providerDoneQuestionCount: number;
  failedQuestionCount: number;
  answerCompletionRate: number;
  averageQuestionsPerTask: number;
  activeQuestionPlatformCount: number;
}

export interface PlatformResultMetrics {
  resultFileCount: number;
  parsedFileCount: number;
  parseFailedFileCount: number;
  rowCount: number;
  validRowCount: number;
  invalidRowCount: number;
  validRowRate: number;
}

export interface PlatformCallbackMetrics {
  callbackCount: number;
  callbackSuccessCount: number;
  callbackFailedCount: number;
  callbackSuccessRate: number;
}

export interface MetricBreakdown {
  label: string;
  value: number;
  ratio: number;
}

export interface ProviderDailyMetric {
  metricDate: string;
  providerCode: string;
  taskCount: number;
  questionCount: number;
  successAnswerCount: number;
  failedAnswerCount: number;
}

export interface PlatformUserMetricsData {
  user: PlatformUser;
  createdFrom: string;
  createdTo: string;
  taskMetrics: PlatformTaskMetrics;
  questionMetrics: PlatformQuestionMetrics;
  resultMetrics: PlatformResultMetrics;
  callbackMetrics: PlatformCallbackMetrics;
  statusBreakdown: MetricBreakdown[];
  failureReasons: MetricBreakdown[];
  recentTasks: TaskSummary[];
  providerDailyMetrics: ProviderDailyMetric[];
}

export interface PlatformUserPayload {
  platformCode: string;
  name: string;
  status?: string;
  callbackMode?: string;
  callbackUrl?: string;
  dispatchProfile: DispatchProfilePayload;
}

export interface DispatchProfilePayload extends DispatchModePayload {
  code?: string;
  name?: string;
  pull?: DispatchModePayload;
}

export interface DispatchModePayload {
  strategy: string;
  enabled: boolean;
  providerCodes: string[];
  fallbackSelectionPolicy?: string;
  rules?: DispatchRulePayload[];
}

export interface DispatchRulePayload {
  code: string;
  name?: string;
  sortOrder?: number;
  taskNameRegex: string;
  labels?: string[];
  providerCodes: string[];
  enabled: boolean;
}

export interface AnswerRisk {
  code: string;
  message: string;
  actualLength: number;
  minLength: number;
  matchedKeywords: string[];
}
export interface ValidationRules {
  riskCallbackAllowedProviderCodes: string[];
  version: string;
  keywords: string[];
  updatedAt: string;
  updatedBy: string;
  refreshIntervalSeconds: number;
}
