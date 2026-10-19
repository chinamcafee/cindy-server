# 实施路线图与任务拆解

> 按能力依赖排序。每个里程碑结束都有可验证的交付物；阶段 3 编码严格按此推进。

---

## 1. 里程碑总览

| 里程碑 | 主题 | 交付物 | 依赖 |
|---|---|---|---|
| M0 | 工程骨架 | Go module、配置、日志、DB/Redis/S3、迁移、健康检查、compose 可跑 | — |
| M1 | 认证闭环 | 发码/验证、JWT/refresh、membership、providers、登出、admin 登录 | M0 |
| M2 | 基础业务 | 模型目录、Chat 房间/消息 + WSS、OSS 预签名、device-link relay 基础 | M1 |
| M3 | 协作与同步 | 设备目录/presence、共享任务、供应商分享、推送、附件 | M2 |
| M4 | 商业化与生态 | 配额/计费、插件市场/发布/审核、Skill Hub、Hook 渠道 | M2 |
| M5 | 运营与更新 | 更新清单、feature flags、公告、后台全模块、审计 | M2–M4 |
| M6 | 加固与验收 | 安全/限流/契约测试/压测/文档校准 | M1–M5 |

---

## 2. M0 工程骨架

- [ ] `go.mod`（Go 1.26.5），依赖版本按 `00-tech-stack.md`。
- [ ] `internal/platform/config`：env 解析与校验。
- [ ] `internal/platform/obs`：slog、requestId、Prometheus、OTel（可选）。
- [ ] `internal/platform/db`：pgxpool、事务管理器、goose 嵌入迁移。
- [ ] `internal/platform/cache`：Redis 客户端与 key 规范。
- [ ] `internal/platform/objectstore`：S3 客户端、桶检查、presigner。
- [ ] `internal/platform/httpapi`：chi 路由、错误信封、恢复、限流、鉴权中间件。
- [ ] `internal/platform/idempotency`：Redis/PG 幂等。
- [ ] `migrations/0001_platform`。
- [ ] `cmd/server` 健康检查 `/healthz`、`/readyz`、`/metrics`。
- [ ] `Makefile`：`run/build/test/migrate/seed/keys`。
- [ ] compose 启动验证。

**验收**：空库迁移成功；`curl /readyz` 返回依赖就绪；指标可抓取。

---

## 3. M1 认证闭环

- [ ] `auth` 域模型、仓储、迁移。
- [ ] providers/discovery/发码/验证码登录。
- [ ] JWT 签发（EdDSA）、JWKS、refresh 轮换、重用检测。
- [ ] membership、多账号 ticket、补绑、SSO 二次验证（骨架）。
- [ ] 登出、会话吊销、账号删除挑战。
- [ ] 组织/域名/SSO 表与后台只读。
- [ ] admin 登录（Argon2id + TOTP + session cookie）。
- [ ] 限流与审计中间件接入。

**验收**：客户端 auth-client 的登录场景（fixtures）可回放；refresh 重用被吊销；区域校验。

---

## 4. M2 基础业务

### 4.1 模型目录
- [ ] catalog 表与 seed（至少 3 个 provider、10 个模型）。
- [ ] `GET /api/model-catalog/catalog?registrySchemaVersion=5`（ETag/缓存）。
- [ ] `GET /api/model-access/models`、credentials、quota/balance（基础）。
- [ ] BYOK 加解密与 CRUD 骨架。

### 4.2 Chat
- [ ] 房间/成员/消息/事件表。
- [ ] HTTP：列表、创建、消息分页、发送、已读、附件元数据。
- [ ] WSS：鉴权、订阅、事件、seq、补读、ack。
- [ ] 幂等 `operationId`/`clientMessageId`。

### 4.3 OSS
- [ ] 桶初始化与对象登记。
- [ ] `/api/oss/presign-put`（头像场景）。
- [ ] `/api/device-link/media/presign-put|get|delete`（先复用对象层）。

### 4.4 Device-link 基础
- [ ] WSS `/api/device-link/ws`：hello/ack、ping/pong、presence、路由、错误码。
- [ ] 设备表与 hello 注册；presence Redis。
- [ ] 关闭码（4400/4401/4409/4410/4429）与顶号。
- [ ] 设备目录 REST。

**验收**：两个 WS 客户端互发 invoke；跨账号不可达；remoteControlEnabled 校验生效。

---

## 5. M3 协作与同步

- [ ] 共享任务 CRUD/邀请/加入/成员/关闭 + relay scope 授权 + 30s 补偿。
- [ ] 供应商分享 CRUD/申请/审批/暂停/撤销 + 跨区 guest 凭证。
- [ ] 推送 token 注册、notify 消费、APNs/FCM 适配（dev 打日志）。
- [ ] ICE 配置（TURN REST 凭证）。
- [ ] Chat 附件、云伙伴、Bot 群聊、执行租约（按分册）。
- [ ] 邀请落地页。

**验收**：共享任务跨账号消息可通；供应商分享审批后 guest 可连；notify 频控。

---

## 6. M4 商业化与生态

- [ ] 套餐/订阅/额度/用量/订单（模型与计费）。
- [ ] 插件列表/详情/下载/成员上传/commit/审核/用量。
- [ ] manifest 校验器（对照客户端测试样例）。
- [ ] Skill Hub 列表/详情/文件/版本/发布/可见性/审核/自动安装。
- [ ] Hook 渠道骨架（先 Telegram 或 Slack 之一）+ 42 消息编解码。
- [ ] GitHub issue 代理（可选 stub）。
- [ ] 语音会话骨架（返回 ticket + mock WS）、Embedding 代理（可选）。

**验收**：插件上传→审核→下载校验通过；Skill 发布→扫描→安装；用量 ACK 去重。

---

## 7. M5 运营与更新

- [ ] 桌面/移动更新清单、渠道、灰度、公告、endpoint manifest。
- [ ] feature flags（isCanary/defaultEnableBeta）。
- [ ] heartbeat 与 DAU。
- [ ] 后台全部模块：用户/组织/SSO/设备/共享/模型/插件/Skill/更新/审计。
- [ ] 审计查询、任务队列、告警占位。
- [ ] Admin WebUI：登录、仪表盘、各列表与详情、审核操作、双主题、命令面板。

**验收**：后台完成一次插件审核、一次模型发布、一次封禁；审计可查。

---

## 8. M6 加固与验收

- [ ] 契约测试（P0 端点）。
- [ ] 安全用例：越权、重放、注入、限流。
- [ ] 压测：登录、消息、relay、WSS。
- [ ] 日志脱敏抽查；审计覆盖检查。
- [ ] 文档与实现一致性校对（路由/字段/错误码）。
- [ ] README、运行手册、已知限制。

---

## 9. 任务拆解粒度（示例）

| 任务 | 产出 | 估时 |
|---|---|---|
| T-AUTH-01 | auth 表迁移 + 仓储 | 0.5d |
| T-AUTH-02 | 发码/验证 + 邮件 | 0.5d |
| T-AUTH-03 | JWT/JWKS/refresh 轮换 | 0.5d |
| T-CHAT-01 | 房间/消息表 + HTTP | 1d |
| T-CHAT-02 | WSS hub + 事件 + 补读 | 1d |
| T-DL-01 | relay 握手/路由/关闭码 | 1d |
| T-DL-02 | presence/设备目录 | 0.5d |
| T-DL-03 | 共享任务/供应商分享 | 1.5d |
| T-MODEL-01 | 目录/seed/接口 | 1d |
| T-PLUGIN-01 | 市场/下载/用量 | 1d |
| T-PLUGIN-02 | 上传/审核/manifest | 1.5d |
| T-SKILL-01 | Hub CRUD/发布/审核 | 1.5d |
| T-ADMIN-01 | 后台 API/RBAC/审计 | 1.5d |
| T-ADMIN-02 | WebUI 框架/页面 | 3d |
| T-E2E-01 | 端到端脚本 | 0.5d |

> 估时仅用于排序，不代表承诺；实际以可运行验证为准。

---

## 10. 风险与应对

| 风险 | 影响 | 应对 |
|---|---|---|
| 客户端契约存在未发现的隐式要求 | 集成失败 | 契约回放 + 逐端点对照 parser |
| 闭源服务端行为未知 | 语义偏差 | 明确标注推测；默认宽松、可配置 |
| WSS 多实例路由 | 消息丢失 | Redis 路由 + 粘性/定向转发 |
| 对象存储跨区/可达性 | 上传失败 | endpoint 可配置 + 本地 RustFS 验证 |
| 插件 manifest 规则复杂 | 审核误拒 | 直接复用客户端测试样例做黄金集 |
| 后台范围过大 | 延期 | 先只读后写入；按 P0/P1 交付 |
| 时间/轮次限制 | 未完成 | 每轮交付可运行切片，持续集成 |

---

## 11. 完成定义（DoD）

一个能力算完成，必须同时满足：

1. 路由、请求/响应、错误码与分册一致。
2. 有数据库迁移（如涉及）与索引。
3. 有单元/集成测试；P0 有契约测试。
4. 鉴权/越权/幂等/限流符合安全分册。
5. 审计与日志接入。
6. 文档更新（实现差异回写）。
7. `go test ./...` 与构建通过。
