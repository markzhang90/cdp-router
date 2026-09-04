# AGENTS.md

## 项目定位

本项目是 `cdp-router`，使用 Go 搭建 CDP/AI 任务业务网关。第一阶段重点能力是任务代理、任务记录、高频轮询下游任务状态、数据清洗、高并发 HTTP 网关代理，以及少量内置后台管理页面。

详细技术方案见 `docs/gateway-architecture.md`。实现时以该文档为架构事实来源；如果代码设计和文档冲突，先更新文档再实现。

## 总体架构规范

- 使用 Go 1.22+。
- HTTP 服务使用 Gin。
- 当前阶段不开放、不注册 gRPC server，也不使用 grpc-gateway 作为运行时链路。
- protobuf 是对外 API 契约事实来源，用于维护 request、response、enum、service、HTTP annotation 和 OpenAPI 生成。
- OpenAPI 必须从 proto 自动生成，并通过 `/api/openapi.json` 暴露。
- 内置后台页面源码放在仓库根目录 `web/admin`，与 Go 服务目录 `cdp-router` 同级维护，使用 Vite + React + TypeScript 构建；构建产物由 Go 服务通过 `/admin/*` 托管。
- 服务整体链路遵循：`internal/httpapi -> internal/service -> internal/biz -> internal/data`。
- 任务类接口不能纯透传，必须落任务记录、状态和事件，保证幂等、轮询兜底、失败重试和结果清洗。

## 环境操作规范

- 测试环境默认指本地环境，包括 `127.0.0.1`、`localhost`、本机启动的 Gin/Vite/mock provider、以及本地 Postgres/Redis/OSS mock 配置。
- 正式环境指公网域名、线上服务器、线上数据库、线上 OSS、真实供应商接口、真实业务回调地址和任何会影响线上业务数据的环境，例如 `https://openapi.mainland.vip`。
- 未经用户明确允许，所有接口调用、任务创建、数据写入、数据删除、后台配置修改、脚本执行和联调验证都必须默认只在测试环境执行。
- 任何可能访问正式环境的操作，执行前必须显式说明目标环境、base URL、账号/接入方、影响范围和将执行的动作，并等待用户明确授权。
- 不得根据脚本名称、变量名、历史文档或上下文中的 `dev/test` 字样推断环境；必须以实际 base URL、数据库连接串、配置文件和用户明确说明为准。
- 如果用户说“测试环境”但没有给出更多说明，本项目中默认解释为本地环境；不得自动切换到公网测试域名或线上域名。
- 对正式环境的删除、批量写入、任务下发、配置变更等高风险操作，必须先提供可审阅的 SQL、curl 或操作清单；用户确认后才能执行。

## 目录职责

```text
cmd/<service>/main.go       进程启动、依赖装配、优雅关闭
configs/                    配置示例
docs/                       架构、接口、部署和运维文档
api/<module>/v1/*.proto     API 契约事实来源
openapi/                    proto 生成的 OpenAPI 文件
web/admin/                  内置后台管理页面源码，和 cdp-router 服务目录同级
internal/config/            配置结构、默认值、校验
internal/httpapi/           Gin server、路由、中间件、统一响应、静态资源托管
internal/service/           HTTP/proto 协议转换、调用 usecase
internal/biz/               业务实体、用例、状态机、仓储接口
internal/data/              DB/Redis/OSS/外部服务仓储实现
internal/worker/            submitter、poller、cleaner、scheduler、retry
internal/provider/          下游 provider adapter 和 registry
internal/cleaner/           Excel/JSON 清洗、校验、去重、聚合
internal/proxy/             高并发代理、transport、限流、熔断
internal/pkg/               项目通用基础库
migrations/                 数据库迁移
scripts/check.sh            本地统一校验入口
```

## 分层代码规范

### cmd

- 只负责配置加载、日志初始化、依赖装配、HTTP server 启动、worker 启动和优雅关闭。
- 不写业务规则、SQL、HTTP handler 细节或第三方接口调用细节。

### internal/httpapi

- 负责 Gin engine、路由注册、中间件、统一错误响应、OpenAPI 和 Admin 静态资源托管。
- 中间件应覆盖 request id、recover、日志、CORS、鉴权、限流、请求体大小限制和超时。
- 不直接写业务规则，不直接访问数据库，不直接调用下游 provider。

### internal/service

- 负责把 Gin 请求绑定到 protobuf request，调用 biz usecase，再把 biz entity 转为 protobuf response。
- 对 protobuf JSON 使用 `protojson`，不要直接用 Gin 默认 JSON 输出 protobuf message。
- 负责把业务错误转换成统一错误结构和 HTTP status。
- 不持有具体数据库客户端，不拼 SQL，不实现 provider HTTP 调用。

### internal/biz

- 定义业务实体、任务状态机、用例编排、仓储接口和稳定业务错误。
- 只依赖标准库和少量稳定类型，避免依赖 Gin、protobuf request/response、数据库 schema、ORM 或具体外部客户端。
- 任务状态流转必须集中在 biz 层，禁止多个层各自修改状态语义。

### internal/data

- 实现 biz 层定义的 repo interface。
- 负责数据库事务、SQL、Redis、OSS、外部持久化细节。
- 将数据库错误转换为 biz 层错误，不向上泄露驱动细节。
- 涉及任务创建、事件记录、outbox 写入时必须使用事务保证一致性。

### internal/worker

- worker 必须支持多实例部署，通过 DB lease 或 Redis lock 避免重复处理同一任务。
- 高频轮询必须按 provider 做 QPS 限制、并发限制、超时、重试和退避。
- worker 的每次处理都要写任务事件，重要失败要记录 `last_error`。

### internal/provider

- 所有下游差异通过 provider adapter 隔离。
- adapter 必须显式声明 provider code、超时、鉴权方式、提交接口、轮询接口和结果导入方式。
- 下游请求日志必须脱敏 token、apiKey、签名 URL 等敏感信息。

### internal/cleaner

- 原始文件永远保留，清洗失败不能覆盖原始数据。
- 清洗规则按 `schema_version` 管理。
- 错误行要单独输出，包含原始行号和错误原因。
- Excel 解析使用结构化库，例如 `excelize`，不要用临时字符串拆分模拟表格解析。

## API 契约规范

- 对外和后台 API 的 request/response 都应定义在 `api/<module>/v1/*.proto`。
- proto 中保留 `service` 和 `google.api.http` annotation，用于描述 HTTP method、path、body 和响应契约。
- Gin handler 手写路由，但必须和 proto annotation 保持一致。
- 新增或修改 API 时必须同步生成 `.pb.go` 和 OpenAPI。
- `/health`、`/ready`、`/metrics` 可以不进入业务 proto，但对外业务 API 和后台 API 必须进入 OpenAPI。
- 增加路由契约测试：比对生成的 OpenAPI path/method 与 Gin engine 的 `Routes()`。

## 任务和数据设计规范

- 任务创建必须支持幂等，优先使用上游 `Idempotency-Key`，没有则按业务请求摘要生成。
- 任务必须记录状态、阶段、进度、下游任务 ID、provider code、batch no、轮询次数、下一次轮询时间和最后错误。
- 任务事件必须记录关键生命周期：创建、提交、提交失败、轮询、进度变化、回调、导入、清洗、完成、失败、取消、重试。
- 任务状态建议使用：`CREATED`、`SUBMITTED`、`POLLING`、`DOWNSTREAM_DONE`、`IMPORTING`、`CLEANING`、`DONE`、`FAILED`、`CANCELED`。
- 回调必须按 `batch_no` 做幂等。
- 查询类接口可以返回本地 DB 中最后一次状态，避免把高频查询压力传递给下游。

## 高并发网关规范

- 入站请求必须有 request id，并写入日志和响应头。
- 所有外部调用必须设置 timeout，并传递 context deadline。
- 按租户、API Key、provider、接口路径进行限流。
- 每个 provider 使用独立 `http.Transport`、连接池、并发 semaphore 和 QPS limiter。
- 重试只允许用于幂等请求或明确可重试的请求。
- 下游 429/5xx 要触发退避；连续失败要进入熔断或降级路径。
- 日志中不得记录明文 token、apiKey、Cookie、Authorization、签名 URL。

## 后台页面设计规范

- 后台页面是内置管理控制台，不做独立仓库或独立部署服务。
- 后台前端源码必须放在仓库根目录 `web/admin`，不得放入 `cdp-router/web/admin` 作为源码目录；`cdp-router/web/admin/dist` 只允许作为服务镜像打包用的构建产物目录。
- 首屏应是任务列表，不做 landing page、hero、营销介绍或装饰型首页。
- 设计风格应安静、克制、信息密集，适合内部运营和开发排障。
- 使用成熟后台组件库，优先 Ant Design；表格、筛选、弹窗、表单、消息反馈要保持一致。
- 页面路由建议包括：`/admin/login`、`/admin/tasks`、`/admin/tasks/:id`、`/admin/providers`、`/admin/cleaning-rules`、`/admin/system`。
- 任务列表默认展示近 24 小时任务，突出异常状态，支持状态、阶段、provider、时间和关键字筛选。
- 任务详情使用 tabs 或清晰分区展示概览、事件、下游调用、清洗结果和原始数据。
- 危险操作必须二次确认，例如取消任务、重试任务、重跑清洗、重置 provider 熔断状态。
- 长 JSON 和大文本默认折叠，避免页面卡顿和信息淹没。
- 自动刷新必须提供暂停开关。
- 工具按钮优先使用图标加 tooltip，例如刷新、重试、下载、复制、展开。

## 前端代码规范

- 前端使用 Vite + React + TypeScript，源码目录固定为 `web/admin/src`。
- 前端代码按职责拆分：`api` 管理 HTTP client 和 session，`types` 管理接口类型，`layouts` 管理页面骨架，`pages` 管理路由页面，`components` 管理可复用展示组件，`utils` 管理纯函数，`styles` 管理全局样式。
- 后台 API 调用统一经过 `src/api/client.ts`，不得在页面组件中散落 `fetch` 调用。
- 后台写接口必须通过统一 client 自动携带 `X-CSRF-Token`，不得在组件里手写 Cookie 或 CSRF 逻辑。
- 页面组件优先使用 Ant Design 组件；图标按钮优先使用 `lucide-react`，并为仅图标按钮提供 tooltip。
- 页面首屏是任务列表，保持信息密度、筛选效率和排障体验；不得加入营销 hero、装饰首页或与后台操作无关的介绍内容。
- 长文本、JSON、引用列表默认折叠或滚动展示，避免撑破表格和详情页。
- 前端样式使用稳定 className 和 CSS 变量/主题 token，避免在业务组件中堆叠大量一次性 inline style。
- 前端构建检查默认执行 `npm run typecheck` 和 `npm run build`；默认不执行 Playwright，除非需求明确要求页面级测试。
- 修改前端源码后，需要在回复中通知用户进行页面验证。

## 后台鉴权规范

- 后台 API 统一放在 `/api/admin/v1/*`。
- 第一阶段使用账号密码登录，服务端签发 HttpOnly Session Cookie。
- Session 存 Redis，设置空闲过期和绝对过期。
- 管理 API 使用 Cookie 鉴权时必须校验 CSRF token。
- 角色先保持简单：`admin`、`operator`、`viewer`。
- 写操作必须记录审计日志，至少包含 user id、action、resource type、resource id、request id、payload 和 created at。

## 配置和日志规范

- 配置包集中定义默认值、读取配置文件和校验必填项。
- 配置文件读取应拒绝未知字段，避免拼写错误静默失效。
- 敏感配置通过环境变量或密钥系统注入，不提交真实 token、apiKey、密码。
- 日志使用 `log/slog`，生产环境默认 JSON 格式。
- 日志字段至少包含 `request_id`，任务相关日志包含 `task_id`、`provider_code`、`batch_no`。
- 错误日志要包含可定位上下文，但不要泄露敏感请求头或下游签名 URL。

## 测试规范

- 接口级 E2E 测试必须使用真实持久层。
- 不允许为 HTTP/API E2E 使用 in-memory store、fake repository、mock store。
- Backend API E2E 必须创建或迁移真实测试数据库，运行真实 Gin HTTP handler/server，通过 HTTP 请求验证行为和数据库状态。
- 所有接口类变更必须完成业务端 E2E 测试，覆盖真实 HTTP 请求、真实持久层状态和关键业务链路。
- 仅当需求明确要求页面级测试时，Admin E2E 才使用 Playwright 跑真实 Gin server 和真实静态页面，验证登录、任务列表、任务详情、重试、取消和审计日志。
- 前端项目变更默认不需要执行 Playwright 页面测试；如果需要页面级测试，需求中会明确说明。
- 前端项目变更默认执行代码语法检查、类型检查和必要的构建检查，完成后通知用户进行页面验证。
- 下游 provider 可以用本地 mock HTTP server 做契约测试，但本服务内部持久化必须是真实 DB/Redis。
- 单元测试重点覆盖 biz 状态机、清洗规则、adapter 响应解析和错误映射。
- `scripts/check.sh` 应作为本地统一校验入口，至少包含 `go test ./...`、proto 生成、OpenAPI 生成和必要的前端检查。

## 代码风格

- Go 代码必须通过 `gofmt`/`go test`。
- 包名短小、语义明确，避免 `common`、`utils` 泛化包；通用能力放在 `internal/pkg/<name>`。
- 函数保持单一职责；跨层数据转换放在 service 或明确 mapper 中。
- 错误要包装上下文，但对外响应使用稳定错误码和用户可理解消息。
- context 必须作为外部调用、数据库调用和 worker 处理的第一参数传递。
- 不在业务代码中硬编码环境地址、token、apiKey、密码或 provider 配置。
- 新增数据库变更必须提供 migration。
- 生成文件不要手改，修改源 proto 或生成配置后重新生成。

## 变更流程

- 所有需求必须先完成需求确认，再执行代码变更；需求未确认清楚时，只能阅读代码、分析方案或更新需求说明，不能直接改业务代码。
- 先阅读 `docs/gateway-architecture.md` 和相关模块代码，再做实现。
- 涉及架构、目录、API、后台页面信息架构或状态机变化时，先更新文档。
- 新增 API：修改 proto、生成 Go message、生成 OpenAPI、实现 Gin handler、补契约测试和 E2E。
- 修改现有 API：同步更新 proto、OpenAPI、Gin handler、契约测试，并完成对应业务端 E2E。
- 新增任务状态或事件：同步更新 biz 状态机、数据库枚举/约束、后台展示和文档。
- 新增 provider：实现 adapter、配置项、限流策略、契约测试和脱敏日志。
- 新增或修改后台页面：先确认路由、API、权限、审计要求和验收方式；源码放在仓库根目录 `web/admin`；默认完成前端语法/类型/构建检查后通知用户验证页面，不主动执行 Playwright，除非用户明确要求。
