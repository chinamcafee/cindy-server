# Cindy Server 技术方案总纲

> 阶段 2 交付物。基于 `docs/rebuild/` 的能力清单，拆解从零实现 Go 服务端的全部工作。
>
> 约束：Go 最新稳定版（本机 1.26.5）；框架/库使用业界主流最新稳定版；中间件统一由本地 Docker Compose 项目 `cindy-middlewares` 提供；PostgreSQL + Redis + S3 兼容对象存储（RustFS）；Admin API 与客户端 API 同仓 Go 实现；Admin WebUI 使用 React 19+。

---

## 1. 总体策略

### 1.1 模块化单体，按服务边界分包

Cindy 闭源服务端在线上可能是多服务部署（auth/chat/device-link/...），但重建版采用**模块化单体**：

- 单一 Go module、单一进程（`cmd/server`），按业务域分包。
- 每个业务域拥有独立的 router、service、repository、迁移与测试。
- 域间只通过显式接口/事件交互，不直接读对方表。
- 生产可按 `cmd/server -modules=auth,chat` 拆分进程；dev 默认全开。
- 好处：本地一键起服、事务简单、共享契约；同时保留拆分路径。

### 1.2 协议前缀兼容多服务

客户端按不同 base URL 调用不同服务。开发环境用单进程 + 路径前缀模拟：

| 逻辑服务 | 本地路径前缀 | 生产 Host 示例 |
|---|---|---|
| auth | `/api/auth/*`、`/api/me/*`、`/captcha/*` | `auth.cindy.local` |
| chat | `/v1/*`、`/v1/ws` | `chat.cindy.local` |
| device-link | `/api/device-link/*` | `device-link.cindy.local` |
| model-access | `/api/model-access/*`、`/api/billing/*` | `model-access.cindy.local` |
| model-catalog | `/api/model-catalog/*` | `model-catalog.cindy.local` |
| voice | `/api/voice/*` | `voice.cindy.local` |
| oauth broker | `/api/user/*`、`/api/integrations/*` | `oauth-broker.cindy.local` |
| oss | `/api/oss/*` | `oss.cindy.local` |
| plugin | `/api/plugins/*`、`/api/publisher/*` | `plugin.cindy.local` |
| skill hub | `/api/skills-hub/*` | `skill-hub.cindy.local` |
| github | `/api/github/*` | `github.cindy.local` |
| heartbeat | `/heartbeat` | `heartbeat.cindy.local` |
| mobile update | `/latest`、`/manifest` | `mobile-update.cindy.local` |
| hooks | `/hook/v1/ws`（或以 Host 区分渠道） | `slack-hook.cindy.local` 等 |
| admin | `/admin/api/v1/*` | `admin.cindy.local` |

网关（Caddy/Nginx）按 Host 转发到同一进程；本地也可直接使用路径前缀，无需 DNS。

### 1.3 一致性策略

- 强一致：账号、令牌、组织、模型目录发布、插件审核、共享任务成员、订单。
- 最终一致：消息扇出、推送、用量聚合、审计派生、清理任务。
- 事务内写业务表 + `outbox_events`；worker 至少一次投递，消费端幂等。

---

## 2. 目录结构

```
cindy-server/
├── cmd/
│   ├── server/main.go            # API + admin API
│   ├── worker/main.go            # outbox、推送、补偿、清理
│   └── migrate/main.go           # goose 迁移（可选）
├── internal/
│   ├── platform/                 # 配置、日志、HTTP、DB、Redis、S3、JWT、限流、审计
│   ├── auth/                     # 登录、SSO、令牌、账号、组织
│   ├── chat/                     # 房间、消息、WSS hub、执行租约
│   ├── devicelink/               # relay、presence、设备、共享任务、供应商分享
│   ├── modelaccess/              # 模型目录、BYOK、配额、计费
│   ├── voice/                    # 语音会话与 ASR 代理
│   ├── oss/                      # 预签名与对象生命周期
│   ├── plugin/                   # 插件市场、发布、审核、用量
│   ├── skillhub/                 # 技能市场、审核、分发
│   ├── hooks/                    # Slack/Telegram/X 渠道适配
│   ├── updates/                  # 桌面/移动更新清单、flags、公告
│   ├── heartbeat/                # 心跳
│   ├── githubissue/              # 反馈 issue 代理
│   ├── admin/                    # 后台 API、RBAC、审计查询
│   └── worker/                   # outbox 消费者、定时任务
├── migrations/                   # goose SQL
├── queries/                      # sqlc SQL（如启用）
├── deploy/
│   ├── compose/                  # cindy-middlewares
│   └── caddy/                    # 本地/生产反代示例
├── docs/
│   ├── rebuild/
│   └── architecture/
├── scripts/
├── go.mod
├── sqlc.yaml
└── Makefile
```

---

## 3. 关键技术决策

| 决策 | 选择 | 理由 |
|---|---|---|
| HTTP 框架 | `chi v5` | 标准 `net/http` 兼容、中间件生态成熟、轻量 |
| DB 驱动 | `pgx v5` + `pgxpool` | PostgreSQL 原生性能与类型支持 |
| 查询层 | 手写 SQL（sqlc 可选） | 复杂列表/聚合多；sqlc 用于稳定 CRUD |
| 迁移 | `goose v3` | SQL-first、嵌入式迁移、事务 |
| 缓存/限流 | `redis/go-redis v9` | 分布式 presence、幂等、限流、会话吊销 |
| 对象存储 | `minio-go v7` | S3 兼容、预签名 |
| JWT | `golang-jwt/jwt v5` + EdDSA | 各服务 JWKS 本地验签 |
| WebSocket | `gorilla/websocket v1.5` | chat/relay 双 hub 成熟稳定 |
| 校验 | `go-playground/validator v10` + 手写 wire parser | 与客户端 zod 契约对齐 |
| 配置 | `caarlos0/env v11` + `.env` | 12-factor |
| 日志 | `log/slog`（JSON） | 标准库、结构化 |
| 指标 | `prometheus/client_golang` | 主流 |
| 追踪 | `otel` + OTLP | 可选导出 |
| 邮件 | `go-mail` + Mailpit | 开发验证码 |
| 密码/TOTP | Argon2id + `pquerna/otp` | 管理员登录与 MFA |
| Admin UI | React 19.3 + Vite 8 + TS 7 + Tailwind 4 | 与客户端一致的最新 React 技术栈 |

### 3.1 版本清单（2026-10-18 查询）

**Go 依赖**

| 模块 | 版本 |
|---|---|
| Go | 1.26.5 |
| chi | v5.5.0 |
| pgx | v5.11.0 |
| go-redis | v9.23.0 |
| minio-go | v7.4.1 |
| golang-jwt | v5.3.1 |
| gorilla/websocket | v1.5.3 |
| goose | v3.29.0 |
| validator | v10.30.5 |
| env | v11.4.1 |
| prometheus client | v1.25.0 |
| otel | v1.47.0 |
| google/uuid | v1.6.0 |
| x/crypto | v0.58.0 |
| go-mail | v0.8.1 |

**前端依赖**

| 包 | 版本 |
|---|---|
| react / react-dom | 19.3.1 |
| vite | 8.3.4 |
| typescript | 7.0.2 |
| tailwindcss | 4.3.3 |
| @tanstack/react-query | 5.104.4 |
| @tanstack/react-table | 9.3.1 |
| react-router-dom | 7.18.4 |
| recharts | 3.10.1 |
| lucide-react | 1.59.0 |
| zod | 4.6.7 |
| react-hook-form | 7.90.0 |
| @vitejs/plugin-react | 6.1.2 |

> 实际编码时以 `go get ...@latest` / `pnpm add ...@latest` 锁定；文档版本作为基线。TypeScript 7 不支持 `baseUrl`，路径别名使用 `paths` 或相对导入。

---

## 4. 请求处理流水线

```
TLS/Host 路由
  → RequestID / TraceID
  → Panic 恢复
  → 结构化访问日志
  → CORS（admin 专用）
  → 限流（Redis）
  → 鉴权（JWT/JWKS 或 admin session）
  → 资源级授权
  → 输入校验（schema）
  → Handler → Service → Repository
  → 统一错误信封
  → 指标/审计
```

- 客户端路由不做 CORS（原生客户端）；admin 独立 CORS 白名单。
- WebSocket 在 Upgrade 前完成鉴权；握手后 15 s 内必须收到 hello（device-link）或订阅（chat）。
- 所有写请求生成 `requestId`，日志与审计贯穿。

---

## 5. 错误模型

```go
type APIError struct {
    Status  int
    Code    string
    Message string
    Details map[string]any
}
```

- 领域错误映射到稳定业务码；内部错误统一 `INTERNAL`，不回显细节。
- 与客户端契约对齐的码（示例）：
  - auth：`REGION_MISMATCH`、`INVALID_CODE`、`CODE_EXPIRED`、`ORG_SSO_NOT_FOUND`、`TOKEN_EXPIRED`、`INVALID_TOKEN`、`AUTH_REQUIRED`
  - chat：`TOKEN_EXPIRED`、`INVALID_TOKEN`、`AUTH_REQUIRED`、`ROOM_NOT_FOUND`、`FORBIDDEN`、`OPERATION_CONFLICT`
  - device-link：`DEVICE_OFFLINE`、`REMOTE_DISABLED`、`VERSION_MISMATCH`、`PAYLOAD_TOO_LARGE`、`RATE_LIMITED`、`BAD_REQUEST`、`INTERNAL`
  - plugin：`PLUGIN_NAMESPACE_CLIENT_REQUIRED`、`PLUGIN_ORGANIZATION_CONTEXT_UNAVAILABLE`、`PLUGIN_NAMESPACE_CONFIG_REQUIRED`、用量拒绝码

---

## 6. 分册索引

| 目录 | 内容 |
|---|---|
| [00-tech-stack.md](00-tech-stack.md) | 技术选型、版本、依赖策略 |
| [01-middlewares/](01-middlewares/README.md) | `cindy-middlewares` compose、端口、初始化、运维 |
| [02-foundation/](02-foundation/README.md) | 工程骨架、配置、迁移、outbox、公共中间件 |
| [03-auth/](03-auth/README.md) | 认证、账号、组织、SSO、令牌 |
| [04-chat/](04-chat/README.md) | 房间、消息、WSS、云伙伴 |
| [05-device-link/](05-device-link/README.md) | relay、presence、共享任务、供应商分享 |
| [06-model-access/](06-model-access/README.md) | 模型目录、BYOK、计费、语音 |
| [07-plugin-skillhub/](07-plugin-skillhub/README.md) | 插件、Skill Hub |
| [08-oss-updates/](08-oss-updates/README.md) | OSS、更新、远程配置、心跳、可观测接入 |
| [09-hooks-im/](09-hooks-im/README.md) | Hook 渠道 |
| [10-admin/](10-admin/README.md) | Admin API 与 WebUI |
| [11-database/](11-database/README.md) | 数据库设计、表清单、索引、分区 |
| [12-security/](12-security/README.md) | 安全、审计、限流 |
| [13-deployment/](13-deployment/README.md) | 本地/生产部署、反代、密钥 |
| [14-testing/](14-testing/README.md) | 测试策略、契约测试、验收 |
| [15-roadmap/](15-roadmap/README.md) | 里程碑、任务拆解、完成定义 |
| [16-implementation-status.md](16-implementation-status.md) | 实现状态、已验证链路、已知偏差 |

---

## 7. 完成定义（阶段 2）

- 每个能力编号都有对应模块方案：表、API、流程、错误码、测试、验收。
- compose 可一键启动全部中间件，健康检查通过。
- 数据库迁移可从空库建到最新。
- 本地可完成：登录 → 拉模型目录 → 连接 device-link → 发消息 → 上传对象 → 管理后台登录。
- 文档之间无冲突，版本与目录结构一致。
