# 邮箱 / 手机号平行登录（个人单账号模式）

> 适用：自建 cindy-server、个人使用；邮箱与手机号都是可独立完成登录的身份，
> 首次验证任一身份后自动绑定到同一个固定账号。
>
> 相关代码：`internal/auth/service.go`、`internal/auth/handler.go`、
> `internal/platform/mailer/mailer.go`、客户端 `LoginPage` / 移动端 `login.tsx`、
> `cindy-admin/src/pages/Accounts.tsx`。

---

## 1. 行为约定

- `providers.email = true` 且 `providers.phone = true`，两种验证码都可登录。
- 登录页在两者都可用时显示「手机号 / 邮箱」切换；未切换时按构建区域默认
  （cn → 手机号，global → 邮箱）。
- 开启 `CINDY_SINGLE_USER_MODE=true` 后：
  - 第一次验证邮箱或手机号会创建账号；
  - 之后验证另一种身份时，自动把该身份挂到同一个账号，不新建账号；
  - 两种身份登录返回同一个 `membershipId` / `accountId`；
  - 最近一次验证成功的邮箱会写回 `personal membership.email`，设置页显示该地址；
    手机号登录不会清空已绑定的邮箱；
  - 如需固定某个账号，设置 `CINDY_SINGLE_USER_ACCOUNT_ID=<uuid>`。
- 关闭单账号模式时恢复原多账号行为：每个未绑定的邮箱/手机号各建一个账号。
- 已存在的身份永远优先返回其原账号，不会因模式切换而迁移。

> 个人部署推荐：`CINDY_SINGLE_USER_MODE=true`，并保持
> `CINDY_SINGLE_USER_ACCOUNT_ID` 为空（自动选择最早创建的账号）。
> 若数据库中已有多个历史账号，建议显式填写要复用的账号 UUID。

---

## 2. SMTP 配置

用户后续提供的「发送验证码邮箱」填入 `CINDY_SMTP_FROM`；SMTP 认证信息只放环境变量，
不要写进仓库或日志。

| 变量 | 默认 | 说明 |
|---|---|---|
| `CINDY_SMTP_ADDR` | `127.0.0.1:1025` | SMTP 主机:端口；本地 Mailpit 默认 1025 |
| `CINDY_SMTP_FROM` | `no-reply@cindy.local` | 发件人地址（用户提供的发码邮箱） |
| `CINDY_SMTP_USERNAME` | 空 | SMTP 用户名；为空则不认证 |
| `CINDY_SMTP_PASSWORD` | 空 | SMTP 密码 / 授权码 |
| `CINDY_SMTP_TLS` | `auto` | `auto` / `starttls` / `ssl` / `none` |
| `CINDY_SMTP_AUTH` | `auto` | `auto` / `plain` / `login` / `none` |
| `CINDY_DEV_FIXED_CODE` | dev 为 `123456` | 仅用于手机短信 dev 固定码；生产必须清空 |
| `CINDY_EMAIL_DEV_FIXED_CODE` | 空 | 邮箱 dev 固定码；配置 SMTP 后保持为空，才会发送真实随机验证码 |

`auto` 规则：
- 端口 `465` → 隐式 SSL；
- 端口 `1025` / `25` 或本地回环地址 → 明文（本地 Mailpit）；
- 其它端口 → STARTTLS（`TLSMandatory`）。

常见配置：
- 企业邮箱 / QQ / 163 等 `465`：`CINDY_SMTP_TLS=ssl`，填写用户名和授权码；
- `587`：`CINDY_SMTP_TLS=starttls`；
- 本地 Mailpit：保持默认 `127.0.0.1:1025`，无需用户名密码。

未配置 SMTP 且没有 dev 固定码时，`POST /api/auth/email/request-code` 返回
`503 EMAIL_NOT_CONFIGURED`；发送失败返回 `502 EMAIL_SEND_FAILED`；冷却中返回
`429 RATE_LIMITED` + `Retry-After: 60`。

### 2.1 Admin 可视化管理（推荐）

打开 Admin 控制台 → **Operations → Email delivery**（`/email`），可直接填写并保存：

- SMTP 服务器（`host:port`）
- 发件邮箱
- 用户名
- 密码 / 授权码（留空表示保留已保存的密码）
- TLS（auto / starttls / ssl / none）
- 认证方式（auto / plain / login / none）

保存后**下一封验证码邮件立即使用新配置**，无需重启服务端。右侧「Send a test email」
会真实发送一封测试邮件，便于当场验证服务器、授权码与 TLS。

实现与安全约定：

- 配置保存在 `app_settings` 的 `email.smtp`，优先于 `.env`；后台显示 `source=database`。
- 密码使用 `CINDY_MASTER_KEY` 派生的 AES-GCM 加密后入库；GET 接口只返回
  `passwordSet`，不回传明文。更换 `CINDY_MASTER_KEY` 后旧密码无法解密，需要在
  Admin 页面重新填写授权码。
- Mailer 在每次发信时读取数据库配置，因此多实例也会在保存后立即生效。
- 接口（需要 Admin 会话；写操作需要 `config.write` 权限）：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/api/v1/settings/email` | 返回当前生效配置（不含密码） |
| PUT | `/admin/api/v1/settings/email` | 保存配置；`password` 空 = 保留旧密码 |
| POST | `/admin/api/v1/settings/email/test` | `{to}`；用当前配置真实发送测试邮件 |

---

## 3. 端点

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/auth/providers` | 同时下发 `email:true`、`phone:true` |
| POST | `/api/auth/email/request-code` | `{email}`；生成 6 位码、10 分钟有效 |
| POST | `/api/auth/email/verify-code` | `{email, code, deviceId, clientType}`；返回登录结果 |
| POST | `/api/auth/phone/request-code` | 手机号登录（Aliyun SMS） |
| POST | `/api/auth/phone/verify-code` | 手机号验证码登录 |
| POST | `/api/auth/discovery` | 邮箱 discover；个人邮箱返回 `email_code` |
| POST | `/api/auth/refresh` | 刷新令牌 |
| POST | `/api/auth/logout` | 登出 |

验证码只存 `crypto.HashToken(code)` 的哈希；5 次错误后作废；消费后不可复用。
邮箱与手机号在写入前分别做小写 / E.164 归一化。

---

## 4. 客户端

### 桌面端

`apps/desktop/src/renderer/components/login/LoginPage.tsx`
- `providers.email && providers.phone` 时显示「手机号登录 / 邮箱登录」切换。
- 切换会清空输入与本地格式错误；邮箱走 `discover → method-choice → 验证码`，
  手机号直接 `request-code`。
- 邮箱格式本地校验 `isValidEmail`，手机号由服务端校验。

### 移动端

`apps/mobile/app/(auth)/login.tsx`
- 同样的切换行；手机号输入在 CN 构建保留 `+86` 前缀清洗。
- 邮箱输入使用 `email-address` 键盘与 `email` autocomplete。

### Admin

`cindy-admin/src/pages/Accounts.tsx`
- 账户列表的登录标识优先显示邮箱；没有邮箱时显示手机号。
- 账户详情「Identities」按类型显示邮件 / 电话图标，并保留 verified 状态。
- 账户搜索支持邮箱、手机号、显示名称、组织名与账号 ID。

---

## 5. 验证

```bash
# 1) 邮箱验证码（dev 固定码）
curl -s -X POST http://127.0.0.1:19828/api/auth/email/request-code \
  -H 'Content-Type: application/json' -d '{"email":"me@example.com"}'
curl -s -X POST http://127.0.0.1:19828/api/auth/email/verify-code \
  -H 'Content-Type: application/json' \
  -d '{"email":"me@example.com","code":"123456","deviceId":"dev","clientType":"desktop"}'

# 2) 手机号验证码登录同一账号
curl -s -X POST http://127.0.0.1:19828/api/auth/phone/request-code \
  -H 'Content-Type: application/json' -d '{"phone":"13900000000"}'
curl -s -X POST http://127.0.0.1:19828/api/auth/phone/verify-code \
  -H 'Content-Type: application/json' \
  -d '{"phone":"13900000000","code":"123456","deviceId":"dev","clientType":"desktop"}'

# 3) 检查两个身份是否同一 account_id
docker exec cindy-postgres psql -U cindy -d cindy -c \
  "select type, identifier, account_id from auth_identities where identifier in ('me@example.com','+8613900000000');"
```

## 6. 生产检查清单

- [ ] `CINDY_DEV_FIXED_CODE` 与 `CINDY_EMAIL_DEV_FIXED_CODE` 均已清空。
- [ ] 在 Admin → Email delivery 保存发件邮箱、SMTP 服务器与授权码，并发送测试邮件成功。
- [ ] SMTP 用户名/密码/授权码未提交仓库；`.env` 仅作为无数据库配置时的兜底。
- [ ] TLS 与邮箱服务商要求一致（465=ssl，587=starttls）。
- [ ] `CINDY_MASTER_KEY` 保持稳定；更换后需在 Admin 重新填写授权码。
- [ ] `CINDY_SINGLE_USER_MODE=true`；如需固定账号，填写
      `CINDY_SINGLE_USER_ACCOUNT_ID`。
- [ ] 用邮箱与手机号各登录一次，确认 `/api/me` 返回同一 `membershipId`。
