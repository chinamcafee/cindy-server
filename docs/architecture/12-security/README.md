# 安全与审计实施方案

> 对应能力：SEC-01…（见 `docs/rebuild/08-observability-security.md`）
> 横切所有模块。

---

## 1. 身份与令牌

### 1.1 客户端 JWT

- 算法 EdDSA（Ed25519）；`kid` 头；私钥文件/KMS。
- Claims：`iss=cindy`、`aud=cindy-client`、`sub=accountId`、`mid=membershipId`、`region`、`typ=access`、`jti`、`iat/exp`（15 min）。
- JWKS：`GET /.well-known/jwks.json`，缓存 5 min；轮换保留旧公钥 ≥ access TTL。
- 校验：签名、`iss/aud/exp/nbf`、region、账号状态；`sessionVersion` 或吊销表。
- 401 码：`AUTH_REQUIRED`、`INVALID_TOKEN`、`TOKEN_EXPIRED`。

### 1.2 Refresh

- 随机 32 字节；只存 SHA-256；`family_id` 轮换链。
- 重用检测：已轮换 token 再次使用 → 吊销家族。
- 绝对过期 + 滑动过期；登出/封禁即时吊销。
- 设备/IP/UA 记录；异常登录审计。

### 1.3 票据

- login/bind/verification/desktop code：随机 ≥128 bit；只存哈希；一次性；短 TTL（5–10 min）。
- 绑定账号/设备/区域，不跨世代复用。

### 1.4 管理员

- Argon2id（m=64MiB,t=3,p=2）密码哈希。
- TOTP（`pquerna/otp`）；生产强制。
- Session cookie：`HttpOnly; Secure; SameSite=Lax`；30 min 空闲/12h 绝对。
- CSRF：double-submit + `X-CSRF-Token`。
- 登录失败限流与锁定。

---

## 2. 加密

### 2.1 BYOK / 敏感配置

- 信封加密：每用户 DEK（AES-256-GCM），DEK 由 KEK（KMS/主密钥）包裹。
- 存储：`ciphertext`、`nonce`、`key_version`；AAD 绑定 `accountId|providerId|credentialId`。
- 只写不读：API 不返回明文；日志/审计永不记录。
- 轮换：新 key_version；后台重加密任务。

### 2.2 哈希

- 验证码/refresh/ticket：SHA-256（高熵）或 HMAC-SHA-256（低熵验证码加 pepper）。
- 比较使用 `subtle.ConstantTimeCompare`。

---

## 3. 鉴权与权限

### 3.1 客户端资源

| 资源 | 规则 |
|---|---|
| Account | token.sub 必须匹配 |
| Membership | token.mid；跨 membership 需 account token |
| Org | membership.org_id；角色 owner/admin/member |
| Chat room | joined 成员；角色 owner/admin/member/guest |
| Device | account_id 匹配；共享任务 scope 例外 |
| Object | key 内嵌 userId 或 sharedTask 成员 |
| Plugin/Skill 发布 | org 角色 + Connection JWT audience |
| Admin | RBAC + 资源 scope |

- 越权统一 404（除明确 403 的业务码）。
- 服务端不接受请求体自报作者/设备；从 token/连接上下文取。

### 3.2 RBAC

- 角色→权限静态表；路由声明权限；资源级再校验。
- `readonly` 只能 GET；`support` 不能改模型/审核。
- 高风险：`accounts.ban`、`plugins.remove`、`models.publish`、`updates.write`、`admins.write`。

---

## 4. 限流

Redis Lua 滑动窗口：

- Key：`rl:{route}:{subject}:{window}`；subject=accountId/IP/device/guestKey。
- 返回 `allowed/remaining/resetAt`；超限 `429 RATE_LIMITED` + `Retry-After`。
- 本地 `x/time/rate` 兜底（Redis 故障时保护进程）。
- 策略表（可配置）：

| 路由 | 限制 |
|---|---|
| 发码 | 1/min、5/h、20/d |
| 登录 | 10/min/IP + 5/min/account |
| refresh | 30/min/session |
| chat 写 | 60/min/account/room |
| device-link notify | 10/min/device |
| WS 连接 | 20/account |
| 上传签票 | 100/h/account |
| 分享写 | 20/min |
| admin 写 | 60/min；高风险 10/min |

---

## 5. 审计

```go
type Event struct {
  ActorType, ActorID string
  Action, TargetType, TargetID string
  Before, After map[string]any
  Reason, RequestID string
  Result string
}
```

- 中间件自动注入 actor/IP/UA/requestId。
- 覆盖：登录/登出/删除、令牌吊销、后台写、插件/Skill 审核、模型发布、更新/flags、分享审批、密钥轮换。
- 敏感字段过滤：`password/token/secret/code/ciphertext/content`。
- 只追加；查询本身审计；保留 ≥1 年。

---

## 6. 输入与输出

- JSON 解析限制 body（按路由）；拒绝未知必填缺失；长度/枚举/正则。
- SQL 参数化；LIKE 转义；排序字段白名单。
- 路径：拒绝 `..`、绝对路径、符号链接；对象 key 白名单。
- Zip：entry 数/解压总量/压缩比/路径；流式读取。
- SSRF：代理下载解析 DNS 并拒绝私网；只允许 HTTPS；重定向逐跳校验。
- 输出：显式 DTO；不返回内部 ID/堆栈；错误码稳定。
- 上传：MIME 嗅探 + 扩展名 + 大小；病毒扫描可插拔。

---

## 7. Web 安全

- 安全头：HSTS、nosniff、Referrer-Policy、frame-ancestors（admin 禁止嵌入）。
- CORS：客户端 API 不开；admin 白名单 + credentials。
- CSRF：admin 全部非 GET。
- Cookie：HttpOnly/Secure/SameSite。
- 管理台 CSP：`default-src 'self'`；禁止 inline（Vite 构建产物）。
- WebSocket：升级前鉴权；Origin 校验（admin 不需要；客户端非浏览器）。

---

## 8. 密钥与配置

- JWT 私钥、S3、TURN、SMTP、APNs/FCM、GitHub App、OAuth broker secret：Secret Manager/文件挂载。
- 启动校验存在性；日志只打印指纹（前 8 位 hash）。
- 轮换计划：JWT 90d（双 key）、TURN 30d、S3 90d、BYOK KEK 年度。
- `.env`/`.keys` 不入库；`gitleaks`/CI 扫描。

---

## 9. 日志脱敏

- 禁止记录：Authorization/Cookie、密码、验证码、token、消息正文、附件内容、BYOK 明文、完整邮箱/手机（脱敏）。
- 统一 `redact` 函数：JWT、Bearer、sk-、AK/SK、私钥块、URL query 敏感值、email、home 路径。
- 客户端日志已在客户端脱敏；服务端查询仍按权限与脱敏展示。

---

## 10. 测试

- JWT：过期/错误签名/错误 region/吊销。
- Refresh：重用、并发、家族吊销。
- 越权：跨账号/跨组织/跨房间/对象 key。
- 限流：阈值、Retry-After、Redis 故障兜底。
- 审计：写操作覆盖、敏感字段不落库。
- 上传：zip 穿越/炸弹/大小；SSRF。
- Admin：RBAC 矩阵、CSRF、会话过期。
- 日志：抽样无敏感信息。

---

## 11. 验收

- 安全用例全部通过；越权均 404。
- 审计可追溯每个后台写操作。
- 密钥轮换不影响在线会话（双 key）。
- 日志/审计无 token、密码、消息正文。
