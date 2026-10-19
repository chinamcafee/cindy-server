# 管理后台（Admin Console）能力清单

> 能力编号前缀：`ADMIN-`
> 证据等级：**推测/推断**。客户端不调用后台接口，但多个服务存在“只有运营/审核人员才能执行”的动作（组织设置、插件审核、Skill 审核、模型目录发布、更新渠道、feature flags），据此重建后台能力。后台 API 建议统一实现在 Go 服务端，WebUI 使用 React 19+。

---

## 1. 后台定位

- 单一运营入口，聚合 auth / chat / device-link / model-access / plugin / skill-hub / hook / update 的运营能力。
- 只允许内部人员访问：独立登录域（admin cookie + MFA），**不复用客户端 OAuth 登录**。
- 所有写操作必须二次确认 + 审计；高风险操作（删除用户、强制下架、改价、吊销令牌）需双人复核（可选 P2）。
- 默认只读原则：列表页可查；写操作按角色授权。

---

## 2. 角色与权限

| 角色 | 权限范围 |
|---|---|
| `super_admin` | 全部；管理管理员账号与角色 |
| `ops` | 用户/设备/在线/任务/通知；不可改模型价格与审核 |
| `support` | 用户查询（脱敏）、会话吊销、工单关联；只读为主 |
| `model_ops` | 模型目录、价格、上下架、配额与套餐 |
| `plugin_reviewer` | 插件审核/下架/组织前缀 |
| `skill_reviewer` | Skill 审核/可见性/分类 |
| `finance` | 订阅、订单、退款、发票、额度调整 |
| `security` | 审计日志、风控、封禁、密钥轮换 |
| `readonly` | 全局只读 |

权限模型：`role -> permission` 静态表 + 资源级 scope（orgId / region）。所有列表默认按 region 过滤。

---

## 3. 能力域与页面

### 3.1 仪表盘（ADMIN-01）

- 核心指标：DAU/WAU、登录成功率、发码成功率、活跃设备、在线设备、消息量、插件下载/使用、Skill 安装、模型调用与收入。
- 实时：device-link 在线连接数、chat WSS 连接数、hook 会话数、错误率、P95 延迟。
- 告警：认证失败突增、推送失败率、对象存储错误、队列积压、证书到期。

### 3.2 用户与账号（ADMIN-02）

| 能力 | 说明 |
|---|---|
| 搜索/详情 | 按 email/phone/accountId/membershipId/org 搜索；展示身份、Membership、设备、登录历史、订阅 |
| 会话管理 | 列出 refresh session，吊销单个/全部；强制下线 |
| 封禁/解封 | 账号级、IP 级；原因与期限；同步到网关黑名单 |
| 账号删除 | 查看删除挑战/状态；审批/撤销；导出删除审计 |
| 资料修正 | 修改显示名/头像（需审计）；不解密用户凭证 |
| 组织关联 | 查看所属 org、角色、邀请记录 |
| 工单备注 | 内部备注，不暴露给客户端 |

### 3.3 组织与 SSO（ADMIN-03）

| 能力 | 说明 |
|---|---|
| 组织 CRUD | 名称、slug、logo、区域、套餐、状态 |
| 成员管理 | 邀请、移除、角色变更（owner/admin/member）、席位 |
| 域名管理 | 验证 TXT/DNS、域名→组织路由、冲突处理 |
| SSO 连接 | OIDC/SAML/CAS 配置、证书、属性映射、启用/禁用、测试登录 |
| 强制 SSO | `ssoRequired`、个人邮箱例外策略 |
| 组织策略 | 默认 beta、插件前缀、自动安装 Skill、数据保留期 |
| 组织用量 | 席位、模型额度、插件/Skill 使用 |

### 3.4 设备与 device-link（ADMIN-04）

| 能力 | 说明 |
|---|---|
| 设备列表 | 账号、设备名、平台、版本、在线、忙碌、远控开关、最后在线 |
| 强制移除 | 写移除标记并断开连接（4410） |
| 连接诊断 | 当前连接数、顶号记录、限流命中 |
| 共享任务 | 列表、成员、状态、强制关闭、异常补偿 |
| 供应商分享 | 列表、跨区分享、凭证状态、强制撤销 |
| 推送 token | 查看/清理失效 token；按平台统计 |
| ICE/TURN | 凭证密钥轮换、节点配置、连通性测试 |

### 3.5 对话与内容合规（ADMIN-05）

| 能力 | 说明 |
|---|---|
| 房间查询 | 按 roomId/actor/成员/时间；只读消息与附件元数据 |
| 举报处理 | 举报队列、证据快照、处置（警告/封禁/删除） |
| 内容审计 | 关键词/模型审核命中；保留期与法务导出 |
| Bot 管理 | Bot actor、绑定设备、启用/停用、模型 fallback 配置 |
| 群聊管理 | 群成员、邀请、转移负责人、强制解散 |
| 云伙伴 | 主力电脑、资料快照、降级/合并记录 |

> 内容读取属于高敏操作：默认脱敏、按需申请、全量审计；不提供批量导出明文消息。

### 3.6 模型与计费（ADMIN-06）

| 能力 | 说明 |
|---|---|
| Provider 管理 | 启用/停用、区域、接口类型、密钥池（只写不读） |
| 模型目录 | 新增/编辑模型、能力、上下文、价格/币种、媒体能力、排序、默认值 |
| 目录发布 | draft → 校验 → 发布 revision；灰度到组织/区域；回滚 |
| BYOK 策略 | 允许的 provider、上限、分享开关、轮换 |
| 套餐与订阅 | 套餐 CRUD、价格、试用、升降级、退款 |
| 额度 | 个人/组织额度、赠送、重置周期、超额策略 |
| 用量 | 按账号/模型/时间聚合；异常检测；账单导出 |
| 供应商分享/组 | 策略、举报、滥用处置 |

### 3.7 插件市场（ADMIN-07）

| 能力 | 说明 |
|---|---|
| 插件列表 | 按 scope/org/namespace/状态/下载量/使用量 |
| 发布审核 | 查看 manifest、包内容 diff、权限变化、扫描结果；通过/拒绝（原因必填） |
| 审核队列 | pending 排序、SLA、批量操作 |
| 下架/删除 | 触发客户端 `removals[].purge` 清理通告 |
| 组织前缀 | 登记/回收 `pluginPrefix`、namespace 校验 |
| 默认安装 | `defaultInstall` 配置、按组织/区域 |
| 安全 | 扫描报告、恶意包黑名单、上传配额 |
| 统计 | 下载请求数、安装量、使用量、留存 |

### 3.8 Skill Hub（ADMIN-08）

| 能力 | 说明 |
|---|---|
| 技能列表/详情 | 版本、文件树、owner、可见性、审核状态 |
| 审核 | 机审结果/人审、隔离/拒绝原因、重新扫描 |
| 可见性 | public/shared/private、团队/部门、公开申请审批 |
| 分类/标签 | 两级分类、合并/迁移、置顶 |
| 组织分发 | auto-install 清单、部门受众、灰度 |
| 下架/删除 | 通知客户端、保留审计 |
| 统计 | 下载、安装、版本分布 |

### 3.9 Hook / IM 渠道（ADMIN-09）

| 能力 | 说明 |
|---|---|
| 渠道绑定 | Slack/Telegram/X 工作区、Bot 配置、密钥轮换 |
| 会话/任务 | 关联 desktop 任务、消息量、失败率 |
| 权限确认审计 | 待确认/已确认/超时；来源消息；不可代答 |
| 官方 Bot | 命令面、白名单、限流、封禁 |
| 通知模板 | 文本/按钮/深链；多语言 |
| 故障 | 上游 API 错误、重连、webhook 失败 |

### 3.10 更新与远程配置（ADMIN-10）

| 能力 | 说明 |
|---|---|
| 桌面版本 | release/beta/canary 渠道、manifest 生成、sha256/size 校验、灰度百分比 |
| 移动版本 | latest、runtimeVersion、minVersion 强更、审核模式 |
| 强制更新 | 按区域/版本/渠道设置 `minVersion`（客户端只认移动字段） |
| 公告 | 按平台/版本/区域；CDN index 生成；预览 |
| Feature flags | `isCanary`、`defaultEnableBeta`、按账号/组织/百分比 |
| 端点清单 | 生成/校验 endpoint.json；多 CDN 镜像；区域一致性 |
| 壁纸/静态资源 | 上传、hash 命名、引用检查 |

### 3.11 可观测与审计（ADMIN-11）

| 能力 | 说明 |
|---|---|
| 审计日志 | actor、action、target、before/after、IP、UA、requestId；只追加；可导出 |
| 查询 | 按服务/账号/动作/时间；敏感字段脱敏 |
| 告警 | 规则、静默、值班；Webhook/邮件 |
| 日志检索 | 对接 SLS/ClickHouse；按 uploadCode 定位客户端上传 |
| 任务/队列 | outbox、重试、死信、手动重放（需确认） |
| 密钥 | JWT 密钥轮换、S3/TURN/APNs 凭证状态（不展示明文） |
| 系统 | 版本、健康、迁移状态、缓存命中、DB 连接池 |

---

## 4. 后台 API 约定

- 前缀：`/admin/api/v1`；独立 JWT（短 TTL）+ HttpOnly refresh cookie。
- 登录：邮箱+密码+TOTP；可选企业 SSO；强制 MFA。
- 所有响应：`{data, meta?}`；错误沿用 `{error:{code,message,details}}`。
- 列表统一 `page/pageSize/q/sort/order/filters`；返回 `{items,total,page,pageSize}`。
- 写操作要求 `Idempotency-Key`；高风险要求 `X-Confirm-Action: <action>` 与原因。
- 审计：中间件自动记录，禁止业务代码绕过。
- 权限：路由级 + 资源级双重校验；越权返回 404。

---

## 5. Admin WebUI 信息架构

```
Login / MFA
├── Dashboard
├── Accounts
│   ├── Users
│   ├── Sessions
│   ├── Deletions
│   └── Organizations
│       ├── Members
│       ├── Domains
│       ├── SSO
│       └── Policies
├── Devices & Sharing
│   ├── Devices
│   ├── Shared Tasks
│   ├── Provider Shares
│   └── Push & ICE
├── Conversations
│   ├── Rooms
│   ├── Bots
│   └── Reports
├── Models & Billing
│   ├── Providers
│   ├── Models
│   ├── Releases
│   ├── Plans
│   ├── Credits
│   └── Usage
├── Plugins
│   ├── Catalog
│   ├── Review Queue
│   ├── Namespaces
│   └── Stats
├── Skills
│   ├── Catalog
│   ├── Review
│   ├── Taxonomy
│   └── Distribution
├── Channels (Slack/Telegram/X)
├── Releases & Config
│   ├── Desktop
│   ├── Mobile
│   ├── Announcements
│   ├── Feature Flags
│   └── Endpoint
└── System
    ├── Audit Log
    ├── Jobs
    ├── Alerts
    └── Settings
```

UI 要求（阶段 3）：React 19 + TypeScript + Vite + Tailwind + shadcn/ui 风格；支持 Light/Dark；表格虚拟化；命令面板（⌘K）；全局搜索；空状态/骨架屏；危险操作确认；响应式。

---

## 6. 关键后台流程

### 6.1 插件审核

`pending` 列表 → 打开详情（manifest、权限变化、包扫描、组织前缀校验）→ 通过/拒绝（原因）→ 发布或回退 → 客户端 `GET /api/plugins` 反映 `currentRelease` → 下架触发 `removals`。

### 6.2 模型目录发布

编辑 draft → 校验（价格、能力、别名、引用完整性）→ 生成新 `registryRevision` → 灰度组织/区域 → 观察错误与收入 → 全量或回滚。

### 6.3 账号删除

用户发起挑战 → 状态查询 → 冷静期 → 后台可见待处理 → 到期自动执行（或审批）→ 吊销会话、匿名化、保留审计 → 用户侧 `accountDeletionRestored` 仅在同一账号恢复时出现。

### 6.4 强制下线/封禁

封禁账号 → 写入风控表 → 网关拒绝新 token → 现有 refresh session 全部吊销 → device-link/chat WSS 主动关闭（4401/普通关闭）→ 审计。

---

## 7. 与客户端契约的边界

- 后台不得改变客户端可见字段语义；所有变更必须保持 append-only。
- 后台操作产生的状态必须通过对应服务的公开 API 反映（例如插件下架通过 `removals`，模型通过 catalog revision）。
- 后台不直接写客户端本地数据；不提供“远程执行命令”能力。

---

## 8. 实现优先级

| 优先级 | 页面/能力 |
|---|---|
| P0 | 登录/MFA、仪表盘基础指标、用户查询、组织列表、设备列表、模型目录只读、插件审核只读、审计日志 |
| P1 | 用户封禁/会话吊销、组织/SSO 编辑、模型目录发布、插件审核写、Skill 审核、更新渠道、feature flags |
| P2 | 计费/退款、内容合规、供应商分享治理、告警规则、双人复核 |
