# 认证与账号模块实施方案

> 对应能力：AUTH-01…（见 `docs/rebuild/02-auth-account.md`）
> 依赖：Foundation

---

## 1. 实现范围

| 优先级 | 能力 |
|---|---|
| P0 | providers、邮箱验证码、登录、refresh 轮换、membership、登出、区域校验 |
| P1 | 手机验证码、社交登录（Apple/Google/微信骨架）、多账号选择、补绑、SSO 二次验证、账号删除、组织/域名/SSO 管理、desktop 托管回调 |
| P2 | Turnstile、插件 OIDC Connection token、身份名片、组织邀请 |

---

## 2. 包结构

```
internal/auth/
├── handler.go        # 路由注册
├── service.go        # 登录/令牌/账号用例
├── sso.go            # OIDC/SAML/CAS 适配
├── social.go         # Apple/Google/WeChat
├── repo.go           # 仓储
├── models.go
├── workers.go        # 过期清理、删除执行
└── tokens.go         # JWT/refresh/ticket
```

---

## 3. 数据表

见 `11-database` §3：`auth_accounts`、`auth_identities`、`auth_memberships`、`auth_organizations`、`auth_org_domains`、`auth_sso_connections`、`auth_verification_codes`、`auth_login_tickets`、`auth_refresh_sessions`、`auth_account_sessions`、`auth_desktop_authorizations`、`auth_oauth_states`、`auth_account_deletions`。

关键约束：

- `auth_identities(type, identifier)` 唯一；邮箱/手机统一小写/规范化。
- `auth_refresh_sessions.token_hash` 唯一；`family_id` 用于重用检测。
- 验证码只存哈希；`attempts` 上限 5。
- 组织 slug、域名全局唯一。

---

## 4. 端点映射

| 方法 | 路径 | Handler | 鉴权 |
|---|---|---|---|
| GET | `/api/auth/providers` | Providers | 匿名 |
| POST | `/api/auth/discovery` | Discovery | 匿名 |
| POST | `/api/auth/sso/discovery` | SSODiscovery | 匿名 |
| POST | `/api/auth/email/request-code` | RequestEmailCode | 匿名 + 限流 |
| POST | `/api/auth/email/verify-code` | VerifyEmailCode | 匿名 |
| POST | `/api/auth/phone/request-code` | RequestPhoneCode | 匿名 + 限流 |
| POST | `/api/auth/phone/verify-code` | VerifyPhoneCode | 匿名 |
| POST | `/api/auth/social/{provider}/start` | SocialStart | 匿名 |
| GET | `/api/auth/social/{provider}/callback` | SocialCallback | 匿名（state） |
| GET | `/api/auth/desktop/callback` | DesktopCallbackPage | 匿名 |
| POST | `/api/auth/desktop/poll` | DesktopPoll | 匿名（code） |
| POST | `/api/auth/refresh` | Refresh | refresh token |
| POST | `/api/auth/logout` | Logout | Bearer/refresh |
| POST | `/api/auth/select-account` | SelectAccount | login ticket |
| POST | `/api/auth/binding/request-code` | BindingRequestCode | bind ticket |
| POST | `/api/auth/binding/verify` | BindingVerify | bind ticket |
| POST | `/api/auth/sso/verification/request-code` | SSOVerificationRequestCode | verification ticket |
| POST | `/api/auth/sso/verification/verify` | SSOVerificationVerify | verification ticket |
| GET | `/api/me` | Me | Bearer |
| PATCH | `/api/me/profile` | UpdateProfile | Bearer |
| GET | `/api/me/identity-card` | IdentityCard | Bearer |
| POST | `/api/me/account/delete/challenge` | DeleteChallenge | Bearer |
| GET | `/api/me/account/delete/status` | DeleteStatus | Bearer |
| POST | `/api/me/account/delete/restore` | DeleteRestore | Bearer |
| POST | `/api/auth/oidc/connection-token` | OIDCConnectionToken | Bearer + org |
| GET | `/captcha/turnstile` | CaptchaPage | 匿名 |
| POST | `/api/captcha/verify` | CaptchaVerify | 匿名 |

> 具体请求/响应字段以 `docs/rebuild/02-auth-account.md` 为准；字段必须严格对齐客户端 parser。

---

## 5. 关键流程

### 5.1 短信验证码登录

- 手机号规范化为 E.164；中国大陆 11 位手机号自动补 `+86`。
- `POST /api/auth/phone/request-code`：校验手机号 → 手机号/IP 频控 → 生成 6 位码（prod 随机、dev 可固定）→ 存哈希（10 min）→ Aliyun SMS `SendSms`（模板参数 `code`）→ `{status:"sent"}`。
- 阿里云签名：RPC 风格 HMAC-SHA1（`GET`、`Action=SendSms`、`Version=2017-05-25`），只从环境变量读取 AK/SK；响应 `Code != OK` 视为失败。
- 发送失败删除验证码并返回 `SMS_SEND_FAILED`；未配置返回 `SMS_NOT_CONFIGURED`；限流返回 429 + `Retry-After`。
- `POST /api/auth/phone/verify-code`：5 次尝试、一次性消费、签发 membership（email 为 null）。
- 开发回退：`CINDY_SMS_PROVIDER=log` 或 `auto` 且无凭证时，dev 日志打印脱敏手机号与验证码；prod 不打印且关闭手机登录入口。

### 5.2 邮箱验证码登录

1. `request-code`：限流（email+IP）→ Turnstile（若配置）→ 生成 6 位码 → 存哈希（10 min）→ 通过可配置 SMTP 发邮件；dev 固定码。
2. `verify-code`：校验次数/过期 → 查/建 account+identity+personal membership → 若存在多 membership 返回 `select_account` + loginTicket；否则签发 access/refresh → `ok`。
3. 若身份不存在但组织域名要求 SSO → 返回 `sso_verification_required` 或拒绝（按配置）。
4. 个人单账号模式（`CINDY_SINGLE_USER_MODE`）：邮箱/手机号任一验证成功后自动绑定到同一固定账号；SMTP、客户端切换与 Admin 展示详见
   [`email-login.md`](./email-login.md)。

### 5.3 多账号选择

- `select_account` 返回 accounts（membership 列表）+ loginTicket（5 min，一次性）。
- `select-account` 校验 ticket 与目标 membership → 签发该 membership 的 token。

### 5.4 Refresh 轮换

- 校验 refresh hash、过期、未吊销。
- 若已被轮换过（`rotated_from` 命中）→ 判定重用 → 吊销同 `family_id` 全部会话 → 401。
- 否则创建新 refresh（同 family）、签发新 access；旧 refresh 标记轮换。
- 并发刷新：同 token 只允许一个成功；其余返回新 token 或 409（按客户端行为选择）。

### 5.5 SSO

- OIDC：discovery → authorize（state/nonce/PKCE）→ callback 换 token → 校验 id_token → 映射邮箱/组织 → 签发。
- SAML：AuthnRequest → ACS 校验签名/断言 → 映射。
- CAS：service ticket 校验。
- 企业要求二次验证：返回 `sso_verification_required` + verificationTicket。

### 5.6 账号删除

- challenge：生成挑战（邮件/短信）→ 校验 → 标记 `pending` + 冷静期（如 7 天）。
- status：返回状态；restore 在冷静期内撤销。
- worker 到期执行：吊销会话 → 匿名化/删除个人数据 → 保留审计与法定数据。

### 5.7 Desktop 托管回调

- `GET /api/auth/desktop/callback` 展示授权页 → 用户确认 → 生成 code → 桌面轮询 `POST /api/auth/desktop/poll` 取 token（一次性，短 TTL）。
- 长轮询 hold ≤ 20s；超时返回 pending。

---

## 6. 安全

- 验证码：只存哈希；5 次失败锁定 15 min；发送频率 1/min、5/hour、20/day。
- 登录枚举：discovery 只返回登录方式，不泄露账号存在性。
- Refresh：只存哈希；重用检测；设备/IP 记录。
- Session 吊销：Redis 黑名单 + `sessionVersion`；封禁即时生效。
- Turnstile：服务端调用 siteverify；失败拒绝。
- 邮件模板不含敏感信息；链接短 TTL。
- 所有登录/删除/SSO 事件写审计。

---

## 7. 测试

- 表驱动：providers/discovery/验证码错误/过期/次数。
- 登录：新账号、已有账号、多 membership、SSO required、补绑。
- Refresh：正常轮换、重用、并发、吊销、过期。
- 区域：cn token 打 global → 拒绝。
- 删除：挑战、冷静期、恢复、到期执行。
- 限流：429 + Retry-After。
- 契约：响应可被 auth-client parser 接受。

---

## 8. 验收

- 客户端 auth-client 的 `loginScenarios` 关键场景可回放。
- 刷新重用触发全家族吊销。
- 管理员可封禁并即时失效会话。
- 所有端点有审计与限流。
