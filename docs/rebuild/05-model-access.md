# Cindy 服务端逆向重建 05：模型访问 / 模型目录 / BYOK / 计费 / 语音 / Embedding / OAuth Broker

## 0. 元信息

| 项目 | 内容 |
|---|---|
| 文档编号 | RB-05（阶段 1 交付物） |
| 版本 | v1.0 |
| 生成日期 | 2026-10-19 |
| 目标 | 用开源客户端与共享协议包重建服务端 wire contract，直接指导 Go 服务端实现 |
| 覆盖服务面 | model-access-server、匿名 model-catalog、BYOK、billing、voice-server、embedding gateway、oauth-broker、provider sharing/group 的服务端边界 |
| 客户端基线 | `cindy` 工作区 `da3d44e1a`（2026-10-19T00:18:09+08:00） |
| 协议包基线 | `cindy-protocol` 工作区 `aa1d60b`（2026-08-10T11:39:23+08:00） |
| 服务端源码 | `cindy-server` 当前仅有空文档目录，**无服务端实现可读** |
| 非目标 | Desktop/Mobile UI、任务编排、通用 device-link 协议、auth-server 账号登录流程 |

路径缩写：`C` = `/Users/changzechuan/InnovationProjects/CindyProjects/cindy`；`P` = `/Users/changzechuan/InnovationProjects/CindyProjects/cindy-protocol`；`S` = `/Users/changzechuan/InnovationProjects/CindyProjects/cindy-server`。

## 1. 证据等级与阅读约定

| 等级 | 含义 | 可支持结论 |
|---|---|---|
| A（契约） | 客户端严格 parser/validator 或两套独立消费端一致 | wire 字段名、闭合枚举、必填性、上限、错误分支可信 |
| B（文档） | 客户端运行期行为 + 仓内规范文档相互印证 | 状态机、超时、回退、缓存与幂等语义可信 |
| C（推测） | 单端注释、基准记录或由字段反推 | 服务端内部实现与数值阈值，需要真实服务端复核 |

本文默认：**A 级只证明客户端接受/拒绝的形状，不证明服务端已经实现**。凡是“必须由服务端保证”的段落，是实现要求而非现状描述。行号均指当前工作区文件。

关键版本事实：共享协议包只到 Catalog v2 / Registry v2，而生产 Desktop 已请求 **ListModels v5**、公共 Catalog 请求 **Registry v5**。`P/packages/model-access-protocol/src/types.ts:1-8` 不能当作当前服务端全量契约；当前客户端 Bean 在 `C/packages/model-providers/src/modelAccessBean.ts:13-18`，Desktop 请求在 `C/apps/desktop/src/main/model-access/modelsSyncRefresh.ts:14-16`。

## 2. 源文件索引

| 主题 | 权威源 |
|---|---|
| 区域端点清单 | `C/config/endpoint.json:3-22`、`C/config/endpoint.global.json:3-22` |
| 会话区域切换 | `C/docs/auth-realm-routing.md:9-18` |
| 统一 HTTP 客户端/401 刷新/错误壳 | `C/apps/desktop/src/main/serverApiClient.ts:4-7,82-99,131-205` |
| ListModels v5 Bean/校验 | `C/packages/model-providers/src/modelAccessBean.ts:13-18,189-345`、`modelAccessValidator.ts:800-875` |
| Desktop 模型同步 | `C/apps/desktop/src/main/model-access/modelsSyncRefresh.ts:13-94`、`index.ts:150-330` |
| 网关凭据同步/轮换 | `C/apps/desktop/src/main/model-access/credentialsSync.ts:43-64,182-330`、`index.ts:80-100` |
| 媒体模型与 Guide | `C/apps/desktop/src/main/model-access/mediaModels.ts:198-440`、`C/apps/desktop/src/shared/mediaInvocation.ts:1-100` |
| 匿名 Catalog 加载 | `C/packages/model-providers/src/source.ts:25-31,122-150,598-800` |
| Catalog/Provider 类型 | `C/packages/model-providers/src/types.ts:537-640,752-767` |
| Registry v5/覆盖层 | `C/packages/model-providers/src/modelAccessBean.ts:91-205`、`modelMetadataLayers.ts:19-63` |
| Registry 发布纪律 | `P/packages/model-access-protocol/MODEL_REGISTRY.md:67-137` |
| 媒体/Registry 版本协商 | `C/docs/model-registry-v4-media.md:1-70,157-195` |
| 目录维护与参考价 | `C/docs/dev-rules/model-catalog-maintenance.md:20-25,55-75,184-203,315-333` |
| BYOK 契约与严格 parser | `C/packages/model-providers/src/byok.ts:4-72,85-385` |
| BYOK 同步/缓存/路由 | `C/apps/desktop/src/main/model-access/byokSync.ts:39-260`、`byokRuntime.ts:20-38,98-180`、`byokProvider.ts:18-141` |
| 计费客户端 | `C/apps/desktop/src/main/billing/index.ts:18-470`、`billing/projection.ts:160-360` |
| 计费 wire 类型 | `C/apps/desktop/src/shared/billing.ts:53-305` |
| 组织配额 | `C/apps/desktop/src/main/usage/organizationAccountUsage.ts:5-27` |
| 语音会话/润色 | `C/apps/desktop/src/main/voice-input/CindyVoiceSessionClient.ts:23-240`、`LiteLlmTextModelClient.ts:112-264` |
| 语音 ASR 协议 | `C/apps/desktop/src/main/voice-input/RealtimeAsrWebSocketProvider.ts:39-71,536-607`、`VolcengineSaucAsrProvider.ts:43-63,145-170`、`C/apps/desktop/src/shared/voiceInputAsrProfiles.ts:60-190` |
| 语音词典 | `C/packages/voice-input-core/src/DictationDictionaryAdvisor.ts:1-110`、`dictionaryLearningPolicy.ts:4-28`、`C/apps/desktop/src/main/voice-input/dictionarySyncDriver.ts:32,141-143` |
| Embedding 协议/目录 | `C/packages/embedding-client/src/client.ts:50-807`、`types.ts:10-184`、`catalog.ts:22-101` |
| OAuth broker | `C/apps/desktop/src/main/cindy-brain/ghostOauthBroker.ts:13-181`、`ghostOauthFlow.ts:529-807` |
| 插件 broker 清单约束 | `C/packages/plugin-protocol/src/manifest.ts:590-625,2495-2560` |
| Feature flags | `C/apps/desktop/src/main/authManager.ts:3155-3206`、`canaryFlagSync.ts:48-70` |
| 供应商分享契约 | `C/docs/provider-sharing-contract.md:16-215`、`C/packages/device-link/src/providerShareApi.ts:224-286` |
| 供应商组（无服务端实体） | `C/docs/product-rules/provider-groups.md:50-76,200-282,455-500` |

## 3. 能力总览

优先级采用 `00-overview.md` 的实施批次口径：P0 = 客户端核心闭环；P1 = 协作与商业化；P2 = 运营与增强。它不等于产品重要性。

| 编号 | 能力 | 服务面 | 主要消费端 | 优先级 |
|---|---|---|---|---|
| MODEL-01 | 账号模型目录 ListModels v5 | model-access | Desktop/Mobile/媒体 | P0 |
| MODEL-02 | 推理网关凭据下发 | model-access | Desktop/Mobile | P0 |
| MODEL-03 | 网关凭据轮换 | model-access | Desktop 设置页 | P1 |
| MODEL-04 | 单模型媒体调用指南 | model-access | Desktop 媒体 | P1 |
| MODEL-05 | 批量媒体调用指南预检 | model-access | Desktop 媒体 | P1 |
| MODEL-06 | 云端组织周期配额 | model-access | Desktop 组织账号 | P0 |
| MODEL-07 | Credit Ledger 余额快照 | model-access | Desktop/Menubar/Mobile | P0 |
| MODEL-08 | Credit 用量与赠送投影 | model-access | Desktop/Menubar | P1 |
| MODEL-09 | 匿名公共 Catalog | model-catalog | 全部客户端 | P0 |
| MODEL-10 | 旧 OSS Catalog 兼容回退 | model-catalog/OSS | 迁移期旧客户端 | P2 |
| MODEL-11 | 公共 Registry v5 元数据/参考价 | model-catalog | 全部客户端 | P0 |
| MODEL-12 | 企业 BYOK 供应商目录 | model-access | Desktop 组织账号 | P1 |
| MODEL-13 | BYOK 成员凭据状态机 | model-access | Desktop 组织账号 | P1 |
| MODEL-14 | BYOK 连接 revision 与路由投影 | model-access 契约 | Desktop | P1 |
| MODEL-15 | 计费商品目录 | billing | Desktop 计费页 | P1 |
| MODEL-16 | 充值订单生命周期 | billing | Desktop 计费页 | P1 |
| MODEL-17 | 订阅购买与当前订阅 | billing | Desktop 计费页 | P1 |
| MODEL-18 | 套餐变更报价/确认/刷新/取消 | billing | Desktop 计费页 | P1 |
| MODEL-19 | 订阅管理门户 | billing | Desktop 计费页 | P2 |
| MODEL-20 | 语音听写会话与一次性 ticket | voice | Desktop/Mobile | P2 |
| MODEL-21 | ASR WebSocket 数据面（三协议） | voice/上游 | Desktop/Mobile | P2 |
| MODEL-22 | 托管润色 SSE | voice | Desktop/Mobile | P2 |
| MODEL-23 | 润色 prompt cache 预热 | voice | Desktop/Mobile | P2 |
| MODEL-24 | 语音词典与跨设备学习 | device-link | Desktop/Mobile | P2 |
| MODEL-25 | Embedding 网关 `/v1/embeddings` | XD Gateway | Desktop 插件/索引 | P2 |
| MODEL-26 | 插件 OAuth code exchange | oauth-broker | Desktop 插件 | P2 |
| MODEL-27 | 插件 OAuth token refresh | oauth-broker | Desktop 插件 | P2 |
| MODEL-28 | OAuth 公网回弹路由 | oauth-broker | 浏览器→本机 | P2 |
| MODEL-29 | 账号 feature flags | oauth-broker | Desktop/Mobile | P2 |
| MODEL-30 | 供应商分享成员关系与 relay | device-link | Desktop 跨账号 | P2 |
| MODEL-31 | 供应商组 | 无服务端实体 | Desktop 多机 | P2 |
| MODEL-32 | 区域/端点路由（sessionRealm） | 全部带 Bearer 服务 | 全部客户端 | P0 |

## 4. 通用协议约定

- **基址按区域成对存在**。CN 与 Global 分别使用 `model-access.cindy.com.cn/.app`、`voice.cindy.com.cn/.app`、`oauth-broker.cindy.com.cn/.app`、`device-link...`、`auth...`；组织 SSO 可让 CN 安装包登录 Global 组织，此时所有带 Bearer 的业务请求随 `sessionRealm` 切换。Go 服务端必须从部署配置注入本区域端点，不根据请求 Host 猜测区域。
- **鉴权**：除匿名 Catalog、OSS 旧目录、OAuth 浏览器回弹外，全部要求 `Authorization: Bearer <auth-server access token>`。客户端在 401 且错误码属于 `TOKEN_EXPIRED|INVALID_TOKEN|UNAUTHORIZED` 时刷新一次并重试；`ACCOUNT_UNAVAILABLE` 或刷新后仍 401 才退登。服务端不得把第三方上游拒绝误报成登录层 401。
- **错误壳**：`{"error":{"code":"...","message":"..."}}`，可选 `details`。客户端从该壳读取 code；非 JSON/空体统一为 `EMPTY_RESPONSE`。`redactErrorDetails` 调用只允许白名单业务码外泄，Go 端应始终返回稳定 code，message 不包含 token、密钥、上游正文。`429` 必须带 `Retry-After`。
- **超时**：模型目录 20s、凭据默认无显式超时但同步状态机 2s/8s 退避、Guide 单取 5s/20s、配额 8s、余额/用量 20s、计费 20s、语音会话/润色预热 10s。服务端应保证这些预算内可失败，而不是无限挂起。
- **缓存**：`/models`、BYOK 两接口由客户端强制 `cache: 'no-store'`；这是客户端请求语义，不替代服务端 `Cache-Control`。带账号态的响应默认应 `Cache-Control: no-store`，公共 Catalog 才使用 ETag/可缓存语义。
- **身份边界**：客户端用 `userId + realm` 组成认证世代；换号或同账号跨区时旧响应一律丢弃。服务端签发的 ticket/credential 也应绑定账号、区域、组织与设备，不能跨世代复用。

## 5. model-access-server 端点全表

基址：`modelAccessApiBaseUrl`。所有响应成功均为 2xx JSON；失败按统一错误壳。

| 方法 | 路径 | 请求要点 | 成功响应 | 超时/缓存 | 关键错误与客户端回退 |
|---|---|---|---|---|---|
| GET | `/api/model-access/models?schemaVersion=5` | 头 `X-Cindy-Model-Capabilities: audio_speech,display_pricing`；无 body | `{schemaVersion:5, accountTier, models[]}` | 20s；请求 `no-store`；只接受 v5 | 401 刷新；503 `MODEL_ACCESS_DISABLED`；403 `ORG_NOT_SUPPORTED`；429 `RATE_LIMITED`；parser 失败/空列表分别保留 LKG/清空 |
| GET | `/api/model-access/credentials` | 无 body | `{endpoint:string, apiKey:string}`；endpoint 必须 http(s) | 无显式缓存头 | 503→`disabled`；403→`unsupported`；其他失败退避 2s/8s 后 `failed`；非法 2xx 保留旧 key |
| POST | `/api/model-access/credentials/rotate` | 空 body | 同上，返回新 key | 无显式缓存头 | 失败原样抛 IPC；客户端映射 `STALE_ACCOUNT`/`INVALID_RESPONSE` |
| GET | `/api/model-access/invocation-guide?modelId=<id>` | `modelId` 为去掉 provider namespace 后的协议模型名 | `{modelId, guide:{schemaVersion:1,guideId,revision,connection:{providerId},operations[]}}` | 5s 预检 / 20s；无显式缓存头 | `MEDIA_INVOCATION_GUIDE_NOT_FOUND`；schema 不匹配→`CLIENT_UPGRADE_REQUIRED`；解析失败→`GUIDE_INVALID` |
| GET | `/api/model-access/invocation-guides` | 无 body/query | `{guides:[...]}`，最多 256 条 | 5s；无显式缓存头 | `GUIDE_SERVICE_UNAVAILABLE` 可重试；重复 modelId 使该模型不可用 |
| GET | `/api/model-access/quota` | 无 body | `{spend:number,maxBudget:number\|null,currency:"CNY"\|"USD",budgetResetAt:string\|null}` | 8s | 404 或 501 `MODEL_ACCESS_QUOTA_NOT_SUPPORTED`→回退旧网关；其他失败视为不可用 |
| GET | `/api/model-access/balance` | 无 body | `{planCredits,purchasedCredits,promotionalCredits,available,scale:9,observedAt}`，金额十进制字符串 | 20s | 401/403 `PERMISSION_DENIED`；404 未开通；501 `UNSUPPORTED_CAPABILITY` |
| GET | `/api/model-access/credit-usage` | 无 body | `{available,plan,purchased,promotional,promotionalGrants[],promotionalGrantsComplete,promotionalGrantConsistency:"OBSERVED",ledgerUpdatedAt,scale:9,observedAt}` | 20s | 同 balance |
| GET | `/api/model-access/byok/providers?schemaVersion=1` | 无 body | `{schemaVersion:1,organizationId,revision,providers[]}` | 20s；`no-store` | 白名单：`ORG_AI_GATEWAY_ERROR`、`BYOK_UNAVAILABLE`、`ORG_AI_MANAGEMENT_DISABLED`、`ORG_AI_NOT_ENABLED`、`ORG_AI_UNSUPPORTED`、`ORG_NOT_SUPPORTED`、`ORGANIZATION_BASELINE_NOT_READY`、`ORGANIZATION_SEAT_REQUIRED`、`MEMBERSHIP_INACTIVE`、`INVALID_PARAMS`、`UNAUTHORIZED`、`TOKEN_EXPIRED`、`INVALID_TOKEN`、`ACCOUNT_UNAVAILABLE`、`FORBIDDEN`、`RATE_LIMITED`、`INTERNAL_ERROR` |
| GET | `/api/model-access/byok/credentials?schemaVersion=1` | 无 body | `{schemaVersion:1,organizationId,credentials[]}`；每项 `ready`/`pending`/`error` | 20s；`no-store` | 同上；`pending` 30s 后最多自愈 3 次 |

**MODEL-01 服务端要求**：`/models` 是“当前账号可执行模型 + Gateway 实价 + 可用性”的唯一权威，也是媒体目录来源（客户端再按 `mode` 过滤图片/视频）。服务端应把上游 Gateway 的 `contextLength`、`supportedEndpoints`、`reasoning`、`supportsServiceTier`、`architecture` 一次性归一化为客户端的 `contextWindow`、`agents`、`efforts`、`supportsFastMode`、`modalities`，不要让客户端二次推导（`C/apps/desktop/src/main/model-access/index.ts:206-225`）。成功空数组表示账号当前没有模型；5xx 时客户端保留上一份成功快照，并撤销“权威拒绝”语义。`availability:"requires_payment"` 必须保留展示但不可路由；失败后的 LKG 会剔除付费差集。

向后兼容：Desktop 只请求 `schemaVersion=5`，但服务端应继续接受 v1–v4 老客户端并按对应版本裁剪字段；v5 的语音/展示价是**能力协商**字段，只有请求头声明 `audio_speech` 时才可下发独立 `audio_speech` 条目，只有声明 `display_pricing` 时才可下发 `displayPricing`。旧 V5 解析器遇到 `audio_speech + agents:[]` 会拒绝整份响应，因此不能用 `schemaVersion=5` 代替能力声明。

**MODEL-02/03 服务端要求**：GET 凭据可顺带探测当前 key 是否仍被网关接受，失效即签发新 key（`gatewayKeyRejection.ts:14`）；rotate 必须真正轮换并撤销旧 key，不能与普通 GET 合并。客户端只在值变化时落盘，避免 Codex 子进程 env-key 重启抖动；服务端应容忍同一账号短时间内重复 GET。

**MODEL-04/05 服务端要求**：Guide 是客户端执行媒体请求的**白名单协议**，不是文档展示。`operations[].capability` 必须属于 `image.generate`、`image.edit`、`video.generate`、`video.image_to_video`；`request` 必须是 POST、含 `bodyModelPath`、超时与请求/响应字节上限；同步响应用 `media[]` 提取，异步响应必须有 `taskIdPath` + `poll`。服务端必须保证同一 `modelId` 至多一条 Guide；批量接口上限 256，重复即视为该模型不可用。

## 6. 匿名公共 model-catalog

### 6.1 端点

| 方法 | 路径 | 说明 | 鉴权 |
|---|---|---|---|
| GET | `/api/model-catalog/catalog?registrySchemaVersion=5&registryMedia=1&registryLocalRuntimes=1` | 完整公共 Catalog：providers、presets、modelRegistry | 匿名 |
| GET | `{fallbackBaseUrl}/cfg/providers.json` | 迁移期旧 OSS 目录，解析前仅剥离退役的 `cindyModelMeta` | 匿名 |

客户端加载顺序：dev 本地文件 → 公共 Catalog API / 对应 LKG → 旧 OSS / 对应 LKG → bundled。远端预算默认 15s，每进程加载一次、无 TTL；坏 JSON/坏 schema 永不覆盖 LKG；Registry 另按 `updatedAt` 整份择新，不做逐字段拼接（`source.ts:598-800`）。

### 6.2 版本与能力协商

| 请求形态 | 服务端应返回 | 说明 |
|---|---|---|
| 无 `registryMedia`、未知值、重复参数、V1–V3 | 冻结兼容快照 | 不跟随完整正本 revision；独立 ETag |
| `registrySchemaVersion=4&registryMedia=1` | V4 完整目录 | 媒体字段可用；V5 官方参考价展开到旧 route 字段 |
| `registrySchemaVersion=5&registryMedia=1` | V5 完整目录 | `baseModels[].referencePriceGroups`、`referencePriceGroup`、`localModels` 生效 |
| `registryLocalRuntimes=1` | 可含 `localModels.llamacpp` | 未声明则不应依赖该域 |

- 每个响应版本必须使用**独立 ETag**，不能跨版本误命中 304。客户端当前不发送 `If-None-Match`，所以 ETag 主要用于 CDN/代理与部署验收；服务端仍应按规范实现强 ETag（对规范 JSON 字节哈希）并正确设置 `Vary`（若未来引入请求头协商）。
- 当前查询参数已编码版本与能力，缓存键应包含完整 URL；`Vary` 至少覆盖 `Accept-Encoding`，不要用 `Vary: *`。
- 旧服务端忽略新参数时返回旧目录，新客户端保持旧格式读取。发布顺序：服务端先行，客户端随后。

### 6.3 Catalog v5 顶层结构

`{version:string, providers:Provider[], presets?:ProviderPreset[], modelRegistry?:ModelRegistry}`。顶层只允许这四个字段，未知字段整份拒绝（`C/packages/model-providers/src/catalog.ts:816-823`）。

- `version` 是诊断/缓存比对字符串，不是 revision。
- `presets` 逐条校验，坏条目丢弃，不因预设错误回退整份目录。
- `modelRegistry` 是跨供应商公共元数据与参考价；不决定账号可用性，不覆盖 Gateway 实价。
- V5 `modelRegistry` 骨架：`schemaVersion:5`、`updatedAt`、`baseModels[]`（`id/aliases/defaults/referencePriceGroups`）、`models[]`（`id/modelRef/routes[]/status/perAgent/nativeApi/mode/modalities`）、`nativeApiRules[]`、`localModels?`。字段级结构见 §7.2。

### 6.4 Registry revision 纪律

- `updatedAt` 必须是 `Date#toISOString()` 形式的规范 UTC；同一 `updatedAt` 必须对应完全相同的 canonical JSON（对象键排序）。
- 修正/回滚只能以更晚 `updatedAt` 前向发布；禁止回拨时间或改写已发布 revision。
- 客户端比较顺序：先比时间，再比 canonical JSON；`older` 保留当前快照，`conflict`（同时间不同内容）保留 LKG 并告警，`invalid` 保留 LKG。
- 部署若需要区域差异，只能把 `newSessionDefault` 投影到该区域 `/models`，不得在保留同一 `updatedAt` 时改写 Registry；确需区域 Registry 变体时，每份不同 canonical JSON 必须使用独立、向前推进的 `updatedAt`。

## 7. 模型目录数据模型

### 7.1 三层对象

| 层 | 字段 | 说明 |
|---|---|---|
| Catalog | `version, providers[], presets?, modelRegistry?` | 匿名公共目录；严格闭合 |
| Provider | `id,name,source,agents,auth,access,titleModel,routing,models,imageModels,imageDefaults,videoModels,videoDefaults,audioModels,embeddingModels,embeddingDefaults` | 供应商连接与投影；`models` 按 agent 分组 |
| Registry | `schemaVersion,updatedAt,baseModels?,models,nativeApiRules?,localModels?` | 跨账号公共元数据；只收跨客户端仍有意义的事实 |

`Provider.models` 是 `Partial<Record<AgentKind, CatalogModel[]>>`；`routing` 按 agent 索引。`source` 区分内置/用户自定义/企业下发；企业连接走自定义路由但不能按个人连接编辑。`access` 描述额度来源；`titleModel` 是起标题一次性轻任务的模型 id。

### 7.2 Registry v5 条目

- `BaseModel`：`id`、`aliases[]`、`defaults: ModelMetadata`、`referencePriceGroups?: [{id,prices[]}]`。
- `ModelRegistryEntry`：`id`、`modelRef?`、`name`、`status?`、`group?`、`description?`、`contextWindow?`、`maxOutputTokens?`、`efforts?`、`defaultEffort?`、`sortOrder?`、`supportsFastMode?`、`defaultEnabled?`、`perAgent?`、`newSessionDefault?`、`nativeApi?`、`supportsImageInput?`、`mode?`、`modalities?`、`officialDocs?`、`routes[]`。
- `routes[]`：`providerId`、`modelId`、`agents[]`、`referencePrices?`、`referencePriceGroup?`、`defaults?`、`forceOverrides?`、`overrideReason?`、`overrideVerifiedAt?`。
- `nativeApiRules[]`：`{providerId, modelIdPrefix, nativeApi}`，为既有模型家族的新成员提供路由级原生协议规则。
- `ModelMetadata`：`nativeApi, mode, modalities, officialDocs, name, description, group, contextWindow, contextWindowMax, maxOutputTokens, efforts, defaultEffort, supportsFastMode, supportsImageInput, supportsToolCalls, reasoningRequired`。`contextWindowMax` 只允许出现在客户端投影，不在 Registry 公共 defaults。

### 7.3 ListModels v5 条目

| 组 | 字段 |
|---|---|
| 身份 | `id`（≤256 且唯一）、`mode`、`currency:"CNY"\|"USD"`、`agents`、`name`、`group`、`description`、`icon` |
| 能力/预算 | `contextWindow`、`maxOutputTokens`、`modalities:{input[],output[]}`、`efforts[]`、`defaultEffort`、`supportsFastMode`、`defaultEnabled`、`perAgent` |
| 目录语义 | `sortOrder`、`newSessionDefault`、`availability:"available"\|"requires_payment"` |
| 价格 | `inputCostPerToken`、`outputCostPerToken`、priority/cache/200k/272k/character/second/audio/image/video/tiered 等 `ModelPricing` 字段、`costDiscount` |
| 展示价 | `displayPricing?`，仅 header 协商，不参与计费/授权 |

版本演进：v1 基础字段；v2 增 `newSessionDefault`；v3 增 `pi` agent、`name/contextWindow` 必填、per-agent `wireProtocol`；v4 允许媒体 standalone `agents: []`；v5 增 `accountTier` 与每条 `availability`，并允许 `displayPricing`。Desktop 同步只接受 v5，旧/未知版本整份拒绝并保留上一份有效列表。v5 顶层只允许 `schemaVersion`、`accountTier`、`models` 三个字段，模型 id 必须唯一；`displayPricing` 缺失、null 或不可解析只省略展示块，不拒绝模型或整份目录。

### 7.4 能力语义

- **视觉输入**：`modalities.input` 含 `image` 或 `supportsImageInput:true`；显式 `false` 会移除继承来的 `image` 输入。客户端另有保守三态 `vision|no-vision|unknown`，只在缺结构化元数据时使用，不写回目录。
- **工具调用**：`supportsToolCalls` 表示模型能力；路由级 `supportsResponsesCustomTools` 表示 Codex Responses 是否原生接受 custom tools；`nativeApi` 决定协议。
- **推理档位**：统一 `minimal,low,medium,high,xhigh,max,ultra`；Pi 不支持 `ultra`。`efforts[]` 是可执行集合，`defaultEffort` 是推荐意图；`efforts:[]` 表示固定档，`defaultEffort:null` 表示无默认档。Pi 原生目录的稀疏 `thinkingLevelMap` 缺省为支持、`null` 为不支持，`xhigh/max` 必须显式映射。
- **媒体 mode**：`chat/responses`、`image_generation`、`video_generation`、`audio_generation`、`audio_speech`、`audio_transcription`、`realtime`、`embedding`。媒体能力为 `image.generate`、`image.edit`、`video.generate`、`video.image_to_video`；媒体原生协议含 `openai-images`、`openai-videos`、`xai-videos`、`openai-audio-speech`、`openai-audio-transcriptions`、`openai-realtime`、`openai-embeddings`。
- **媒体路由约束**：媒体 route 的 `agents` 必须为空；Provider 的 `imageModels/videoModels/audioModels/embeddingModels` 是 Registry 的投影。V4/V5 才允许空 agents 的媒体 standalone 条目。
- **上下文窗口**：`contextWindow` 是工作默认值，`contextWindowMax` 是上游容量，`maxOutputTokens` 是输出上限。OpenAI 聊天模型日常默认至多 272K；Astra/Sol/Terra/Luna 官方容量 1,050,000；用户显式 override 优先。`contextWindowVerified` 只活在 host 目录，不进入跨端 `ModelDescriptor`。
- **生命周期**：`status` 为 `preview|active|deprecated|retired`；`retired` 是显式墓碑，缺席不等于退役；`retired` 不得带 `newSessionDefault`。`sortOrder` 只影响选择器顺序，`defaultEnabled` 只影响默认可见，`newSessionDefault` 只影响新对话冷启动种子。多候选时低 `sortOrder` 优先，缺省排后，仍相同按响应数组序。

### 7.5 价格与币种

两层价格必须分开建模，不能互相覆盖：

1. **Registry 官方参考价 `ModelReferencePrice`**：`currency:CNY|USD`、`variant:standard|priority|batch|fast`、`inputPerMtok`、`outputPerMtok`、`cacheReadPerMtok?`、`cacheWritePerMtok?`、`cacheWrite1hPerMtok?`、`minInputTokens?`、`maxInputTokens?`、`effectiveFrom`、`effectiveUntil?`、`source:{kind:"provider-official",url,verifiedAt}`。单位是每百万 token；按输入区间和生效日期分档；同币种同 variant 的区间不得重叠；无匹配即估价未知。V5 把厂商参考价放在 `baseModels[].referencePriceGroups`，route 用 `referencePriceGroup` 选择；route 自己的 `referencePrices` 优先于组，且整组替换、不逐字段补齐。
2. **Gateway 实价**：`/models` 的 per-token 原值、`costDiscount` 与 priority/cache/200k/272k/字符/秒/音频/图片/视频/tiered 价格。客户端把 per-token 转 per-Mtok 生成 `ModelPriceQuote`；缺 `currency` 时不按区域猜，使用同目录已声明币种或上次账本币种，并标记 `currencyInferred`。

币种规则：`/models` 每条 `currency` 是该账号计价权威；混合币种会使账号币种不可用；未声明保留上次已知，最终回退 USD。账单/套餐的 `currency` 是字符串，金额是十进制字符串。服务端不得根据 `Accept-Language`、系统地区或登录区域推断币种。

### 7.6 local runtime

`modelRegistry.localModels` 形状为 `{version:1, models[], featuredIds[]}`：`models` 至多 32、`featuredIds` 至多 8；每项含 `id`、`modelRef?`、`name`、`aliases[]`、`variants[]`（`libraryName,sizeBytes,minUnifiedMemoryGb,appleSiliconOnly?`）、`llamacpp?`（`repo,file,quantization,sizeBytes,verifiedAt`）、五语 `descriptions`、`runtimeProfile:"plain"|"qwen-xhigh"`、`evidence[]`。它只描述下载与策展，不提供命令、路径或任意下载 URL；执行与平台/内存适配仍在客户端。`localModels` 整域缺失才回退随包本地域；显式空 `models`/`featuredIds` 分别撤下目录/推荐。

### 7.7 别名与覆盖顺序

覆盖优先级从低到高：

```text
同厂商/同系列前代能力（仅补缺）
→ baseModels[].defaults
→ 预设默认（适用时）
→ models[] 顶层
→ routes[].defaults
→ models[].perAgent[agent]
→ 供应商接口明确实报
→ routes[].forceOverrides
→ 用户显式配置（model-catalog-overrides.json / 表单）
```

规则：缺字段继承；`false` 明确关闭；数组整体替换；`null` 按字段合同保留。`defaultEffort` 例外：已配置的模型默认（含 route/perAgent）优先于 Harness 实报推荐档，force/用户覆盖仍最高。用户文件先应用公共型号补丁，再应用具体连接/引擎补丁。`modelRef` 或唯一精确 alias 才能关联公共资料，不能把模糊名称当身份。

### 7.8 地区差异

- CN / Global 是两套安装包与端点清单；`sessionRealm` 控制 auth、device-link、oauth-broker、OSS、heartbeat、model-access、voice、GitHub、SkillHub、plugin、hook 等所有带 Bearer 的服务。
- 公共 Registry 本身不因区域改写；部署可以把 `newSessionDefault` 按区域投影到 ListModels，但不得在保留同一 `updatedAt` 时改写 Registry。
- `/models` 的 `currency`、`accountTier`、`availability` 按账号/部署下发；客户端不得按 UI 语言或系统地区推断币种。
- 供应商分享跨区域默认关闭，由 `PROVIDER_SHARE_CROSS_REGION_ENABLED` 控制。

## 8. BYOK

### 8.1 端点与对象

- `GET /api/model-access/byok/providers?schemaVersion=1`：目录 `{schemaVersion:1,organizationId,revision,providers[]}`。`revision` 是字符串；provider `id` 形如 `byok-...`，含 `name`、`connectionRevision`（正整数）、`models[]`、可选 `imageBinding`。
- `GET /api/model-access/byok/credentials?schemaVersion=1`：`{schemaVersion:1,organizationId,credentials[]}`。每项只能是 `ready{endpoint,apiKey}`、`pending{}`、`error{code:"UNAVAILABLE"|"REVOKED"}`，且 `connectionRevision` 必须与目录一致。
- `imageBinding`：`{enabled:true,modelId,name?,supportsEdit,wireModel,litellmModel}`。
- BYOK 仅对已登录外部组织账号启用：`membershipKind='org'`、有非空 `orgId`、不是 xd 组织。个人账号、未登录、xd 组织均不应调用。

### 8.2 服务端签发/轮换/撤销

- 服务端是 key 的唯一签发、轮换、撤销方；客户端只拉取，不生成 key，不把 key 回传。
- `pending` 是明确中间态：旧 key 已撤销、新 key 尚未签发。服务端签发宽限期约 `MODEL_GATEWAY_TIMEOUT_MS × 2`（默认 20s）；客户端每 30s 重试，最多 3 次。
- 目录 revision 变化时，客户端先撤旧路由，再取新 key；服务端必须保证 `connectionRevision` 单调递增，revocation 不可回退。
- 客户端注释提到服务端 `byok-member-access` 限流默认 60 秒 10 次（含手动刷新）。Go 端应把限流键设为 `account+org+provider+member`，并对 `pending` 自愈请求留出额度。
- 目录与凭据分两次请求；目录必须先发布且稳定，凭据才能按 `connectionRevision` 对齐。服务端不能在凭据响应里下发目录中不存在的 provider。

### 8.3 校验约束（客户端严格 parser，服务端必须满足）

| 对象 | 约束 |
|---|---|
| provider id | `^byok-[a-z0-9][a-z0-9-]{0,185}$`；≤100 个 provider；重复 id 拒绝 |
| provider name | 1–128，trim 后非空，无控制字符 |
| connectionRevision | 安全正整数 |
| model id | 每 provider ≤2000；普通 ≤256；以 `providerId/` 开头时 ≤448；不得含 `*`；重复拒绝 |
| model mode | 仅 `undefined`、`chat`、`image_generation` |
| chat model | `agents` 非空数组；必须显式 `perAgent`；`claude-code → anthropic-messages`、`codex → openai-responses`、`pi → anthropic-messages / openai-responses / openai-completions` |
| image model | `agents: []`；不得有 `perAgent`；`nativeApi` 只能是 `openai-images` |
| endpoint | `https` 或 loopback `http`（localhost/127.0.0.1/[::1]）；不得带 userinfo/query/hash |
| apiKey | 1–8192，trim 后非空，无控制字符 |
| credentials | ≤100 项；每 provider 至多一项；`ready` 必须带 endpoint+apiKey |

### 8.4 加密存储与客户端边界

- BYOK 凭据只存在于 Main 内存与 `providerSecretStore`，**不经 IPC 下发**；渲染进程只能看到 provider 状态。
- 冷启动加密快照键为 `byok_cache_<sha256(scope+orgId)>`，整个 JSON（catalog+credentials）用 Electron `safeStorage` 加密写入 `userData/safe-storage/<key>.enc`；24 小时过期，scope/orgId 不匹配即丢弃。
- 服务端对应要求：数据库中的上游 key 必须用 KMS/信封加密，密文与 provider/member/revision 绑定；日志、审计、错误 message 不得出现 key；管理面查询只返回掩码。
- 客户端投影：`source:'organization'`、`auth.method:'managed'`、`access.kind:'managed'`；Codex 上游补 `/v1`，Claude 用 base，Pi 按 `wireProtocol` 生成 per-model route；图片模型单独进 `imageModels`。

### 8.5 供应商分享（provider sharing）的服务端职责

服务端（device-link-server）是**成员关系权威**，分享者电脑是**使用范围与用量权威**。

- REST 基址 `/api/device-link/provider-shares`，全部要求 Access Token + 设备绑定 `actor={accountId,deviceId}`；拒绝多余字段；响应 `Cache-Control: no-store`；错误 `{error:{code,message}}`；写操作按账号限流。
- 分享者接口：`GET /`、`POST /links`、`POST /requests/{id}/approve|reject`、`POST /members/{id}/pause|resume|remove`。
- 受邀者接口：`POST /preview`、`POST /requests`、`GET /requests/{id}`、`POST /requests/{id}/withdraw`、`GET /received`、`POST /received/{memberId}/leave`。
- 跨区域 `/x/*`：名片或 requestToken/guest credential 认证；`/x/preview`、`/x/requests`、`/x/requests/{id}`、`/x/requests/{id}/withdraw`、`/x/session`、`/x/received/{memberId}/leave`。开关关闭时一律 `403 PROVIDER_SHARE_CROSS_REGION_DISABLED`。
- 身份名片由 auth-server 签发：`POST /api/me/identity-card`，请求 `{audience:"cindy-provider-share",purpose:"share-link"|"share-request"|"share-session",nonce}`，响应 `{card,expiresAt}`；RS256 JWT，claims 含 `typ/iss/aud/sub/name/picture/region/purpose/nonce/iat/exp`，约 5 分钟有效；不放邮箱/手机号/组织。
- relay：`wss://<device-link>/api/device-link/ws`；同区域 Bearer，跨区域 `ProviderShareGuest <credential>`；能力 `provider-share-v1`；帧带 `providerShare:{shareId,target,source}`；同一帧不得同时带 `sharedTask` 与 `providerShare`。
- 限制：链接 5 分钟、申请 24 小时、配对码 4 位、每成员最多 64 台设备、分享人数不限；单设备运行额度默认 16、整机合计默认 64，可配 1–1000，单设备不得高于合计。
- 服务端不保存邮箱/手机号/组织/区域，只保存昵称/头像快照；不建用量表，用量在分享者电脑本地 `provider-share-usage.json`。错误码：`NOT_FOUND`、`PROVIDER_SHARE_LINK_USED`、`PROVIDER_SHARE_LINK_EXPIRED`、`PROVIDER_SHARE_SELF`、`PROVIDER_SHARE_ALREADY_MEMBER`、`PROVIDER_SHARE_IDENTITY_INVALID`、`PROVIDER_SHARE_NOT_HOST_DEVICE`、`PROVIDER_SHARE_CROSS_REGION_DISABLED`。

### 8.6 供应商组（provider groups）

结论：**没有独立的服务端实体/表/端点**。组是组所在电脑上、某个供应商连接的一项本地设置；组所在电脑负责选电脑、维护在线/暂停/模型/运行数/冷却，并通过既有 device-link relay 转发。同账号其他电脑通过 `provider-group:remote` 询问“该用哪台”，然后直接连被选中的电脑；转分享同意记录在本机 `provider-share-reshare.json`；用量按账号本地记录。服务端若未来新增组辅助接口，本仓无证据，不应在阶段 1 假设。

## 9. 套餐 / 订阅 / 额度 / 信用 / 用量

### 9.1 billing 端点全表

基址与 model-access 相同：`modelAccessApiBaseUrl`。全部 20s、`redactErrorDetails`、Bearer；客户端还要求个人账号与主窗口 sender。计费服务端应拒绝组织账号（或明确返回 `PERMISSION_DENIED`）。

| 方法 | 路径 | 请求 | 响应 | 业务错误 |
|---|---|---|---|---|
| GET | `/api/billing/catalog` | — | `{products:[{code,name,kind:"CREDIT_TOPUP"\|"SUBSCRIPTION",level,sortOrder,offers[]}]}` | 400/401/403/404/409/412 |
| GET | `/api/billing/orders?limit=N` | `limit` 1–100 | `{orders[],nextCursor}` | 同上 |
| GET | `/api/billing/orders/{orderId}` | — | `BillingPaymentOrder` | 同上 |
| POST | `/api/billing/credit-topup/orders` | `{offerCode,amount?,purchaseOptionId}` + `Idempotency-Key` | `BillingPaymentOrder` | 同上 |
| POST | `/api/billing/orders/{orderId}/refresh` | — | `BillingPaymentOrder` | 同上 |
| POST | `/api/billing/orders/{orderId}/cancel` | — | `BillingPaymentOrder` | 同上 |
| POST | `/api/billing/orders/{orderId}/retry` | — + `Idempotency-Key` | `BillingPaymentOrder` | 同上 |
| POST | `/api/billing/subscriptions` | `{offerCode,purchaseOptionId}` + `Idempotency-Key` | `BillingSubscription` | 同上 |
| GET | `/api/billing/subscription` | — | `{subscription:BillingSubscription\|null}` | 同上 |
| DELETE | `/api/billing/subscription` | — | `BillingSubscription` | 同上 |
| POST | `/api/billing/subscription/resume` | — | `BillingSubscription` | `RESUME_NOT_AVAILABLE` |
| POST | `/api/billing/subscriptions/purchases/{purchaseAttemptId}/refresh` | — | `BillingSubscription` | 同上 |
| POST | `/api/billing/subscription/plan-change-quotes` | `{targetOfferCode}` + `Idempotency-Key` | `BillingPlanChange` | `PLAN_CHANGE_NOT_AVAILABLE`、`PLAN_CHANGE_RENEWAL_PREPAID` |
| POST | `/api/billing/subscription/plan-changes/{id}/confirm` | — | `BillingPlanChange` | `PLAN_CHANGE_RENEWAL_PREPAID` |
| POST | `/api/billing/subscription/plan-changes/{id}/refresh` | — | `BillingPlanChange` | 同上 |
| DELETE | `/api/billing/subscription/plan-changes/{id}` | — | `BillingPlanChange` | 同上 |
| POST | `/api/billing/subscription/portal` | — | `{url}`（客户端只允许白名单 URL） | 同上 |

### 9.2 数据模型

- **Catalog**：`BillingCatalogProduct.kind=CREDIT_TOPUP|SUBSCRIPTION`，`level:number|null`，`sortOrder:number`，`offers[]`。Offer 含 `code`、可选 `name`、`salesState:"COMING_SOON"|"AVAILABLE"`、`purchasable?`、`unavailableReason:"OFFER_COMING_SOON"|"NO_AVAILABLE_PAYMENT_CHANNEL"|null`、`interval:"MONTH"|"YEAR"|null`、`currency`、`amount|minAmount|maxAmount|creditAmount|rolloverCap`（十进制字符串或 null）、`purchaseOptions[]`。
- **PurchaseOption**：`{id,provider,capability:"ONE_TIME_PAYMENT"|"MERCHANT_INITIATED_MANDATE"|"PROVIDER_MANAGED_SUBSCRIPTION",paymentAction:"QR_CODE"|"REDIRECT"}`。
- **支付动作**：`{type:"QR_CODE",value,expiresAt}` 或 `{type:"REDIRECT",url,expiresAt}`；URL 由服务端下发，客户端主进程只允许白名单域。
- **Order**：`orderId`、`productCode`、`offerCode`、`amount`、`currency`、`status:CREATED|PENDING|SUCCEEDED|FAILED|CANCELED|EXPIRED`、`paymentAction|null`、可选 `fulfillmentStatus:NOT_STARTED|PENDING|SUCCEEDED|FAILED`、`createdAt`、`updatedAt`。
- **Subscription**：`subscriptionId`、`status:INCOMPLETE|INCOMPLETE_EXPIRED|TRIALING|ACTIVE|PAST_DUE|UNPAID|CANCELED|PAUSED`、`provider?`、`entitlementStatus?`、`currentPeriodStartAt/EndAt`、`entitlementValidUntil`、`cancelAtPeriodEnd`、`resumable?`、`effectivePlan|null`、`purchaseAttemptId|null`、`paymentAction|null`、`pendingPlanChange?`。
  - `effectivePlan.version:1`，含 `product{code,kind:"SUBSCRIPTION",level}`、`offer{code,interval}`、`terms{amount,currency,creditAmount,rolloverCap}`、`capturedAt`。
  - 已有生效订阅的阻断状态：`TRIALING|ACTIVE|PAST_DUE|UNPAID|PAUSED`；`INCOMPLETE*` 只属于当前 checkout。
- **PlanChange**：`planChangeId`、`changeType:UPGRADE|DOWNGRADE`、`status:QUOTED|PENDING_PROVIDER|AWAITING_PAYMENT|SCHEDULED|APPLIED|CANCELED|FAILED|EXPIRED`、`quotedAmountMinor:number|null`、`quotedCurrency:string|null`、`quoteExpiresAt|null`、`effectiveAt`、`paymentAction|null`。`pendingPlanChange` 是服务端恢复投影：`null` 表示无开放变更，字段缺失表示旧服务端，客户端不得当作拒绝。
- **金额**：客户端接受 `^(0|[1-9]\d{0,14})(?:\.\d{1,9})?$`；`quotedAmountMinor` 是整数最小货币单位。服务端内部应使用整数最小单位或 `scale=9` 的十进制，禁止 float 累计。

### 9.3 幂等与状态机

- `Idempotency-Key` 正则 `^[A-Za-z0-9._:-]{8,128}$`。创建充值、重试支付、创建订阅、套餐变更报价必须携带；服务端应按 `accountId + operation + key` 建唯一索引，保存请求指纹与首次结果。
- 同 key 同请求重放返回首次资源（相同 HTTP 状态与 body）；同 key 不同请求返回 409/412，不得静默覆盖。
- `refresh`/`cancel`/`confirm` 通过资源状态天然幂等，但仍需状态机守卫：终态不可回退；`confirm` 只接受开放态（如 `QUOTED`、`AWAITING_PAYMENT`）；`cancel` 只接受开放态；`resume` 只接受 `cancelAtPeriodEnd=true` 且账期未过的订阅，否则 `RESUME_NOT_AVAILABLE`。
- 支付回调与客户端 `refresh` 可能并发。服务端应以支付事件为事实源，订单/订阅投影最终一致；客户端只读，不推送逐笔用量。

### 9.4 额度、信用与用量口径

| 额度 | 来源 | 关键字段/语义 |
|---|---|---|
| Cindy AI Credit Ledger | `/api/model-access/balance`、`/credit-usage` | `plan/purchased/promotional` 三类池、`available`、`scale:9`、逐笔赠送、`ledgerUpdatedAt` |
| 云端组织周期配额 | `/api/model-access/quota` | `spend>=0`、`maxBudget>0 或 null`、`currency`、`budgetResetAt`；`maxBudget=null` 表示不限 |
| ChatGPT/Codex 订阅 | 本机账号快照/app-server 桶 | primary/secondary 窗口、模型专属桶 |
| Claude 订阅 | 本机 Claude 账号窗口 | 5 小时、总周、模型专属周 |
| SuperGrok | 本机 xAI 周窗口 | `creditUsagePercent`、`resetsAt` |
| 供应商组 | 组所在电脑 + 各电脑订阅快照 | 先排除耗尽，再“快到期的先用”，再“已用百分比低” |

Credit 不变量（客户端会逐项校验，违反即整份响应判非法）：

- `available = plan.remaining + purchased.remaining + promotional.remaining`；`plan`/`promotional` 的 remaining 非负，`purchased` 允许为负（客户端投影如此，业务原因未确认）。
- 每个池的 `used` 与 `total` 要么同时为 null，要么同时存在且 `used + remaining = total`。
- `promotionalGrants` ≤1000；`grantId` 唯一；`originalAmount>0`；`active` 状态必须 `remaining>0` 且 `used+remaining=original`；`depleted` 必须 `remaining=0` 且 `used=original`；`expired|voided` 必须 `remaining=0` 且 `used<=original`。
- `promotionalGrantsComplete=true` 时，`promotional.total/used/remaining` 必须等于未过期 `active|depleted` 赠送的合计；`false` 时不得给出 `used/total`（历史超过安全分页上限，只返回最近记录）。
- `promotionalGrantConsistency` 固定 `"OBSERVED"`；`observedAt` 必填；`ledgerUpdatedAt` 可 null。
- 真实扣费在 Gateway / Credit Ledger，客户端只读余额与用量，不上传逐轮用量。客户端本地报价按 `/models` 的 per-token 价格 + `costDiscount` 估算，磁盘缓存 `model-pricing.json`（v9），按账号/区域/端点/Key 指纹分 scope；订阅类金额是估算，不是账单。

## 10. 语音服务

### 10.1 会话与 ticket

`POST /api/voice/sessions`（Bearer；10s；401 刷新一次重试）：

- 请求：`{mode:"dictation",language?,client:"desktop"|"mobile",clientVersion,asrProvider,refinerProvider?}`。
- 响应：`{sessionId,ticket,expiresAt,asr:{provider,websocketUrl,protocolProfile,sampleRate,model?,resourceId?},refiner:{enabled,provider?}}`。
- 客户端校验：响应 `asr.provider` 必须等于请求值；`ticket`/`sessionId` 非空；`websocketUrl` 必须 `ws://` 或 `wss://`。
- 旧服务端不认识 `refinerProvider:"auto"` 时返回 400；Desktop 会去掉 refiner 再试一次，Mobile 只对 `400 INVALID_PARAMS` 降级。Mobile 收到结构化 `429 RATE_LIMITED` 时提示“语音请求过于频繁”，不切换其他 ASR 候选；Desktop 按普通请求失败处理。
- 服务端要求：ticket 一次性、短时、绑定 session/account/asrProvider；ASR WS 握手成功后立即失效或标记 consumed；过期由 `expiresAt` 表达，客户端不硬编码 TTL。`websocketUrl` 由服务端决定，客户端不拼接路径。

### 10.2 ASR WebSocket 协议

| protocolProfile | 上游协议 | 会话初始化 | 结束方式 | 采样率 |
|---|---|---|---|---|
| `openai-transcription-manual` | OpenAI Realtime 兼容 WS | `session.update` 用嵌套 `session.audio.input.format={type:"audio/pcm",rate}`、`transcription.model/language`、`turn_detection:null` | 客户端 `input_audio_buffer.commit` 并等完成 | 24 kHz（LiteLLM/自定义） |
| `qwen-asr-server-vad` | DashScope Realtime 兼容 WS | 扁平 `session.update`：`modalities:["text"]`、`input_audio_format:"pcm"`、`sample_rate`、`turn_detection:{type:"server_vad",threshold:0,silence_duration_ms:400}` | `session.finish`，服务端 VAD 自行 commit | 16 kHz |
| `volcengine-sauc-duration` | 火山 SAUC 原生 WS | gzip 二进制：`0x1` full client request、`0x2` audio only；header 含 protocol/version/header size/message type/flags/serialization/compression；payload JSON 含 `audio{format:"pcm",codec:"raw",rate,bits:16,channel:1}`、`request{model_name,result_type:"full",show_utterances,enable_nonstream,end_window_size,enable_punc,enable_itn,language}` | 发送负序号静音尾包，等 definite/最终结果 | 16 kHz |

- 握手：OpenAI/Qwen 用 `Authorization: Bearer <ticket>`；Volcengine 另带 `X-Api-Resource-Id`、`X-Api-Connect-Id`。服务端代理时应重写为上游凭据，绝不把上游 key 回给客户端。
- 音频：PCM16、单声道；客户端把麦克风 48k/16k 重采样到 profile 采样率；首包可 10ms，之后 40ms；未确认音频保留 60s 重放缓冲；25s ping、8s pong 超时。
- 服务端应实现背压与有界缓冲：每个 session 一个上游连接，客户端断开即释放；上游错误映射为可区分的 close reason，不能把一次上游失败变成账号级 401。
- ASR 候选链（客户端默认）：`litellm-volcengine-sauc-asr → litellm-qwen3-asr-flash-realtime → litellm-gpt-realtime-whisper`。Mobile managed 链相同但整链固定 16 kHz；Mobile 在 socket open 后即发送，Desktop 等首个协议响应。服务端只收到 `asrProvider`，内部模型选择/failover 不暴露。

### 10.3 托管润色

- `POST /api/voice/sessions/{sessionId}/refine?provider=auto`：Bearer（不是 ticket）。`auto` 表示由 voice-server 自选模型链与 failover。
- 请求为 OpenAI Chat Completions 形态：`{model,response_format:{type:"json_object"},prompt_cache_key,stream:true,stream_options:{include_usage:true},messages:[system,user]}`。
- 响应为 SSE `chat.completion.chunk`；客户端解析 `choices[].delta.content`、`model`、`usage`。10s 建连看门狗 + 流式空闲超时；401 刷新/重试，429 与流错误按失败处理，空输出视为失败。
- `POST /api/voice/sessions/{sessionId}/refine-warmup`：`{prompt_cache_key,messages:[system,user]}`，只要求 2xx；共享真实润色的 prompt 前缀（`dictationText:""`）预热上游缓存；失败被吞掉，不消耗润色额度。服务端必须保证 warmup 不计费、不创建可提交结果。

### 10.4 词典与端侧前置

- 词典没有独立 voice-server HTTP 端点。`VoiceInputSettings.dictionaryEntries` 是物化视图，真正同步状态在 `voice-dictionary-sync.v1.json`（CRDT/HLC sidecar）。
- 学习动作：`add_candidate / add_entry / update_entry`，含 `term/aliases/type/confidence/reason`；低置信度不计数；候选累计 2 次晋升正式词条。Advisor 由文本模型执行，输入含 `beforeText/afterText/rawTranscriptText/context/existingEntries/existingCandidates`，输出最多 3 个动作。
- 跨设备走 device-link：`device-link:voice:dictionary:get`、`...:dictionary-learning`、`...:dictionary:sync-state`、`...:dictionary:snapshot`；同账号设备合并，手机拉快照并回写学习 evidence。
- 端侧 `SpeakerGate` 在麦克风与 ASR 之间做 VAD + speaker embedding；被拒绝音频用静音替代以保持 ASR 时钟。Silero VAD 与 3D-Speaker CAM++ 模型按内容哈希从 CDN 下载到 `voice-models/<sha256>.onnx`，服务端只负责静态分发。
- 限额：客户端只识别 `429 RATE_LIMITED`；语音会话并发、ticket 有效期、润色次数与 ASR 分钟配额由服务端控制，本仓不可见。

## 11. Embedding 网关

- 端点：`POST {XD Gateway base}/v1/embeddings`，`Authorization: Bearer <XD Gateway key>`；key 与 base 来自 `/api/model-access/credentials`。OpenAI 兼容，无 version 参数。
- 请求：`{model,input:string[]|string[][],input_type?,dimensions?}`。二维 `input` 用于 `voyage-context-*` 的上下文化索引（内层 = 一个文档的 chunk 序列）；查询侧传一维。
- `input_type` 按 provider 翻译：Voyage 小写 `query|document`；Google 大写 `RETRIEVAL_QUERY|RETRIEVAL_DOCUMENT`；OpenAI 不发（无此参数，发了也会被忽略）。
- `dimensions` 统一发 OpenAI 字段名；三家经网关都认。显式维度必须在响应中兑现，否则 `INVALID_MODEL`。
- 响应：扁平 `data:[{object:"embedding",embedding,index}]`；`voyage-context-*` 返回两层 `data:[{object:"list",index,data:[{embedding,index}]}]`。客户端按 `index` 做双射校验：项数相等、无重复、无越界；批内向量必须全为有限数字、非空、等长。
- 目录（客户端硬编码，XD Provider 白名单逐字一致）：

| model id | provider | 默认维度 | 单条 token 上限 | 参考价 USD/MTok |
|---|---|---:|---:|---:|
| `text-embedding-3-small` | openai | 1536 | 8192 | 0.02 |
| `text-embedding-3-large` | openai | 3072 | 8192 | 0.13 |
| `gemini-embedding-2-preview` | google | 3072 | 8192 | 0（preview） |
| `voyage/voyage-4` | voyage | 1024 | 32000 | 0.06 |
| `voyage/voyage-4-large` | voyage | 1024 | 32000 | 0.12 |
| `voyage/voyage-code-3` | voyage | 1024 | 32000 | 0.18 |
| `voyage/voyage-context-3` | voyage | 1024 | 120000（目录值；注释称实为整批上限） | 0.18 |
| `voyage/voyage-context-4` | voyage | 1024 | 32000 | 0.12 |

- 批量上限：客户端不自动切块，按 model 的 `maxTokens` 由调用方预分块；`voyage-context-*` 经网关另有整批约 120K token / 至多 1000 个文档的限制（目录注释，C 级）。`embedDocuments()` 拒绝空文档序列，且**不走缓存**；缓存只用于 `embed()`。
- 重试：5xx / 429 / network → 1s、5s、15s，最多 4 次尝试；401/403/400/超时不重试。`timeoutMs` 覆盖含重试的整条链，预算耗尽抛 `TIMEOUT`。
- 缓存：进程内 LRU 1000，key=`sha256(model + \0 + variant + \0 + text)`，`variant=inputType:dimensions`；缓存项同时保存上游实际 `model`，别名换后端时避免混用不同向量空间。批内缓存与新结果不同源时清空缓存整批重取一次。
- 错误码：`AUTH_FAILED`、`RATE_LIMITED`、`INVALID_MODEL`、`NETWORK_ERROR`、`SERVER_ERROR`、`TIMEOUT`。状态映射：401/403→`AUTH_FAILED`；400/404/422→`INVALID_MODEL`；429→`RATE_LIMITED`；5xx→`SERVER_ERROR`；网络异常→`NETWORK_ERROR`；预算耗尽→`TIMEOUT`。Go 网关若只做反向代理，也应保留 OpenAI 错误体与状态码的稳定映射。

## 12. OAuth broker

### 12.1 服务定位与端点

oauth-broker **不是** Cindy 账号登录服务；账号登录在 auth-server。它承担两类职责：

| 方法 | 路径 | 请求 | 响应 | 鉴权 |
|---|---|---|---|---|
| POST | `/api/integrations/{slug}/oauth/exchange` | `{code,redirectUri,codeVerifier?}` | `{accessToken,refreshToken?,expiresIn?,scope?}` | Cindy Access Token |
| POST | `/api/integrations/{slug}/oauth/refresh` | `{refreshToken}` | 同上 | Cindy Access Token |
| GET | 由 `brokerBounce.path` 拼出的公网回弹路由，如 `/jira/bounce`、`/slack-mcp/bounce` | 浏览器 `?code&state` | 302 到本机 `redirectPort+callbackPath` | 浏览器回调，无 Bearer |
| GET | `/api/user/feature-flags` | — | `{isCanary?:boolean,defaultEnableBeta?:boolean}` | Access Token |

支持 slug：`feishu`（PKCE，`codeVerifier` 透传上游）、`jira`（`pkce:false`，client secret 留在服务端）；`slack` 已于 2026-07-19 随 cindy-slack 退役。本仓未发现 broker 自己的 device-code 端点；device-code 由客户端直连各 IdP。broker 端点响应字段是 camelCase；`expiresIn` 是秒，客户端会减去安全余量后计算 `expiresAt`。

### 12.2 标准授权与双地址弹跳

1. 本机 `127.0.0.1:<redirectPort 或随机端口><callbackPath>` 起 loopback。
2. 生成 32 字节随机 `state`；PKCE 缺省开，verifier/challenge S256。
3. 打开 `authorizeUrl`，带 `response_type=code`、`client_id`、`redirect_uri`、`scope`、`state`、`code_challenge`、`code_challenge_method=S256`。
4. 回调先校验 `state`，再取 `code`。
5. 直连模式 POST `tokenUrl`（form-encoded；OpenRouter 例外用 JSON）；broker 模式 POST `/api/integrations/{slug}/oauth/exchange`。
6. token 响应支持 `access_token/refresh_token/expires_in/scope`；refresh token 轮换时用新值，未返回则沿用旧值。

双地址弹跳：服务商侧 `redirect_uri = oauthBrokerBase + brokerBounce.path`；浏览器落 broker，broker 302 回本机 `redirectPort + callbackPath`。服务端必须把 `path`/`callbackPath` 与 `redirectPort` 视为一套注册约定；`path`/`callbackPath` 都是 `/` 开头的站内绝对路径，段字符限字母/数字/`_`/`-`，≤128。

清单约束（插件协议，服务端应同步校验）：`tokenBroker` 与 `clientSecret` 互斥；声明 `tokenBroker` 时必须声明 `redirectPort`；`clientIdAlternatives` 仅允许与 `tokenBroker` 一起出现；`brokerBounce` 必须与 `tokenBroker` + `redirectPort` 同时声明；slug 形状 `^[a-z][a-z0-9_-]{0,31}$`。

### 12.3 错误映射

| 场景 | 客户端结果 | 服务端要求 |
|---|---|---|
| 未登录/本地无 token | `EXCHANGE_FAILED`，提示先登录 | broker 不应被调用；不得误判 `invalid_grant` |
| 网络失败（status 0 / `NETWORK_ERROR`） | `NETWORK` | 可重试，不销毁 refresh token |
| 404 / 5xx | `SERVICE_UNAVAILABLE` | 不暴露上游正文；可重试 |
| 401 且 code ∈ `{FEISHU_OAUTH_FAILED,JIRA_OAUTH_FAILED}` | `EXCHANGE_FAILED` + `invalidGrant:true` | 仅上游明确拒绝时返回这些码 |
| 其他 401（`TOKEN_EXPIRED`/`UNAUTHORIZED`/未知） | `invalidGrant:false` | fail-safe，避免 JWT 轮换导致全员第三方授权被销毁 |
| 响应缺 `accessToken` | `EXCHANGE_FAILED` | 成功响应必须含非空 `accessToken` |

### 12.4 Feature flags

`GET /api/user/feature-flags` 在登录后异步读取；失败/非法保留本地旧值；旧账号迟到响应按 auth epoch/userId 丢弃。服务端应返回稳定布尔字段，未知字段客户端忽略；标志变更不需要版本参数，但应带 `Cache-Control: no-store`。

## 13. 客户端证据索引

| 结论 | 证据（文件:行号） |
|---|---|
| 端点清单与区域 | `C/config/endpoint.json:3-22`、`endpoint.global.json:3-22`、`C/docs/auth-realm-routing.md:9-18` |
| Bearer/401 刷新/错误壳 | `C/apps/desktop/src/main/serverApiClient.ts:4-7,131-205` |
| `/models` 请求与能力头 | `C/apps/desktop/src/main/model-access/modelsSyncRefresh.ts:14-16,77-91` |
| `/models` 版本闸/空列表 | `modelsSyncRefresh.ts:35-51`、`index.ts:227-301` |
| 凭据下发/轮换/退避 | `credentialsSync.ts:43-64,182-267,299-318` |
| 凭据失效探测 | `C/apps/desktop/src/main/model-access/gatewayKeyRejection.ts:14` |
| 媒体目录与 Guide | `mediaModels.ts:198-440`、`C/apps/desktop/src/shared/mediaInvocation.ts:7-93` |
| 配额解析 | `C/apps/desktop/src/main/usage/organizationAccountUsage.ts:5-27` |
| 余额/用量投影 | `C/apps/desktop/src/main/billing/projection.ts:169-351` |
| 计费端点/超时/错误 | `C/apps/desktop/src/main/billing/index.ts:25-95,216-465` |
| 计费 wire 类型 | `C/apps/desktop/src/shared/billing.ts:96-247` |
| Catalog 源与回退 | `C/packages/model-providers/src/source.ts:25-31,598-800` |
| Catalog 顶层校验 | `C/packages/model-providers/src/catalog.ts:816-823` |
| Provider 字段 | `C/packages/model-providers/src/types.ts:537-640` |
| Registry v5 字段 | `C/packages/model-providers/src/modelAccessBean.ts:100-205` |
| ListModels v5 校验 | `modelAccessValidator.ts:800-875` |
| 参考价/覆盖顺序 | `C/docs/dev-rules/model-catalog-maintenance.md:315-333`、`C/docs/product-rules/model-metadata-precedence.md:6-33` |
| BYOK 端点/严格 parser | `C/packages/model-providers/src/byok.ts:4-72,85-385` |
| BYOK pending/限流 | `C/apps/desktop/src/main/model-access/byokSync.ts:39-53,121-220` |
| BYOK 路由投影 | `C/apps/desktop/src/main/model-access/byokProvider.ts:18-141` |
| BYOK 加密快照 | `byokCache.ts:17-84`、`secrets/providerSecretStore.ts:1071-1081` |
| 语音会话/降级 | `CindyVoiceSessionClient.ts:60-109,196-240` |
| 润色 URL/SSE | `CindyVoiceSessionClient.ts:112-136`、`LiteLlmTextModelClient.ts:112-194` |
| 预热 | `CindyVoiceSessionClient.ts:145-185` |
| ASR profile/候选链 | `C/apps/desktop/src/shared/voiceInputAsrProfiles.ts:60-190` |
| ASR 协议细节 | `RealtimeAsrWebSocketProvider.ts:39-71,536-607`、`VolcengineSaucAsrProvider.ts:43-63,145-170` |
| Embedding 重试/缓存 | `C/packages/embedding-client/src/client.ts:50-61,587-807` |
| Embedding 目录 | `C/packages/embedding-client/src/catalog.ts:22-101`、`types.ts:10-51` |
| OAuth broker 白名单/错误 | `ghostOauthBroker.ts:41-58,109-181` |
| broker 清单约束 | `C/packages/plugin-protocol/src/manifest.ts:590-625,2495-2560` |
| Feature flags | `authManager.ts:3155-3206`、`canaryFlagSync.ts:48-70` |
| 分享契约/错误码 | `C/docs/provider-sharing-contract.md:44-215` |
| 组无服务端实体 | `C/docs/product-rules/provider-groups.md:50-76,455-500` |

## 14. Go 实现映射建议

### 14.1 包与路由

| Go 包 | 负责端点 | 关键依赖 |
|---|---|---|
| `internal/httpapi` | 中间件、错误壳、locale、request-id、限流 | auth JWT 校验、realm、审计 |
| `internal/modelaccess` | `/models`、`/credentials*`、`/invocation-guide*`、`/quota`、`/balance`、`/credit-usage` | Gateway 客户端、媒体 Guide 存储、Ledger 客户端 |
| `internal/byok` | `/byok/providers`、`/byok/credentials` | 组织服务、KMS、网关签发 API |
| `internal/modelcatalog` | `/api/model-catalog/catalog`、旧 `/cfg/providers.json` | 只读快照、ETag、版本投影 |
| `internal/billing` | `/api/billing/*` | 支付渠道适配、订单/订阅状态机、幂等表 |
| `internal/voice` | `/api/voice/sessions*`、ASR WS、refine SSE | 上游 ASR、会话缓存、ticket |
| `internal/embedding` | `/v1/embeddings`（Gateway 侧） | 上游路由、错误映射、限流 |
| `internal/oauthbroker` | `/api/integrations/{slug}/oauth/*`、回弹 | 集成注册表、OAuth 客户端、feature flags |
| `internal/featureflags` | `/api/user/feature-flags` | 账号灰度存储 |

### 14.2 数据表建议

| 表 | 主键/唯一键 | 要点 |
|---|---|---|
| `model_catalog_snapshot` | `snapshot_id`；唯一 `(schema_version, capabilities_hash, updated_at)` | 原始 canonical JSON、ETag、`updated_at`；只追加不可变 revision |
| `gateway_credential` | `(account_id, key_id)` | 密文、`endpoint`、`status`、`created_at/revoked_at`；每账号至多一个 active |
| `byok_provider` | `(org_id, provider_id)` | `connection_revision`、名称、配置 JSON、状态 |
| `byok_member_credential` | `(org_id, provider_id, member_id, connection_revision)` | 密文、`ready`/`pending`/`error`、签发/撤销时间 |
| `billing_product/offer` | `code` | 展示目录；金额用最小单位或字符串 |
| `billing_order` | `order_id`；`(account_id, idempotency_key)` 唯一 | 状态、支付动作、fulfillment |
| `billing_subscription` | `subscription_id`；账号 active 唯一 | 周期、权益、取消/恢复 |
| `billing_plan_change` | `plan_change_id` | 报价过期、确认、生效 |
| `billing_idempotency` | `(account_id, operation, key)` | 请求指纹、响应快照、过期 |
| `voice_session` | `session_id` | 账号、provider、ticket 摘要、`expires_at`、consumed |
| `oauth_integration` | `slug` | client_id、secret 密文、authorize/token/refresh 端点、scopes |
| `feature_flag_assignment` | `(account_id)` | canary/defaultEnableBeta |

### 14.3 关键实现顺序

1. **公共 Catalog 与 `/models` 投影**：先做只读快照 + 版本矩阵 + ETag，再做账号级 Gateway 投影；两者不能共用同一缓存键。
2. **凭据与 BYOK**：先做密钥加密、审计与轮换事务，再接 Gateway 签发；`pending` 必须可重复查询且幂等。
3. **计费**：先做幂等表与订单状态机，再接支付渠道；所有金额用整数最小单位，`quotedAmountMinor` 与十进制字符串在边界转换。
4. **语音**：先做会话/ticket 与 WS 代理骨架，再补三种 ASR profile；refine 作为独立 SSE 代理，不与 ticket 鉴权混用。
5. **OAuth**：先做 slug 注册表与错误码白名单，再做 exchange/refresh；回弹路由只做 302，不落任何 token。
6. **Feature flags 与区域**：最后接入灰度；所有服务从部署配置读取本区域端点，禁止从请求推断。

### 14.4 服务端不变量

- 任何账号态响应默认 `Cache-Control: no-store`；公共 Catalog 才允许强 ETag 与长缓存。
- 任何密钥/ token 不进入日志、错误 message、URL query；ticket 只存摘要。
- `connectionRevision`、Catalog `updatedAt`、Order/Subscription 状态只能前进，不能回退。
- 所有写操作按 `accountId + operation + idempotencyKey` 去重；所有跨账号读取必须先校验 org/member 归属。
- 媒体 Guide、BYOK endpoint、OAuth redirect URI 都必须做 allowlist 校验，不能直接透传用户输入。

## 15. 未确认项与推测

| # | 未确认项 | 影响 | 建议核实 |
|---|---|---|---|
| 1 | 服务端错误码全集 | 客户端只识别少量码，Go 端可能返回客户端不认识的 code | 以 `model-access-server` 源码/线上探测为准；至少保持统一错误壳 |
| 2 | Catalog ETag/304/Cache-Control 细节 | 客户端不发 `If-None-Match`，CDN 行为未知 | 部署后核对各版本 ETag、`Vary`、304 与 revision |
| 3 | voice WS 固定路径与 ticket TTL | 客户端只消费 `websocketUrl` | 以 voice-server 源码为准；仓内 `/api/voice/asr` 只是基准样例 |
| 4 | voice 内部模型链、failover、配额 | 客户端只传 provider/auto | 服务端源码与运营配置 |
| 5 | oauth-broker 隐藏路由/其他 provider | 本仓只证实 exchange/refresh/feature-flags/bounce | 服务端路由表；未发现 device-code |
| 6 | provider sharing 数据库 schema/Redis key/限流阈值 | 契约只描述行为 | device-link-server 源码与迁移 |
| 7 | provider groups 是否有隐藏服务端接口 | 本仓只能证明“无组实体” | 服务端路由表 |
| 8 | embedding gateway 实际路由、计费、限流、`dimensions` 行为 | 客户端目录是硬编码与实测注释 | Gateway 侧配置与探测 |
| 9 | `accountTier:"paid"` 对应套餐/权益边界 | 客户端只做展示与付费锁定 | 计费/权益系统 |
| 10 | 区域 `newSessionDefault` 是否实际投影 | 文档允许，线上未必 | 对比 CN/Global `/models` |
| 11 | BYOK 签发宽限期与 60s/10 次限流 | 来自客户端注释，非服务端证明 | 服务端源码/压测 |
| 12 | 支付渠道与 webhook 对账模型 | 客户端只轮询 refresh | billing 服务端与渠道文档 |
| 13 | Credit `scale:9` 的负 purchased 语义 | 客户端允许负值，业务原因未知 | Ledger 数据模型 |
| 14 | 媒体 Guide 异步轮询上限/超时 | 客户端只按 Guide 字段执行 | 媒体执行器与 Guide 生成器 |
| 15 | `/models` 的 `displayPricing` 单位与折扣应用顺序 | 客户端只展示，不参与计费 | Gateway 报价契约 |
| 16 | 旧 OSS `/cfg/providers.json` 的退役时间 | 迁移期回退仍有代码 | 发布计划 |

总体结论：从开源侧可以高置信度重建五条服务平面：model-access（账号目录 + 凭据 + BYOK + Guide + 配额/余额/计费）、匿名 model-catalog（Catalog + Registry v5 + 多版本/ETag/LKG）、voice（会话 ticket + 三协议 ASR + 托管润色）、oauth-broker（插件 token exchange/refresh + feature flags）、embedding gateway（OpenAI 兼容 `/v1/embeddings`）。供应商分享由 device-link-server 承担成员关系与 relay 授权；供应商组没有服务端实体。Go 实现应优先锁定 wire 形状、版本矩阵、幂等与密钥边界，再补运营配置与上游适配。
