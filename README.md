# Cindy Server（Go 重建版）

基于开源客户端 [`cindy`](../cindy) 与协议仓 [`cindy-protocol`](../cindy-protocol) 逆向重建的 Cindy 服务端。仓库同时包含：

- 客户端 API 服务（auth / chat / device-link / model-access / plugin / skill-hub / oss / updates / hooks…）
- 管理后台 API（Go，同进程）
- 管理后台 WebUI（独立工程 [`cindy-admin`](../cindy-admin)，React 19）
- 逆向能力清单（[`docs/rebuild`](docs/rebuild)）
- 技术方案（[`docs/architecture`](docs/architecture)）
- 本地中间件 Compose 组 `cindy-middlewares`（PostgreSQL 18 / Redis 8 / RustFS / coturn / Mailpit / Prometheus / Grafana）

> 服务端官方实现闭源；本项目是**契约兼容重建**，不是官方服务端源码。所有无法从客户端证明的行为均标注为推测，详见各分册。

---

## 1. 快速开始

前置：Go 1.26+、Docker 29+/Compose v2+、Node 26 + pnpm 10（仅管理台）。

```bash
cd cindy-server

# 1) 中间件
docker compose -p cindy-middlewares -f deploy/compose/docker-compose.yml up -d

# 2) 配置与密钥
cp .env.example .env
make keys

# 3) 迁移 + 种子
make migrate
make seed

# 4) 启动 API（:19828）
make run

# 5) 管理台（另一个终端）
cd ../cindy-admin
pnpm install
pnpm dev        # http://127.0.0.1:15900
```

验证：

```bash
curl http://127.0.0.1:19828/healthz
curl http://127.0.0.1:19828/api/auth/providers
curl 'http://127.0.0.1:19828/api/model-catalog/catalog?registrySchemaVersion=5'
```

开发登录：手机短信 dev 固定码 `123456`（`CINDY_DEV_FIXED_CODE`）；邮箱验证码通过 Admin 配置的 SMTP 发送（`CINDY_EMAIL_DEV_FIXED_CODE` 留空）。管理员账号由 `.env` 的 CINDY_ADMIN_BOOTSTRAP_* 配置。

---

## 2. 目录

```
cmd/server/           API + admin API 进程
cmd/migrate/          执行迁移
cmd/seed/             种子数据（模型/套餐/flags/公告）
cmd/keys/             生成 Ed25519 JWT 密钥
internal/platform/    config/obs/db/cache/objectstore/authn/httpapi/idempotency/audit/crypto/mailer
internal/auth/        认证、账号、令牌、SSO 骨架、账号删除
internal/chat/        房间、消息、WSS、执行租约、计划、媒体
internal/devicelink/  relay、presence、设备、共享任务、供应商分享、推送
internal/modelaccess/ 模型目录、BYOK、配额、计费、语音/Embedding 骨架
internal/plugin/      插件市场、发布、审核、用量
internal/skillhub/    Skill 市场、发布、审核、自动安装
internal/oss/         对象存储签票
internal/updates/     桌面/移动更新、endpoint、公告
internal/heartbeat/   心跳
internal/githubissue/ 反馈 issue 代理
internal/hooks/       Slack/Telegram/X WSS 骨架
internal/admin/       管理后台 API
migrations/           goose SQL（0001–0007）
deploy/compose/       cindy-middlewares
docs/rebuild/         能力清单（逆向）
docs/architecture/    技术方案（分模块）
```

---

## 3. 服务与路径

单进程按客户端契约挂载多个逻辑服务（生产可按模块拆分）：

| 逻辑服务 | 路径前缀 |
|---|---|
| Auth | `/api/auth/*`、`/api/me*`、`/.well-known/jwks.json` |
| Chat | `/v1/*`、`/v1/ws` |
| Device-Link | `/api/device-link/*`（含 `/ws`） |
| Model Access | `/api/model-access/*`、`/api/billing/*` |
| Model Catalog | `/api/model-catalog/*` |
| Voice / Broker | `/api/voice/*`、`/api/user/*`、`/api/integrations/*` |
| OSS | `/api/oss/*`、`/api/device-link/media/*` |
| Plugin | `/api/plugins/*`、`/api/publisher/*` |
| Skill Hub | `/api/skills-hub/*` |
| Updates | `/api/mobile-update/*`、`/api/endpoint.json`、`/api/release-notes/*` |
| Heartbeat | `/heartbeat` |
| GitHub | `/api/github/*` |
| Hooks | `/ws`、`/hook/v1/ws` |
| Admin | `/admin/api/v1/*` |
| 运维 | `/healthz`、`/readyz`、`/metrics` |

---

## 3.1 短信验证码登录（手机号账号）

手机号账号体系、验证码存储/校验、限流与登录签发已实现；短信下发支持阿里云 SMS。

```dotenv
CINDY_SMS_PROVIDER=aliyun
CINDY_ALIYUN_SMS_ACCESS_KEY_ID=...
CINDY_ALIYUN_SMS_ACCESS_KEY_SECRET=...
CINDY_ALIYUN_SMS_SIGN_NAME=...
CINDY_ALIYUN_SMS_TEMPLATE_CODE=SMS_xxx
CINDY_ALIYUN_SMS_REGION_ID=cn-hangzhou
CINDY_ALIYUN_SMS_TEMPLATE_PARAM=code
```

- `CINDY_SMS_PROVIDER=auto`（默认）：四项凭证齐全时使用阿里云，否则在 dev 使用日志发送（验证码打印在服务日志，手机号脱敏）；生产环境不打印验证码，且 `providers.phone=false`。
- 模板参数默认 `{"code":"123456"}`；如模板变量名不同，设置 `CINDY_ALIYUN_SMS_TEMPLATE_PARAM`。
- 手机号统一规范化为 E.164（中国大陆手机号自动补 `+86`）。
- 频率限制：同手机号 60 秒冷却、每小时 5 次；同 IP 每分钟 10 次；验证码 10 分钟有效、最多 5 次尝试、一次性消费。
- 发送失败会删除本次验证码并返回 `SMS_SEND_FAILED`；未配置短信返回 `SMS_NOT_CONFIGURED`。
- 密钥只放在 `.env` / Secret Manager，不写入仓库；`.env` 已被 `.gitignore` 忽略。

## 4. 能力覆盖

| 域 | 状态 |
|---|---|
| 邮箱/手机验证码登录、阿里云 SMS、JWT/refresh 轮换、membership、登出 | 已实现（短信需配置阿里云密钥） |
| 多账号/SSO/社交/托管回调/账号删除 | 契约与骨架；SSO/社交需接 IdP/平台密钥 |
| Chat 房间/消息/快照/成员/WSS/执行租约/媒体/计划 | 已实现（P0/P1 核心） |
| Device-Link 中继/关闭码/presence/设备目录/媒体/ICE | 已实现（单实例；多实例留 Redis 接口） |
| 共享任务/供应商分享/推送 | 已实现 CRUD 与路由授权；APNs/FCM 需配置 |
| 模型目录 v5/BYOK/配额/计费 | 已实现核心；支付渠道为 mock |
| 插件市场/上传/审核/用量；Skill Hub | 已实现核心；zip 扫描为轻量校验 |
| OSS 头像/媒体；桌面/移动更新；endpoint/公告 | 已实现 |
| Hook 42 类型 | 骨架（hello/ping/task.dispatch/turn.end）；渠道 API 需密钥 |
| 管理后台 API + React 19 WebUI | 已实现核心运营面 |

> 未实现/需外部密钥的能力在代码与文档中显式标注；不会伪报为已接通。

---

## 5. 技术栈

Go 1.26.5 · chi v5 · pgx v5 · goose v3 · Redis 8 · RustFS（S3）· Ed25519 JWT · gorilla/websocket · Prometheus · slog。
管理台：React 19.3 · Vite 8 · TypeScript 7 · Tailwind 4 · TanStack Query/Table · Recharts 3。

---

## 6. 测试

```bash
go test ./...
go vet ./...
cd ../cindy-admin && pnpm build
```

契约测试样例与验收清单见 [`docs/architecture/14-testing/README.md`](docs/architecture/14-testing/README.md)。

---

## 7. 安全

- 不提交 `.env`、`.keys/`、用户凭证。
- 生产必须：替换默认密钥、关闭固定验证码、启用 TLS、接 KMS、限制管理台访问。
- 所有管理端写操作写审计；敏感字段不入日志/审计。
- device-link 隧道 payload 对 relay 可见，服务端按最小权限与审计约束访问。

---

## 8. 文档

- 能力清单：[`docs/rebuild/00-overview.md`](docs/rebuild/00-overview.md)
- 技术方案：[`docs/architecture/README.md`](docs/architecture/README.md)
- 中间件：[`docs/architecture/01-middlewares/README.md`](docs/architecture/01-middlewares/README.md)
- 数据库：[`docs/architecture/11-database/README.md`](docs/architecture/11-database/README.md)
- 部署：[`docs/architecture/13-deployment/README.md`](docs/architecture/13-deployment/README.md)
- 路线图：[`docs/architecture/15-roadmap/README.md`](docs/architecture/15-roadmap/README.md)

## 9. 许可与边界

本项目仅用于互操作与自建服务端研究。Cindy 客户端与协议的版权归其权利人所有；请遵守原仓库许可证（Apache-2.0）与相关条款。
