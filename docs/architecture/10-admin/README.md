# 管理后台实施方案（Admin API + WebUI）

> 对应能力：ADMIN-01…ADMIN-11（见 `docs/rebuild/10-admin-console.md`）
> 依赖：所有业务域导出的应用服务；不直接跨域读表。

---

## 1. 范围

| 优先级 | 模块 |
|---|---|
| P0 | 管理员登录/MFA、仪表盘、用户查询、组织列表、设备列表、模型目录只读、插件审核只读、审计日志 |
| P1 | 封禁/会话吊销、组织/SSO 编辑、模型发布、插件审核写、Skill 审核、更新渠道、feature flags |
| P2 | 计费/退款、内容合规、供应商分享治理、告警、双人复核 |

---

## 2. 后端结构

```
internal/admin/
├── handler.go        # /admin/api/v1
├── auth.go           # 登录、MFA、session、CSRF
├── rbac.go           # role -> permission
├── dashboard.go
├── accounts.go
├── orgs.go
├── devices.go
├── models.go
├── plugins.go
├── skills.go
├── updates.go
├── audit.go
└── jobs.go
```

原则：admin 只调用各域 `Service`；跨域聚合在 admin 层；所有写操作写审计。

---

## 3. 认证与 RBAC

- `POST /admin/api/v1/auth/login`：email + password（Argon2id）+ TOTP（可选/强制）。
- 成功：HttpOnly `admin_session` cookie（Secure/SameSite=Lax）+ CSRF token（响应体/头）。
- `POST /auth/logout`、`GET /auth/me`。
- Session 30 min 空闲过期；绝对 12h；可在后台吊销。
- 角色：`super_admin/ops/support/model_ops/plugin_reviewer/skill_reviewer/finance/security/readonly`。
- 权限常量：`accounts.read/write`、`orgs.write`、`devices.write`、`models.write`、`plugins.review`、`skills.review`、`updates.write`、`audit.read`、`admins.write`。
- 越权返回 404；高风险写要求 `X-Confirm-Action` 与 reason。

---

## 4. Admin API

前缀 `/admin/api/v1`。列表统一 `{items,total,page,pageSize}`；写操作 `Idempotency-Key`。

### 4.1 仪表盘

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/dashboard/summary` | DAU、登录成功率、在线设备、消息量、WSS、插件/Skill、收入 |
| GET | `/dashboard/timeseries?metric=&from=&to=` | 时序 |
| GET | `/dashboard/alerts` | 当前告警 |

### 4.2 账号与组织

| 方法 | 路径 |
|---|---|
| GET | `/accounts?q=&status=&orgId=&page=` |
| GET | `/accounts/{id}` |
| POST | `/accounts/{id}/ban`、`/unban` |
| GET | `/accounts/{id}/sessions` |
| POST | `/accounts/{id}/sessions/revoke` |
| GET | `/accounts/{id}/devices`、`/usage`、`/audit` |
| GET | `/deletions?status=`、`POST /deletions/{id}/approve`、`/cancel` |
| GET/POST | `/orgs` |
| GET/PATCH | `/orgs/{id}` |
| GET/POST | `/orgs/{id}/members` |
| GET/POST | `/orgs/{id}/domains`、`POST /domains/{id}/verify` |
| GET/POST | `/orgs/{id}/sso`、`PATCH /sso/{id}`、`POST /sso/{id}/test` |
| PATCH | `/orgs/{id}/policies` |

### 4.3 设备与分享

| 方法 | 路径 |
|---|---|
| GET | `/devices?q=&online=&accountId=` |
| DELETE | `/devices/{id}`（强制移除） |
| GET | `/shared-tasks?status=`、`POST /shared-tasks/{id}/close` |
| GET | `/provider-shares?status=`、`POST /provider-shares/{id}/revoke` |
| GET | `/push-tokens`、`DELETE /push-tokens/{id}` |
| GET/PUT | `/ice-config`、`POST /ice-config/rotate` |

### 4.4 模型与计费

| 方法 | 路径 |
|---|---|
| GET/POST | `/providers`、`PATCH /providers/{id}` |
| GET/POST | `/models`、`PATCH /models/{id}` |
| POST | `/catalog/draft`、`/catalog/validate`、`/catalog/publish`、`/catalog/rollback` |
| GET | `/plans`、`POST /plans`、`PATCH /plans/{id}` |
| GET | `/subscriptions`、`POST /subscriptions/{id}/cancel` |
| GET | `/orders`、`POST /orders/{id}/refund` |
| GET | `/credits/ledger?accountId=`、`POST /credits/adjust` |
| GET | `/usage?accountId=&modelId=&from=&to=` |
| GET/PUT | `/byok-policies` |

### 4.5 插件与 Skill

| 方法 | 路径 |
|---|---|
| GET | `/plugins?scope=&orgId=&status=&q=` |
| GET | `/plugins/{id}`、`/plugins/{id}/releases` |
| POST | `/plugins/{id}/releases/{releaseId}/approve`、`/reject` |
| POST | `/plugins/{id}/remove` |
| GET/POST | `/plugin-namespaces` |
| PATCH | `/plugins/{id}/default-install` |
| GET | `/skills?q=&visibility=&status=` |
| GET | `/skills/{id}`、`/skills/{id}/versions` |
| POST | `/skills/{id}/versions/{version}/approve`、`/reject`、`/rescan` |
| PATCH | `/skills/{id}`（可见性/分类） |
| GET/POST | `/skill-categories`、`/skill-auto-install` |

### 4.6 更新、渠道、配置

| 方法 | 路径 |
|---|---|
| GET/POST | `/releases/desktop`、`POST /releases/desktop/{id}/publish` |
| GET/POST | `/releases/mobile`、`POST /releases/mobile/{id}/publish` |
| GET/POST | `/announcements`、`POST /announcements/{id}/publish` |
| GET/POST | `/feature-flags`、`PATCH /feature-flags/{key}` |
| GET/PUT | `/endpoint-manifest`（按 region） |
| GET | `/channels`、`POST /channels/{id}/disable` |

### 4.7 系统

| 方法 | 路径 |
|---|---|
| GET | `/audit?actor=&action=&target=&from=&to=` |
| GET | `/jobs?status=`、`POST /jobs/{id}/retry` |
| GET | `/alerts`、`POST /alerts/{id}/silence` |
| GET | `/system/health`、`/system/migrations` |
| GET/POST | `/admins`（仅 super_admin） |

---

## 5. Admin WebUI（cindy-admin）

### 5.1 技术栈

React 19.3 + Vite 8 + TypeScript 7 + Tailwind 4 + TanStack Query 5 + TanStack Table 9 + React Router 7 + Recharts 3 + React Hook Form 7 + Zod 4 + lucide-react。

### 5.2 目录

```
cindy-admin/
├── src/
│   ├── main.tsx
│   ├── app/router.tsx
│   ├── app/queryClient.ts
│   ├── lib/api.ts          # fetch 封装、错误、CSRF
│   ├── lib/auth.ts
│   ├── components/ui/      # Button/Card/Table/Dialog/Input/Badge/Toast...
│   ├── components/layout/  # Sidebar/Topbar/CommandPalette
│   ├── features/dashboard/
│   ├── features/accounts/
│   ├── features/orgs/
│   ├── features/devices/
│   ├── features/models/
│   ├── features/plugins/
│   ├── features/skills/
│   ├── features/updates/
│   ├── features/audit/
│   └── styles/index.css
├── vite.config.ts
├── tsconfig.json
└── package.json
```

### 5.3 设计规范

- Light/Dark 双主题，CSS 变量语义 token（背景/前景/边框/主色/危险/成功/警告）。
- 侧栏可折叠；顶部全局搜索 + 命令面板（⌘K）。
- 表格：排序、筛选、列固定、分页、空状态、骨架屏。
- 详情页：头部摘要 + Tabs；危险操作红色并二次确认。
- 图表：Recharts 面积/柱状；加载骨架；无数据占位。
- 表单：React Hook Form + Zod；行内错误；保存/取消粘底。
- 响应式：≥1280 三栏；<1024 折叠侧栏；<768 卡片列表。
- 国际化：English / 简体中文，默认跟随浏览器语言并持久化；所有页面、共享组件、状态与审计动作需走 i18n key。
- 可访问性：键盘焦点、ARIA、对比度。

### 5.4 页面清单（P0/P1）

登录、仪表盘、用户列表/详情、组织列表/详情（成员/域名/SSO）、设备、模型目录/发布、插件审核、Skill 审核、更新渠道、Feature Flags、审计日志、系统状态。

---

## 6. 审计与安全

- 所有写操作经 `audit.Log`；记录 before/after、reason、requestId。
- 敏感字段（token、密钥、消息正文）禁止入审计。
- 登录失败限流；MFA 强制（生产）；会话固定防护。
- CSRF：双重提交 cookie + 自定义头。
- 高风险操作双人复核（P2）：`admin_action_requests`。

---

## 7. 测试

- RBAC 矩阵：每个角色对每个路由的允许/拒绝。
- 登录/MFA/会话过期/CSRF。
- 插件审核→客户端可见；下架→removals。
- 模型发布→catalog revision；回滚。
- 封禁→会话吊销→WSS 断开。
- 审计完整性。
- 前端：构建、路由、主题、表格交互。

---

## 8. 验收

- 管理员可完成一次插件审核、一次模型发布、一次封禁，且审计可查。
- UI 双主题、响应式、无控制台错误。
- 所有管理端 API 有权限校验与幂等。
