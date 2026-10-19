# 实现状态与验证记录

> 快照时间：2026-10-19（Asia/Shanghai）。
> 本文记录**已实际运行验证**的能力、已知偏差与后续工作，避免把“编译通过”当成“功能完成”。

---

## 1. 运行环境

| 项 | 值 |
|---|---|
| Go | 1.26.5 |
| Node / pnpm | 26.7.0 / 10.33.2 |
| Docker / Compose | 29.7.2 / v5.3.1 |
| PostgreSQL | 18-alpine（`cindy-postgres`，healthy） |
| Redis | 8-alpine（`cindy-redis`，healthy） |
| 对象存储 | RustFS（`cindy-rustfs`，healthy；MinIO 官方镜像已不可拉取，按用户允许切换） |
| Mailpit | healthy（1025/8025） |
| API | `:19828`，`/healthz`、`/readyz`、`/metrics` |
| 中间件容器 | 7 个容器全部运行：postgres / redis / rustfs / mailpit / coturn / prometheus / grafana |
| 管理台 | `cindy-admin`，Vite dev `:15900`；生产构建见 §6 |

迁移：goose 0001–0007 在空库执行成功，版本 7。种子：3 providers / 5 models / 3 plans / 4 flags / 1 plugin / 1 skill / 2 devices / 1 desktop release / 1 mobile release / 演示插件与 Skill 对象已上传 RustFS。

---

## 2. 已验证的端到端链路

| # | 链路 | 证据 |
|---|---|---|
| 1 | 邮箱发码 → 验证码登录 → access/refresh/account token → `/api/me` | `scripts/smoke.sh` 全绿；集成测试 `internal/auth` |
| 2 | Refresh 轮换 + 旧 refresh 重用拒绝 | 集成测试 `TestEmailLoginRefreshFlow` |
| 3 | 模型公共目录 v5、账号模型列表、配额/余额 | smoke + curl 200 |
| 4 | Chat：创建 Bot actor → 建群 → 发消息 → 列表/快照 | curl 返回真实房间与消息（seq/head 推进） |
| 5 | Device-Link：双 WS hello/hello-ack、presence、link-open/accept、invoke/result、src 重写 | `scripts/wsprobe` 全通过 |
| 6 | Device-Link REST：设备、ICE（TURN 凭证）、共享任务、供应商分享 | curl 200；ICE 返回 `iceServers` |
| 7 | OSS：头像预签名 PUT → 公开读 → `file_objects` 落库 | PUT/GET 200，内容回读一致 |
| 8 | 插件：列表/详情/用量事件/下载签票 → 下载字节 | 下载 454B（演示包），SHA/大小更新 |
| 9 | Skill：详情/文件清单/单文件内容/下载签票 | 文件内容正确，下载 URL 有效 |
| 10 | 更新：endpoint.json、移动 latest | curl 200 |
| 11 | Heartbeat | smoke 200 |
| 12 | 管理后台：登录（super_admin）→ dashboard/accounts/orgs/devices/models/plugins/skills/flags/audit/system health | 全部 200 |
| 13 | 管理后台在线设备统计 | presence 按 `cindy:{env}:dl:presence:{accountId}` 扫描聚合（已修复键不一致） |
| 14 | Chat WSS：subscribe → 发消息 → 收到 `message.created` | `scripts/chatwsprobe` 通过 |
| 15 | 管理台真实浏览器：登录、Dashboard、Accounts、Plugins、Light/Dark | 无运行时错误（仅登录前预期 401）；`pnpm build` 通过 |

命令：

```bash
cd cindy-server
./scripts/smoke.sh
go run ./scripts/wsprobe
go run ./scripts/chatwsprobe
go test ./...
make test-integration
```

---

## 3. 模块实现状态

| 模块 | 状态 | 说明 |
|---|---|---|
| platform | 完成 | config/log/metrics/DB/Redis/S3/JWT/幂等/审计/邮件/加密/限流 |
| auth | 完成（P0/P1 核心） | 邮箱/手机验证码、阿里云 SMS（可配置，未配置时 dev 日志回退）、refresh 轮换与重用检测、多账号 ticket、账号删除、JWKS、资料、名片、Connection token、桌面 poll、页面；SSO/社交为契约骨架 |
| chat | 完成（P0/P1 核心） | 房间/消息/成员/快照/actor/preferences/执行租约/计划/媒体/邀请/列表态/WSS；WSS 事件与补读为最小实现 |
| devicelink | 完成（P0/P1 核心） | WS 中继、关闭码、presence、设备、媒体、ICE、共享任务、供应商分享、身份挑战骨架；单实例 relay |
| modelaccess | 完成（P0/P1 核心） | catalog v5、models、credentials、quota/balance/credit、BYOK、billing、语音/Embedding/OAuth 骨架 |
| plugin | 完成（P0/P1 核心） | 列表/详情/下载/用量/上传/commit/我的发布；manifest 深度校验与审核扫描为轻量实现 |
| skillhub | 完成（P0/P1 核心） | 列表/详情/文件/版本/发布/可见性/自动安装/下载；zip 解析与扫描为轻量实现 |
| oss | 完成 | 头像预签名与公开桶策略 |
| updates | 完成 | endpoint、移动 latest/manifest、公告、release notes |
| heartbeat | 完成 | 心跳 upsert + presence TTL |
| githubissue | 完成（mock） | 无 GitHub App token 时落库 mock issue |
| hooks | 骨架 | hello/welcome、ping/pong、dispatch→ack→turn.end；42 类型常量；真实渠道未接 |
| admin | 完成（核心） | 61 条路由、RBAC、CSRF、审计、TOTP（内联 RFC6238） |
| worker | 完成（基础） | outbox 消费、过期清理 |

---

## 4. 已知偏差与未完成

1. **SSO / 社交登录**：只保留端点和协议骨架；未配置 OIDC/SAML/CAS 与 Apple/Google/微信凭证。
2. **短信**：阿里云 SMS 已接入并可用环境变量启用；当前未提供密钥，dev 使用日志回退，生产需配置 `CINDY_ALIYUN_SMS_*`。
3. **推送**：device-link `notify` 仅记录日志；APNs/FCM 发送未接。
4. **Hook 渠道**：未接 Slack/Telegram/X 真实 API；WSS 使用 stdlib RFC6455 实现，可与标准客户端互通。
5. **语音 / Embedding / OAuth Broker**：会话 ticket、feature flags、错误映射已实现；真实 ASR/上游交换未接。
6. **计费**：订单/订阅/额度账本可用；支付渠道为 mock，无 webhook。
7. **插件 manifest**：上传 commit 的 zip 深度校验与人工审核扫描为轻量实现；下载与进度链路完整。
8. **单实例**：device-link/chat 的跨实例路由仅留 Redis 接口，未实现多节点定向转发。
9. **管理台**：页面已生成；生产构建与浏览器目检结果见 §6；部分写操作以只读或最小表单呈现。
10. **对象存储**：按用户允许由 MinIO 切换为 RustFS；S3 SDK 与接口保持不变。公开桶读策略由服务启动时设置。
11. **JWT 身份名片**：使用 EdDSA（与 JWKS 一致），而逆向文档记录官方为 RS256；客户端不解析该 JWT，device-link 侧用同一 JWKS 验证。
12. **端到端加密**：device-link 隧道对 relay 明文，与客户端文档一致；未新增 E2E。
13. **管理台在线统计**：采用 SCAN 聚合各账号 presence 哈希，规模大时需改为专用在线索引。

---

## 5. 安全基线

- JWT Ed25519，私钥文件 0600；refresh/票据/邀请 token 只存 SHA-256。
- 验证码 5 次尝试、60s 冷却；全局与路由级限流（Redis）。
- BYOK 信封加密（AES-256-GCM，AAD 绑定账号/provider/credential），只写不读。
- 后台 Argon2id + CSRF + 会话 cookie；所有写操作审计。
- 对象私有 ACL；公开桶只读；签名 URL 短 TTL。
- 已知限制：管理台 TOTP 未强制（`totp_secret` 为空时跳过）；生产必须强制并改默认密码。

---

## 6. 管理台验证

- `pnpm install` / `pnpm build` 成功（含 `tsc --noEmit`）。
- 页面：Login/Dashboard/Accounts/Organizations/Devices/Models/Plugins/Skills/Releases/Feature Flags/Audit/System。
- 浏览器实测：登录 → Dashboard（真实 12 账号 / 79 审计 / 健康依赖全绿）→ Accounts（12 行真实数据、搜索/状态筛选/分页）→ Plugins（真实插件 Hello World）→ Light/Dark 切换；控制台除登录前预期 401 外无运行时错误。

---

## 7. 设计规范落实（ui-ux-pro-max / ui-styling）

- 风格：克制的内部运营台；语义化 CSS token（light/dark 双主题），深色使用分层表面而非简单反色。
- 组件：Button/Card/Table/Dialog/Drawer/Tabs/Badge/Input/Skeleton/EmptyState/Toast 全部源码内联，统一圆角、阴影、focus ring。
- 数据：表格支持排序（`aria-sort`）、搜索、状态筛选、分页、空态、骨架；危险操作二次确认。
- 可访问性：可见焦点、图标按钮 `aria-label`、表单可见标签与 autocomplete、Toast `aria-live`、Dialog `aria-modal`、跳转主内容链接、`prefers-reduced-motion` 降级。
- 响应式：桌面侧栏 + 移动抽屉；移动端按钮最小 44px（`h-11`/`size-11`），桌面保持紧凑（`sm:h-9`）。
- 对比度：浅色 `--subtle` 提升至 `#737c90`，深色 `#7d879a`；正文/次要文字满足 4.5:1 / 3:1 目标。
- 图表：单序列面积图，带空态、加载骨架、错误重试与 `role="img"` 摘要标签。
- 图标：统一 lucide-react，无 emoji 结构图标。
- 国际化：English / 简体中文切换（顶栏与登录页），默认跟随浏览器语言并持久化；布局、全部页面、表格、弹层、Toast、状态徽章、审计动作、日期/数字/图表轴均已接入；语言切换已在浏览器实测。

## 8. 下一步（P1/P2）

1. 接 SSO/社交/APNs/FCM/支付真实凭证（配置即可，无需改协议）。
2. 插件 manifest 深度校验复用客户端黄金集；Skill 扫描接静态分析。
3. 多实例 relay/chat 的 Redis 定向路由与在线索引。
4. 管理台写操作补全（模型编辑、SSO 配置、退款）。
5. 契约测试：用客户端 parser 对 P0 响应做回放。
6. 压测与 24h 长稳；日志脱敏抽查。
