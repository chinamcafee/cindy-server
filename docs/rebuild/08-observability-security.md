# 安全、审计与可观测（横切能力）

> 能力编号前缀：`SEC-` / `OBS-`
> 证据等级：**文档/契约**为主；服务端实现细节为建议。

---

## 1. 威胁模型

| 资产 | 威胁 | 缓解 |
|---|---|---|
| Access/Refresh Token | 窃取、重放、跨区使用 | 短 TTL、轮换、设备绑定、区域校验、TLS、最小化日志 |
| 用户消息/附件 | 未授权读取、越权分享 | 房间成员校验、对象私有 ACL、签名 URL 短 TTL、key 归属校验 |
| BYOK 凭据 | 明文泄漏 | 信封加密（KMS/主密钥）、只写不读、审计、轮换 |
| 设备控制 | 恶意控制端 | `remoteControlEnabled`、逐设备撤权、allowlist 本机执行、`src` 服务端覆写 |
| 插件包 | 供应链投毒 | manifest 校验、SHA-256、权限扩张确认、扫描、审核、组织前缀 |
| 管理后台 | 内部越权 | 独立登录、MFA、RBAC、双人复核、全量审计 |
| Hook/IM | 冒充、重放、越权确认 | 签名校验、nonce/时间窗、来源绑定、确认不可代答 |
| 对象存储 | 目录穿越、公开桶 | key 规范化、私有桶、生命周期、预签名最小权限 |
| 日志 | PII/密钥泄漏 | 客户端脱敏 + 服务端再脱敏、白名单字段、保留期 |

**信任边界**：TLS 终止于负载均衡；device-link 隧道 payload 对 relay 可见（非端到端加密），因此服务端必须严格限制访问与日志；插件授权 v3、远程桌面自动解锁、通讯录同步是客户端应用层 E2E，服务端不得尝试解密。

---

## 2. 认证与授权

- JWT：EdDSA（Ed25519）或 ES256；`iss/aud/sub/exp/iat/jti/region/membershipId/accountId`；JWKS 轮换（kid）。
- Access 15 min、Refresh 30 天（滑动 + 绝对上限）；Refresh 轮换，旧 token 重用检测→吊销全家族。
- 多账号：Account Token 与 Membership Token 分离；Login Ticket 一次性。
- 会话吊销：Redis 黑名单 + `sessionVersion`；封禁即时生效。
- 服务间：mTLS 或内部 JWT；不信任来自公网的 `X-*` 身份头。
- 管理后台：独立 cookie（`HttpOnly; Secure; SameSite=Lax`）+ CSRF token + MFA；IP 允许列表可选。

---

## 3. 输入与输出安全

- 所有入参 schema 校验（`go-playground/validator` 或手写），限制长度/枚举/UTF-8。
- 路径参数防穿越；对象 key 只允许白名单字符；邀请 token 只从 fragment/POST body 读取。
- SQL 全部参数化（pgx）；禁止字符串拼接。
- 输出按契约字段白名单，避免过度返回；错误不回显内部堆栈。
- 上传：限制大小、MIME 嗅探、zip 炸弹防护（entry 数/解压总量/压缩比）、SHA-256 校验。
- SSRF：服务端代理下载禁止内网地址；OAuth/回调 URL 只允许 HTTPS 与声明域名。
- CORS：客户端 API 不允许浏览器跨域（移动/桌面原生），admin 单独域名白名单。

---

## 4. 限流与反滥用

| 场景 | 维度 | 建议阈值（可配置） |
|---|---|---|
| 发码 | email/phone + IP | 1/min、5/hour、20/day |
| 登录尝试 | IP + account | 10/min；失败指数退避 |
| 刷新令牌 | session | 30/min |
| Chat 写 | account + room | 60/min；突发 20 |
| device-link notify | sender | 10/min；超限 `RATE_LIMITED` |
| device-link 连接 | account | 20 并发（超出 4429） |
| 媒体签票 | account | 100/hour；对象 2 GiB |
| 插件用量 | account | 批次 100，事件 6 次重试 |
| 分享写操作 | account/guestKey/IP | 20/min |
| Admin 写 | admin + IP | 60/min；高风险 10/min |

实现：Redis 令牌桶/滑动窗口，网关中间件统一；返回 `Retry-After` 与 `X-RateLimit-*`。

---

## 5. 审计

所有安全敏感动作写 `audit_events`（只追加）：

```sql
audit_events(
  id uuid pk, occurred_at timestamptz, region text,
  actor_type text, actor_id text, actor_ip inet, user_agent text,
  action text, target_type text, target_id text,
  before jsonb, after jsonb, request_id text,
  result text, reason text
)
```

- 覆盖：登录/登出/删除、令牌吊销、后台读写、插件审核/下架、模型发布、组织/SSO 变更、分享审批、强更/flags、密钥轮换。
- 保留期：≥ 1 年；导出需审批；敏感字段（token、密钥、消息正文）永不入审计。
- 审计查询本身也审计。

---

## 6. 日志

- 结构化 JSON（`slog`）：`ts, level, service, region, requestId, traceId, accountId?, route, status, latencyMs, errorCode`。
- 禁止记录：Authorization/Cookie、请求体中的密码/验证码/token、消息正文、附件内容、BYOK 明文。
- 客户端日志上传：服务端只接收已脱敏批次（SLS），不自行采集；`uploadCode` 用于支持定位。
- 采样：正常请求 1–10%；错误 100%；慢请求单独采样。
- 保留：应用日志 30 天；审计 1 年；指标 13 个月。

---

## 7. 指标与追踪

### 7.1 指标（Prometheus）

- RED：每路由请求率、错误率、P50/P95/P99。
- 业务：登录成功/失败、发码、DAU、消息量、WSS 连接、relay 路由失败、推送成功/失败、插件下载/使用、模型调用/收入、额度耗尽。
- 资源：DB 连接池、Redis 延迟、S3 错误、队列深度、Goroutine、GC。
- 安全：限流命中、封禁、异常登录、签名失败。

### 7.2 追踪（OpenTelemetry）

- HTTP/WS 入口生成 trace；下游 PG/Redis/S3/队列打 span。
- 传播 `traceparent`；日志带 `traceId`。
- 采样：1–5%，错误全采。

### 7.3 健康检查

- `/healthz` 存活；`/readyz` 依赖就绪（DB/Redis/S3 可选）。
- `/version` 返回构建版本、commit、迁移版本。

---

## 8. 数据保护与合规

- 传输：TLS 1.2+；HSTS；内部服务 mTLS。
- 静态：PG 磁盘加密；S3 SSE；备份加密。
- 密钥：KMS/Secret Manager；主密钥轮换；BYOK 使用信封加密（每用户 DEK + 主密钥）。
- 数据最小化：只存业务必要字段；日志脱敏；分析用匿名 ID。
- 删除：账号删除 → 冷静期 → 级联删除/匿名化；对象与备份按保留策略处理；删除凭证。
- 区域：cn/global 数据不互通；跨区分享只交换最小凭证（24 h）。
- 保留：消息/附件按组织策略；插件/Skill 包保留历史版本；审计 1 年。

---

## 9. 安全响应

- 漏洞报告渠道与 SLA；依赖扫描（Govulncheck、Trivy）；镜像签名。
- 密钥泄漏应急预案：轮换 JWT 签名密钥（双 key 过渡）、吊销会话、通知受影响账号。
- 插件恶意包：下架 + `removals[].purge` + 黑名单 + 审计 + 用户通知。
- 异常流量：网关限流 + 区域熔断 + 值班告警。

---

## 10. 验收标准

- 所有公开端点均有鉴权（除明确匿名：providers、catalog、heartbeat、更新清单、公开邀请页）。
- 越权测试：跨账号读取设备/房间/对象均 404。
- 令牌：刷新轮换、重用检测、吊销即时生效。
- 限流：命中返回 429 与 Retry-After。
- 审计：后台全部写操作有记录。
- 日志：抽样检查无 token/密码/消息正文。
- 健康检查与指标可用；关键告警有 runbook。
