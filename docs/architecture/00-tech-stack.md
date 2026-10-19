# 技术栈与工程规范

> 本文是 `docs/architecture/README.md` 的细化，规定版本基线、依赖选择理由、编码规范与模块边界。

---

## 1. 语言与运行时

| 项 | 选择 | 说明 |
|---|---|---|
| Go | 1.26.5 | 本机已安装；`go.mod` 使用 `go 1.26.5` |
| 构建 | `go build` / `make build` | 静态二进制；CGO 关闭 |
| 容器 | `gcr.io/distroless/static-debian12` 或多阶段 alpine | 生产镜像 |
| Node（仅 admin UI） | Node 26 | 与客户端仓一致的现代工具链 |
| 包管理 | pnpm 10 | admin 独立工程 |

---

## 2. Go 依赖

### 2.1 核心

| 领域 | 库 | 版本基线 | 选择理由 |
|---|---|---|---|
| 路由 | `github.com/go-chi/chi/v5` | v5.5.0 | 标准库兼容、轻量、中间件丰富 |
| PG 驱动 | `github.com/jackc/pgx/v5` | v5.11.0 | 原生协议、批量、COPY、类型支持 |
| 迁移 | `github.com/pressly/goose/v3` | v3.29.0 | SQL-first、可嵌入、事务 |
| Redis | `github.com/redis/go-redis/v9` | v9.23.0 | 事实标准 |
| S3 | `github.com/minio/minio-go/v7` | v7.4.1 | S3 兼容、预签名 |
| JWT | `github.com/golang-jwt/jwt/v5` | v5.3.1 | 多算法、JWKS 生态 |
| WebSocket | `github.com/gorilla/websocket` | v1.5.3 | 稳定、广泛使用 |
| 校验 | `github.com/go-playground/validator/v10` | v10.30.5 | struct tag 校验 |
| 配置 | `github.com/caarlos0/env/v11` | v11.4.1 | 环境变量解析 |
| UUID | `github.com/google/uuid` | v1.6.0 | UUIDv7 支持 |
| 密码 | `golang.org/x/crypto/argon2` | v0.58.0 | Argon2id |
| 邮件 | `github.com/wneessen/go-mail` | v0.8.1 | SMTP、附件 |
| MFA | `github.com/pquerna/otp` | latest | TOTP |
| 指标 | `github.com/prometheus/client_golang` | v1.25.0 | Prometheus |
| 追踪 | `go.opentelemetry.io/otel` | v1.47.0 | OTLP |
| 日志 | `log/slog` | stdlib | JSON handler |
| 限流 | 自研 Redis 滑动窗口 + `x/time/rate` 本地兜底 | — | 需跨实例 |
| 测试 | `github.com/stretchr/testify` | latest | 断言与 mock |

### 2.2 依赖纪律

- 只引入维护活跃、许可证兼容（Apache/MIT/BSD）的库。
- 不为单一功能引入大框架（如 ORM）；SQL 显式可控。
- 所有版本在 `go.mod` 锁定；升级走 PR + 测试。
- 禁止 `init()` 副作用；依赖显式注入。
- 生产关闭 pprof（或仅内网）。

---

## 3. 包结构与边界

```
internal/platform/*    纯基础设施，不含业务
internal/<domain>/     业务域，导出 Handler/Service/Repository
internal/admin/        只依赖各域导出的应用服务，不直接读表
cmd/server             装配所有域
cmd/worker             订阅 outbox
```

规则：

1. 域之间只能调用对方导出的 `Service` 接口或发布事件；禁止跨域 repository。
2. `platform` 不得 import 业务域。
3. handler 不写业务逻辑；service 不依赖 HTTP 类型。
4. repository 只接受 `context.Context` 与域参数，返回域模型。
5. 所有跨域写操作在用例层编排，使用事务管理器。

---

## 4. 配置

使用环境变量（`.env` 仅 dev）。核心配置项：

```dotenv
CINDY_ENV=dev
CINDY_REGION=global
CINDY_HTTP_ADDR=:19828
CINDY_PUBLIC_BASE_URL=http://127.0.0.1:19828
CINDY_ADMIN_BASE_URL=http://127.0.0.1:15900

CINDY_DATABASE_URL=postgres://cindy:cindy_dev_password@127.0.0.1:5432/cindy?sslmode=disable
CINDY_REDIS_URL=redis://127.0.0.1:6379/0
CINDY_S3_ENDPOINT=http://127.0.0.1:9000
CINDY_S3_ACCESS_KEY=cindy
CINDY_S3_SECRET_KEY=cindy_dev_password
CINDY_S3_BUCKET_PUBLIC=cindy-public
CINDY_S3_BUCKET_PRIVATE=cindy-private
CINDY_S3_BUCKET_PLUGINS=cindy-plugins
CINDY_S3_BUCKET_SKILLS=cindy-skills
CINDY_S3_USE_SSL=false

CINDY_JWT_ISSUER=cindy
CINDY_JWT_PRIVATE_KEY_FILE=./.keys/jwt-ed25519.pem
CINDY_ACCESS_TTL=15m
CINDY_REFRESH_TTL=720h

CINDY_SMTP_ADDR=127.0.0.1:1025
CINDY_SMTP_FROM=no-reply@cindy.local
CINDY_DEV_FIXED_CODE=123456

CINDY_TURN_SECRET=cindy_turn_secret
CINDY_TURN_URLS=turn:127.0.0.1:3478?transport=udp
CINDY_APNS_KEY_FILE=
CINDY_APNS_KEY_ID=
CINDY_APNS_TEAM_ID=
CINDY_FCM_CREDENTIALS_FILE=

CINDY_ADMIN_BOOTSTRAP_EMAIL=admin@cindy.local
CINDY_ADMIN_BOOTSTRAP_PASSWORD=<设置强密码>
CINDY_METRICS_ENABLED=true
CINDY_OTEL_EXPORTER_OTLP_ENDPOINT=
```

- 启动时校验必填与格式；失败即退出。
- 密钥文件不入库；`.keys/` 在 `.gitignore`。
- dev 允许 `CINDY_DEV_FIXED_CODE`，生产必须为空。

---

## 5. 编码规范

- `gofmt` + `go vet`；CI 运行 `staticcheck`（可选）。
- 错误使用 `%w` 包装；对外只暴露 `APIError`。
- 时间统一 UTC；DB `timestamptz`。
- 金额用整数最小货币单位（分/厘）或 `numeric` + 明确币种；禁止 float。
- ID：`uuid.UUID`（v7 用于时间有序）；对外字符串。
- JSON 字段使用契约中的 camelCase；内部结构可用 `snake_case` tag。
- 分页默认 20、最大 100（后台）/ 100（插件）。
- 日志字段名固定：`request_id`、`trace_id`、`account_id`、`route`、`status`、`latency_ms`、`error_code`。
- 注释只解释“为什么”，不解释“做了什么”。

---

## 6. 版本与兼容

- 客户端 wire 契约视为外部 API：新增字段 append-only；不删除/不改语义。
- 服务端内部 API（admin）可演进，但需保留一个版本的兼容。
- 数据库迁移只前进；破坏性变更分两步（新增列 → 回填 → 切读 → 删除）。
- 模型目录、插件、Skill 均使用显式 schemaVersion；不匹配拒绝并记录。
- 能力协商（device-link capability、hook protocol version）优先于版本号硬切。

---

## 7. 性能目标（dev 基线）

| 端点 | P95 |
|---|---|
| 登录/刷新 | < 150 ms |
| Chat 列表/消息 | < 100 ms |
| device-link 转发 | < 20 ms（同区域） |
| 模型目录 | < 50 ms（缓存命中） |
| 插件列表 | < 150 ms |
| 预签名 | < 50 ms |
| Admin 列表 | < 200 ms |

- 连接：单实例 ≥ 10k WSS（需调优）；消息扇出走内存 hub + Redis pub/sub（多实例）。
- DB：所有高频查询有索引；列表禁止 N+1；大表按时间分区（消息、审计、事件）。

---

## 8. 前端技术栈（cindy-admin）

| 领域 | 选择 |
|---|---|
| 框架 | React 19.3 |
| 构建 | Vite 8 |
| 语言 | TypeScript 7（注意：无 `baseUrl`） |
| 样式 | Tailwind CSS 4 + CSS 变量主题 |
| 组件 | shadcn/ui 设计语言 + Radix primitives + 自研源码组件 |
| 数据 | TanStack Query 5 |
| 表格 | TanStack Table 9 |
| 路由 | React Router 7 |
| 表单 | React Hook Form 7 + Zod 4 |
| 图表 | Recharts 3 |
| 图标 | lucide-react |
| 测试 | Vitest + Testing Library（可选） |

UI 规范：

- Light/Dark 双主题；语义 token；不使用硬编码色值。
- 间距 4px 基线；圆角 8/12/16；字阶 12/13/14/16/20/24/32。
- 表格支持列固定、排序、筛选、空状态、骨架屏、分页/虚拟滚动。
- 危险操作二次确认；表单错误就近展示；全局 Toast。
- 键盘可达；焦点可见；对比度 ≥ 4.5:1。
- 命令面板（⌘K）、面包屑、可折叠侧栏。
