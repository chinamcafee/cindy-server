# Plugin Server / Skill Hub 逆向重建分册

> 文档定位：阶段 1「服务端能力清单」分册，目标是把客户端可观测契约还原成可直接指导 Go 服务端实现的端点、数据模型、状态机与校验清单。
>
> 证据等级：**【契约】** = 客户端严格 parser、协议包、测试固件固化的 wire 契约，服务端返回不符即被拒；**【文档】** = 仓内产品/开发规则明确写出的服务端行为；**【推测】** = 由字段、错误码、调用顺序反推，服务端可自定义但必须满足客户端契约。未逐条标注时：端点/字段/错误码默认契约，产品策略默认文档，表结构/对象键/后台流程默认推测。
>
> 生成日期：2026-10-19。源码快照：`cindy` 工作区（2026-10-19）、`cindy-protocol`（2026-08-10 最后提交）。能力编号：`PLUGIN-01..12`、`SKILL-01..12`。

## 1. 源文件索引与协议副本

### 1.1 关键源文件

根目录：`/Users/changzechuan/InnovationProjects/CindyProjects/`；表中路径均相对该根，`{a,b}` 表示同名目录下多个文件。

| 领域 | 文件 | 用途 |
|---|---|---|
| 公开基线 | `cindy-protocol/packages/plugin-protocol/src/{manifest,delivery,memberUpload}.ts` | ghost v2、Plugin API v2、成员上传 |
| 客户端上位集 | `cindy/packages/plugin-protocol/src/{manifest,delivery,usage,memberUpload}.ts` | ghost v2/v3、namespace、统计、用量、新失败码 |
| 客户端上位集 | `cindy/packages/plugin-protocol/src/{recommendations,routineEvents}.ts`、`internal/namespace.ts` | v3 附属声明与 namespace |
| 协议文档 | `cindy-protocol/docs/plugin-protocol.md` | 字段语义、兼容、成员上传、清理通告 |
| Plugin 市场 | `cindy/apps/desktop/src/main/plugin-market/{api,service,install,download,ledger}.ts` | 列表/详情/下载/默认安装/账本 |
| Plugin 发布 | `cindy/apps/desktop/src/main/plugin-publisher/{api,orchestrator,putObject,host}.ts` | 上传状态机与 Connection JWT |
| Plugin 用量 | `cindy/apps/desktop/src/main/plugin-usage/{reporter,reporting,service,identity}.ts` | 批量、ACK、去重、重试 |
| Skill Hub | `cindy/apps/desktop/src/main/skillhub/{marketService,hubApi,publishService,installService,organizationAutoInstall,infoMapping,syncMapping}.ts` | 全部 `/api/skills-hub/*` 调用点 |
| Skill 规则 | `cindy/docs/skillhub-organization-distribution.md` | 组织自动分发 |
| 用量规则 | `cindy/docs/plugin-usage-statistics-{implementation,plan}.md` | 统计口径与服务端回执 |
| 插件规则 | `cindy/docs/dev-rules/plugin-security-and-authoring.md` | v3、市场投影、安装/更新、包体边界 |

### 1.2 两个协议副本的关系

`cindy-protocol` 是公开基线（ghost v2 + Plugin API v2）；`cindy/packages/plugin-protocol` 是客户端实际消费的上位集，新增：

- ghost.json `schemaVersion: 3`、直接能力字段、`mainView`、`setup`、`routineEvents`、`mobile`、`recommendations`；
- 交付层 `namespace`、`currentOrganization`、`organizationAccessError`、`usageStatistics`、`downloadStatistics`、下载身份字段；
- 用量 `usage.ts` 与成员上传新增失败码。

**重建服务端应以客户端上位集为目标，以公开基线为兼容下限。** 只按 `cindy-protocol` 实现会拒绝 v3 包、漏发 namespace、漏掉统计与用量回执。

### 1.3 边界

- `docs/dev-rules/plugin-download-api.md` 描述的是插件运行时 `cindy.downloads`（Host 代下匿名文件），不是 Plugin Server 端点，本分册不把它当服务端契约。
- Plugin Server 与 Skill Hub 是两个独立部署、独立基址：`pluginApiBaseUrl`、`cindySkillHubApiBaseUrl`；区域（cn/global）各自部署，客户端不跨区回退。
- Skill 的调用统计只存在于客户端本地 SQLite；Skill Hub 没有 `usage-events` 端点。服务端只提供下载计数 `stats.downloads`。

## 2. 能力总览

| 编号 | 能力 | 消费端 | 优先级 |
|---|---|---|---|
| PLUGIN-01 | 市场列表：分页、搜索、scope、企业准入错误、可选统计 | desktop | P0 |
| PLUGIN-02 | 详情：客户端兼容 Release 投影、完整 manifest、图标、namespace | desktop | P0 |
| PLUGIN-03 | 下载签票：短期 HTTPS URL、sha256/size、下载计数 | desktop | P0 |
| PLUGIN-04 | 组织成员上传：prepare、raw PUT、commit | desktop publisher | P0 |
| PLUGIN-05 | 上传状态与我的发布：状态机、失败码、审核状态、分页 | desktop publisher | P0 |
| PLUGIN-06 | ghost.json v2/v3 校验与发布准入 | server 内部 | P0 |
| PLUGIN-07 | Release 审核、版本、上架/下架、清理通告 | server 内部 | P0 |
| PLUGIN-08 | 组织命名空间、插件前缀、`currentOrganization` | desktop | P0 |
| PLUGIN-09 | `defaultInstall` 计算与退订语义 | desktop | P1 |
| PLUGIN-10 | 用量上报：批量事件、eventId 去重、ACK | desktop | P1 |
| PLUGIN-11 | 共享统计读取：使用次数、下载次数、7 日窗口 | desktop | P1 |
| PLUGIN-12 | 客户端版本兼容投影与历史 Release 回退 | desktop | P0 |
| SKILL-01 | 市场列表：分页、排序、搜索、分类、可用过滤、我的发布 | desktop | P0 |
| SKILL-02 | 详情/批量详情：可见性、审核、版本、删除标记 | desktop | P0 |
| SKILL-03 | 文件浏览：文件树、单文件内容、哈希 | desktop | P1 |
| SKILL-04 | 版本列表与版本选择 | desktop | P1 |
| SKILL-05 | 发布：init、OSS 直传、commit、版本递增 | desktop | P0 |
| SKILL-06 | 审核与扫描状态：机审、人审、可见性审核 | desktop | P0 |
| SKILL-07 | 可见性与组织分发：public/shared/private、团队/部门 | desktop | P0 |
| SKILL-08 | 发布管理：PATCH、删除、下架、set-visibility | desktop | P1 |
| SKILL-09 | 下载签票与安装包校验 | desktop | P0 |
| SKILL-10 | 组织自动安装 `auto-install` | desktop | P1 |
| SKILL-11 | 分类、团队、部门身份端点 | desktop | P1 |
| SKILL-12 | Skill 包模型与校验：SKILL.md、忽略规则、大小、确定性 | server 内部 | P0 |

## 3. Plugin Server 端点全表

### 3.1 通用约定

- 基址：`pluginApiBaseUrl`；路径均以 `/api` 开头。
- 鉴权：普通客户端 `Authorization: Bearer <Access Token>`；匿名可读 public。组织成员上传使用 **Connection JWT**，audience 固定 `<orgSlug>:cindy-publisher`，不是 Access Token。
- 客户端版本头：`X-Cindy-Version: <SemVer>`（`CINDY_CLIENT_VERSION_HEADER = x-cindy-version`）。缺失或非法按“版本未知”处理。
- 错误信封：`{"error":{"code":"...","message":"..."}}`。列表/详情/下载的 4xx 错误会被客户端按业务码分流。
- 时间：带毫秒 UTC ISO 8601（`YYYY-MM-DDTHH:mm:ss.sssZ`）；SHA-256：64 位小写十六进制。

### 3.2 端点表

| # | 方法/路径 | 用途 | 鉴权 | 请求 | 响应 | schemaVersion | 幂等 | 主要错误 |
|---|---|---|---|---|---|---|---|---|
| 1 | `GET /api/plugins` | 目录列表 | 可选 Bearer；企业条目需组织身份 | `scope=all`（客户端固定）、`limit=100`、`query?`、`cursor?` | `plugins[]`、`nextCursor`、`removals[]`、`currentOrganization`、`organizationAccessError?` | 2 | 否 | 401/429/5xx；企业码见 §3.7 |
| 2 | `GET /api/plugins/:pluginId` | 详情 | 可选 Bearer | 路径参数 | `plugin`（含 `currentRelease.manifest`） | 2 | 否 | 404（不可见/已清理/无兼容版本）、401 |
| 3 | `GET /api/plugins/:pluginId/releases/:releaseId/download` | 下载签票 | 可选 Bearer | 路径参数；`X-Cindy-Version` | `url`、`expiresAt`、`sha256`、`sizeBytes`；可选身份四元组 | 无 | 否 | 404、403、429；身份不一致 |
| 4 | `POST /api/publisher/uploads` | 成员上传 prepare | Connection JWT | `{sizeBytes, sha256}`；可选 `Idempotency-Key` | `{uploadId, putUrl, headers, expiresAt, status:"awaiting_upload"}` | 无 | 可选 `Idempotency-Key` | 400/401/403/429/503 |
| 5 | `PUT <putUrl>` | 上传 `.cindy` 原始字节 | 签名 URL，无 Bearer | 原始 body；必须精确 `Content-Length` | 200/409 视为对象已在；3xx 拒绝；5xx 转 commit | 无 | 对象键防覆盖 | 403 过期、409 已存在、5xx |
| 6 | `POST /api/publisher/uploads/:uploadId/commit` | 启动异步发布 | Connection JWT | 空对象 `{}`；可选 `Idempotency-Key` | `{uploadId, status}`；HTTP 202 | 无 | 可选 `Idempotency-Key` | 400/401/403/404/429/503 |
| 7 | `GET /api/publisher/uploads/:uploadId` | 上传/审核状态 | Connection JWT | 路径参数 | §3.6 状态 DTO | 无 | 只读 | 403/404 |
| 8 | `GET /api/publisher/releases/mine` | 我的发布 | Connection JWT | `cursor?` | `{releases[], nextCursor}` | 无 | 只读 | 403/429 |
| 9 | `POST /api/plugins/:pluginId/usage-events` | 批量用量上报 | 可选 Bearer（匿名 public 允许） | `{events:[{eventId,releaseId,occurredAt}]}` | `{accepted[], duplicate[], rejected[]}` | 无 | `eventId` 唯一 | 400、403、404、413、429、503 |

### 3.3 列表 `GET /api/plugins`

请求参数（客户端实际发送）：

```
?scope=all&limit=100&query=<trimmed>&cursor=<opaque>
```

- `limit` 上限 100；`query` 对 `name/description/author/ghostId` 做服务端搜索（具体匹配方式【推测】）。
- `cursor` 必须原样回传；服务端当前使用 `sortOrder + Plugin.id` 的复合位置，同时兼容滚动期旧 Plugin ID 游标。客户端遇到 `PLUGIN_LIST_CHANGED` 会从第一页重试一次。
- 列表只含“当前身份可见且在架”的条目；被清理条目不回列表。

响应字段：

```jsonc
{
  "schemaVersion": 2,
  "plugins": [{
    "id":"cxxxxxxxxxxxxxxxxxxxxxxxx","ghostId":"acme-report","name":"Report","description":null,
    "author":null,"scope":"public","namespace":null,"organizationId":null,"defaultInstall":false,
    "currentRelease":{"id":"release-id","version":"1.0.0","sha256":"…64hex…","sizeBytes":1024,
      "publishedAt":"2026-10-12T08:00:00.000Z","icon":null},
    "usageStatistics":{"usageCount":"123","usageCountLast7Days":"12","asOf":"…"},
    "downloadStatistics":{"requestCount":"4321","last7DaysRequestCount":"12"}
  }],
  "nextCursor":null,"removals":[],
  "currentOrganization":{"organizationId":"org_123","orgSlug":"acme","pluginPrefix":"acm"},
  "organizationAccessError":{"code":"PLUGIN_NAMESPACE_CLIENT_REQUIRED","message":"请升级 Cindy"}
}
```

- `id` 固定 `^c[a-z0-9]{24}$`；`ghostId` 固定 `^[a-z0-9][a-z0-9-]{0,31}$` 且排除 Windows 保留名。
- `scope` 仅 `public|organization|personal`；组织条目 `organizationId` 非空、`namespace=orgSlug`，其余两者为 `null`。
- `name/description/author` 必须与 `currentRelease.manifest` 一致；`ghostId=manifest.id`，`version=manifest.version`。
- `currentRelease.icon`：发布时从包内提取、独立存储。`null` 表示未声明或暂无对象；有值时 `mimeType` 必须 `image/*`，URL 必须 HTTPS，`sha256/sizeBytes/expiresAt` 合法，否则整个列表解析失败。
- `currentOrganization`：每页重复或仅首页均可；客户端取第一个非 null。`pluginPrefix` 为 `null` 表示组织已登录但未登记前缀；对象存在但缺 `pluginPrefix` 键会被判畸形。
- `organizationAccessError`：仅当 `currentOrganization` 非 null、且本页无企业条目/清理通告时允许出现。出现表示 `scope=all` 的 public 部分仍返回，企业部分被拒。
- `removals`：可选；缺失规范化为 `[]`；每页必须完整且相同；客户端按 `pluginId` 跨页去重；同页 `plugins` 与 `removals` 不得有相同 `pluginId`。

### 3.4 详情 `GET /api/plugins/:pluginId`

- 只返回 active 且对当前身份可见、存在客户端兼容 Release 的插件；否则 404。
- `plugin.currentRelease` 必须包含完整、已规范化的 `manifest`；`manifest.id = ghostId`，`manifest.version = currentRelease.version`，`manifest.namespace = plugin.namespace`。
- 展示字段必须与 manifest 的 `name/description/author` 逐字一致；`description/author` 缺失时为 `null`。
- 声明 `network.secrets[].source = "oidc-token"` 的 manifest 只允许 `scope = organization`；public/personal 详情必须被服务端拒绝发布，客户端 parser 也会拒绝。
- 详情与列表的 `id/ghostId/scope/organizationId/namespace/defaultInstall/currentRelease.id` 必须一致；客户端会先拉列表再拉详情做绑定校验。

### 3.5 下载 `GET /api/plugins/:pluginId/releases/:releaseId/download`

响应：

```json
{"pluginId":"cxxxxxxxxxxxxxxxxxxxxxxxx","releaseId":"release-id","ghostId":"acme-report",
 "namespace":"acme","url":"https://...","expiresAt":"2026-10-12T08:05:00.000Z",
 "sha256":"…64hex…","sizeBytes":1024}
```

- 无 `schemaVersion`；只在列表/详情成功解析后请求。
- 身份四元组可选；但出现任一键时，`pluginId/releaseId/ghostId/namespace` 必须齐全且合法。建议始终返回。
- `url` 必须 HTTPS、短期、单跳可用（客户端下载器 `redirect: 'error'`）；不得把 Bearer 转发给对象存储。
- `sha256/sizeBytes` 必须与客户端所选 Release 摘要完全一致；否则客户端 `PRECONDITION_FAILED`。
- 每次成功签发即计一次下载；包含升级、重装、再次取票。**不是成功安装量**。计数用于 `downloadStatistics.requestCount`。

### 3.6 成员上传

**prepare** `POST /api/publisher/uploads`

```json
{"sizeBytes": 1048576, "sha256": "…64hex…"}
```

- body 只允许这两个键，出现 `organizationId/membershipId/ghostId/version/objectKey` 等一律 400；`sizeBytes` 为 `1..134217728`（128 MiB）的安全整数。
- 身份只来自已验证的 Connection JWT；幂等键走 `Idempotency-Key` 请求头，相同身份范围与同一幂等键重放返回同一 upload session。
- 响应 `putUrl` 必须 HTTPS；`headers` 必须是对象存储签名要求的精确头，客户端原样携带；header 名合法、值不含 CR/LF；`expiresAt` 建议 1 小时（客户端 fallback 60 分钟并预留 90 秒 commit 余量）。

**PUT**：客户端流式上传原始 `.cindy`，携带 prepare 的 headers 与精确 `content-length`；不跟随重定向。
- 200 成功；409 视为对象已存在（可继续 commit）；3xx 视为未写入、可重试同一 URL；5xx 客户端会直接 commit 同一 upload；对象键必须不可覆盖（如 `x-oss-forbid-overwrite`）。

**commit** `POST /api/publisher/uploads/:uploadId/commit`

- body 必须为空；不得覆盖 actor、organization、ghostId、version、hash、大小或对象位置；必须返回 HTTP 202，`expired` 是 body 状态，不是 4xx。
- 只启动或复用持久化异步任务；相同身份与幂等键重放必须观察同一任务/结果，不能重复创建 Release 或审计记录。

**status** 与 **my-publishes**：

```ts
interface PluginMemberUploadStatusResponse {
  uploadId: string;
  status: 'awaiting_upload'|'validating'|'publishing'|'succeeded'|'failed'|'expired';
  pluginId: string|null; releaseId: string|null; ghostId: string|null; version: string|null;
  reviewStatus: 'pending'|'approved'|'rejected'|null;
  failure: {code: string; message: string}|null;
}
```

- `succeeded` 必须同时带非空 `pluginId/releaseId/ghostId/version/reviewStatus`；其他状态 `reviewStatus` 必须为 `null`。
- `failed` 必须带 `failure`，其他状态禁止带；`message` 是用户可见原因（≤1000 字符），不得包含对象键、审计 detail、凭证或签名 URL。
- `failure.code` 必须匹配 `^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*$`、≤64 字符；新码客户端保留为 `knownCode:null`，不会整包失败。
- 上传任务状态与 Release 审核状态独立；`succeeded` 后审核仍可 pending→approved/rejected，客户端会继续查 status 刷新。
- my-publishes 只含当前已验证成员有权查看的记录；`nextCursor` 为 1–4096 字符不透明串或 null。

### 3.7 组织命名空间与前缀

- `namespace`：organization = `orgSlug`，public/personal = `null` 或省略；作者 `ghost.json` 不允许声明，发布时必须拒绝。
- `currentOrganization.orgSlug` 是 Auth 永久组织 slug（`^[a-z0-9](?:[a-z0-9-]{0,126}[a-z0-9])?$`）；`pluginPrefix` 是组织登记前缀（`^[a-z0-9]{2,16}$`，不含尾连字符），`null` 表示未登记。
- 列表内所有企业条目/清理通告的 `organizationId` 与 `namespace` 必须与 `currentOrganization` 一致（当 `orgSlug` 存在时）。
- `scope=all` 企业部分被拒时允许 `PLUGIN_NAMESPACE_CLIENT_REQUIRED`（客户端不支持命名空间）、`PLUGIN_ORGANIZATION_CONTEXT_UNAVAILABLE`（无法确认企业身份）、`PLUGIN_NAMESPACE_CONFIG_REQUIRED`（企业插件配置不可用）。
- 新组织插件 ghostId 必须满足前缀规则：未登记返回 `PUBLISH_PREFIX_UNREGISTERED`，不匹配返回 `PUBLISH_GHOST_ID_PREFIX_MISMATCH`；分隔符未在客户端固化，建议 `<prefix>-<name>`。

## 4. ghost.json manifest 校验清单

### 4.1 版本与顶层

| 项 | v2 | v3 |
|---|---|---|
| `schemaVersion` | 必须 `2` | 必须 `3` |
| `slots` | 必须为数组；公开基线要求非空，客户端 v3 兼容层允许空数组并丢弃已知空能力槽；未知但格式合法的槽保留但不授权 | 禁止出现；出现即拒 |
| `minCindyVersion` | 可选 | 必须声明，合法 SemVer |
| 新插件建议 | 仅存量兼容 | 新插件统一使用 v3 |
| 未知顶层字段 | 忽略 | 原样保留，但不解释、不展示、不授权 |

- **id**：`^[a-z0-9][a-z0-9-]{0,31}$`，排除 Windows 保留名（`con/prn/aux/nul/com1-9/lpt1-9`，含扩展名变体）。
- **name** 1–64 非空；**version** 1–32 非空（不强制 SemVer，仅展示与版本键）；**author** 1–64 非空；**description/whenToUse** 1–300 非空。
- **kind** 可省略，写了必须 `"chip"`；**entry** 必填安全相对路径；**launch** 仅 `on-demand|resident`，缺省 on-demand。
- **minCindyVersion** 合法 SemVer；`0.0.0` / `0.0.0-*` 视为开发构建恒兼容；非法版本不参与比较；**namespace** 作者禁止声明（含显式 null），由服务端注入。

### 4.2 路径与文件声明

- 安全相对路径：`a/b/c.ext`，`/` 分段，段字符 `^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,63}$`，总长 ≤256；禁止 `\`、空段、`.`、`..`、Windows 保留名。`entry/icon/settingsHtml/panel.html/mainView.html/node.entry/node.entries/locales/skill.dir/manual.dir` 共用。
- 大小写折叠冲突：`locales`、`manual.dir`、`skill.items[].dir`、声明文件路径之间不得相同或互为祖先/后代。
- 包内文件上限：`ghost.json` 256 KiB；icon 512 KiB；locale JSON 64 KiB；SKILL.md 64 KiB；MANUAL.md 与手册内每个 Markdown 64 KiB。

### 4.3 locales

- 支持语言固定 `zh-CN/en/ja/ko`；声明时 **en 必填**；每个值必须是包内安全相对路径且以 `.json` 结尾。
- 不同语言路径大小写折叠后不得重复、不得互为祖先/后代，也不得与 `ghost.json/entry/icon/settingsHtml/panel.html/mainView.html/node.entry/node.entries/skill.dir/manual.dir` 冲突；旧客户端忽略该字段。

### 4.4 图标

- 扩展名白名单 `.png/.jpg/.jpeg/.webp/.gif`，对应 `image/png|jpeg|webp|gif`；发布时服务端必须从包内提取、校验普通文件/非符号链接、≤512 KiB，并独立存储；不得把包内相对路径直接下发。

### 4.5 panel / mainView

- **panel**：`html` 必填安全相对路径；`title` 1–64；`position` 仅 `left|right|tab`，`top/bottom` 显式拒绝，缺省 right；`minWidth` 120–1200、`defaultFraction` 0.05–0.8，`position:"tab"` 时禁止两者；v2 声明 panel 必须有 `panel` 槽、有槽必须有 panel；v3 顶层 `panel` 即能力。
- **mainView**：`html` 必填；`title` 1–64；`icon` 仅 `puzzle/globe/code/folder/database/chart-column/image/message-circle/calendar-days`，缺省 puzzle；v2 与 `main-view` 槽严格成对，v3 直接声明；与 panel 独立授权，不附赠网络/文件/凭证。

### 4.6 tools / card / agent

- **tools**：1–16 项；`name` `^[a-z][a-z0-9_-]{0,63}$` 且唯一；`description` 1–1024；`parameters` 为对象且 `JSON.stringify` ≤16 KiB；v2 必须同时有 `tool` 槽，v3 直接声明。
- **card**：仅允许 `externalLinks: boolean`；true 为独立加档权限；v2 与 `card` 槽成对；v3 `card:{}` 表示基础渲染。
- **agent**：v2 仅允许 `background:true`（且必须为 true）；v3 允许 `background/errand/tasks/hiddenTasks/schedule` 布尔；`hiddenTasks` 必须与 `tasks:true` 同声明；`agent:{}` 表示仅真实用户点击触发。
- **badge**：v2 槽或 v3 布尔；必须同时有 panel，未读点必须有可打开内容。

### 4.7 cindy / subscribe

- **cindy** 类目与动作：`image: generate|edit`、`video: generate|edit`、`media: deposit`、`text: oneshot`、`embed: text`、`search: web`；每个类目非空、动作不重复；详单不能为空对象；`oneshotModel` 1–128 且必须与 `text` 含 `oneshot` 成对；`search.web` 必须同时有 `tool` 槽和至少一个工具；v2 旧字段 `model` 归一化为 `cindy`。
- **subscribe**：`topics` 仅 `turn|session`；`hooks` 仅 `will-user-message|will-assistant-message`；二者至少一个；声明 hooks 时 `launch` 必须 `resident`。
- **routineEvents**：仅 v3；1–32 个事件；`type` `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$` 且唯一；`name` 1–200；`fields` ≤32 且唯一、同名正则。

### 4.8 network 与凭证

- **hosts**：1–8 条（仅声明 `connections` 时可空）；小写域名、至少两段；通配只允许最左 `*.`；不收 IP、端口、路径、协议；去重。
- **secrets**：1–4 条；`key` `^[a-z][a-z0-9_]{0,31}$` 且唯一；`label` 1–64；`source` 仅 `user|login-email|oauth|login-feishu-token|oidc-token|gh-cli`；`inject` 必填。
- **inject**：`header` 1–64 字母/数字/连字符，禁止 `host/content-length/transfer-encoding/connection/cookie/origin/referer/content-type`；`format` ≤200 且恰含一个 `{value}`；`hosts` 必须是非空子集且逐字取自 `network.hosts`（oidc 禁止通配）。
- **user 凭证**：必须同时声明 `settingsHtml`；凭证由设置页经 `/secrets` 只写通道收单；`input` 已退役，遗留 `"ghost"` 接受并忽略，其余拒绝。**login-email/login-feishu-token**：禁止 `url/exchange/input:"ghost"`，settingsHtml 豁免。
- **oidc-token**：`inject` 固定 `Authorization: Bearer {value}`；`inject.hosts` 必须显式非空、精确域名、无通配；禁止 `url/exchange/oauth/input`；只允许 organization Release。**gh-cli**：必须 `settingsHtml`；`inject` 固定 `Authorization: Bearer {value}`，`inject.hosts` 只能是 `api.github.com`；禁止 `exchange/oauth/input`。
- **oauth**：仅 `source:"oauth"` 可声明；`authorizeUrl/tokenUrl` 必须 HTTPS、默认端口、无内嵌凭证、命中 hosts；`clientSecret` 必须与 `clientId` 成对；scopes 0–256、每条 1–200 无空白且唯一；`pkce` 布尔；`scopeDelimiter` 仅 `","`；`extraAuthorizeParams` 1–8 且禁止协议保留参数；`redirectPort` 1024–65535；`tokenBroker` 与 `clientSecret` 互斥；`brokerBounce` 必须与 `tokenBroker+redirectPort` 同声明。
- **exchange**：仅 https 默认端口、命中 hosts；`bodyFormat` ≤2048 且恰含一个 `{value}`；`contentType` 仅 `application/json|application/x-www-form-urlencoded`；`tokenPath` 点分路径；`ttlSeconds` 60–2592000。
- **connections**：1–2 条；必须声明 `settingsHtml`；`key` 与 secrets/node 共用命名空间；`inject.hosts` 禁止声明；`maxConnections` 1–8，缺省 8。

### 4.9 node

- `entry` 必填、安全相对路径、必须以 `.js`/`.cjs` 结尾；不能与浏览器沙箱 `entry` 同文件（大小写折叠比较）。
- `protocol` 仅 `json-rpc-stdio|mcp-stdio`；`lifecycle` 仅 `on-demand|resident`；`resident` 时禁止 `idleTimeoutSeconds`；后者 30–3600。
- `entries` ≤4 条，每项 CJS、与主入口/沙箱入口/彼此不重复；`childSpawn` 布尔。
- `secretBindings` 1–4 条；每条 `key` 唯一、`label` 1–64、`methods` 1–16 且安全方法名；`entry` 必须逐字命中 `node.entry` 或 `node.entries`；`mcp-stdio` 禁止 `initialize/notifications/initialized`；`url` HTTPS 无凭证；必须 `settingsHtml`。
- v2 必须有 `node` 槽；v3 直接声明。`node.secretBindings` 与 `network.secrets/connections` 的 key 不得撞名。

### 4.10 preview / skill / manual / setup

- **preview**：v2 与槽严格成对，v3 直接声明；`hosts` 1–4，语法同 network，另允许 `localhost/127.0.0.1/[::1]`；去重。
- **skill**：v2 与槽严格成对，v3 直接声明；`items` 1–4；`dir` 安全相对路径且唯一；`name` `^[a-z0-9]+(?:-[a-z0-9]+)*$`、≤64 且唯一；`description` 1–1024。SKILL.md 的 `name/description` 必须逐字一致。
- **manual**：独立顶层字段，不是 slot；`items` 1–8；`dir` 含固定 `MANUAL.md`；`name` 同 skill name 规则；`description` 1–300；目录内仅普通 `.md`、逐文件 ≤64 KiB；不得与声明文件/locales 路径冲突。
- **setup**：v3 可声明的使用前置；`requires` 0–8 组，每组 `anyOf` 1–8 条；字符串条目 `secret:<key>` / `connection:<key>`，或对象 `{kv,label}`；引用必须命中 `network.secrets`、`network.connections` 或 `node.secretBindings`；禁止引用 Host 派生来源（login-email/gh-cli/oidc-token）；kv 键 1–64、label 1–64、必须有 `settingsHtml`；同组不得重复。
- **command**：1–32、不含空白与 `/`，必须有 tools；**keywords**：1–8、每项 2–24，已废弃但保留校验。
- **recommendations**：v3 可选作者内容，不是权限；建议按 `validateGhostRecommendations` 限制 ≤24 条、每条 id/label/prompt、locales 5 语言、整包 ≤64 KiB。

### 4.11 v2 slot 到 v3 直接字段

`tool→tools`、`card→card`、`panel→panel`、`main-view→mainView`、`subscribe→subscribe`、`skill→skill`、`cindy→cindy`、`agent→agent`、`node→node`、`network→network`、`preview→preview`；`notify/badge/confirm/fs/library/session-context/pick/workspace/ios-simulator` 映射到同名布尔字段（`iosSimulator` 已退役，仅保留识别）。v2 的 `model` 归一化为 `cindy`。

### 4.12 包体边界

| 包类型 | 压缩包 | 解压总量 | 条目数 |
|---|---:|---:|---:|
| 普通沙箱 `.cindy` | 8 MiB | 32 MiB | 256 |
| 含 node 的 `.cindy` / 成员上传 | 128 MiB | 256 MiB | 2048 |
| Skill 包（客户端安装） | 200 MiB | 500 MiB | 10000 |
| Skill 产品红线（建议发布准入） | 10 MiB | — | — |

ZIP 还必须拒绝：路径穿越、绝对路径、盘符、反斜杠、符号链接、加密条目、大小写折叠重名、重复条目、`__MACOSX` 噪声（Skill 忽略规则）、manifest 不在根、解压后超限。

## 5. 发布/审核状态机与失败码

### 5.1 上传任务

```
awaiting_upload → validating → publishing → succeeded
   └→ expired/failed   └→ failed/expired        └→ (审核态独立刷新)
```

- commit 只创建/复用持久任务；worker 重新读取对象，校验大小、SHA-256、ZIP、manifest、组织成员状态、发布授权、ghostId/版本冲突。
- `succeeded` 之后进入 Release 审核，上传任务不再变化；失败必须落库为稳定码，不能把内部异常直接透传。

### 5.2 Release 审核与上架

```
release created (pending review)
      ├→ approved → on-shelf (active) → revoked/withdrawn
      └→ rejected (不可交付)
plugin on-shelf → delisted → removal notice(purge) → 可重新上架（removedAt 刷新）
```

- 客户端只消费“已批准 + 曾发布并上架 + 未撤销 + 与当前 Cindy 版本兼容”的 Release；`current` 不兼容时回退到历史兼容项中按内部创建顺序最新者，没有兼容项时列表不下发、详情/下载 404。
- 同一插件同一版本不得重复创建 Release；并发 commit 用 `(pluginId, version)` 唯一约束，冲突返回 `PUBLISH_VERSION_EXISTS` 或让幂等重放观察同一结果。

### 5.3 成员上传失败码全表

| 失败码 | 含义 | 处置 |
|---|---|---|
| `UPLOAD_OBJECT_MISSING` | 对象不存在 | 重新 prepare/PUT |
| `UPLOAD_SIZE_MISMATCH` | 对象大小与 prepare 不一致 | 重新上传 |
| `UPLOAD_SHA256_MISMATCH` | 对象哈希不一致 | 重新上传 |
| `PLUGIN_PACKAGE_INVALID` | ZIP/manifest/包结构不合法 | 修复包 |
| `MEMBERSHIP_INACTIVE` | 组织成员身份失效 | 联系管理员 |
| `PUBLISH_NOT_AUTHORIZED` | 无发布权限 | 联系管理员 |
| `PLUGIN_GHOST_ID_CONFLICT` | ghostId 冲突 | 换 id |
| `PUBLISH_PREFIX_UNREGISTERED` | 组织未登记插件前缀 | 管理员登记 |
| `PUBLISH_GHOST_ID_PREFIX_MISMATCH` | ghostId 不符组织前缀 | 改 id |
| `PUBLISH_GHOST_ID_INVALID` | 新 id 形状非法 | 改 id |
| `PLUGIN_GHOST_ID_RESERVED` | `cindy-` 等官方保留 | 改 id |
| `PLUGIN_NAMESPACE_CONFIG_REQUIRED` | 企业命名空间配置缺失 | 稍后重试/管理员 |
| `PLUGIN_NAMESPACE_CLIENT_REQUIRED` | 客户端版本不支持 | 升级客户端 |
| `PUBLISH_VERSION_EXISTS` | 版本已存在 | 升版本 |
| `PUBLISH_STORAGE_UNAVAILABLE` | 对象存储不可用 | 稍后重试 |
| `PUBLISH_INTERNAL_ERROR` | 内部错误 | 稍后重试 |

同步 HTTP 错误码（客户端会映射文案）：`FORBIDDEN`、`MEMBERSHIP_INACTIVE`、`INVALID_PARAMS`、`RATE_LIMITED`、`RATE_LIMIT_UNAVAILABLE`、`AUTH_CONTEXT_UNAVAILABLE`、`STORAGE_UNAVAILABLE`、`CONNECTION_UNAUTHORIZED`、`INVALID_CONNECTION_TOKEN`、`CONNECTION_TOKEN_EXPIRED`、`PUBLISH_GHOST_ID_INVALID`、`PLUGIN_GHOST_ID_RESERVED`、`PLUGIN_NAMESPACE_CLIENT_REQUIRED`、`PLUGIN_ORGANIZATION_CONTEXT_UNAVAILABLE`、`PLUGIN_NAMESPACE_CONFIG_REQUIRED`。

## 6. 版本兼容、默认安装与清理通告

### 6.1 版本兼容

- `PLUGIN_API_SCHEMA_VERSION` 固定 2（envelope）；`ghost.json` 独立演进的 v2/v3 不能替代 envelope 版本。
- `supportsCindyVersion(current, min)`：`min` 缺失恒兼容；合法 SemVer 比较；`0.0.0` 与 `0.0.0-*` 开发构建恒兼容；非法版本不兼容。
- 客户端缺失 `X-Cindy-Version` 或头非法：服务端视为“未知版本”，只能交付没有 `minCindyVersion` 的 Release。
- `ios-simulator` v2 槽必须同时声明 `minCindyVersion`；v3 已退役，不授予能力。
- 列表、详情、下载必须使用同一客户端版本选择同一个 Release；客户端下载真实包后仍用同一函数复核 manifest，不兼容则首次安装失败、更新保留旧 Release。
- 旧客户端不支持 envelope 版本时停止对账并保留本地状态；服务端不提供 capability 协商或多 current Release。

### 6.2 默认安装

- `defaultInstall` 是“对当前请求身份计算后的有效值”：未安装时自动安装，不表示强制安装或强制启用；客户端只在 `installState=not-installed|conflict`、ghostId 在当前目录唯一（或组织 identity 唯一）、未退订、未移除内置插件时执行。
- 组织默认插件可“接管”无有效市场来源的普通本地安装，但必须精确匹配 namespace、organizationId、安装记录 scope 与批准事实；公共默认插件没有该接管权。
- 服务端默认下发的首装与更新不向用户弹权限确认；用户退订后再自行安装的扩权更新仍需确认。受众模型（组织管理员/产品白名单/部门）未在客户端固化，服务端可自定义但必须按身份稳定计算。

### 6.3 清理通告

```jsonc
{
  "pluginId": "cxxxxxxxxxxxxxxxxxxxxxxxx",
  "ghostId": "acme-report",
  "scope": "organization",
  "namespace": "acme",
  "organizationId": "org_123",
  "action": "purge",
  "removedAt": "2026-10-12T08:00:00.000Z"
}
```

- 只对已验证的组织身份下发其所属组织的通告，与请求 `scope` 无关；当前 `scope` 恒 organization、`action` 恒 purge。
- 每页完整重复，不受 query/cursor 影响；客户端按 `pluginId` 去重，整轮分页完成后再执行。
- 同页与 `plugins` 不得有相同 `pluginId`；跨页冲突以在架为准，不清理。`removedAt` 是最近一次下架时间，重新上架再下架会刷新，不保证单调。
- 客户端执行前必须双重校验 `pluginId`、来源为服务端市场、本地记录 scope 为 organization；不通过时最多标记不可更新，不删除本地内容。
- 被清理插件 detail/download 维持 404；`purge` 表示删除本地安装副本及插件本地数据。

## 7. 插件用量统计模型

### 7.1 事件

```json
{"events":[
  {"eventId":"<callId UUID>","releaseId":"<release-id>","occurredAt":"2026-10-12T08:00:00.000Z"}
]}
```

- 每批只含一个插件；客户端每批最多 100 条，请求超时 5 秒；413 时下一次减半。
- 不发送用户身份、工具名、任务 ID、参数、结果正文或凭证；服务端也不得要求这些字段。
- `eventId` 是客户端 callId，作为全局去重键；`releaseId` 必须属于该插件且存在发布/上架历史（允许历史版本，不仅 current）。
- `occurredAt` 保留客户端事件时间；服务端产生 `receivedAt`。未来时间容差建议 5 分钟，超出拒绝，不静默改写。
- 已撤销版本：仅当 `occurredAt` 早于撤销时间时可接收；插件当前不可见或企业权限已撤销时不接收新回执。

### 7.2 去重与 ACK

```json
{"accepted":["id1"],"duplicate":["id2"],"rejected":[{"eventId":"id3","code":"EVENT_ID_CONFLICT"}]}
```

- 数据库对 `eventId` 建唯一约束；同 eventId 同事实返回 `duplicate`，不覆盖原行；同 ID 不同内容返回 `rejected/EVENT_ID_CONFLICT`。不能只写 Redis 计数器；事件表不自动过期，避免长期补发时去重凭证丢失。
- 结构不合法整批拒绝；合法批次内可逐项拒绝。ACK 必须在数据库事务提交后返回；存储失败返回可重试错误（如 `USAGE_STORAGE_UNAVAILABLE`），不得假装成功。
- 客户端只确认 ACK 中实际返回且与请求匹配的 eventId，缺失项保持 pending；ACK 三数组只能包含本批 eventId 且不得重复。`rejected[].code` 已知集合：`EVENT_ID_CONFLICT`、`RELEASE_NOT_DELIVERED`、`OCCURRED_AT_OUT_OF_RANGE`；未知 UPPER_SNAKE 码仍解析但不标记停止。

### 7.3 统计读取

- `usageStatistics`：`usageCount`、`usageCountLast7Days`、`asOf`；十进制字符串，按事件时间统计滚动 168 小时。
- `downloadStatistics`：`requestCount`、`last7DaysRequestCount`；每次成功签发下载 URL 计一次，包含升级/重装/取票，**不是安装量**。
- 字段可选；非法/缺失指标独立省略，真实 0 返回 `"0"`；`installationCount` 虽被 parser 容忍，但当前没有成功安装回执，不要伪造。共享统计建议缓存 60 秒，个人查询不缓存；列表先权限过滤再取统计。

### 7.4 建议表结构

```sql
plugin_usage_events(event_id text primary key, plugin_id text not null, release_id text not null,
  occurred_at timestamptz not null, received_at timestamptz not null,
  foreign key (plugin_id, release_id) references plugin_releases(plugin_id, id));
create index on plugin_usage_events (plugin_id, occurred_at);
plugin_download_counters(plugin_id, day date, count bigint, primary key(plugin_id, day));
```

## 8. Skill Hub 端点全表

### 8.1 通用约定

- 基址：`cindySkillHubApiBaseUrl`；**不得**回退旧 `skillhubApiBaseUrl`（旧服务只面向 XD 身份，新服务自行路由只读兼容）。
- 鉴权：`Authorization: Bearer <Access Token>`；读接口可匿名，写接口必须登录且服务端重新判定组织/个人身份；401 自动 refresh 一次。
- 分页：`page` 从 1 开始、`pageSize` 客户端默认 24；响应 `{items,total}`，客户端用 `page*pageSize < total` 推导 `nextCursor` 字符串。
- 排序：`sort=trending|downloads|updated_at|created_at`，同时发送 `order=desc`。
- 目录 scope：`scope=market|team` 查询参数；缺省为“native/管理视图”，三者不得互相别名。`catalogScope` 会随列表项回传，后续 detail/download 必须带同一 scope。
- 错误信封同 Plugin Server；错误码用于客户端业务分支。

### 8.2 端点表

| # | 方法/路径 | 用途 | 鉴权 | 请求 | 响应 | 主要错误 |
|---|---|---|---|---|---|---|
| 1 | `GET /api/skills-hub/skills` | 市场/团队列表 | 可选 Bearer | `page,pageSize,sort,order,q,category,scope` | `{items:[SkillInfo],total}` | 401/429/5xx |
| 2 | `POST /api/skills-hub/skills/list` | 带已安装版本的可用列表 | 可选 Bearer | 同 1 + `{installedSkills:[{slug,version}]}` | `{items,total,availableCount?}` | 400/401 |
| 3 | `GET /api/skills-hub/users/published` | 我的发布 | Bearer | `page,pageSize,sort,order,q,category` | `{items,total}` | 401 |
| 4 | `POST /api/skills-hub/skills/batch-detail` | 批量详情 | 可选 Bearer | `{slugs:string[]}`，≤100/批；`scope?` | `{items:[SkillInfo],availableCount?}` | 400 |
| 5 | `GET /api/skills-hub/skills/{slug}` | 详情 | 可选 Bearer | `scope?` | `SkillInfo` 或 `{deleted:true}` | 404 |
| 6 | `GET /api/skills-hub/skills/{slug}/files` | 文件树 | 可选 Bearer | `version?`、`includeHashes=1`、`scope?` | `{slug,version,files:[{path,size,language,truncated,sha256?}]}` | 404 |
| 7 | `GET /api/skills-hub/skills/{slug}/file` | 单文件内容 | 可选 Bearer | `path`、`version?`、`scope?` | `{path,size,language,truncated,content}` | 400/404 |
| 8 | `GET /api/skills-hub/skills/{slug}/versions` | 版本列表 | 可选 Bearer | `scope?` | 版本对象数组（形状未强校验） | 404 |
| 9 | `POST /api/skills-hub/skills/publish/init` | 发布初始化 | Bearer | `{slug, version?}` | `{nextVersion, ossKey, uploadUrl}` | 401/409/429 |
| 10 | `PUT <uploadUrl>` | OSS 直传 zip | 签名 URL | `Content-Type: application/zip`，原始字节 | 2xx/403 | 403 过期、5xx |
| 11 | `POST /api/skills-hub/skills/publish/commit` | 发布提交 | Bearer | 见 §8.3 | 成功结果（客户端不解析字段） | `VERSION_RACE` 等 |
| 12 | `GET /api/skills-hub/skills/{slug}/scan` | 扫描/审核状态 | Bearer | `version?`、`scope?`，`no-store` | `{status,rejectionReason?,gates?,scorecard?}` | 404 |
| 13 | `GET /api/skills-hub/skills/{slug}/download` | 下载签票 | Bearer | `version?`、`scope?` | `{url,expiresAt,fileHash,fileSize,zipSha256}` | 404 |
| 14 | `PATCH /api/skills-hub/skills/{slug}` | 更新发布信息 | Bearer | `{displayName?,summary?,description?,tags?,contentLocale?,visibility?,teamSlug?}` | 结果对象（客户端不解析） | `PERMISSION_DENIED` |
| 15 | `DELETE /api/skills-hub/skills/{slug}` | 删除 | Bearer | 无 | 结果对象 | `SKILL_DELETED` |
| 16 | `POST /api/skills-hub/skills/{slug}/unpublish` | 下架 | Bearer | 无 | 结果对象 | `SKILL_UNPUBLISHED` |
| 17 | `POST /api/skills-hub/skills/{slug}/set-visibility` | 设置可见性 | Bearer | `{visibility,teamSlug?,visibleSlugs?}` | `{slug,visibility,requestedVisibility?,reviewStatus?}` | `INVALID_VISIBILITY` |
| 18 | `GET /api/skills-hub/skills/{slug}/visibility` | 读取可见范围 | Bearer | 无 | `{sharedTeams:[{id,slug,name}],visibleDepts:string[]}` | 404 |
| 19 | `GET /api/skills-hub/categories` | 分类 | 可选 Bearer | `scope=market|team`、`includeEmpty=false?` | 分类数组（含 children/skillCount/mySkillCount） | 5xx |
| 20 | `GET /api/skills-hub/users/teams` | 我的团队 | Bearer | 无 | `[{slug,name,type,source?,isPersonal?,myRole?}]` | 401 |
| 21 | `GET /api/skills-hub/users/departments` | 我的部门 | Bearer | 无 | `{departments?,firstLevelDepts?,allDeptIds?}` | 401 |
| 22 | `GET /api/skills-hub/auto-install` | 组织自动安装清单 | Bearer | `cursor?` | `{skills:[{name,version,catalogScope:"team"}],nextCursor}` | 404/5xx |

### 8.3 发布 init/commit

**init**：

```json
{"slug":"my-skill","version":"1.2.0"}
```

- `version` 可选；服务端返回 `nextVersion`（服务端建议的下一个版本）、`ossKey`、`uploadUrl`。客户端先按用户输入版本改写 SKILL.md 并打包，commit 时才用 `nextVersion`，因此包内 frontmatter 版本可能与发布版本不同，服务端应解耦两者。
- `slug` 规则 `^[a-z0-9][a-z0-9-]{0,127}$`；版本冲突在 commit 返回 `VERSION_RACE`，客户端最多重试 2 次（重新 init + PUT + commit）。

**commit**：

```jsonc
{"ossKey":"…","slug":"my-skill","version":"1.2.0","changelog":"…","displayName":"…","summary":"…",
 "description":"…","tags":["…"],"visibility":"public|private|shared","deptTeamSlug":"od-…",
 "teamSlug":"…","visibleSlugs":["…"]}
```

- 服务端必须校验 `ossKey` 属于当前用户/技能/版本，对象存在、大小与哈希、zip 结构、SKILL.md、slug/version 一致性；commit 是不可逆副作用，成功后客户端立即写本地 snapshot/registry 并开始扫描轮询。
- 首次发布才允许改 `tags/visibility/teamSlug/deptTeamSlug/visibleSlugs`；更新只允许元信息与 changelog。

### 8.4 可见性与审核

- 原始 `visibility`：`private|shared|public`；个人身份只允许 `public/private`，组织身份只允许 `public/shared`（DEPARTMENT_SCOPED）。
- `teamSlug` 可为团队 slug 或 `od-` 部门 id，`null` 表示收回到个人；`deptTeamSlug` 是部门团队归属。
- `set-visibility` 直接生效返回 `{slug,visibility}`；公开审核中返回 `{slug,visibility:<旧值>,requestedVisibility:"public",reviewStatus:"pending"}`，旧目录可见性继续生效，管理视图已切到原生记录。
- `GET /visibility` 返回 `{sharedTeams:[{id,slug,name}],visibleDepts:string[]}`；客户端不提交部门 ID 做筛选，服务端按成员身份选择。
- 公开审核状态另由 `visibilityReview:{requestedVisibility:"public",status:"pending|rejected",reason?}` 表达。

### 8.5 扫描/审核状态

`GET /scan` 返回 `status`，客户端归一化：

| 服务端可返回值 | 客户端归一化 |
|---|---|
| `published/passed/pass/approved` | published（终态，通过） |
| `pending/machine_reviewing` | pending（等待，停止轮询） |
| `scanning` | scanning（继续轮询） |
| `quarantine/warning/warn/manual_reviewing` | quarantine（人工审核，停止轮询） |
| `rejected/failed/fail/blocked` | rejected（终态） |

- `gates[]` 建议 `{name,label?,status,issues?}`；`scorecard` 客户端只透传。客户端发布后每 5 秒轮询，终态或 pending 停止；详情/列表刷新时重读 `moderationStatus/pendingVersion`。

### 8.6 Skill 发布错误码

| 类别 | 错误码 |
|---|---|
| 身份/权限 | `AUTH_REQUIRED`、`PERMISSION_DENIED`、`NOT_AUTHOR`、`SKILL_HUB_READ_ONLY` |
| 命名/归属 | `NAME_TAKEN`、`INVALID_NAME`、`INVALID_DEPT`、`CATEGORY_REQUIRED`、`SKILL_DELETED`、`SKILL_UNPUBLISHED` |
| 参数/包体 | `INVALID_PARAMS`、`MANIFEST_INVALID`、`SKILL_FILE_TOO_LARGE`(413)、`PACK_FAILED` |
| 版本/对象 | `VERSION_RACE`、`CHECKSUM_MISMATCH`、`OSS_PUT_FAILED`、`OSS_PUT_EXPIRED`、`OSS_OBJECT_NOT_FOUND`、`API_KEY_MISSING` |
| 可见性/流量 | `INVALID_VISIBILITY`、`RATE_LIMITED`、`SERVICE_UNAVAILABLE`、`REQUEST_REJECTED`、`INTERNAL` |

## 9. Skill 数据模型、安装与组织分发

### 9.1 SkillInfo（列表/详情/批量详情）

```jsonc
{
  "slug":"my-skill","icon":"https://…","displayName":"My Skill","summary":"一句话","description":"详情",
  "version":"1.2.0","marketVersion":"1.2.0","pendingVersion":{"version":"1.3.0","status":"pending"},
  "visibilityReview":{"requestedVisibility":"public","status":"pending","reason":"…"},
  "folderHash":"…","fileHash":"…","owner":{"type":"organization|personal","slug":"acme","name":"Acme"},
  "publisher":{"name":"发布者"},"visibility":"private|shared|public",
  "moderationStatus":"published|pending|scanning|quarantine|rejected",
  "updatedAt":"2026-10-12T08:00:00.000Z","isMine":true,"isCreator":true,"canManage":true,
  "categories":[{"slug":"dev","name":"开发","source":"platform"}],
  "tags":[{"slug":"cli","name":"CLI","source":"platform"}],"githubUrl":null,"stats":{"downloads":123}
}
```

- `version` 是安装/下载默认版本；`marketVersion` 与 `pendingVersion` 用于区分“已发布版本”和“审核中版本”。
- `visibility` 客户端映射：`public` → PUBLIC，其余 → DEPARTMENT_SCOPED；原始三值另存 `publishedVisibility`。
- `isMine`/`isCreator`/`canManage` 由服务端按查看者计算，客户端不自行推断权限。
- `folderHash` 是目录内容摘要，`fileHash` 是 ZIP 校验和；二者不可混用。

### 9.2 版本与文件

- 版本对象建议至少包含 `version`、`createdAt`、`changelog`、`status`、`size`、`sha256`、`downloads`；客户端当前只把 `/versions` 当数组透传，字段未强校验。
- 文件树来自已发布版本快照；`path` 为 POSIX 相对路径，拒绝绝对路径、`..`、反斜杠、符号链接。
- 单文件读取返回文本内容与 `truncated`；服务端应有单文件大小/总响应上限（建议 1 MiB 文本、图片单独处理）。
- 版本一旦发布不可原地修改；新版本新对象、新路径。

### 9.3 Skill 包模型

- ZIP 根直接是 `SKILL.md`；`SKILL.md` 必填，frontmatter 必填 `name`、`description`（非空），`version` 可为字符串或数字。
- 客户端打包确定性：条目时间戳固定 `2020-01-01T00:00:00Z`、不生成隐式目录条目、忽略规则与 `folderHash` 一致。
- 忽略路径：敏感路径、`node_modules`、`__MACOSX`、`.venv`、`.hg`、`.svn`、Terraform 凭证、`.m2/settings*.xml`、`.DS_Store`、`._*`、`.xdt-tmp`、scanner 重命名备份。
- 大小：客户端安装上限 200 MiB / 500 MiB / 10000 条目；产品建议单 Skill <10 MiB。服务端必须校验 ZIP 安全、解压总量、条目数、frontmatter、slug 一致、无重复/大小写折叠冲突。

### 9.4 安装与版本选择

客户端流程（服务端只需保证数据正确）：未指定版本时先 `GET /skills/{slug}` 取 `version`；再 `GET /download?version=` 取签名 URL、`fileSize`、`zipSha256`；校验大小与 SHA-256、解压 staging、拒绝 zip-slip/超限；同名目录存在且未 `force` → `CONFLICT_USER_OWNED`，force → 旧目录备份/替换；原子换目录、写本地 registry、建 Claude/Codex/Pi 兼容链接。

服务端必须保证：同一 `(slug,version)` 的 zip 字节不可变；`zipSha256` 与 `fileSize` 精确；旧版本仍可按版本下载；删除/下架后默认版本不可下载，但已安装本地副本不受影响。

### 9.5 审核与可见性状态

- `moderationStatus` 管版本内容审核：pending/scanning/quarantine/rejected/published；`pendingVersion` 表示新版本审核中、旧版本仍对外提供；无 pendingVersion 时 `moderationStatus` 作用于当前 latest。
- `visibilityReview` 管“转为 public”的可见性审核：pending 时旧可见性继续生效，rejected 带 reason。
- 组织技能不能 `private`，个人技能不能 `shared`；服务端是最终裁决，客户端 identityPolicy 只是 UI 投影。公开市场只展示已通过审核且可见性允许的技能，组织目录按团队成员/部门受众过滤。

### 9.6 组织分发 `auto-install`

响应严格为：

```json
{"skills":[{"name":"org-skill","version":"1.2.0","catalogScope":"team"}],"nextCursor":null}
```

- 每页最多 100 条；`name` 匹配 `^[a-z0-9][a-z0-9-]{0,127}$`；`version` 必须 `\d+\.\d+\.\d+`；`catalogScope` 必须 `"team"`。
- 客户端登录/身份变化后请求一次，整个分页总预算 5 秒；不轮询策略。
- **部门 ID 永远不由桌面端提交**；服务端按已认证 membership 与两类受众（团队/部门）选择候选。
- 组织候选按同名 slug 覆盖产品默认候选；产品默认来自 CDN `cfg/auto-sync-skills.json`，不是 Skill Hub 端点。
- 策略变化不卸载已安装文件；旧服务 404/瞬时错误不清除本地忽略偏好，也不阻止产品默认处理。
- 部署顺序：先服务端与迁移，再发布消费该端点的桌面端；旧桌面仍可手动安装。

## 10. 存储与对象键设计建议

### 10.1 原则

- 包与图标全部私有桶，禁止公共读；只通过短期签名 URL 下发，不把 Bearer 转发到对象存储。
- 内容寻址、不可变：key 含 sha256；同 key 不允许覆盖；发布重试复用同一对象。
- 数据库存 `object_key/sha256/size_bytes/mime/created_at`；不把 URL 持久化。
- 签名 TTL：下载 10–15 分钟；图标 30–60 分钟；上传会话 1 小时。响应 `expiresAt` 必须与签名一致。
- 上传桶与发布桶分离；上传对象设置生命周期（如 24 小时）自动清理，commit 成功后复制/移动到发布桶或直接引用。

### 10.2 建议键

```text
# Plugin
plugin/uploads/<uploadId>/<sha256>.cindy                     # 成员上传暂存
plugin/releases/<pluginId>/<releaseId>/<sha256>.cindy        # 已发布包（不可变）
plugin/icons/<pluginId>/<releaseId>/<sha256>.<ext>           # 提取图标
plugin/quarantine/<uploadId>/<sha256>.cindy                  # 校验失败隔离

# Skill Hub
skillhub/uploads/<ownerType>/<ownerId>/<slug>/<version>/<zipSha256>.zip
skillhub/packages/<ownerType>/<ownerId>/<slug>/<version>/<zipSha256>.zip
skillhub/snapshots/<skillId>/<version>/manifest.json          # 可选：解压后索引
skillhub/icons/<skillId>/<sha256>.<ext>                       # 可选
```

- `ownerType=user|org`；`ownerId` 用稳定 ID，不用 slug。
- 版本目录与 DB 唯一约束双保险；对象键不包含用户可改的 displayName。
- 下载签名 URL 的 path 必须与 DB 记录绑定；服务端签票前重新校验身份/可见性/版本状态。

### 10.3 建议核心表

```sql
plugins(id pk, ghost_id, owner_type, owner_id, scope, organization_id, namespace, plugin_prefix,
  default_install_policy, status, created_at, updated_at, removed_at, unique(owner_type, owner_id, ghost_id));
plugin_releases(id pk, plugin_id fk, version, sha256, size_bytes, object_key, icon_object_key,
  icon_sha256, icon_mime, icon_size, review_status, shelf_status, min_cindy_version, manifest_json,
  sort_order, published_at, revoked_at, unique(plugin_id, version));
plugin_upload_sessions(id pk, actor_id, organization_id, idempotency_key, size_bytes, sha256, object_key,
  status, failure_code, failure_message, plugin_id, release_id, ghost_id, version, expires_at,
  unique(actor_id, organization_id, idempotency_key));
plugin_removal_notices(plugin_id pk, ghost_id, scope, organization_id, namespace, action, removed_at);
plugin_usage_events(event_id pk, plugin_id, release_id, occurred_at, received_at);
skillhub_skills(id pk, slug, owner_type, owner_id, visibility, moderation_status, latest_version,
  pending_version, created_at, updated_at, deleted_at, unique(owner_type, owner_id, slug));
skillhub_skill_versions(id pk, skill_id fk, version, zip_sha256, size_bytes, object_key, status,
  changelog, created_at, unique(skill_id, version));
skillhub_skill_audiences(skill_id, audience_type, audience_id);
skillhub_auto_install_policies(id pk, organization_id, skill_id, team_id, dept_id, enabled);
```

- `plugin_releases.sort_order` 用于目录稳定排序与 cursor；`plugin_id + sort_order + id` 建索引。
- `plugin_removal_notices` 按 organization_id 过滤；detail/download 查询必须排除 removed。
- `skillhub_skills.visibility` 与 audiences 分离，便于团队/部门多受众。
- 所有状态字段用枚举/check 约束，避免字符串漂移。

## 11. Go 实现映射建议

### 11.1 包结构

```text
internal/plugin/{http,service,manifest,store,storage,worker}/
  http: list detail download publisher usage | service: catalog publish review defaultinstall removal
  manifest: validate v2 v3 paths network node setup | store: plugin release upload usage repo
  storage: signer objectkey | worker: publish review stats_rollup
internal/skillhub/{http,service,store,storage,worker}/
  http: catalog detail files publish visibility admin | service: catalog publish scan distribution visibility
  store: skill version audience repo | storage: signer objectkey | worker: scan publish
```

- 两个服务共享 `internal/platform/auth`（Access Token/Connection JWT）、`httpx`（错误信封/分页/JSON）、`objstore`（签名/校验）、`idgen`。
- manifest 校验器按 TS 规则逐条移植：JSON `UseNumber`、保留 v3 未知字段、v2 slot 映射、路径大小写折叠、SemVer 比较；不要用宽松的 `map[string]any` 校验后丢失未知字段。
- 建议把协议包测试里的正反例做成 Go golden fixtures，至少覆盖：v2/v3、namespace、oidc/gh-cli、setup、mainView、locales/manual 冲突、网络通配、node secretBindings、大小写冲突。

### 11.2 关键实现点

1. **鉴权与 namespace**：public 目录匿名；organization 必须解析 membership/orgId；personal 只允许本人；成员上传只接受 audience `<orgSlug>:cindy-publisher` 的 Connection JWT；namespace 由组织事实注入，作者声明即拒。
2. **Release 投影**：查询“approved + on-shelf + 未撤销 + 版本兼容”的 current；不兼容时按 `sort_order/published_at` 回退历史兼容项；列表/详情/下载共用同一函数。
3. **cursor 与幂等**：Plugin 用 `base64url({"sortOrder":...,"id":...})` 并兼容纯 pluginId 旧游标，Skill 用 page/pageSize+total；成员上传 `Idempotency-Key` 唯一、usage `event_id` 主键、skill `(skill_id,version)` 唯一，commit 重放返回同一 task/release。
4. **异步与审核**：commit 只落 `publishing` 任务并投 outbox；worker 校验对象、manifest、权限、冲突后创建 Release，失败写稳定码；Release 审核与上传任务分离，清理通告按组织下发，重新上架刷新 `removedAt`。
5. **统计**：下载计数在签票成功后写入（按天聚合也可，但必须精确覆盖 168h）；usage 事件落库后聚合；按 `plugin_id + occurred_at` 索引。
6. **存储与错误**：签名前校验对象存在/大小/哈希；PUT 使用 `x-oss-forbid-overwrite` 防覆盖；下载签名禁止 redirect；内部错误统一映射为 `PUBLISH_*`/`USAGE_*`/`SKILL_*`，不把对象 key、SQL、URL 写入 message。
7. **限流与可观测**：列表/详情/下载按 IP+身份，成员上传按 membership，usage 独立业务 key，429 带 `Retry-After`；按 pluginId/releaseId/uploadId 打结构化日志，脱敏插件身份与签名 URL。

### 11.3 测试建议

- 契约测试：直接用客户端 parser/fixtures 校验 Go 响应；覆盖缺失可选字段、`null`、未知枚举、非法时间/哈希、namespace 不一致。
- 状态机测试：上传六态、审核三态、上架/撤销/清理、版本并发冲突、幂等重放。
- 权限测试：匿名/public、组织成员、跨组织、personal、Connection JWT 过期、企业准入拒绝只返回 public。
- 兼容测试：v2/v3 包、`0.0.0` 开发版本、缺失 `X-Cindy-Version`、历史兼容回退、无兼容版本 404。
- 存储测试：PUT 409/3xx/5xx、签名过期、对象缺失/大小/哈希不符、ZIP bomb/zip-slip。
- Skill 测试：版本竞态、首次发布字段、公开审核 pending、组织受众、auto-install 分页与 100 条上限、旧服务 404。

## 12. 未确认项

| # | 未确认项 | 影响 | 建议 |
|---|---|---|---|
| 1 | Plugin 管理面（管理员登记前缀、设置默认安装受众、审核操作）端点 | 无法完整重建运营闭环 | 单独向服务端/运营确认；客户端不可观测 |
| 2 | 公开/个人插件的发布入口（CI raw POST、Admin） | public/personal Release 来源 | 以成员上传为模板另建内部 API，标记为内部 |
| 3 | 审核 SLA、审核人角色、拒绝原因字段 | 审核状态机细节 | 先实现 pending/approved/rejected 三态与审计 |
| 4 | `pluginPrefix` 与 ghostId 的精确拼接规则 | 组织发布准入 | 固化 `<prefix>-<slug>` 并写入发布文档 |
| 5 | Plugin 列表 `query` 的匹配字段与排序权重 | 搜索体验 | 先 name/description/ghostId 前缀+全文 |
| 6 | `sortOrder` 的生成规则与重排策略 | cursor 稳定性 | 用发布时间+id 的稳定复合排序 |
| 7 | 下载/图标签名 TTL 的具体值 | 客户端只校验未过期 | 下载 10–15 分钟、图标 30–60 分钟 |
| 8 | 下载计数是否在签名时还是 GET 时递增 | 统计口径 | 以“成功签发 URL”为准，注释写明 |
| 9 | `installationCount` 是否允许出现 | 客户端 parser 容忍但产品无回执 | 当前省略，不伪造 |
| 10 | Skill `/versions` 响应字段 | 客户端只透传 | 先返回 version/createdAt/changelog/status/size/sha256 |
| 11 | Skill 文件内容截断阈值与二进制处理 | 文件浏览体验 | 文本 1 MiB 截断；二进制返回元数据 |
| 12 | Skill 公开审核的触发条件与审核后台 | public 可见性 | 先实现 requestedVisibility+reviewStatus |
| 13 | `teamSlug` / `deptTeamSlug` / `visibleSlugs` 的精确语义 | 组织分发 | 按团队 slug 或 `od-` 部门 id 解析，服务端统一 |
| 14 | 组织默认安装受众模型（全员/部门/团队） | `defaultInstall` 计算 | 建 policy 表，按 membership 求值 |
| 15 | auto-install cursor 格式与排序 | 分页 | 不透明 cursor，按 skill id/name 稳定排序 |
| 16 | 旧 `skillhubApiBaseUrl` 的只读兼容路由 | 旧客户端 | 新服务内部路由，不让新客户端回退 |
| 17 | Plugin 成员上传对象存储是否为 OSS/S3 | 签名实现 | 抽象 Signer 接口，按部署配置 |
| 18 | 审核中的 Release 是否出现在管理端列表 | 运营体验 | 管理端单独查询，不混入客户端目录 |
| 19 | v3 `mobile`/`recommendations` 是否参与发布校验 | 作者内容 | 保留 opaque；recommendations 用 24 条/64 KiB 规则 |
| 20 | 插件用量事件保留期 | 去重与补发 | 当前不自动过期；按合规另定归档策略 |
