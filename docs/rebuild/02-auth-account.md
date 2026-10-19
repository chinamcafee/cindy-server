# 02 · 认证与账号域（Auth & Account）

## 0. 文档元信息

| 项 | 内容 |
|---|---|
| 能力域 | 认证与账号（auth-server），含登录方式发现、验证码、社交与企业 SSO、Passport/Membership、令牌轮换、账号注销、资料与人机验证 |
| 文档编号 | `02-auth-account` |
| 阶段 | 阶段 1：客户端契约逆向重建，可直接作为 Go 服务端实现输入 |
| 证据基线 | 客户端仓 `cindy` 当前工作区；服务端仓 `cindy-server` 尚无实现源码，仅有本文档目录 |
| 证据等级 | A = 【契约】源码契约（`packages/auth-client` 的 zod schema、desktop/mobile 调用点、契约测试）；B = 【文档】仓内跨端文档（`docs/*.md`）；C = 【推测】由字段/错误码/行为反推，正文显式标注 |
| 总体结论强度 | 端点路径、请求/响应字段、客户端状态机为 A/B；服务端内部表结构、TTL、限流阈值、密钥算法为 C，需实现时定稿 |
| 源文件根 | 下文 `R` = `/Users/changzechuan/InnovationProjects/CindyProjects/cindy` |

**源文件索引（本分册实际依据）**

- 契约层：`R/packages/auth-client/src/client.ts`、`types.ts`、`emailLoginDiscovery.ts`、`orgRealmDiscovery.ts`、`sessionRealm.ts`、`accountMetadata.ts`、`src/__tests__/client.test.ts`
- Desktop：`R/apps/desktop/src/main/authManager.ts`、`authHostedCallback.ts`、`authLoopbackCallback.ts`、`authRefreshFailure.ts`、`authRefreshBackoff.ts`、`authAccountLogoutPolicy.ts`、`authTokenClaims.ts`、`accountDeletionIpc.ts`、`profileEdit.ts`、`cindy-brain/index.ts`、`cindy-brain/connectionTokenProvider.ts`、`device-link/providerShareApi.ts`、`shared/authIpc.ts`
- Mobile：`R/apps/mobile/src/auth/AuthContext.tsx`、`nativeSocial.ts`、`oauthCallback.ts`、`pkce.ts`、`loginCaptchaUrl.ts`、`loginCaptchaMessage.ts`、`deviceId.ts`、`secureStorage.ts`、`cnPhone.ts`
- 配置/文档/文案：`R/config/endpoint.json`、`endpoint.global.json`、`endpoint-manifest-mirrors.json`；`R/docs/desktop-login-hosted-callback.md`、`auth-realm-routing.md`、`provider-sharing-contract.md`；`R/apps/desktop/src/renderer/i18n/locales/zh-CN/common.json`（`login.errors`、`accountDeletion`）

**边界声明**：本文只重建“客户端观察并依赖”的 auth-server wire 契约。Web Console、组织成员管理、邀请、席位、订阅账单、MFA/Passkey、会话列表、审计查询等未在客户端出现的接口不在本文契约内，仅在 §9 作为缺口列出。凡客户端本地合成的错误码，均不得实现为服务端 wire 码。

> **个人自建补充**：AUTH-03 与 AUTH-04 可平行作为登录方式；开启
> `CINDY_SINGLE_USER_MODE=true` 后，首次验证任一邮箱/手机号即绑定到同一固定账号。
> SMTP 配置、客户端切换、Admin 身份展示与生产检查清单见
> [`../architecture/03-auth/email-login.md`](../architecture/03-auth/email-login.md)。

---

## 1. 能力总览

| 编号 | 能力 | 客户端依赖 | 优先级 |
|---|---|---|---|
| AUTH-01 | 双区域独立部署与 `region` 自报 | Desktop/Mobile 端点清单、登录前 `providers` 校验、跨区组织发现 | P0 |
| AUTH-02 | 登录方式能力发现 `GET /api/auth/providers` | 登录页首屏、邮箱/手机/社交入口显隐、Turnstile 开关 | P0 |
| AUTH-03 | 邮箱验证码发码与核验 | Desktop/Mobile 主登录路径 | P0 |
| AUTH-04 | 手机验证码发码与核验 | CN 主登录路径；Global 手机入口按 providers 显隐 | P0 |
| AUTH-05 | 邮箱登录方式发现 `POST /api/auth/discovery` | 邮箱提交后的 `email_code` / 企业 SSO 分流 | P0 |
| AUTH-06 | 企业 SSO 发现 `POST /api/auth/sso/discovery` | 企业入口、邮箱域名跨区发现、登录后企业提示 | P1 |
| AUTH-07 | 社交浏览器 OAuth（authorize + PKCE 兑换） | Desktop 全社交登录、Global Android Apple 回落、移动浏览器授权 | P1 |
| AUTH-08 | 社交原生凭证兑换（Apple/Google/微信） | Mobile iOS/Android 原生登录 | P1 |
| AUTH-09 | 企业 SSO 授权（OIDC/SAML/CAS） | Desktop/Mobile 企业登录与 IdP 跳转 | P1 |
| AUTH-10 | PKCE 授权码兑换 `POST /api/auth/token` | 所有浏览器授权、Desktop 托管回调 | P0 |
| AUTH-11 | Desktop 托管回调轮询 | Desktop 当前默认浏览器登录链路 | P0 |
| AUTH-12 | Passport / Account 会话与多身份选择 | 多 membership 账号、保存账号、切换账号 | P1 |
| AUTH-13 | 联系方式补绑（binding） | 新 SSO/社交身份缺少手机/邮箱时 | P1 |
| AUTH-14 | SSO 联系方式二次验证 | 企业首次登录、IdP 联系方式待验证 | P1 |
| AUTH-15 | Resource token 刷新与轮换 | 所有已登录会话续期、多实例竞态恢复 | P0 |
| AUTH-16 | 登出（Resource / Account） | 设置页登出、切换账号前撤销 | P0 |
| AUTH-17 | 个人资料 `PATCH /api/me/profile` | 设置页改名、头像上传后回写 | P1 |
| AUTH-18 | 身份名片 `POST /api/me/identity-card` | 供应商分享跨账号身份证明 | P1 |
| AUTH-19 | 插件 Connection token `POST /api/auth/connections/token` | 企业插件 OIDC 调用 | P2 |
| AUTH-20 | 账号注销生命周期 | 设置页注销、登录页 30 天状态轮询 | P1 |
| AUTH-21 | Turnstile 人机验证 | 邮箱/手机发码前闸门 | P1 |
| AUTH-22 | 统一错误体、429/Retry-After、区域 fail-closed | 全客户端错误处理与令牌保留策略 | P0 |

---

## 2. 服务部署与端点基址

### 2.1 双区域独立部署

auth-server 必须按 `cn` / `global` 两套独立部署，数据库、Redis、JWT 签名密钥、JWKS、邮件/短信通道、Turnstile 密钥、社交平台应用、IdP 信任配置均不跨区共享。客户端从不把一区 token 发往另一区。

| 区域 | `authApiBaseUrl` | `authDesktopCallbackUrl`（当前清单） |
|---|---|---|
| cn | `https://auth.cindy.com.cn` | `https://auth.cindy.com.cn/api/auth/desktop/callback` |
| global | `https://auth.cindy.app` | `https://auth.cindy.app/api/auth/desktop/callback` |

- 证据：`R/config/endpoint.json:3,21`、`R/config/endpoint.global.json:3,21`。本地 dev 示例为 `http://localhost:3344`（`R/config/endpoint.dev.json.example`），可沿用 00-overview 的端口规划。
- 两份生产清单的 `authDesktopCallbackUrl` 均非空，因此 Desktop 当前实际走**托管回调轮询**；`docs/desktop-login-hosted-callback.md` 中“当前为空、默认 loopback”的描述已过时，但 loopback 分支仍须保留，供清空字段回滚。
- 每个部署必须配置 `PUBLIC_BASE_URL`（外网 origin，如 `https://auth.cindy.app`）。它用于：托管回调 `redirect_uri` 同源放行、结果页链接、身份名片 `iss`、JWKS 地址、邮件/短信中的链接。不得从请求 `Host` / `X-Forwarded-Host` 推导。
- 结果页静态资源由客户端仓导出（`pnpm --filter desktop run export:login-callback-template`），服务端只负责按 manifest 的 `scriptHashes` 拼 CSP，不手写 HTML。

### 2.2 区域校验与路由规则

1. `GET /api/auth/providers` 与 `POST /api/auth/sso/discovery` 的响应必须包含 `region`，且等于部署自身区域。客户端发现不一致即 `REGION_MISMATCH`（`statusCode: 0`）；在双区组织发现中则升级为 `ORG_REALM_UNAVAILABLE` 并 fail-closed。
2. 服务端启动时必须校验 `REGION` 已设置为 `cn` 或 `global`，缺失/非法直接拒绝启动；区域是部署不变量，不是请求参数。
3. 个人邮箱验证码、手机验证码、社交登录固定使用安装包 `buildRegion`；只有企业 SSO 可因发现结果切到对端 `sessionRealm`。个人 token、社交凭据、两区 Passport 不得互相迁移。
4. 客户端对组织发现执行并行双区探测，服务端只需保证单区语义正确：
   - 一区成功、另一区 404 `ORG_SSO_NOT_FOUND` → 采用成功区；
   - 两区均 404 → 保留 `ORG_SSO_NOT_FOUND`；
   - 两区均成功 → 客户端合成 409 `ORG_REALM_AMBIGUOUS`，服务端不参与；
   - 任一区网络失败、5xx、非法响应、`region` 不符 → 客户端合成 503 `ORG_REALM_UNAVAILABLE`，即使另一区成功也不采用。
5. 企业发现只匹配“已验证域名 + 已启用 SSO 连接”；未验证域名、未启用连接返回 404 `ORG_SSO_NOT_FOUND`。`POST /api/auth/sso/discovery` 允许以企业 ID、slug、已验证域名查询（文档另称支持企业名称，客户端只发前三种）。
6. 端点清单的区域身份由构建期受信域决定：`*.cindy.com.cn` 归 cn，`*.cindy.app` 归 global；远端清单不得自报区域。离线清单不授予离线登录能力，吊销仍由 auth-server 执行。

### 2.3 必须存在与必须不存在的路由

- 必须存在：`/api/auth/*` JSON API、`/api/me`、`/api/me/profile`、`/api/me/identity-card`、`/captcha/turnstile`、`/desktop/login-callback`、provider/IdP 内部回调路由（如 `googleCallbackUrl()`）、`/console/callback`（既有先例）。必须不存在：`GET /api/auth/desktop/callback` 仅作为 `redirect_uri` 的“托管模式”标识，不得注册可访问路由，访问应为 404；浏览器永远不会被重定向到该地址。
- 非生产：`POST /api/auth/dev-login`、`GET /api/health` 只属于本地 mock/dev server，不是生产契约。

---

## 3. 端点契约

### 3.1 通用约定

- **方法选择**：客户端 `body === undefined` 用 GET，否则 POST；固定 `Content-Type: application/json`；仅显式传 token 的端点带 `Authorization: Bearer <token>`。证据 `R/packages/auth-client/src/client.ts:525-534`。
- **成功响应**：必须通过对应 zod schema；多余字段被忽略（可向后兼容新增），少字段/改名/类型变化即客户端 `INVALID_RESPONSE`。因此服务端只能做“加字段”演进，不能删除既有必填字段。
- **错误响应与 429**：同时接受 `{ error: { code, message } }` 与扁平 `{ code, message }`；建议统一输出嵌套形。`message` 仅供诊断，客户端从不透传服务端 message 给用户。429 无论 body 是否合法，一律归一为 `RATE_LIMITED`；解析 `Retry-After`（纯数字秒，或带月份的 HTTP-date；后者优先用响应 `Date` 头换算，避免本机时钟偏差）。非法值不猜冷却时间。证据 `client.ts:76-95,540-579`。
- **超时**：默认 15s；托管回调 poll 30s（允许服务端长轮询约 20s）；`/api/auth/refresh` 与 `/api/auth/account/refresh` 客户端 `timeoutMs: 0`（不超时），网关仍应设 30-60s 上限。
- **字段上限**：邮箱 ≤254；组织标识 ≤253；IPC identifier ≤320；opaque id ≤256；验证码 wire 为 6 位数字（客户端输入 6 位）；`captchaToken` ≤2048；displayName 服务端上限 100（客户端收紧到 40）；Connection audience ≤64；身份名片 `nonce` 1..128，`card` ≤8192。
- **`LoginOutcome` 四态**（`types.ts:187-233`）：

| `status` | 必填字段 | 可选字段 | 语义 |
|---|---|---|---|
| `ok` | `accessToken`、`refreshToken`、`membership` | `accountToken` + `accountRefreshToken`（必须成对）、`accountDeletionRestored: true` | 登录完成 |
| `select_account` | `loginTicket`、`accounts[]`（≥1） | 同上 | 需选择个人/企业身份 |
| `binding_required` | `bindType`（`phone`/`email`）、`bindTicket` | 无 | 需补绑联系方式 |
| `sso_verification_required` | `verificationTicket`、`channel`（`email`/`sms`）、`targetMasked` | 无 | 需二次验证 IdP 联系方式 |

- **HTTP 状态与标准错误面**：200 正常；400 参数/验证码/人机验证；401 凭证/票证无效、refresh 确定性失效；403 企业要求 SSO、权限不足；404 组织/成员/receipt 不存在；409 注销进行中等冲突；410 注销挑战失效（可接受）；429 限流；5xx 临时失败。下表所有 JSON 端点均适用 `400 INVALID_PARAMS`、`429 RATE_LIMITED`、`5xx`（逐项见 §3.8）；客户端另会合成 `NETWORK_ERROR`、`REQUEST_TIMEOUT`、`USER_CANCELLED`、`INVALID_RESPONSE`、`AUTH_REQUEST_FAILED`。端点行只列“除标准面外”的业务码。

### 3.2 发现与发码

| ID | 方法与路径 | 用途 | 请求字段 | 响应字段 | 鉴权 | 幂等/限流 | 业务错误码（另含标准面） |
|---|---|---|---|---|---|---|---|
| EP-01 | `GET /api/auth/providers` | 登录方式能力发现 | 无 | `region`、`attribution`（phone/email）、`email` bool、`phone` bool、`social[]`（apple/google/wechat）、`captcha?{provider:"turnstile",siteKey,requiredFor[]}` | 无 | 只读；建议缓存 ≤30s；按 IP 60/min | `REGION_MISMATCH`（客户端合成） |
| EP-02 | `POST /api/auth/discovery` | 邮箱登录方式探测 | `email` | `methods[]`：`{type:"email_code"}` 或 `{type:"sso",connectionId,protocol,orgName,connectionName,ssoRequired}` | 无 | 按 IP 30/min；按邮箱 10/min | `400 INVALID_PARAMS`；`429` |
| EP-03 | `POST /api/auth/sso/discovery` | 按企业 ID/slug/已验证域名发现已启用 SSO | `org`（trim+lower，≤253） | `region`、`orgName`、`connections[]{connectionId,protocol,connectionName}`；空数组合法 | 无 | 按 IP 60/min | `404 ORG_SSO_NOT_FOUND`；`REGION_MISMATCH`（客户端合成） |
| EP-04 | `POST /api/auth/email/request-code` | 发送邮箱验证码 | `email`、`locale?`、`captchaToken?` | `{status:"sent"}` | 无 | 单邮箱 60s 冷却、5/h、20/day；按 IP 10/min | `400 CAPTCHA_REQUIRED`、`CAPTCHA_INVALID`、`CAPTCHA_UNAVAILABLE`、`429 RATE_LIMITED` |
| EP-05 | `POST /api/auth/phone/request-code` | 发送短信验证码 | `phone`（E.164）、`locale?`、`captchaToken?` | `{status:"sent"}` | 无 | 同上，按手机号冷却 | 同 EP-04 |
| EP-06 | `POST /api/auth/email/verify-code` | 邮箱验证码核验并登录 | `email`、`code`、`deviceId`、`clientType`（desktop/mobile/web）、`locale?` | `LoginOutcome` | 无 | 单码 5 次尝试；按 IP 10/min | `400/401 INVALID_CODE`、`CODE_ATTEMPTS_EXCEEDED`、`403 SSO_LOGIN_REQUIRED`、`429` |
| EP-07 | `POST /api/auth/phone/verify-code` | 手机验证码核验并登录 | `phone`、`code`、`deviceId`、`clientType`、`locale?` | `LoginOutcome` | 无 | 同上 | 同 EP-06 |

**补充语义**

- `providers` 是登录页唯一能力源。`attribution` 虽为必填 wire 字段，当前 UI 不按它分支；`social` 只允许三个已知值；`captcha` 关闭时整字段省略（`undefined` = 不需挑战）。`requiredFor` 客户端只预认 `email_request_code` / `phone_request_code`，未知动作忽略，因此服务端可后置开启短信挑战而不发客户端版。
- `discovery` 的未知 method 类型被丢弃；若列表非空但全部不可识别，客户端报 `INVALID_RESPONSE`。服务端若返回空数组，客户端随后走普通邮箱验证码。
- `sso/discovery` 的 `200 + connections: []` 会被客户端显式映射为 `ORG_SSO_NOT_FOUND`（在 region 校验之后），因此“企业存在但未启用 SSO”可与 404 等价表达。发码接口对“账号不存在/已存在”应统一返回 `{status:"sent"}`，避免账号枚举；真实投递失败也不应泄露存在性（建议进入重试队列）。

### 3.3 浏览器授权与授权码

| ID | 方法与路径 | 用途 | 请求字段 | 响应字段 | 鉴权 | 幂等/限流 | 业务错误码（另含标准面） |
|---|---|---|---|---|---|---|---|
| EP-08 | `GET /api/auth/social/{provider}/authorize` | 社交 OAuth 浏览器入口 | query：`client_type`、`device_id`、`redirect_uri`、`code_challenge`、`code_challenge_method=S256`、`client_state`、`ui_locale?` | 302 到 provider；或最终 302 到托管结果页/loopback 回调 | 无 | 按 IP 20/min；`client_state` 一次性占位 | `400 INVALID_PARAMS`（缺 state/重复 state/redirect 非法）；provider 错误经 `error` 透传 |
| EP-09 | `GET /api/auth/sso/{connectionId}/authorize` | 企业 SSO 浏览器入口（OIDC/SAML/CAS） | 同 EP-08 | 同 EP-08 | 无 | 同 EP-08 | 同 EP-08；连接不存在建议 404 |
| EP-10 | `POST /api/auth/token` | PKCE 授权码兑换令牌 | `grantType:"authorization_code"`、`code`、`codeVerifier`、`deviceId`（无 clientType/locale） | `LoginOutcome` | 无（`codeVerifier` 即凭证） | 授权码 60s、一次性；按 IP 30/min | `400/401 INVALID_AUTH_CODE`、`400 INVALID_PARAMS`、`400 STATE_MISMATCH`（若服务端管理 state）、`429` |
| EP-11 | `POST /api/auth/desktop/callback/poll` | 托管回调取回一次性授权码 | `pollSecret`、`deviceId`（服务端不校验） | 四态：`{status:"pending"}` / `{status:"ok",code}` / `{status:"error",error}` / `{status:"expired"}` | 无（`pollSecret` 即取回凭证） | 读取幂等；按 IP 300/60s；未知 secret 必须 `pending` | 非 2xx 标准错误体；`429 RATE_LIMITED`；不得对未知 secret 返回 4xx/expired |

**补充语义**

- `authorize` 的 `redirect_uri` 白名单至少包含 `${PUBLIC_BASE_URL}/api/auth/desktop/callback`（同源恒放行）与既有 `${PUBLIC_BASE_URL}/console/callback`；其他 URI 按严格精确匹配校验。
- 走托管模式时 `client_state` 必填；服务端建登录事务时用 Redis `SET NX` 占位（TTL 10 分钟，与事务对齐），重复 `client_state` 返回 400 `INVALID_PARAMS`。占位值带一次性 nonce，失败时比较删除，仅删自己写入的占位。
- provider 回调后照常签发授权码（TTL 60s，自签发起算），但识别到 `redirect_uri` 是托管地址时不把 code 放进 URL，而是按 `client_state` 寄存到 `deskcb:<client_state>`（TTL 5 分钟），再 302 到 `/desktop/login-callback?status=ok|error[&detail=]`。
- poll 的读取是幂等的，不做 GETDEL；同一 `pollSecret` 在 code 存活期内可重复取到同一 code。真正的一次性在 `/api/auth/token`（authCode 侧 GETDEL）。`expired` 只用于 code 自身 60s TTL 已过；寄存记录活 5 分钟以确保能返回该终态。
- `pollSecret` 与 `client_state` 的关系固定为 `client_state = base64url(sha256(pollSecret))`，两端逐字节一致；服务端不得把 `pollSecret` 写入可检索日志。
- `deviceId` 在 poll 中仅为兼容字段，不构成授权判断。

### 3.4 社交原生、身份选择、补绑与二次验证

| ID | 方法与路径 | 用途 | 请求字段 | 响应字段 | 鉴权 | 幂等/限流 | 业务错误码（另含标准面） |
|---|---|---|---|---|---|---|---|
| EP-12 | `POST /api/auth/social/apple` | Apple 原生凭证兑换 | `identityToken`、`authorizationCode?`、`rawNonce`、`user?{name?}`、`email?`（string 或 null）、`deviceId`、`clientType`、`locale?` | `LoginOutcome` | 无 | 按设备/IP 10/min | `400/401 SOCIAL_TOKEN_INVALID`、`SOCIAL_PROVIDER_DISABLED`、`SOCIAL_PROVIDER_NOT_CONFIGURED`、`SOCIAL_PROVIDER_UNAVAILABLE`、`429` |
| EP-13 | `POST /api/auth/social/google` | Google 原生凭证兑换 | `idToken`、`deviceId`、`clientType`、`locale?` | `LoginOutcome` | 无 | 同上 | 同 EP-12 |
| EP-14 | `POST /api/auth/social/wechat` | 微信授权码兑换 | `code`、`deviceId`、`clientType`、`locale?` | `LoginOutcome` | 无 | 同上 | 同 EP-12 |
| EP-15 | `POST /api/auth/select-account` | 旧路径：用 `loginTicket` 选择身份 | `loginTicket`、`accountId`、`deviceId` | `LoginOutcome` | 无（ticket 即凭证） | ticket 一次性；按 IP 20/min | `401 INVALID_LOGIN_TICKET`、`429` |
| EP-16 | `POST /api/auth/binding/request-code` | 补绑发码 | `bindTicket`、`phone` 或 `email`（字段名随 `bindType`，不是统一 `contact`）、`locale?` | `{status:"sent"}` | 无（bindTicket） | 单目标 60s 冷却；ticket 一次性 | `401 INVALID_BIND_TICKET`、`400 INVALID_PARAMS`、`429` |
| EP-17 | `POST /api/auth/binding/verify` | 补绑核验并完成登录 | `bindTicket`、`phone` 或 `email`、`code`、`deviceId` | `LoginOutcome` | 无（bindTicket） | 5 次尝试；ticket 一次性 | `401 INVALID_BIND_TICKET`、`400 INVALID_CODE`、`CODE_ATTEMPTS_EXCEEDED`、`429` |
| EP-18 | `POST /api/auth/sso/verification/request-code` | SSO 联系方式二次验证发码 | `verificationTicket`、`deviceId` | `{status:"sent"}` | 无（verificationTicket） | 可重复发码（客户端无 60s 倒计时）；建议 60s 冷却 | `401 INVALID_SSO_VERIFICATION_TICKET`、`429` |
| EP-19 | `POST /api/auth/sso/verification/verify` | 二次验证核验 | `verificationTicket`、`code`、`deviceId` | `LoginOutcome` | 无（verificationTicket） | 5 次尝试；ticket 一次性 | `401 INVALID_SSO_VERIFICATION_TICKET`、`400 INVALID_CODE`、`CODE_ATTEMPTS_EXCEEDED`、`429` |

**补充语义**

- Apple 首次授权需把明文 `email` 与 `user.name` 一并补传；服务端应以 `identityToken` 的 `sub` 为主键建立/关联 Passport，`rawNonce` 必须与客户端交给 SDK 的 SHA-256 nonce 校验一致。
- 微信仅 CN iOS 展示；Global Android/CN Android 不展示。服务端仍应校验区域开关，不能只依赖客户端显隐。
- `select-account` 的 `accountId` 是 membership id。新版优先走 Account token（EP-21/EP-22），旧路径仅在服务端未下发 account token 时使用。

### 3.5 令牌与会话

| ID | 方法与路径 | 用途 | 请求字段 | 响应字段 | 鉴权 | 幂等/限流 | 业务错误码（另含标准面） |
|---|---|---|---|---|---|---|---|
| EP-20 | `POST /api/auth/refresh` | Resource refresh 轮换 | `refreshToken`、`deviceId` | `{accessToken,refreshToken,membership}` | 无（refresh token 即凭证） | 一次性轮换；单会话 30/min（60s 窗口）；单 IP 60/min | `401 INVALID_REFRESH_TOKEN`、`REFRESH_TOKEN_EXPIRED`、`DEVICE_MISMATCH`、`MEMBERSHIP_DISABLED`、`ACCOUNT_UNAVAILABLE`、`429` |
| EP-21 | `GET /api/auth/account` | 列出 Passport 下 memberships | 无 | `{memberships:[Membership + orgSlug]}`，`orgSlug` 为 string 或 null | `Bearer accountToken` | 只读；按 token 60/min | `401 ACCOUNT_UNAVAILABLE`、`429` |
| EP-22 | `POST /api/auth/account/exchange` | Account token + membershipId 换 Resource token | `{membershipId}` | `{accessToken,refreshToken,membership}` | `Bearer accountToken` | 每次签发新会话；建议撤销同设备旧 family | `401/404`（账号或 membership 不可用）、`429` |
| EP-23 | `POST /api/auth/account/refresh` | Passport refresh 轮换 | `{accountRefreshToken,deviceId}` | `{accountToken,accountRefreshToken}` | 无（account refresh token 即凭证） | 一次性轮换；单飞 | `401 INVALID_REFRESH_TOKEN`、`REFRESH_TOKEN_EXPIRED`、`DEVICE_MISMATCH`、`429` |
| EP-24 | `POST /api/auth/account/logout` | 注销 Account 会话 | `{}` | `{status:"ok"}` | `Bearer accountToken` | 幂等 | `401`、`429` |

**补充语义**

- Resource refresh 返回的 `membership` 是服务端权威快照；客户端用它更新展示资料与保存账号元数据。refresh token 一次性轮换，服务端按 `(membership, deviceId)` 绑定；同 family 重用视为泄露，建议撤销整个 family。
- `DEVICE_MISMATCH` 不得删除客户端磁盘 token（token 对真实设备可能仍有效），但接收方进程应登出。`INVALID_REFRESH_TOKEN` / `REFRESH_TOKEN_EXPIRED` / `MEMBERSHIP_DISABLED` 才是确定性清态码；429/5xx/断网必须保留 token。
- Account token 只在一个登录的 membership 选择阶段存活；客户端不持久化、不续期、不用于业务请求。Account refresh 只在“保存账号切换/元数据同步”时使用。
- `account/exchange` 没有幂等键；客户端每次用户确认只调一次。服务端应保证同一 `accountToken` 可安全重试（Network 不确定时），并让同一设备旧 Resource 会话失效或并存策略明确。

### 3.6 资料、名片、插件令牌与账号注销

| ID | 方法与路径 | 用途 | 请求字段 | 响应字段 | 鉴权 | 幂等/限流 | 业务错误码（另含标准面） |
|---|---|---|---|---|---|---|---|
| EP-25 | `GET /api/me` | 当前身份与全部 identities | 无 | `{membership,passportId,identities:[Membership]}` | `Bearer accessToken` | 只读；按 token 60/min | `401 ACCOUNT_UNAVAILABLE`、`429` |
| EP-26 | `PATCH /api/me/profile` | 自助改昵称/头像 | `displayName?`（≤100）、`avatarUrl?`（string 或 null；null 清除头像）；至少一个字段 | `{membership?}` | `Bearer accessToken` | 按值幂等；按账号 10/min | `401 ACCOUNT_UNAVAILABLE`、`400 INVALID_PARAMS`、`429` |
| EP-27 | `POST /api/me/identity-card` | 签发供应商分享身份名片 | `audience:"cindy-provider-share"`、`purpose:"share-link"/"share-request"/"share-session"`、`nonce`（1..128） | `{card,expiresAt}` | `Bearer accessToken` | nonce 一次性（建议 5 分钟去重）；按账号限流 | `401`、`400 INVALID_PARAMS`（nonce 重放/长度）、`429` |
| EP-28 | `POST /api/auth/connections/token` | 插件 OIDC Connection token | `audience`（`${orgSlug}:${pluginSlug}`，≤64） | `{token,expiresIn,audience}`；`expiresIn ≤1800`、token ≤32KB、audience 必须回显 | `Bearer accessToken` | 可缓存至 exp-60s；按 membership 30/min | `401`、`403 PERMISSION_DENIED`（非 org/插件无权）、`400 INVALID_PARAMS`、`429` |
| EP-29 | `GET /api/auth/account/deletion` | 查询注销可用性与验证渠道 | 无 | `{available,verification?{channel,maskedTarget},manualAppleRevocationRequired}` | `Bearer accessToken` | 只读 | `401`、`429` |
| EP-30 | `POST /api/auth/account/deletion/challenge` | 申请注销验证码与 receipt | `locale?` | `{challengeId,receiptToken,channel,maskedTarget,expiresAt}` | `Bearer accessToken` | 同一账号仅一个活跃挑战 | `400 ACCOUNT_DELETION_UNAVAILABLE`、`409 ACCOUNT_DELETION_PENDING`、`ACCOUNT_DELETION_PROCESSING`、`429` |
| EP-31 | `POST /api/auth/account/deletion/confirm` | 确认注销 | `challengeId`、`receiptToken`、`code`、`acknowledged:true` | `AccountDeletionStatus` | `Bearer accessToken` | 单飞；challenge 一次性 | `400/410 ACCOUNT_DELETION_CHALLENGE_INVALID`、`409 PENDING/PROCESSING`、`400 INVALID_CODE`、`CODE_ATTEMPTS_EXCEEDED`、`429` |
| EP-32 | `POST /api/auth/account/deletion/status` | 用 receipt 查询注销状态 | `receiptToken` | `{status,requestedAt,deleteAfter,processingAt?,completedAt?,cancelledAt?}` | 无（receipt 即凭证） | 只读；登录页 30s 轮询 | `400/404 ACCOUNT_DELETION_RECEIPT_INVALID`、`429` |
| EP-33 | `POST /api/auth/logout` | 注销当前 Resource 会话 | `{}`（auth-client）或 `{deviceId}`（Desktop best-effort）；服务端须容忍缺省 | `{status:"ok"}` | `Bearer accessToken` | 幂等 | `401`、`429` |

**补充语义**

- `PATCH /api/me/profile` 只接受 `displayName` 与 `avatarUrl`；头像字节先由 oss-server 预签名直传，服务端只收 `publicUrl`。`avatarUrl:null` 清除头像，UI 回落首字母。
- 身份名片必须是 RS256 JWT，沿用本区签名 key 与 JWKS（`kid`），claims 固定：`typ:"identity-card"`、`iss`、`aud:"cindy-provider-share"`、`sub:<membershipId>`、`name`、`picture`、`region`、`purpose`、`nonce`、`iat`、`exp=iat+300`。**只放昵称与头像**，不得放邮箱、手机号（含脱敏）、组织。device-link-server 用 JWKS 验签并核对 `typ/aud/purpose/nonce/exp`。
- Connection token 只对 org membership 签发，audience 必须由服务端重新解析并校验插件归属，不能信任客户端拼接；`expiresIn` 不得超过 1800，token 不得超 32KB。
- 注销状态值只有 `pending` / `processing` / `completed` / `cancelled`；时间字段为 ISO 8601 datetime。确认后本机立即退出，其他客户端在 access token 到期续期时退出；账号进入 **30 天等待期**，期内重新登录可取消。
- 注销 confirm 的网络结果不确定时，Desktop 会用 receipt 调 status 查明；只有 `cancelled` 视为失败。普通 logout 会清 receipt，但**已确认注销的 receipt 有意保留**，供登录页查询等待期状态。

### 3.7 页面与非 JSON 端点

| ID | 方法与路径 | 用途 | 参数 | 响应 | 鉴权 | 幂等/限流 | 错误/状态 |
|---|---|---|---|---|---|---|---|
| EP-34 | `GET /captcha/turnstile` | auth-server 托管 Turnstile 挑战页 | `action`（`email_request_code`/`phone_request_code`）、`theme`（light/dark）、`lang`（BCP-47） | HTML；结果写 `location.hash`：`#cindy-captcha=ok.<token>` / `#cindy-captcha=err.<code>`；移动端另支持 `ReactNativeWebView.postMessage({type:"cindy-captcha",ok,token/code})` | 无 | 按 IP 30/min；挑战总时限 120s | token ≤2048；失败写 `err.<code>`；最终由服务端 Siteverify |
| EP-35 | `GET /desktop/login-callback` | 托管回调结果展示页 | `status=ok/error`、`detail=<错误码>?` | 静态 HTML；成功页 3 秒倒计时关闭，失败页“回到 Cindy” | 无 | `no-store`；按 IP 60/min | `detail` 按 HTML 文本转义；语言跟随 `Accept-Language`，缺省 en；CSP 用模板 manifest 的 script hash |
| EP-36 | `GET /api/auth/desktop/callback` | 不存在的路径，仅作 `redirect_uri` 标识 | 无 | 404 | 无 | 不适用 | 必须 404，不得注册路由 |

**非生产端点**：`POST /api/auth/dev-login`（本地 smoke/mock，body `{deviceId}`）、`GET /api/health`（本地联调）。不属于生产 auth-server 契约，部署时不得暴露。

### 3.8 错误码总表

**服务端 wire 码（应实现）**

| 错误码 | 建议 HTTP | 触发 | 客户端行为 |
|---|---|---|---|
| `INVALID_PARAMS` | 400 | 字段缺失、格式/长度非法、state 缺失或重复 | 展示输入错误 |
| `INVALID_CODE` | 400/401 | 验证码错误或过期 | 停留输码页，可重试 |
| `CODE_ATTEMPTS_EXCEEDED` | 400/429 | 单码尝试次数超限 | 要求重新发码 |
| `RATE_LIMITED` | 429 | 任意限流 | 按 `Retry-After` 显示等待，不自动重试 |
| `CAPTCHA_REQUIRED` | 400 | 发码需要人机验证但未带 token | 客户端重新出题一次后重试 |
| `CAPTCHA_INVALID` | 400 | Turnstile token 无效/过期/动作不符 | 同上，仅一次防循环 |
| `CAPTCHA_UNAVAILABLE` | 503 | Siteverify 不可用或密钥错误 | 提示稍后再试 |
| `SSO_LOGIN_REQUIRED` | 403/409 | 该邮箱/域名企业要求 SSO，个人登录被拒 | 引导企业 SSO |
| `SSO_EMAIL_REQUIRED` | 400/403 | IdP 未返回有效邮箱，无法建立身份 | 提示联系管理员检查 IdP |
| `ORG_SSO_NOT_FOUND` | 404（或 200+空数组） | 企业不存在、域名未验证、无启用连接 | 保留原个人登录/提示未找到 |
| `SOCIAL_TOKEN_INVALID` | 400/401 | Apple/Google/微信凭证校验失败 | 提示第三方授权失效 |
| `SOCIAL_PROVIDER_DISABLED` | 400/403 | 区域或配置关闭该 provider | 隐藏/提示不可用 |
| `SOCIAL_PROVIDER_NOT_CONFIGURED` | 500/503 | 服务端缺少 provider 配置 | 提示不可用 |
| `SOCIAL_PROVIDER_UNAVAILABLE` | 503 | provider 上游不可用 | 提示稍后再试 |
| `STATE_MISMATCH` | 400 | OAuth state 校验失败（若服务端管理 state） | 安全校验失败 |
| `INVALID_AUTH_CODE` | 400/401 | 授权码过期、已用、缺失 | 要求重新登录 |
| `INVALID_LOGIN_TICKET` | 401 | 身份选择票据过期/已用 | 要求重新登录 |
| `INVALID_BIND_TICKET` | 401 | 补绑票据过期/已用 | 要求重新登录 |
| `INVALID_SSO_VERIFICATION_TICKET` | 401 | 二次验证票据过期/已用 | 要求重新发起 SSO |
| `INVALID_REFRESH_TOKEN` | 401 | refresh token 不存在、已轮换、family 重用 | 确定性清态（可能先 replacement-retry） |
| `REFRESH_TOKEN_EXPIRED` | 401 | refresh token 自然过期 | 清态重登 |
| `DEVICE_MISMATCH` | 401 | refresh token 与 deviceId 不匹配 | 本进程登出，不删磁盘 token |
| `MEMBERSHIP_DISABLED` | 401/403 | membership 被禁用/删除 | 清态，提示账号不可用 |
| `ACCOUNT_UNAVAILABLE` | 401/403 | Passport/账号不可用 | 清态或终止操作 |
| `ACCOUNT_DELETION_UNAVAILABLE` | 400 | 账号不满足自助注销条件 | 显示不支持 |
| `ACCOUNT_DELETION_PENDING` | 409 | 已在注销等待期 | 显示等待期 |
| `ACCOUNT_DELETION_PROCESSING` | 409 | 正在删除 | 显示处理中 |
| `ACCOUNT_DELETION_CHALLENGE_INVALID` | 400/410 | challengeId/receipt/code 不匹配或过期 | 重新发码 |
| `ACCOUNT_DELETION_RECEIPT_INVALID` | 400/404 | receipt 不存在/已失效 | 清本地 receipt，停止轮询 |

**资源服务码的边界**：`UNAUTHORIZED`、`TOKEN_EXPIRED`、`INVALID_TOKEN`（资源侧另常见 `AUTH_REQUIRED`）来自 Desktop `serverApiClient` 对资源服务的观察，不是 auth-server 的 refresh 失效表达；auth-server 应使用 `ACCOUNT_UNAVAILABLE`、`INVALID_REFRESH_TOKEN`、`REFRESH_TOKEN_EXPIRED` 等精确码，避免客户端误判为可刷新。

**客户端本地码（不得作为服务端契约实现）**：`NETWORK_ERROR`、`REQUEST_TIMEOUT`、`USER_CANCELLED`、`INVALID_RESPONSE`、`AUTH_REQUEST_FAILED`、`AUTH_SERVICE_UNAVAILABLE`、`ORG_REALM_AMBIGUOUS`、`ORG_REALM_UNAVAILABLE`、`REGION_MISMATCH`、`BROWSER_OPEN_FAILED`、`BROWSER_OPEN_TIMEOUT`、`CALLBACK_LISTENER_FAILED`、`LOGIN_BUSY`、`AUTH_FLOW_SUPERSEDED`、`INVALID_AUTH_ACTION`、`NO_BROWSER_AUTH_IN_PROGRESS`、`CREDENTIAL_STORE_UNAVAILABLE`、`ACCOUNT_NOT_FOUND`、`ACCOUNT_REAUTH_REQUIRED`、`SAVED_ACCOUNT_NOT_FOUND`、`SAVED_ACCOUNT_REAUTH_REQUIRED`、`SAVED_ACCOUNT_MISMATCH`、`PASSIVE_AUTH_MUTATION_BLOCKED`、`ACCOUNT_DELETION_RECEIPT_MISSING`、`ACCOUNT_DELETION_RECEIPT_STORE_FAILED`、`UNAUTHENTICATED`、`PHONE_LOGIN_DISABLED`、`CONNECTION_NOT_FOUND`。其中 `REGION_MISMATCH` 由客户端比较 `providers.region` 合成；服务端只需保证自报区域正确。

---

## 4. 认证流程时序

### 4.1 邮箱验证码登录

1. 登录页先 `GET /api/auth/providers`，按 `email`/`phone` 布尔与区域偏好选择输入形态（global 优先邮箱，cn 优先手机；不并列双 tab）；客户端本地校验邮箱（trim、lower、≤254、`local@domain.tld`）。
2. 若从邮箱入口进入：先提取域名（绝不把完整邮箱发给对端区），并行两区 `POST /api/auth/sso/discovery {org: domain}`。命中对端区则显示 `realm-confirmation`；用户确认后企业 SSO 走对端区，个人验证码仍走安装包区域。
3. 同区 `POST /api/auth/discovery {email}` 获取 `methods`。若只有 SSO 或 `ssoRequired=true`，转企业 SSO；否则展示邮箱验证码。
4. 若 `providers.captcha.requiredFor` 含 `email_request_code`，先打开 `/captcha/turnstile?action=email_request_code&theme=...&lang=...` 取得 token；用户取消则不发码不报错。
5. `POST /api/auth/email/request-code {email, locale, captchaToken?}` → `{status:"sent"}`；客户端进入 60s 重发倒计时（绝对 deadline，休眠恢复自校正，失败不重置）。
6. `POST /api/auth/email/verify-code {email, code, deviceId, clientType, locale}` → `LoginOutcome`。
7. 按 outcome 分流：`ok` 完成；`select_account` → §4.6；`binding_required` → §4.7；`sso_verification_required` → §4.8。个人登录成功后可选用 `membership.email` 域名做一次企业提示；发现失败/歧义/限流一律保留个人登录，不迁移 token。

### 4.2 手机验证码登录

与邮箱对称：`/api/auth/phone/request-code`、`/api/auth/phone/verify-code`，字段为 `phone`。Mobile 仅支持中国大陆 `+86`：输入保留数字、可剥 `86`、截断 11 位，提交拼回 E.164，本地校验 `1[3-9]\d{9}`；Desktop 不本地校验，原样提交由服务端校验。Desktop 若 `providers.phone=false`，发码前本地抛 `PHONE_LOGIN_DISABLED`。

### 4.3 社交登录

- **Desktop（全部走系统浏览器）**：`providers.social` 控制按钮显隐 → 生成 PKCE（32 字节 verifier，S256）与 `client_state` → `GET /api/auth/social/{provider}/authorize` → 用户在 provider 授权 → 回调按 §4.5 或 loopback 取 code → `POST /api/auth/token` → `LoginOutcome`。Desktop loopback 的 `state` 为 `randomUUID()`，仅用于 CSRF 比对，回调后即弃。
- **Mobile 原生优先**：Apple 传 `identityToken` + `authorizationCode?` + `rawNonce`，首次授权补传明文 `email` 与 `user.name`（SHA-256(nonce) 交 SDK，服务端校验原始 nonce）；Google 传 `idToken`（Android 先检查 Play Services）；微信传 `code`（`snsapi_userinfo`，前台计时 8 秒，后台不计）。
- **Mobile 浏览器回落**：`WebBrowser.openAuthSessionAsync`，回调 `cindycn://auth` 或 `cindy://auth`；PKCE verifier 与 state 存 SecureStore，10 分钟 TTL；回调必须精确匹配 `scheme://auth`，state 不匹配即 `STATE_MISMATCH`，缺 code 即 `INVALID_AUTH_CODE`。

### 4.4 企业 SSO（OIDC/SAML/CAS）

1. 用户输入企业 ID/slug/已验证域名，或由邮箱域名触发双区发现；客户端 trim + lower，≤253。
2. `POST /api/auth/sso/discovery {org}` 返回 `region`、`orgName`、`connections[]`。多连接进入 `method-choice`；唯一连接自动开始授权；空连接按 `ORG_SSO_NOT_FOUND`。
3. `GET /api/auth/sso/{connectionId}/authorize`，携带 PKCE、`client_state`、`redirect_uri`、`device_id`、`client_type`、`ui_locale`。OIDC/SAML/CAS 细节全部由 auth-server 与 IdP 完成，客户端不接触 metadata/XML/ticket。
4. IdP 回调 auth-server，签发一次性 code；托管模式寄存后轮询取回，loopback 模式直接 302 到本地回调。
5. `POST /api/auth/token` 兑换 → `LoginOutcome`：`ok` 完成；`select_account` → §4.6；`binding_required` → §4.7；`sso_verification_required` → §4.8。
6. 失败终态：`SSO_EMAIL_REQUIRED`（IdP 未给有效邮箱）、`SSO_LOGIN_REQUIRED`（个人登录被企业强制 SSO 拒绝）终止流程。
7. 跨区命中后，本次 SSO authorize / callback / token 兑换 / 联系方式验证 / 身份选择全部使用目标区域；不得把两区 passport 合并。

### 4.5 Desktop 托管回调（当前默认）

1. 端点清单 `authDesktopCallbackUrl` 非空 → 托管；空 → RFC 8252 loopback。
2. 客户端生成 `pollSecret = base64url(randomBytes(32))`，`client_state = base64url(sha256(pollSecret))`；只有哈希进浏览器，`pollSecret` 只留进程内存，每次尝试重新生成。
3. 打开系统浏览器到 EP-08/EP-09，`redirect_uri` = 清单值，`client_state` 必填。
4. 服务端建事务时 `SET NX` 占位 `client_state`（10 分钟）；重复值 400。provider 回调后签发 code（60s），按 `client_state` 写入 `deskcb:<client_state>`（5 分钟），302 到 `/desktop/login-callback`。
5. 客户端自打开浏览器起轮询 EP-11：前 30s 每 1s，之后每 2s；连续失败 8 次放弃；总预算 5 分钟；单次请求 30s，允许服务端长轮询约 20s。
6. `pending` 继续；`ok` 拿 code 走 EP-10；`error` 原样透传；`expired` 映射 `INVALID_AUTH_CODE`。
7. 服务端约束复述：未知 `pollSecret` 必须 `pending`；读取幂等；`expired` 仅 code 自身 TTL 过期；poll 限流按 IP 300/60s，不按 secret；`deviceId` 不校验。清空清单字段即可回退 loopback，客户端无需发版；服务端仍须保留 provider 回调与授权码签发逻辑。

### 4.6 多账号选择、Passport 与 Account token

1. 登录结果 `select_account` 携带 `loginTicket`、`accounts[]`，并可能携带 `accountToken` + `accountRefreshToken`（必须成对）。
2. 新路径：`GET /api/auth/account`（Bearer accountToken）列出 memberships（含 `orgSlug`）→ 用户选择 → `POST /api/auth/account/exchange {membershipId}` → Resource token。旧路径：服务端未给 account token 时，用 `loginTicket` + `POST /api/auth/select-account {loginTicket, accountId, deviceId}`。
3. Passport 是“登录身份家族”：一个 passport 下多个 membership（personal/org）；Passport refresh 只用于列举/兑换 membership，每个 membership 另有自己的 Resource refresh。
4. 保存账号恢复：先用该 membership 的 Resource refresh；确定性失败后回落 Passport refresh + account list + exchange；成功后提交本地 owner/session。保存账号 key 为 `JSON.stringify([realm, membershipId])`，renderer 只拿 opaque key。
5. Account token 在兑换 Resource token 后立即从内存清空，不持久化、不续期；跨区个人 session 仅在 `sessionRealm === buildRegion` 时允许恢复，组织 membership 可跨区恢复。

### 4.7 补绑联系方式

1. 收到 `binding_required`：`bindType` 为 `phone` 或 `email`，`bindTicket` 一次性。
2. `POST /api/auth/binding/request-code {bindTicket, [bindType]: contact, locale?}`，字段名必须是 `phone` 或 `email`，不是统一 `contact`。
3. `POST /api/auth/binding/verify {bindTicket, [bindType]: contact, code, deviceId}` → `LoginOutcome`。
4. 票据过期/缺失 → `INVALID_BIND_TICKET`；客户端在 binding 步骤支持重新发码与输码；验证码错误按 `INVALID_CODE` / `CODE_ATTEMPTS_EXCEEDED`。

### 4.8 SSO 联系方式二次验证

1. 收到 `sso_verification_required`：`verificationTicket`、`channel`（email/sms）、`targetMasked`。
2. `POST /api/auth/sso/verification/request-code {verificationTicket, deviceId}` → `{status:"sent"}`。客户端此步骤没有 60s 倒计时，可重复发码；服务端应自行 60s 冷却并限次。
3. `POST /api/auth/sso/verification/verify {verificationTicket, code, deviceId}` → `LoginOutcome`。
4. 票据过期 → `INVALID_SSO_VERIFICATION_TICKET`；验证码错误按 `INVALID_CODE` / `CODE_ATTEMPTS_EXCEEDED`。

### 4.9 Refresh 轮换

- **Resource**：客户端在 access token `exp - 300s` 调度刷新；系统 resume 时剩余 ≤5 分钟也刷新。`POST /api/auth/refresh {refreshToken, deviceId}` 返回轮换后的 access/refresh + membership。刷新单飞；瞬时失败至少等 60s；429 的 `Retry-After` 更长时按服务端时间；冷启动 429 不等待，保留 token 以未登录放行 UI。
- **确定性失效**：`REFRESH_TOKEN_EXPIRED`、`INVALID_REFRESH_TOKEN`、`MEMBERSHIP_DISABLED` 清 token；`DEVICE_MISMATCH` 只让本进程登出，不删磁盘 token。
- **多实例竞态**：同机共享 userData 时，旧 token 命中 `INVALID_REFRESH_TOKEN` 但磁盘已出现新 token → 用新 token replacement-retry（最多 2 次，冷启动 100/250ms，运行时 250/1000ms 重读）。
- **Account**：`POST /api/auth/account/refresh {accountRefreshToken, deviceId}` → 新 account token 对；Passport 刷新也单飞，避免同一代 token 被消费两次。Account refresh 不设客户端超时。

### 4.10 登出

- Resource：`POST /api/auth/logout`，Bearer access token。auth-client 发 `{}`，Desktop best-effort 可能带 `{deviceId}`；服务端须容忍缺省。建议撤销当前 refresh family，并把 access token `jti` 放入短期 denylist（或递增 `sessionVersion`）直到 exp。
- Account：`POST /api/auth/account/logout`，Bearer account token，body `{}`。
- Desktop 普通登出：若本机还有其它保存账号，优先切换（`switchSavedAccount`），成功后 best-effort 撤销旧 access/passport token；否则写 logout tombstone、清兼容 session/vault、清注销 receipt、提交 signed-out owner，再 best-effort 撤销。共享 userData 的 passive 实例禁止登出（本地码 `PASSIVE_AUTH_MUTATION_BLOCKED`）。
- Mobile：先本地原子清凭证，再对当前账号与所有保存的 Resource/Passport 会话并发 best-effort refresh + logout；推送 token 注销由 `clearLocalSession` 处理。
- 账号删除确认后的本地清理**不调用**普通 logout，因为服务端已撤销 refresh family；receipt 保留供登录页查询。

### 4.11 账号注销

1. `GET /api/auth/account/deletion` 查询 `available`、`verification`、`manualAppleRevocationRequired`；`available=false` 隐藏入口。
2. `POST /api/auth/account/deletion/challenge {locale}` → `challengeId`、`receiptToken`、`channel`、`maskedTarget`、`expiresAt`。
3. 客户端把 receipt 加密存储：Desktop `safe-storage/cindy_auth_account_deletion_receipt.enc`（record v2 绑定 `realm + authIdentity`）；Mobile SecureStore `cindy.mobile.auth.accountDeletionReceipt`。
4. 用户输入 6 位码并勾选确认 → `POST /api/auth/account/deletion/confirm {challengeId, receiptToken, code, acknowledged:true}` → `AccountDeletionStatus`。
5. 结果不确定（网络/超时/非法响应）时，Desktop 用 receipt 调 `POST /api/auth/account/deletion/status` 查明；只有 `cancelled` 视为失败。
6. 之后登录页每 30s 调 status（无需登录，仅凭 receipt）；`ACCOUNT_DELETION_RECEIPT_INVALID` 清 receipt，`INVALID_RESPONSE` 停止轮询但保留 receipt。
7. 状态流转：`pending`（30 天等待期）→ `processing`（到期删除中）→ `completed`；期内重新登录取消 → `cancelled`，登录响应可带 `accountDeletionRestored:true`，客户端只提示一次。服务端应记录 `requestedAt`、`deleteAfter`、`processingAt`、`completedAt`、`cancelledAt`；到期任务由后台 worker 执行，必须幂等、可重试。

---

## 5. 数据模型

### 5.1 建模原则

- 每区独立库；所有表可带 `region` 冗余列用于审计与误路由检测，但不得跨区查询。身份分三层：Passport（登录身份家族）→ Membership（业务身份）→ Organization（企业）；个人 membership 的 `org_id` 为空，企业 membership 必须属于一个 org。
- 联系方式、社交 subject、SSO subject 均作为 Passport 的 identity 记录，验证状态独立；membership 只暴露对外展示字段。
- 一次性凭证（OTP、ticket、auth code、refresh token、receipt）只存哈希/指纹，不存明文；检索用 `token_hash` 唯一索引。
- 软删除用于审计；注销完成后再做不可逆匿名化/清理，保留最小合规审计。

### 5.2 建议表结构

| 表 | 关键字段 | 约束/索引 | 生命周期 |
|---|---|---|---|
| `passports` | `id`、`region`、`status`（active/disabled/deleted）、`primary_email`、`primary_phone`、`created_at`、`updated_at`、`deleted_at` | PK `id`；`(region,status)` | 长期；注销完成后匿名化 |
| `passport_identities` | `id`、`passport_id`、`kind`（email/phone/apple/google/wechat/sso）、`identifier`（邮箱/手机号/subject）、`connection_id?`、`verified_at`、`is_primary`、`metadata jsonb` | UNIQUE `(region,kind,identifier)`；INDEX `passport_id` | 与 passport 同生命周期 |
| `memberships` | `id`、`passport_id`、`org_id?`、`kind`（personal/org）、`role`（owner/admin/member）、`display_name`、`avatar_url`、`email`、`status`、`created_at`、`updated_at` | INDEX `passport_id`；INDEX `org_id`；若产品保证单 personal，再加部分唯一索引 `(passport_id) WHERE kind='personal'` | 长期；禁用保留 |
| `organizations` | `id`、`slug`（全局唯一，由已验证域名派生）、`name`、`logo_url`、`status`、`created_at` | UNIQUE `slug`；`status` | 长期 |
| `organization_domains` | `id`、`org_id`、`domain`（lower）、`verified_at`、`verified_by`、`is_primary` | UNIQUE `domain`；INDEX `org_id` | 验证后长期；撤销需审计 |
| `sso_connections` | `id`、`org_id`、`protocol`（oidc/saml/cas）、`name`、`enabled`、`config jsonb`（加密）、`created_at`、`updated_at` | INDEX `org_id,enabled` | 长期；配置加密 |
| `login_transactions` | `id`、`region`、`kind`（social/sso）、`provider/connection_id`、`client_type`、`device_id`、`redirect_uri`、`code_challenge`、`code_challenge_method`、`client_state`（unique）、`ui_locale`、`status`、`created_at`、`expires_at` | UNIQUE `client_state`；INDEX `expires_at` | 10 分钟；定期清理 |
| `verification_codes` | `id`、`region`、`purpose`（email_login/phone_login/binding/sso_verification/account_deletion）、`target_type`、`target_normalized`、`code_hash`、`attempts`、`max_attempts`、`sent_at`、`expires_at`、`consumed_at`、`request_ip`、`device_id?` | INDEX `(purpose,target_normalized,sent_at)`；`expires_at` | 10 分钟；保留审计 30 天 |
| `auth_tickets` | `id`、`ticket_hash`、`type`（login/bind/sso_verification）、`passport_id?`、`payload jsonb`、`device_id?`、`expires_at`、`consumed_at` | UNIQUE `ticket_hash`；INDEX `expires_at` | 5-10 分钟；一次性 |
| `auth_codes` | `code_hash`、`transaction_id`、`passport_id`、`membership_id?`、`redirect_uri`、`code_challenge`、`device_id`、`issued_at`、`expires_at`、`consumed_at` | UNIQUE `code_hash`；INDEX `expires_at` | 60 秒；GETDEL 语义 |
| `refresh_sessions` | `id`、`family_id`、`passport_id`、`membership_id`、`token_hash`、`device_id`、`client_type`、`generation`、`issued_at`、`expires_at`、`rotated_at`、`revoked_at`、`revoke_reason`、`replaced_by` | UNIQUE `token_hash`；INDEX `(family_id)`、`(membership_id,device_id)` | 滑动/固定过期；轮换保留旧代审计 |
| `account_refresh_sessions` | `id`、`family_id`、`passport_id`、`token_hash`、`device_id`、`generation`、`issued_at`、`expires_at`、`rotated_at`、`revoked_at` | UNIQUE `token_hash`；INDEX `(passport_id,device_id)` | 同上 |
| `account_deletion_requests` | `id`、`passport_id`、`status`、`requested_at`、`delete_after`、`processing_at`、`completed_at`、`cancelled_at`、`receipt_hash`、`challenge_id`、`channel`、`masked_target`、`verified_at` | UNIQUE `receipt_hash`；INDEX `(status,delete_after)`；部分唯一 `passport_id WHERE status IN ('pending','processing')` | 30 天等待期 + 审计保留 |
| `account_deletion_challenges` | `id`、`request_id`、`code_hash`、`receipt_hash`、`channel`、`target_normalized`、`attempts`、`expires_at`、`consumed_at` | UNIQUE `id`；INDEX `request_id` | 10 分钟；一次性 |
| `identity_card_nonces` | `nonce_hash`、`membership_id`、`purpose`、`created_at`、`expires_at` | UNIQUE `nonce_hash` | 5 分钟；防重放 |
| `devices`（可选） | `id`、`passport_id`、`device_id`、`client_type`、`first_seen_at`、`last_seen_at`、`last_ip`、`revoked_at` | UNIQUE `(passport_id,device_id)` | 审计与设备列表基础 |
| `audit_events` | `id`、`region`、`actor_passport_id?`、`action`、`ip`、`user_agent`、`metadata jsonb`、`created_at` | INDEX `(actor_passport_id,created_at)`、`(action,created_at)` | 合规保留期 |
| `outbox_jobs` | `id`、`kind`（email/sms/deletion）、`payload jsonb`、`status`、`run_at`、`attempts`、`last_error` | INDEX `(status,run_at)` | 完成后清理 |

### 5.3 Redis 键

| 键 | 值 | TTL | 用途 |
|---|---|---|---|
| `deskcb:<client_state>` | `{status,code,error,codeExpiresAt}` 或占位 nonce | 结果 5 分钟；占位 10 分钟 | 托管回调暂存；`SET NX` 防抢占 |
| `rl:<bucket>:<dimension_hash>` | 计数器/滑动窗口 | 窗口长度 | 限流；key 用哈希，避免明文邮箱/手机 |
| `otp:cooldown:<purpose>:<target_hash>` | `1` | 60s | 发码冷却 |
| `auth:denylist:<jti>` | `1` | 至 access token exp | 登出后 access token 立即失效 |
| `sso:tx:<state>`（若服务端存 state） | 事务 id | 10 分钟 | OAuth/SSO state 一次性 |

### 5.4 关系摘要

`passports 1—N passport_identities`；`passports 1—N memberships`；`organizations 1—N memberships`；`organizations 1—N organization_domains`；`organizations 1—N sso_connections`；`memberships 1—N refresh_sessions`；`passports 1—N account_refresh_sessions`；`passports 1—1..N account_deletion_requests`（同一时刻最多一个活跃）。`orgSlug` 属于 organization，仅 account list 与 access token claim 暴露。

---

## 6. 安全与风控

### 6.1 Turnstile

- 能力经 `providers.captcha` 下发：`provider` 固定 `turnstile`、`siteKey`、`requiredFor`；关闭时整字段省略。发码前由客户端打开托管页 `/captcha/turnstile`；服务端生成挑战、校验 Siteverify、按 `action` 绑定 `email_request_code` / `phone_request_code`，token 一次性、约 300s 有效（客户端注释）。
- 失败返回 `CAPTCHA_REQUIRED`（未带）、`CAPTCHA_INVALID`（校验失败/动作不符/过期）、`CAPTCHA_UNAVAILABLE`（Siteverify 不可用/密钥错）。客户端只自动重试一次，服务端不得依赖无限重试。
- 托管页必须与 auth 主域同源，桌面 WebView 只放行精确 path，移动 WebView 只放行同源与 `challenges.cloudflare.com`；页面结果只经 hash/postMessage 回传，不回传 cookie。

### 6.2 限流与 Retry-After

| 维度 | 建议阈值 | 超限响应 |
|---|---|---|
| 发码 `request-code` | 单目标 1/60s、5/h、20/day；单 IP 10/min | 429 + `Retry-After` |
| 验证码核验 | 单码 5 次；单 IP 10/min | `CODE_ATTEMPTS_EXCEEDED` / 429 |
| `discovery`（邮箱） | 单 IP 30/min；单邮箱 10/min | 429 |
| `sso/discovery` | 单 IP 60/min | 429 |
| 授权入口 `authorize` | 单 IP 20/min；同一 `client_state` 仅一次 | 400 / 429 |
| 托管 poll | 单 IP 300/60s（文档明确） | 429 |
| refresh / account refresh | 单会话 30/min（60s 窗口）；单 IP 60/min | 429 |
| 身份名片 | 按账号 10/min | 429 |
| Connection token | 按 membership 30/min | 429 |
| 注销 status | 单 IP 20/min；登录页 30s/次 | 429 |

- `Retry-After` 支持秒数与 HTTP-date；返回 date 形式时必须带 `Date` 响应头，便于客户端按服务器时间换算。非法值客户端不猜冷却时间。限流 key 不得使用客户端可控的 `pollSecret`、`client_state`、`state` 原值；使用 IP、账号、目标哈希。IP 仅信任已知反代的 `X-Forwarded-For`/`X-Real-IP`。429 不触发客户端自动重试、不触发凭证恢复、不持久化；refresh 退避至少 60s，服务端 `Retry-After` 更长时以服务端为准。

### 6.3 Token 生命周期

| 凭证 | 建议格式 | 建议 TTL | 存储/轮换 |
|---|---|---|---|
| Access token | JWT（RS256/EdDSA/ES256），claims 含 `sub`(membershipId)、`accountId`/`passportId`、`ctx`(personal/org)、`orgId`/`orgSlug`、`region`、`sid`、`iat`/`exp`/`jti`、`iss`、`aud` | 15 分钟（与 08 横切文档一致；客户端按 `exp-300s` 刷新） | 客户端仅内存；登出用 `jti` denylist 或 `sessionVersion` |
| Resource refresh token | 32 字节随机 base64url，不透明 | 30 天滑动 + 绝对上限（与 08 横切文档一致） | 服务端仅存 SHA-256；一次性轮换，family 重用撤销 |
| Account token | JWT 或 opaque，短 TTL | 5-15 分钟（推测） | 仅登录选择阶段内存；不持久化 |
| Account refresh token | 32 字节随机 | 30 天（推测） | 仅存哈希；一次性轮换 |
| 授权码 | 随机，不透明 | 60s（文档明确） | 一次性 GETDEL；绑定 PKCE/redirect/device |
| 托管回调记录 | Redis 值 | 5 分钟（文档明确） | 按 `client_state`；读取幂等 |
| `client_state` 占位 | Redis `SET NX` | 10 分钟（文档明确） | 比较删除 |
| `loginTicket` / `bindTicket` / `verificationTicket` | 随机不透明 | 5-10 分钟（推测） | 一次性，仅内存于客户端 |
| 注销 receipt | 随机不透明 | 覆盖 30 天等待期（推测） | 客户端加密存储；可跨 logout |
| Turnstile token | Cloudflare token | ~300s（客户端注释） | 一次性 Siteverify |
| 身份名片 | RS256 JWT | 300s（文档明确） | 不落库；nonce 防重放 |
| Connection token | JWT（插件 OIDC） | ≤1800s（客户端硬限制） | 客户端缓存至 exp-60s；服务端按 audience 绑定 |

### 6.4 设备指纹

- Desktop `deviceId = machineIdSync()`，仅 dev 可用 `XDT_DEVICE_ID_OVERRIDE` 覆盖；Mobile 首次生成 UUID 存 SecureStore。它是设备标识，不是鉴权凭证。`deviceId` 出现在 verify-code、social、token、refresh、account refresh、select-account、binding verify、SSO verification 与 authorize query 中；托管 poll 带但服务端不校验。
- 服务端应把 refresh family 绑定到 `(membership, deviceId)`；不匹配返回 `DEVICE_MISMATCH`，但不得据此删除用户其他设备会话。`deviceId` 不得进入日志明文，建议哈希后存储。

### 6.5 区域 fail-closed

- `providers.region` 与 `sso/discovery.region` 必须等于部署区域；不一致宁可让客户端失败，也不能返回“看起来可用”的数据。
- 双区发现只接受“一区成功 + 另一区明确 404”的组合；网络失败、5xx、非法响应、区域不符一律不可用，禁止猜测。
- 服务端不得把 cn token 的验证委托给 global JWKS，反之亦然；身份名片跨区只允许 P3 开关下由 device-link 额外信任对端 issuer（默认关）。端点清单不可用时客户端使用缓存/随包清单，但离线清单不授予登录或绕过吊销；auth-server 始终是吊销权威。

### 6.6 一次性凭证与 PKCE

- OTP：6 位数字，按 `purpose + target` 绑定，HMAC/哈希存储，常数时间比较；重发使旧码失效；尝试次数独立计数；消费后立即失效。票据：login/bind/verification ticket 均一次性、短期、绑定 `deviceId`/passport；过期返回对应 `INVALID_*_TICKET`。
- 授权码：仅 S256 PKCE；`code_verifier` 43-128 字符；授权码绑定 `redirect_uri`、`device_id`、`client_state`；兑换一次性。
- 托管回调：`client_state` 一次性占位防抢占；公开写入端点已删除；结果只从签发处写 Redis；`pollSecret` 与浏览器可见值分离。
- 登出/删除：refresh family 撤销；access token 短期 denylist；删除确认后不依赖普通 logout。

### 6.7 枚举、日志与合规

- 发码统一 `{status:"sent"}`；`discovery` 按域名返回登录方式属于产品设计，但不得返回账号是否存在；`sso/discovery` 只暴露已启用连接。永不记录：access/refresh/account token、OTP、ticket、auth code、`pollSecret`、`client_state` 原值、身份名片原文；邮箱/手机号日志需脱敏。
- 审计至少记录：登录成功/失败（含原因码）、refresh family 撤销、SSO 连接变更、注销申请/取消/完成、资料修改；审计不存凭证。
- 浏览器页面：`Cache-Control: no-store`；CSP 用模板 manifest 的 script hash，不使用 `unsafe-inline`；`detail` 参数按 HTML 文本节点转义。

---

## 7. 客户端证据索引

| 主题 | 证据（文件:行号） |
|---|---|
| 通用请求/错误/RateLimit | `R/packages/auth-client/src/client.ts:495-620`、`client.ts:76-95,103-109` |
| providers / region 校验 | `client.ts:125-138`、`types.ts:27-74`、`client.test.ts:115-210` |
| discovery | `client.ts:140-165`、`types.ts:94-131` |
| sso discovery / 空连接 | `client.ts:167-193`、`types.ts:76-92`、`client.test.ts:489-561` |
| 发码 / 核验 | `client.ts:195-230`、`types.ts:308` |
| token 兑换 | `client.ts:232-242`、`docs/desktop-login-hosted-callback.md:160-164,234-237` |
| 托管 poll | `client.ts:244-272`、`types.ts:285-306`、`docs/desktop-login-hosted-callback.md:131-177` |
| 社交原生 | `client.ts:274-294`、`apps/mobile/src/auth/nativeSocial.ts:78-176` |
| 身份选择 | `client.ts:296-302`、`apps/desktop/src/main/authManager.ts:5578-5605` |
| 补绑 | `client.ts:304-332`、`apps/desktop/src/shared/authIpc.ts:245-256` |
| refresh / account | `client.ts:334-386`、`types.ts:174-185` |
| SSO 二次验证 | `client.ts:388-405` |
| `/api/me` | `client.ts:407-411`、`types.ts:241-246` |
| 账号注销 API | `client.ts:413-460`、`types.ts:248-283` |
| logout | `client.ts:462-469`、`authManager.ts:6078-6086` |
| authorize URL | `client.ts:471-493` |
| LoginOutcome | `types.ts:187-233` |
| 区域路由 / 双区发现 | `R/packages/auth-client/src/orgRealmDiscovery.ts:26-73`、`R/docs/auth-realm-routing.md:18-62,120-138` |
| 邮箱 discovery | `R/packages/auth-client/src/emailLoginDiscovery.ts:20-73` |
| PKCE / deviceId | `authManager.ts:399-405,2249-2252`、`apps/mobile/src/auth/pkce.ts:3-14` |
| 托管回调凭据/轮询 | `R/apps/desktop/src/main/authHostedCallback.ts:36-68,136-166` |
| 浏览器分流 | `authManager.ts:2881,2974-2985` |
| refresh 失效分类 | `R/apps/desktop/src/main/authRefreshFailure.ts:12-24,100-128` |
| refresh 退避 | `R/apps/desktop/src/main/authRefreshBackoff.ts:13-17` |
| 资料更新 | `authManager.ts:4412-4485`、`apps/desktop/src/main/profileEdit.ts:18-30,86-110` |
| 身份名片 | `apps/desktop/src/main/device-link/providerShareApi.ts:97-115`、`R/docs/provider-sharing-contract.md:137-147` |
| Connection token | `apps/desktop/src/main/cindy-brain/index.ts:3204-3216`、`connectionTokenProvider.ts:5-9,73-95`、`connectionAudienceResolver.ts:121-164` |
| Captcha 页面/宿主 | `types.ts:56-63`、`renderer/components/login/LoginCaptchaOverlay.tsx:16-62`、`apps/mobile/src/auth/loginCaptchaMessage.ts:10-32` |
| 注销生命周期 | `authManager.ts:4265-4317,4319-4325,4363-4400`、`accountDeletionIpc.ts:88-130`、`LoginPage.tsx:437-490` |
| 30 天等待期文案 | `renderer/i18n/locales/zh-CN/common.json:7374-7379,7383-7389` |
| 错误码/文案 | `renderer/i18n/locales/zh-CN/common.json:7535-7564`、`apps/mobile/src/auth/loginMessages.ts:708-884` |
| 端点基址 / 托管开关 | `R/config/endpoint.json:3,21`、`R/config/endpoint.global.json:3,21` |
| 托管回调契约 | `R/docs/desktop-login-hosted-callback.md:40-53,87-129,155-177,179-217` |

---

## 8. Go 实现映射建议

### 8.1 目录与分层

建议在 `cindy-server` 新建 `cmd/auth-server` 与 `internal/auth`，按“transport → service → repository”分层，领域模型不依赖 HTTP/DB：

- `cmd/auth-server/main.go`：加载配置、装配 DB/Redis/JWKS/邮件短信/Turnstile、启动 HTTP 与后台 worker。
- 核心分层：`internal/auth/handler/`（每端点一个文件，只做解码、校验、调用 service、写错误体）、`internal/auth/service/`（登录编排、票据、刷新轮换、注销状态机、区域校验）、`internal/auth/repository/postgres/`（sqlc 查询与事务）、`internal/auth/repository/redis/`（托管回调暂存、限流、denylist、冷却）。
- `internal/auth/middleware/`：request id、recover、访问日志、body limit、locale、region、rate limit、bearer auth。
- 支撑：`internal/auth/domain/`（Passport、Membership、Organization、SSOConnection、RefreshSession 等）、`internal/auth/worker/`（注销到期、邮件/短信 outbox、过期票据清理）、`pkg/`（`jwt`、`turnstile`、`otp`、`idgen`、`httpx` 统一错误体）、`assets/login-callback/` 与 `assets/captcha/`（静态模板与 manifest）。

### 8.2 Handler 映射

| Handler 文件 | 覆盖端点 | 关键职责 |
|---|---|---|
| `providers.go` | EP-01 | 读取区域配置与 Turnstile 公钥，输出 `attribution/email/phone/social/captcha` |
| `discovery.go` | EP-02、EP-03 | 邮箱登录方式、企业连接发现；空数组语义 |
| `otp.go` | EP-04..07 | 发码、核验、captcha 前置、60s 冷却、尝试计数 |
| `oauth.go` | EP-08、EP-12..14 | 社交 authorize 入口、原生凭证兑换、provider 回调内部路由 |
| `sso.go` | EP-09、EP-18、EP-19 | SSO authorize、OIDC/SAML/CAS 回调、二次验证 |
| `token.go` | EP-10、EP-11 | PKCE 兑换、托管 poll、授权码一次性 |
| `account.go` | EP-15、EP-16、EP-17、EP-21、EP-22、EP-24 | 身份选择、补绑、Account token、Account 登出 |
| `refresh.go` | EP-20、EP-23 | Resource/Account 轮换、family 重用检测 |
| `me.go` | EP-25、EP-26、EP-27 | 当前身份、资料更新、身份名片 |
| `connection_token.go` | EP-28 | 插件 OIDC token、audience 校验 |
| `deletion.go` | EP-29..32 | 注销可用性、挑战、确认、状态 |
| `logout.go` | EP-33 | Resource 登出与 denylist |
| `pages.go` | EP-34..36 | Turnstile 页、结果页、404 保留路径 |

### 8.3 Service 职责

- `ProviderService`：按区域与配置生成 `providers`；`attribution` 固定；`captcha.requiredFor` 可配置。
- `DiscoveryService`：邮箱 → 登录方式；org → 启用连接；只查已验证域名与启用连接；对空结果返回明确语义。
- `OtpService`：生成 6 位码、HMAC 存储、发码冷却、尝试计数、消费；发码统一返回 sent；邮件/短信写入 outbox。
- `TicketService`：login/bind/verification 三类票据的签发、校验、消费；绑定 passport/device。
- `TokenService`：PKCE 校验、授权码签发/消费、access JWT 签发、refresh family 创建/轮换/撤销、account token 签发。
- `SocialService` / `SsoService`：provider 凭证校验、OIDC/SAML/CAS 事务、IdP 回调、联系方式二次验证。
- `AccountService`：Passport/Membership 查询、多身份选择、补绑、资料更新、名片 nonce。
- `DeletionService`：状态机、30 天等待期、登录取消恢复、到期任务。
- `CaptchaService`：Siteverify、动作校验、失败码映射。
- `RegionService`：启动期区域校验、响应 `region` 注入、跨区拒绝。

### 8.4 Repository 接口

建议定义 `PassportRepository`、`IdentityRepository`、`MembershipRepository`、`OrganizationRepository`、`SsoConnectionRepository`、`OtpRepository`、`TicketRepository`、`AuthCodeRepository`、`RefreshSessionRepository`、`DeletionRepository`、`AuditRepository`、`OutboxRepository`。写路径使用事务：兑换授权码（消费 code + 创建 refresh family + 签发 token）必须原子；注销确认（更新 request + 撤销 sessions + 写 receipt 状态）必须原子。

### 8.5 中间件链

建议顺序：`recover` → `request_id` → `access_log`（脱敏） → `body_limit`（64KB） → `locale` → `region`（响应注入/自检） → `rate_limit`（按路由配置） → `bearer_auth`（仅受保护路由） → handler。`bearer_auth` 解析 JWT 后把 `membership_id`、`passport_id`、`ctx`、`jti` 放入 context；`ctx=org` 时注入 `orgSlug`。错误统一由 `httpx.WriteError` 输出 `{error:{code,message}}`。

### 8.6 配置与环境

每区独立环境变量：`REGION`、`PUBLIC_BASE_URL`、`DATABASE_URL`、`REDIS_URL`、`JWT_PRIVATE_KEY`/`JWT_PUBLIC_KEY`/`JWKS_KID`、`TURNSTILE_SITE_KEY`/`TURNSTILE_SECRET`、`EMAIL_PROVIDER_*`、`SMS_PROVIDER_*`、`APPLE_*`、`GOOGLE_*`、`WECHAT_*`、`SSO_STATE_SECRET`、`OTP_PEPPER`。启动时校验：区域合法、`PUBLIC_BASE_URL` 为 https、密钥可解析、`authDesktopCallbackUrl` 与 `PUBLIC_BASE_URL` 同源。

### 8.7 测试与上线

- 契约测试直接复用 `packages/auth-client` 的 `CindyAuthClient` 与 `fixtures/loginScenarios.ts`，覆盖四态 outcome、错误体两种形状、429/Retry-After、poll 四态、区域不符；集成测试用临时 Postgres + Redis，覆盖邮箱/手机登录、SSO mock IdP、托管回调全链路、refresh 轮换与 family 重用、注销 30 天状态机、身份名片验签。
- 上线顺序遵循 `docs/auth-realm-routing.md`：先两区 auth-server 支持 `region`，再确认两区清单 `authApiBaseUrl` 同区，最后发客户端；托管回调通过清空/填入 `authDesktopCallbackUrl` 灰度与回滚。
- 可观测性：按端点/错误码计数，记录延迟与限流命中；不得把邮箱、手机号、token、OTP、pollSecret 写入指标标签。

---

## 9. 未确认项与推测

| 编号 | 事项 | 现有证据 | 影响 | 建议 |
|---|---|---|---|---|
| U-01 | Access token TTL 与完整 claims | 客户端只解码 `orgSlug`；按 `exp-300s` 刷新；08 横切文档建议 15 分钟 | 刷新节奏、登出 denylist 时长 | 15 分钟；claims 至少 `sub`、`accountId`/`passportId`、`ctx`、`orgId`/`orgSlug`、`region`、`sid`、`jti`、`iss`、`aud` |
| U-02 | Refresh token TTL 与 family 策略 | 客户端只依赖确定性失效码；08 横切文档建议 30 天滑动 + 绝对上限 | 安全与用户体验 | 30 天滑动 + 绝对上限；重用即撤销 family；保留旧代审计 |
| U-03 | OTP 长度/TTL/尝试上限与服务端发码冷却 | 客户端输入 6 位；错误码存在；客户端仅本地倒计时 | 风控强度与防刷 | 6 位、10 分钟、5 次尝试；重发使旧码失效；服务端独立执行 60s 冷却 |
| U-04 | Account token 格式/TTL、ticket TTL | 客户端只透传，不解析；只处理过期码 | Passport 会话安全、流程完成率 | Account token 短 TTL（5-15 分钟）；login/bind/verification ticket 5-10 分钟且一次性 |
| U-05 | `ssoRequired` 的计算与 `SSO_LOGIN_REQUIRED` 触发 | 客户端只展示文案 | 企业策略 | 按“已验证域名 + 连接启用 + 组织强制 SSO”判定；个人登录前检查 |
| U-06 | `SSO_EMAIL_REQUIRED` 的精确触发 | 客户端有文案 | IdP 兼容 | IdP 未返回可用邮箱或邮箱未验证时返回 |
| U-07 | `attribution` 的产品含义 | 必填但 UI 不分支 | 统计/默认入口 | 先固定为区域默认（cn=phone，global=email），保留字段 |
| U-08 | 注销 30 天等待期与 receipt TTL | UI 文案明确 30 天；receipt 跨 logout 保留 | 合规与状态机 | `delete_after = requested_at + 30d`；receipt 至少活到终态 |
| U-09 | 注销到期处理方式 | 客户端只查状态 | 数据删除合规 | 后台 worker 幂等处理；完成前可取消；审计保留最小字段 |
| U-10 | 邮件/短信文案与供应商 | 客户端只传 locale | 交付体验 | 每区独立供应商；模板按 locale；退信/失败进 outbox 重试 |
| U-11 | provider 内部回调路径 | 文档仅举例 `googleCallbackUrl()` | 部署配置 | 由服务端与开放平台注册决定；不在客户端契约内 |
| U-12 | 身份名片签名算法与 JWKS | 文档明确 RS256 + 现有 JWKS | device-link 验签 | 必须 RS256；`kid` 轮换需双 key 过渡 |
| U-13 | Connection token claims 与 audience 校验 | 客户端只校验长度/回显 | 插件安全 | 服务端按 `orgSlug:pluginSlug` 重新解析；短 TTL；按 membership 限流 |
| U-14 | Turnstile 不可用时 fail-open/fail-closed | 客户端有 `CAPTCHA_UNAVAILABLE` | 可用性 vs 风控 | 建议 fail-closed，返回 503；监控告警 |
| U-15 | `discovery` 是否泄露账号存在性 | 客户端按域名分流 | 隐私 | 只按域名/企业配置返回，不查账号；统一响应时间 |
| U-16 | 同邮箱在两区是否合并 Passport | 客户端明确不合并两区 passport | 数据模型 | 两区独立 Passport；跨区只允许组织 SSO 会话 |
| U-17 | 组织成员/邀请/席位/SSO CRUD、MFA/Passkey/邮箱变更/会话列表 | 客户端无调用 | 管理后台与未来扩展 | 属于 auth console 或其他服务域；`discovery` 可 append-only 新 method，客户端会忽略未知类型 |
| U-18 | `STATE_MISMATCH`、`UNAUTHORIZED`/`TOKEN_EXPIRED`/`INVALID_TOKEN` 的归属 | 前者由客户端在 loopback/mobile 本地合成；后者来自 `serverApiClient` 资源服务 | 错误码归属与刷新触发 | 仅当服务端自己管理 OAuth state 时返回 `STATE_MISMATCH`；auth-server 用 `ACCOUNT_UNAVAILABLE`/`INVALID_REFRESH_TOKEN` 更精确 |
| U-19 | `account/exchange` 幂等与旧会话处理 | 客户端只调一次 | 网络重试 | 支持同一 accountToken 重试；同设备旧 family 撤销策略需定稿 |
| U-20 | `logout` 是否必须带 `deviceId` | auth-client 发 `{}`，Desktop best-effort 带 | 兼容性 | 服务端必须容忍缺省；有值时只撤销该设备 family |
| U-21 | 端点清单文档与配置不一致 | 文档称回调字段为空，实际两份清单均非空 | 上线路径 | 以配置现状为准，托管与 loopback 两条客户端路径都保留；服务端只需支持托管 |

**实现优先级建议**：先交付 AUTH-01/02/03/05/10/11/15/16/22（providers + 邮箱 OTP + PKCE token + refresh + 托管回调 + logout + 错误契约），再补手机（04）、企业 SSO（06/09/13/14）、多身份（12）、社交（07/08）、注销（20）、资料与名片（17/18）、Connection token（19）；captcha（21）按发码风控需要开启。每批都以客户端契约测试作为验收门槛。
