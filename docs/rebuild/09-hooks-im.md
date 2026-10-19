# 09 · Hook Server / IM 渠道（Slack / Telegram / X）

> 分册：Cindy 服务端逆向重建 · 阶段 1 交付物
> 范围：中心 hook server（`slack-hook-server` / `telegram-hook-server` / `x-hook-server`）与 desktop 之间的 WSS 双工协议、IM 入站到 agent turn 的完整生命周期、渠道绑定/偏好/群中继/权限确认，以及可直接落 Go 的实现映射。
> 非范围：device-link relay、plugin-server、model-access-server、Chat Server（伙伴群聊）；这些分别在其它分册。
> 基线：`cindy-protocol` 公开快照（2026-08-10）与当前客户端本地协议副本（2026-10-19）。客户端是当前运行的“上位集”，服务端必须按上位集实现；只按公开快照实现会让新功能静默回落。
> 结论先行：hook server 是**服务端组件**；desktop 主动拨出，本机不监听端口；官方 Slack/Telegram/X 的 bot 连接、OAuth/OIDC 回调、命令分发、消息投递和限速全部由服务端持有；desktop 只负责 agent 执行、会话映射、渲染与终稿账本。

## 0. 元信息、证据等级、源文件索引
### 0.1 证据等级

| 等级 | 含义 | 使用方式 |
|---|---|---|
| E1 | 当前客户端运行代码直接消费或强制约束 | 服务端必须逐字实现；不实现会在真实链路上被拒收、丢弃或功能静默失效 |
| E2 | 协议类型/校验器/官方协议文档明确 | wire 形状与联动约束的权威来源；字段名、枚举、上限以此为准 |
| E3 | 产品规则/设计文档明确，但服务端实现细节未在客户端仓出现 | 作为行为目标；落地时需与服务端代码/线上行为二次核验 |
| E4 | 由客户端行为反推或跨仓缺口 | 标注“未确认”，不得当成已实现契约 |

### 0.2 关键纠偏

1. **消息数不是 24 种。** 公开快照 `HOOK_MESSAGE_TYPES` 有 41 种；当前客户端副本多 `provider.commands.set`，共 **42 种**。本分册按 42 种逐项登记。
2. **“v2”是版本号仍为 1 的增量批次。** 协议不做 `v` 升级，靠“`type` 开放集合 + 字段级宽容 + `hello.features`/`welcome.features` 能力协商”演进。新增帧族必须能力门控；老端未知 type 丢帧不断连。
3. **桌面端的 `hook-control/**` 只是协议一半。** 它不持有官方渠道凭证，也不接收平台 webhook；它主动拨出到中心 WSS，用登录 accessToken 鉴权。官方 Telegram 的 Bot API 连接由服务端 `telegram-hook-server` 持有；Slack App 的 bot token/user token、X 的 OAuth2 token 同理。
4. **当前客户端只对 X 消费 `turn.delivery`。** Slack/Telegram 仍是 fire-and-forget；服务端不能假定所有 provider 都有交付 ACK。
5. **群聊内容不得驻留在 server。** server 只存 `chatId ↔ principal` 成员元数据用于路由；滚动窗口、游标、上下文拼装全在 desktop 本地。

### 0.3 源文件索引

| 路径 | 用途 | 证据 |
|---|---|---|
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy-protocol/packages/slack-hook-protocol/src/types.ts` | 公开协议类型、消息全集（41 种）、常量、能力标识 | E2 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy-protocol/packages/slack-hook-protocol/src/parse.ts` | 收帧唯一入口与逐字段联动校验；上限、错误路径 | E2 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy-protocol/packages/slack-hook-protocol/src/build.ts` | 构造器；自动填 `v/id/ts`；`randomUUID()` | E2 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy-protocol/docs/slack-hook-protocol.md` | 协议四幕、可靠性语义、阶段 18/19、兼容策略 | E2 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/packages/slack-hook-protocol/src/types.ts` | 客户端上位集：第 42 种消息、`session-new`、`session-result`、`clientFinal`、msg.op purpose、`slack-communications` | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/transport.ts` | WSS 拨出、Bearer、401 刷新、心跳、退避、standby 4000 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/manager.ts` | 三条 lane、能力声明、bind/provider.bind、lifecycle、tool/prefs 回执 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/dispatcher.ts` | dispatch 幂等、定位/排队/停止/收尾、turn.reopen、turn.delivery、msg.op | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/session-runner.ts` | 新任务默认配置、agent 执行、权限模式、渠道说明 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/bindings.ts` | `externalKey → sessionId` 映射；无授权状态 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/requestLedger.ts` | 终稿持久账本、ACK 重放、24h 时效 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/groupWindow.ts` | 群消息本地窗口、反查 id、命名空间、保留策略 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/interactions.ts` | 交互卡、按钮→决策映射、30min 超时 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/ackReactions.ts` | 👀/处理中/失败表情，断线补发 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/attachments.ts` | 入站 base64 解码、图片白名单、文件分流 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/outbound.ts` | 出站附件收集、限额、xdt-image/xdt-file | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/telegramTurnCarrier.ts` | Telegram 进度/终稿客户端发布、NO_REPLY、失败回落 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/telegramMsgOp.ts` | msg.op 回执配对、错误映射、opId 命名 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/telegramCardOps.ts` | Telegram 交互卡客户端发布、旧路径回落 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/backgroundResults.ts` | `turn.end(background:true)` 后台结果回传 | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/apps/desktop/src/main/hook-control/telegramDeepLink.ts` / `xDeepLink.ts` | Telegram/X 绑定链接的严格 allowlist | E1 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/docs/dev-rules/im-turn-flow.md` | 一轮任务的入口账本、状态清单、不变量、停止语义 | E3 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/docs/product-rules/im-permission-confirmation.md` | 多端共享权限确认、来源消息、迟到点击 | E3 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/docs/product-rules/telegram-bot-parity.md` | 官方/个人 Telegram 能力台账、命令面、缺口 | E3 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/docs/product-rules/telegram-bridge-design.md` | 官方/个人消息桥、终稿栅栏、能力降级 | E3 |
| `/Users/changzechuan/InnovationProjects/CindyProjects/cindy/config/endpoint.json` / `endpoint.global.json` | 三端点与鉴权/中继地址 | E1 |

## 1. 能力总览
| ID | 能力 | 渠道 | 优先级 | 说明 |
|---|---|---|---|---|
| HOOK-01 | 三条 WSS lane 接入与 hello/welcome 握手 | Slack/Telegram/X | P0 | 平级部署，独立连接与能力集 |
| HOOK-02 | 设备鉴权与同设备互斥 | 全部 | P0 | Bearer accessToken；同账号同 deviceId first-wins，后到连接 4000 |
| HOOK-03 | 双向能力协商 | 全部 | P0 | `hello.features` / `welcome.features`；缺席即不启用 |
| HOOK-04 | `task.dispatch` / `task.ack` 三态 | 全部 | P0 | accepted / queued / rejected；requestId 幂等 |
| HOOK-05 | lane/externalKey 与设备绑定 | 全部 | P0 | externalKey 不透明；同 key 同 session |
| HOOK-06 | 任务定位/新建/接管/默认目录 | 全部 | P0 | workspace 别名；`sessionId` 仅接管 |
| HOOK-07 | 每会话排队与停止 | 全部 | P0 | 队列上限 20；`task.cancel` |
| HOOK-08 | 终稿发布与持久出箱 | 全部 | P0 | `turn.end`；失败重试；约 24h 放弃线 |
| HOOK-09 | 两阶段交付回执 | X（当前唯一消费方） | P1 | `turn.delivery` accepted/retrying/delivered/failed |
| HOOK-10 | 收口后续跑回流 | Slack/Telegram | P1 | `turn.reopen`；断连孤儿收口 |
| HOOK-11 | 进度渲染 | Slack/Telegram | P1 | `turn.progress` latest-wins；Telegram 可转客户端发布 |
| HOOK-12 | 执行中交互卡 | Slack/Telegram | P0 | ask / plan / permission；哑渲染器 |
| HOOK-13 | 多端共享权限确认 | Slack/Telegram | P0 | 桌面/手机/IM 同一决定；来源消息保留 |
| HOOK-14 | Slack OIDC 绑定 | Slack | P0 | Sign in with Slack；not-installed 引导 |
| HOOK-15 | Slack multi-team | Slack | P1 | `bind.state` 权威快照；teamId 消歧 |
| HOOK-16 | provider-neutral 绑定 | Telegram/X | P0 | `provider.bind.*`；深链/OAuth2 PKCE |
| HOOK-17 | Slack 目录偏好 | Slack | P1 | `prefs.*`；`/model` 卡镜像 |
| HOOK-18 | provider-neutral 偏好 | Telegram/X | P1 | `provider.prefs.*`；binding/scope 二选一 |
| HOOK-19 | Telegram 行为配置 | Telegram | P1 | 表情/引用/群激活；三态 patch |
| HOOK-20 | Slack 网关工具 | Slack | P2 | `tool.request/response`；绑定用户 user token |
| HOOK-21 | 实时清单查询 | 全部 | P1 | workspaces / models / sessions |
| HOOK-22 | `/new` 立即建任务 | Telegram | P1 | `session-new-v1`；新 externalKey 换代 |
| HOOK-23 | Slack 消息投递 | Slack | P0 | Block Kit / thread / 文件 |
| HOOK-24 | Telegram legacy 消息投递 | Telegram | P0 | 进度占位、终稿新发、topic/reply |
| HOOK-25 | Telegram 客户端驱动 msg.op | Telegram | P1 | 进度/终稿/卡片由 desktop 渲染，server 执行 |
| HOOK-26 | X 单条公开消息投递 | X | P0 | 无交互控件、无群中继；每次公开回帖计费 |
| HOOK-27 | 群消息中继 | Telegram | P1 | `group.message` fire-and-forget；server 不存内容 |
| HOOK-28 | 上下线通知偏好 | Slack | P2 | `lifecycleAnnouncement` + `lifecycle.preference` |
| HOOK-29 | 附件与媒体 | 全部 | P1 | 入站 base64；出站收集/限额/相册 |
| HOOK-30 | 表情与 typing | Telegram/Slack | P1 | 👀/处理中/失败；typing 保活 |
| HOOK-31 | 命令分发与菜单 | 全部 | P0 | 服务端解析命令；Telegram `setMyCommands` |
| HOOK-32 | 后台结果回传 | Slack/Telegram | P1 | `turn.end(background:true)`；X 不参与 |
| HOOK-33 | Slack 本机通讯授权 | Slack | P2 | `slack-communications` purpose，独立于 Bot 接收设备 |
| HOOK-34 | 审计、限速与可观测 | 全部 | P1 | requestId/opId 贯穿；渠道 429 退避 |

## 2. 中心 hook server 部署模型

### 2.1 三条平级 WSS

| lane | 端点（CN/Global 同值） | 服务端持有的凭证 | 入站 | 出站 |
|---|---|---|---|---|
| Slack | `wss://slack-hook.cindy.app` | Slack App bot token；每绑定用户的 user token；OIDC client secret | Slack Events API（webhook） | Slack Web API：chat.postMessage/update/delete、reactions、files |
| Telegram | `wss://telegram-hook.cindy.app` | 官方 bot token；绑定深链签名密钥 | Bot API update（长轮询或 webhook，未确认） | Bot API：sendMessage/editMessageText/deleteMessage/setMessageReaction/sendChatAction/sendMediaGroup/setMyCommands |
| X | `wss://x-hook.cindy.app` | 官方账号 OAuth2（PKCE）access/refresh token；API bearer | 提及/回复事件（filtered stream 或轮询，未确认） | X API：创建回复/帖子、媒体上传 |

- desktop 通过 `config/endpoint.json` 读取三条 URL；CN 与 Global 配置当前同值。每个 lane 由独立开关控制是否连接；一个 lane 故障不影响其它 lane。
- desktop **不监听端口**，也不接收平台 webhook。鉴权在 WS upgrade 阶段完成：`Authorization: Bearer <登录 accessToken>`；token 每次重连实时取，刷新天然兼容。
- **官方凭证只在服务端**：Slack bot token/user token、Telegram 官方 bot token、X OAuth token 均不下发 desktop。desktop 需要 Slack 用户能力时只发 `tool.request`，由服务端用绑定用户 token 代调。
- 三条 lane 的故障域、部署、扩缩容彼此独立；数据库可共享账号/绑定主数据，但连接注册表必须按 `provider` 分区。

### 2.2 一次 IM 请求的端到端路径

```text
平台事件 → 服务端 ingress → 事件去重 → lane 解析（externalKey）
        → 命令？──是──► 服务端执行命令（query/task.cancel/session.archive/session-new）
        │
        否
        ▼
   task.dispatch ──WSS──► desktop dispatcher
        │                    ├─ 定位/新建/接管 session
        │                    ├─ 排队或立即执行
        │                    └─ task.ack（accepted/queued/rejected）
        ▼
   turn.progress / interaction.* ◄──WSS── desktop 执行中
        ▼
   turn.end ──WSS──► 服务端出箱 → 渠道发布 → 终态消息/卡片清理
```

- 服务端在 ingress 层完成事件去重（Slack `event_id`、Telegram `update_id`、X mention id），同一事件重放不得产生第二个 dispatch。
- 服务端为每条 lane 生成不透明 `externalKey`（格式约定 `<providerId>:...`，客户端只做前缀分类，不解析内部段）。同一 `externalKey` 恒指向同一 desktop session，除非 desktop 判定绑定失效并重建。
- 服务端只派发 `hello.workspaces` 注册过的别名；绝对路径永不过网线。

### 2.3 连接与设备注册表

- 服务端维护 `(accountId, provider, deviceId) → connection` 映射。同账号同 deviceId 的**后到连接被拒绝**：close code `4000`，reason `device already connected`。客户端进入 standby，30s 低频探测首实例是否退出。
- 因为 first-wins 拒绝的是新连接，服务端必须能回收死连接（ping/pong 超时、TCP 半开、进程崩溃），否则设备会永久 standby。建议服务端心跳 30s、静默 90s 判死，并释放设备槽位。
- 同一账号可有多个 deviceId（多台电脑）；服务端按 lane 的绑定/principal 路由到具体设备。绑定是设备级，不是账号级。
- 连接断开时服务端必须做三件事：标记 lane 离线、把该连接上“已 reopen 未 end”的消息孤儿收口、停止向该连接发送任何新帧（后续 `turn.end` 进入出箱而非丢弃）。

## 3. 信封、握手、鉴权、重连

### 3.1 信封

```json
{ "v": 1, "type": "task.dispatch", "id": "<sender-frame-uuid>", "ts": 1760000000000, "payload": { } }
```

| 字段 | 约束 | 用途 |
|---|---|---|
| `v` | 必须等于 `HOOK_PROTOCOL_VERSION = 1` | 不等直接拒收 |
| `type` | 开放集合；未知 type 丢帧不断连 | 无版本号演进 |
| `id` | 发送方生成的帧唯一标识 | 日志/去重；**不用于业务配对** |
| `ts` | 发送方 unix ms | 仅诊断，不参与逻辑 |
| `payload` | 按 type 校验；未知字段静默忽略 | 业务数据 |

- 单帧上限 `HOOK_MAX_FRAME_CHARS = 48 MiB`（JSON 序列化后字符数），纯防 OOM；附件精细限额由 provider 负责。
- 收帧唯一入口等价于 `parseHookMessage`：接受 WS 文本或已 parse 对象；坏帧返回 `{ok:false,error}`（带字段路径），不抛异常。服务端必须实现同语义校验器；坏帧只丢该帧并记日志，不能因单帧坏数据断开整条 lane（握手/鉴权失败除外）。
- `ping`/`pong` 的 payload 必须是空对象；多余键拒收。

### 3.2 握手时序

1. desktop 建立 TLS/WSS，带 Bearer accessToken；服务端校验账号、设备、渠道开关。
2. upgrade 成功（或 401 拒绝）后，desktop 发送第一帧 `hello`。
3. 服务端校验 `protocolVersion === 1`、`workspaces` 非空且首位为 `chat`、`defaultWorkspace` 属于 `workspaces`、`agents` 合法；返回 `welcome{serverName,features[]}`。
4. desktop 收到 `welcome` 才置 connected；此前 socket open 不算已连接。
5. 任一侧发送 `ping`，对端必须回 `pong`；除 pong 外任何入站帧都算活性。
6. 重连后 desktop 重发 `hello`；服务端以**最新一帧**为准（工作区别名可能已变更）。重连后能力集重新协商，旧快照不得跨连接使用。

### 3.3 鉴权与失败码

| 场景 | 服务端行为 | 客户端行为 |
|---|---|---|
| 无/坏 Bearer | HTTP 401 拒绝 upgrade | 刷新 token 一次后重连；仍失败按退避重试 |
| 账号停用/渠道关闭 | 401 或 1008；不建立 lane | 停止该 lane，等待设置变化 |
| 同账号同 deviceId 已有连接 | close `4000` + `device already connected` | 进入 standby，30s 探测 |
| hello 非法（版本/别名/默认目录） | close 1008（或协议定义码）并记审计 | 修正后重连；不得进入 connected |
| 单帧坏数据 | 丢弃该帧，不断连 | 记日志；继续 |
| 服务端过载 | close 1013（建议） | 指数退避重连 |

### 3.4 重连与心跳

- desktop：25s 发 `ping`；65s 无任何入站帧判死链并 `terminate()`；重连 1s→30s 指数退避，延迟取 `[0.7, 1.0] × 退避值`（向下抖动，避免全端齐步重连）；standby 固定 30s。
- 服务端：必须回 `pong`；建议独立心跳 30s、静默 90s 关闭；关闭前完成该连接的孤儿收口。
- `welcome.features` 是连接级快照。服务端滚动发布时客户端可能落到老节点；老节点不得发送自己未宣告的能力帧。客户端断连即清空 `serverFeatures`，不得拿旧快照发帧。

### 3.5 能力矩阵（客户端 hello 实际声明）

| lane | desktop `hello.features` | 服务端 `welcome.features` 必须包含 |
|---|---|---|
| Slack | `session-result-v1`、`multi-team`、`session-picker-v1` | `slack-tools`（可选）、`multi-team`、`lifecycle-announcement-v1`、`turn-reopen-v1`；`turn-delivery-v1` 不建议对 Slack 发（客户端不消费） |
| Telegram | `provider-bind-v1`、`provider-prefs-v1`、`session-picker-v1`、`provider:telegram`、`session-result-v1`、`group-relay-v1`、`group-relay-recipient-v1`、`provider-behavior-v1`、`msg-op-v1`、`telegram-progress-ops-v1`、`telegram-final-ops-v1`、`telegram-card-ops-v1`、`telegram-commands-v1`、`session-new-v1` | 与 hello 对应的同名能力；`provider:telegram` 是渠道可见性的硬门 |
| X | `provider-bind-v1`、`provider-prefs-v1`、`session-picker-v1`、`turn-delivery-v1` | 必须含 `provider:x`（客户端要求，但 X hello 不声明它）；可选 `turn-delivery-v1` |

- 任一侧缺席即整条新增帧族不可用。服务端不得向未声明能力的 desktop 推新帧；desktop 不得向未宣告的 server 发新帧。
- Telegram 的 `provider.commands.set` 只在 `telegram-commands-v1` 双方协商后发送；服务端只存内存、不落库。
- Slack 的 `slack-communications` 只在服务端宣告后由客户端消费；授权“本机通讯”不改变 Bot 接收设备。

## 4. 42 种消息逐项表

> 方向：`d→s` = desktop 到 server；`s→d` = server 到 desktop。
> 版本：`v1` = 原始 7 种；`v2` = 公开快照的增量批次（版本号仍 1）；`C+` = 当前客户端副本追加、公开快照未覆盖；`cap` = 需能力协商；字段中的 `/null` 表示可为 null 的联合分支。
> ack/结果列写“线上应答帧”；没有应答的帧明确写 fire-and-forget。

### 4.1 连接、任务与消息操作（12 种）

| # | 类型 | 方向 | 用途 | 关键字段与强制约束 | ack/结果 | 版本 |
|---|---|---|---|---|---|---|
| 1 | `hello` | d→s | 建连自报家门 | `protocolVersion,deviceId,deviceName,workspaces[],defaultWorkspace?,agents[],features?,lifecycleAnnouncement?`；`workspaces` 首位恒 `chat`；`defaultWorkspace` 必须是成员；别名变更后重发 hello 以最新帧为准；`lifecycleAnnouncement` 缺省按 true | `welcome` | v1 |
| 2 | `welcome` | s→d | 握手完成 | `serverName,features[]`；空数组 = 无任何可选能力 | — | v1 |
| 3 | `ping` | 双向 | 心跳 | payload 必须 `{}` | `pong` | v1 |
| 4 | `pong` | 双向 | 心跳应答 | payload 必须 `{}` | — | v1 |
| 5 | `task.dispatch` | s→d | 派发一轮任务 | `requestId,externalKey,workspace/null,sessionId/null,prompt`；`sessionId=null` 时 `workspace` 必填；非 null 表示接管，目标 workingDir 必须落在本连接注册别名内；`options{model,permissionMode,agentKind,effort}`；`attachments[≤16]{name,mimeType,dataBase64}`；`source{im,channelName,teamId,teamName,threadContext[],triggerMessageId,userText,xContext?}` | `task.ack` | v1；source 增量为 v2/C+ |
| 6 | `task.ack` | d→s | dispatch 立即三态应答 | `requestId,result(accepted/queued/rejected),reason/null,sessionId/null,queuePosition/null`；`reason` 仅 rejected 且必须是 `unknown_workspace/workspace_not_allowed/session_not_found/disabled/invalid`；`queuePosition` 仅 queued 的非负整数；rejected 时 `sessionId=null`，accepted/queued 必须非空 | 对 dispatch 的应答 | v1 |
| 7 | `turn.end` | d→s | 一轮收口 | `requestId,externalKey,sessionId,status(ok/error/cancelled),finalText,errorMessage,usage.durationMs,attachments?`；ok 时 `errorMessage=null`，error 时必须非空，cancelled 恒 null；`background?:true` 表示绑定 lane 的后台结果而非本次 dispatch；`clientFinal{complete}` 仅 Telegram 客户端终稿 | 无 wire ack；协商 `turn-delivery-v1` 时另有 `turn.delivery` | v1；attachments/background/clientFinal 为 v2/C+ |
| 8 | `turn.delivery` | s→d | 普通 `turn.end` 的两阶段交付回执 | `requestId,state(accepted/retrying/delivered/failed),attempt,retryAt/null,error{code,message,retryable}/null`；accepted 的 attempt=0；retrying 必须 `retryable=true` 且 `retryAt` 正安全整数；failed 必须 `retryable=false`；error 只允许三键 | 对 `turn.end` 的应答 | v2；cap `turn-delivery-v1` |
| 9 | `turn.progress` | d→s | 执行中渲染快照 | `requestId,text`；完整 markdown 快照，整帧替换/latest-wins；桌面约 1.5s 节流；server 不拼接、不累积、不补发 | 无 | v2 |
| 10 | `turn.reopen` | d→s | 已收口任务续跑回流 | `requestId`（续跑新 id，必须全局抗碰撞）,`reopenOf`,`externalKey`,`sessionId`,`reason`；`requestId≠reopenOf`；只路由 progress/end/cancel，刻意不含 `interaction.*`；server 认不出 `reopenOf` 时静默忽略整帧并隔离该 requestId | 无 | v2；cap `turn-reopen-v1` |
| 11 | `msg.op` | d→s | 客户端驱动渠道消息操作 | `opId,requestId?,scope{externalKey},action`；`action`：`send{text,replyToMessageId?,tier?,silent?,buttons?,effectId?}`、`edit{messageId,text,tier?,buttons?}`、`delete{messageId}`、`react{targetMessageId,emoji,big?}`（空 emoji=撤销）、`typing`、`media{items[],album?,replyToMessageId?}`；`scope` 只准 `externalKey`，携带 chatId/threadId 一律拒收；`purpose?`、`finalPart?`、`interactionId?`、`interactionClosed?` | `msg.op.result` | v2；purpose/finalPart 等为 C+；cap `msg-op-v1` + 逐 purpose 能力 |
| 12 | `msg.op.result` | s→d | 消息操作结果 | `opId,ok,messageId?,messageIds?,error?,errorCode?,channelErrorCode?,retryAfterMs?`；`messageId` 是后续 edit/delete/react 的唯一依据；`messageIds` 用于相册；`retryAfterMs` 是限速建议 | 对 `msg.op` 的应答 | v2/C+ |

### 4.2 绑定、查询、交互、偏好、工具与行为（30 种）

| # | 类型 | 方向 | 用途 | 关键字段与强制约束 | ack/结果 | 版本 |
|---|---|---|---|---|---|---|
| 13 | `bind.start` | d→s | 发起 Slack 绑定 | 新端发 `{}`（设备身份取 hello 的 deviceId）；`teamId?` 把 OIDC 授权页 pin 到指定 workspace；`email?` 仅识别旧客户端（deprecated） | `bind.update` | v2 |
| 14 | `bind.update` | s→d | Slack 绑定状态机 | `state(none/pending/confirmed/denied/expired/failed/revoked),slackUserId,slackUserName,message,authorizeUrl?,reason?,installUrl?,teamName?,teamId?`；pending 必带 `authorizeUrl`；confirmed 必带 `slackUserId`；failed 必带 `message`；`reason` 已知 `not-installed`/`superseded`；`installUrl` 仅 `not-installed` | — | v2 |
| 15 | `bind.revoke` | d→s | 解除 Slack 绑定 | `{teamId?,pendingOnly?}`；`teamId` 非空=只解绑该 team；空/缺省=解绑本设备全部；`pendingOnly=true`=只作废在途授权；未知键拒收；旧 server 要求空对象，故 desktop 仅在 multi-team 协商后发非空形态 | `bind.update(revoked)` / `bind.state` | v2；multi-team cap |
| 16 | `provider.bind.start` | d→s | 发起 provider 绑定尝试 | `requestId,provider(slack/telegram/x),scopeId?`；`scopeId` 如 Telegram bot id | `provider.bind.update` | v2；cap `provider-bind-v1` |
| 17 | `provider.bind.cancel` | d→s | 取消指定尝试 | `requestId,provider,attemptId`；只取消 attempt，不影响 confirmed binding | `provider.bind.update` | 同上 |
| 18 | `provider.bind.revoke` | d→s | 解除指定绑定 | `requestId,provider,bindingId`；只解除该 binding | `provider.bind.update` | 同上 |
| 19 | `provider.bind.update` | s→d | 绑定过程事件 | `provider,replyTo/null,state,attemptId,bindingId,principalId,principalName,scopeId,scopeName,connectUrl,expiresAt,reason,remediationUrl,actions[]`；状态 `none→pending→awaiting_confirmation?→confirmed`，终态 `denied/expired/failed/revoked/superseded`；pending 必带 HTTPS `connectUrl`+attemptId+expiresAt；confirmed 必带 bindingId/principalId/scopeId；失败终态必须非空 reason 并清空 binding/principal/expiry；revoked/superseded 保留 bindingId 但清空 attempt/expiry；`actions` ≤16 且不重复 | — | v2 |
| 20 | `provider.bind.state` | s→d | 某 scope 权威快照 | 同 `provider.bind.update` 的完整形状；所有可空字段显式给出，避免旧值残留 | — | v2 |
| 21 | `query.request` | s→d | 实时拉清单 | `queryId,kind(workspaces/models/sessions/session-new)`；`queryId` 由 server 生成；`session-new` 时带 `sessionNew{previousExternalKey,externalKey,workspace,options?,source?}` | `query.response` | v2；session-new 为 C+ cap |
| 22 | `query.response` | d→s | 清单应答 | `queryId,kind,ok,error`；`ok=false` 必须非空 error；ok 时按 kind 带 `workspaces`、`agents[{agentKind,models[{id,label,efforts[],defaultEffort,group?}],permissionModes?}]`、`sessions[≤20]`（只允许 `id/title/workspace/lastActiveAt`，workspace 不得绝对路径）或 `sessionId` | 对 query.request 的应答 | v2；C+ 字段 |
| 23 | `task.cancel` | s→d | 中断在跑任务 | `requestId`；desktop 以 `turn.end(cancelled)` 收口；未知/已收口静默忽略 | `turn.end(cancelled)` | v2 |
| 24 | `session.archive` | s→d | 归档 externalKey 绑定会话 | `externalKey`；`/new` 换代用；desktop 幂等，无绑定/不存在/已归档静默忽略 | 无 | v2 |
| 25 | `interaction.request` | d→s | 执行中交互卡 | `requestId,interactionId,kind,title,body,buttons[{id,label,style}]`；`kind` 开放集合（ask_user_question/plan_review/权限审批等）；按钮 1–24 个；id 卡内唯一且不含竖线；style 仅 `primary/danger/default`；server 只渲染不理解决策 | `interaction.decision`（用户点击） | v2 |
| 26 | `interaction.decision` | s→d | 用户按下按钮 | `requestId,interactionId,buttonId`；desktop 查本地映射；迟到/未知静默忽略 | — | v2 |
| 27 | `interaction.cancel` | d→s | 交互已收口 | `requestId,interactionId,reason`；desktop 超时/轮次结束发；server 摘按钮并改写文案；幂等 | 无 | v2 |
| 28 | `prefs.get` | d→s | 拉 Slack 目录偏好 | `requestId`；用于配对 `prefs.state.replyTo` | `prefs.state` | v2 |
| 29 | `prefs.set` | d→s | 部分更新 Slack 偏好 | `requestId,workspace,model?,effort?,agentKind?,permissionMode?,teamId?`；`undefined`=不动、`null`=显式清空；server 只 shape 校验，不校验值合法性；workspace 必须是别名 | `prefs.state` | v2；teamId 为 multi-team |
| 30 | `prefs.state` | s→d | Slack 偏好全量快照 | `replyTo/null,bound,prefs[{workspace,model,effort,agentKind,permissionMode,teamId?}]`；get/set 回显 requestId；主动推送（渠道 `/model` 写入后）为 null；`bound=false` 时 prefs 必空 | — | v2 |
| 31 | `provider.prefs.get` | d→s | 拉 provider 偏好 | `requestId,provider,bindingId/null,scopeId/null`；二者恰一非空 | `provider.prefs.state` | v2；cap `provider-prefs-v1` |
| 32 | `provider.prefs.set` | d→s | 更新 provider 偏好 | 选择器 + `requestId,workspace,model?,effort?,agentKind?,permissionMode?`；workspace 必须是别名；不写 teamId | `provider.prefs.state` | v2 |
| 33 | `provider.prefs.state` | s→d | provider 偏好快照 | 选择器 + `replyTo/null,bound,prefs[{workspace,model,effort,agentKind,permissionMode}]`；`bound=false` 时 prefs 必空；条目不接受 teamId | — | v2 |
| 34 | `tool.request` | d→s | 调 Slack 网关工具 | `requestId,tool(开放集合),args?,teamId?`；未知工具回 `UNKNOWN_TOOL` 而非丢帧；多绑定缺 teamId 时回 `AMBIGUOUS_TEAM` | `tool.response` | v2；cap `slack-tools` |
| 35 | `tool.response` | s→d | Slack 工具应答 | `replyTo,ok,result?,error?{code,message}`；`ok=false` 必须非空 error；`ok=true` error 缺席/null；desktop 按 code 分支 | 对 tool.request 的应答 | v2 |
| 36 | `bind.state` | s→d | Slack 绑定全量快照 | `bindings[{teamId,teamName,slackUserId,slackUserName}]`；连接建立与任何绑定变化后推送；C+ 增加 `communications[{...binding,enabled}]`；旧 desktop 丢帧不断连 | — | v2；multi-team cap |
| 37 | `group.message` | s→d | 群消息中继 | `provider,recipient?{bindingId,principalId},chatId,threadId/null,messageId,chatName/null,author{name,isBot?,id?,username?},text,fileNames?,sentAt`；fire-and-forget；`recipient` 在 `group-relay-recipient-v1` 后必填；`text` ≤8192 且与 fileNames 至少一个；fileNames ≤20、每个 ≤256；`author.id` 是 52-bit 内规范正整数，`username` 仅 `[A-Za-z0-9_]{1,32}`；server 必须过滤一次性凭证 | 无 | v2；cap `group-relay-v1` |
| 38 | `lifecycle.preference` | d→s | 当前设备上下线通知偏好 | `{enabled:boolean}`；仅在 `welcome.features` 含 `lifecycle-announcement-v1` 后发送；缺省/旧 server 保持既有开启行为 | 无 | v2 |
| 39 | `provider.behavior.get` | d→s | 拉 Telegram 行为配置 | `requestId,provider(telegram),bindingId`；只认 bindingId，不接受 scopeId | `provider.behavior.state` | v2；cap `provider-behavior-v1` |
| 40 | `provider.behavior.set` | d→s | 部分更新 Telegram 行为 | `requestId,provider,bindingId,emojiReactions?,replyQuoteDm?,replyQuoteGroup?,groupActivation?{chatId,value}`；三个全局字段是 `枚举/null` 三态：缺省=不动、枚举=显式 override、null=清除回落默认；`groupActivation` 单条 patch，`chatId` 必须是 52-bit 内规范负整数；至少要有一个实际改动，空 set 拒收 | `provider.behavior.state` | v2 |
| 41 | `provider.behavior.state` | s→d | Telegram 行为全量快照 | `provider,bindingId,replyTo/null,bound,emojiReactions,replyQuoteDm,replyQuoteGroup,groupActivation{chatId:'always'}`；三字段必须是已解析有效值，不得 null；map 只列偏离默认的 chatId；`bound=false` 时强制回 `DEFAULT_TELEGRAM_BEHAVIOR` + 空 map | — | v2 |
| 42 | `provider.commands.set` | d→s | Telegram 命令菜单 | `provider:'telegram',menus[{languageCode/null,commands[{command,description}]}]`；**仅客户端副本**；每次握手成功后发一次；server 只存内存、按 `{type:'chat',chat_id:principalId}` 调 `setMyCommands`；只管理默认菜单与 zh/ja/ko，全量重写；server 重启后回自己的默认菜单 | 无 | C+；cap `telegram-commands-v1` |

合计 **42 种**。其中公开快照 41 种；第 42 种 `provider.commands.set` 只在当前客户端副本，但服务端必须支持，否则官方 Telegram 菜单会回落到服务端默认。

## 5. 任务生命周期与 IM turn flow
### 5.1 入口状态机

```text
inbound → admitted → queued? → dispatching → running → finalizing → terminal(ok|error|cancelled)
             │            │            │            │
             └ rejected   └ discarded  └ stopped ───┘
```

- **inbound**：平台事件到达；服务端按事件 id 去重，解析发送者、chat/topic、消息 id、附件、回复关系、群名、消息链接。
- **admitted**：服务端解析 lane（`externalKey`）、命令或普通消息；普通消息构造 `task.dispatch`。desktop 的 `handleDispatch` 先查终稿账本，再查 ackHistory，再用 inflight 串行化。
- **queued**：目标 session 正在跑或队列非空；desktop 回 `queued + queuePosition`，队列上限 20；排队任务只保存 commit 回调，provider 尚未受理。
- **dispatching**：前置层（交接/计划对账/Goal/渠道说明）在真正发送前加入，不进入持久化文本与队列文本。
- **running**：一轮只有一个观察者；终态只由 done、终止 error、会话 closed/error 三个出口给出。
- **finalizing**：收口到渠道；成功只发正文；失败正文为空、错误走独立字段；带附件单独发送。
- **terminal**：服务端保存终态路线（供续跑锚点）、清理进度消息/卡片、打终态表情（如启用）。

### 5.2 找任务 / 建任务

| 场景 | 判据 | 行为 |
|---|---|---|
| 普通消息，已有绑定 | `(connectionId, externalKey)` 有 sessionId，且该会话 workingDir 仍在 workspace 映射内 | 复用；每条消息现场重算，不依赖持久授权状态 |
| 普通消息，无绑定 | 无映射 / 映射失效 | 按 workspace 别名新建任务并落绑定；`workspace` 为空时用 lane 默认目录 |
| 接管 `/session` | `task.dispatch.sessionId` 非空 | 目标会话 workingDir 必须落在本连接注册别名内，否则 `workspace_not_allowed`；成功后把 externalKey 重绑到该 session |
| 接管失效 | session 不存在/不可用 | 静默新建任务并重绑；不向用户追加“原任务读不到”的说明 |
| `/new`（旧协议） | `session.archive(previousExternalKey)` + 新 externalKey 的 dispatch | 新任务先建立、绑定落地后再归档旧任务；旧任务保留历史 |
| `/new`（`session-new-v1`） | `query.request.kind=session-new` | desktop 立即创建空白任务并绑定新 externalKey，返回 `sessionId`；随后归档旧任务；旧 desktop 回落“下一条消息懒创建” |
| 绑定失效 | 任务被归档/删除/移出工作目录 | 丢弃旧绑定、静默新建；原任务最近内容可交接给新任务，但不在 IM 里追加说明 |

- `externalKey` 是服务端生成的不透明字符串；实测 Telegram 群 lane 形如 `telegram:group:<botId>:<chatId>:<principal>:g<n>`，topic 形如 `telegram:topic:<botId>:<chatId>:<threadId>:<principal>:g<n>`，DM 形如 `telegram:dm:<botId>:<userId>:g<n>`；Slack 有 `slack:...`、`team-slack:...`、`dm:...` 等历史形态。客户端只按前缀分类，服务端不得让段数成为客户端解析前提。
- `g<n>` 是绑定换代后缀；`/new` 必须递增 generation，避免旧任务映射串到新任务。服务端负责生成并保证同一 lane 的 generation 单调。
- `defaultWorkspace` 取值顺序：本 lane 已定目录 > `hello.defaultWorkspace` > `chat`；`hello.defaultWorkspace` 必须是 `workspaces` 成员。
- 新任务默认配置由 desktop 解析：本机目录偏好 > 桌面 IM 新会话默认 > 当前可用模型首项 > 草稿裸值；权限档单独走“显式且支持 > 不支持时该 agent 最严档 > 从未填过则 `bypassPermissions`”。服务端只下发 `options` 与目录偏好，不替 desktop 决定；`options.permissionMode` 仅对新建会话生效，复用/接管以 session meta 为准。

### 5.3 排队、插话、停止

- **排队**：每会话 FIFO，上限 20；溢出回 `rejected(invalid)`。`queued` 的 `queuePosition` 从 0 起。排队任务在 desktop 重启后丢失；服务端必须用 lease 超时收口（建议 10 分钟无 heartbeat/ack 视为过期），不能让用户永久等。
- **插话**：当前没有 steer 能力。用户在任务运行中再发一条消息，是**新的一轮请求**，进队列，不会合并进正在跑的 turn。服务端应明确回“已排队”，不要假装插话成功。排队消息在桌面端不可见、不可删；`/stop` 只取消正在跑的 requestId，已排队的请求默认继续跑。
- **停止**：`/stop` → 服务端对正在跑的 `requestId` 发 `task.cancel`；desktop 走与桌面 Stop 同一套 `stopSessionTurnExplicitly`：撤自动续跑与退避、取消上下文溢出恢复、暂停 Goal、停输入队列、中止当前轮；以 `turn.end(cancelled)` 收口。排队中的同 requestId 若被取消，desktop 直接以 cancelled 收口，任务从未开始。
- **未知/已收口**：`task.cancel`、`session.archive`、`interaction.decision` 对未知 id 静默忽略；服务端不得因迟到帧报错刷屏。
- **收尾**：`turn.end` 先进入 desktop 持久账本；连接在线则发送，离线则进内存/持久出箱；服务端发布成功后清理进度消息与未收口卡片；失败重试有界。

### 5.4 进度、终稿与续跑

- `turn.progress` 是完整快照，latest-wins；服务端只做“整体替换占位消息”，不拼接、不缓存、不补发。Telegram 协商 `telegram-progress-ops-v1` 后，desktop 自己经 `msg.op(purpose=turn-progress)` 发/改进度消息；服务端仍收 `turn.progress` 只用于续 turn lease，不再渲染（私聊草稿模式例外）。
- 成功终稿只发正文；失败终稿正文为空，错误走独立 `errorMessage`；`cancelled` 可带已产出的部分文本。输出上限错误是例外：desktop 可在失败 `turn.end.finalText` 保留截断正文。`background:true` 仅用于绑定 lane 的后台结果（scheduler、其它任务来信、Agent 自己唤醒），server 在原私聊/话题新发消息；普通 IM 轮次与已有 retry/reopen 轮次不另发，X 不参与。
- `turn.end.clientFinal.complete=true`：服务端跳过自己渲染，把 `finalPart=0` 的客户端终稿段提升为续跑锚点，照常做群中继/清理/表情；`false`：删除已登记的客户端终稿段并自己发布。带附件的轮次不认 `clientFinal`，附件始终由服务端发。
- `turn.reopen`：只用于“用户在桌面端显式续跑”；新 requestId 必须全局抗碰撞；服务端把 `reopenOf` 对应消息位置登记给新 requestId，后续 progress/end 原地刷新/定稿，渠道不新增消息。认不出 `reopenOf` 时静默忽略整帧并隔离该 requestId；连接断开时必须收口该连接上所有“已 reopen 未 end”的消息，收口落笔要校验消息仍归“被断开连接 + 那次 reopen”所有。
- 续跑轮 progress/end 一律直发、不缓存、不重连补发；这是对通用“断线补发终稿”的刻意偏离，因为服务端已在断连时收口并解绑。

## 6. 权限确认、来源消息与多端确认

### 6.1 交互卡

- 触发：agent 执行中产生 `ask_user_question`、`plan_review`、权限审批；desktop 合成渠道无关卡片，经 `interaction.request` 发给服务端。
- 卡片约束：标题 ≤60（hook 侧截断）、正文 markdown、按钮 ≤24（实际产品上限更小：ask 至多 6 个选项、plan 固定 2、权限固定 3）、button id 卡内唯一且不含竖线、style 仅三种。
- 服务端是**哑渲染器**：只渲染标题/正文/按钮，不解释 kind、不构造决策、不改变按钮语义；Slack 用 Block Kit actions，Telegram 用 inline keyboard，X 没有交互控件（见 6.4）。
- 点击：服务端把 `buttonId` 原样回 `interaction.decision`；desktop 用本地映射解析决策。迟到/未知 decision 静默忽略。
- 收口：desktop 超时（hook 30 分钟）按安全默认自决（ask 空答 / plan deny+dismissed / permission deny），发 `interaction.cancel`；服务端摘按钮、改写文案。服务端必须保留卡片定位直到最终决定，不能在点击瞬间就把正文改成“已允许”。
- 发卡未完成时用户已在 Cindy 确认：服务端/desktop 仍按同一最终决定更新卡片、撤按钮；发卡或更新失败不撤销已作出的决定，不自动重发。

### 6.2 多端共享权限

- 普通工具权限由 desktop `maker-ipc/interactionRouter.ts` 创建 `SharedPermission`；桌面、同账号远控/手机、IM 渠道共同回应，**第一份有效决定生效**，后续回答不覆盖。
- 官方 Hook 的权限也进入共同路由；desktop 通过既有 `interaction.cancel` 发送包含来源和结果的收口正文，**不新增 wire 字段**。服务端最终排版仍受各平台渲染器约束。
- 渠道收到回答只表示“已接收”，不承诺继续执行；只有共享 `result` 确定后才显示允许/拒绝/失效。暂停期间取消不能先显示“继续处理”。
- 机器人接管一个正在等待授权的 Cindy 任务时，权限面板保留并增加 IM 呈现；所有共享授权在暂停时只暂存首份回答，恢复后才发布最终结果。
- 服务端配套要求：保留权限卡定位到最终决定；`InteractionDelivery` 处理“发卡未完成、临时选择提示、最终结果交错”；迟到点击不覆盖最终正文；旧客户端没有最终回传时保留原正文/已选择提示并撤按钮。

### 6.3 来源消息

- `IMMessageEvent.interactionSource` 是可选传输层元数据：群/聊天名、话题名、发起人、平台原消息链接。
- 共享层取**本轮实际触发消息**，截取短摘要；绝不取聊天里“最新”一条。没有名称时显示平台聊天标识；没有链接时保留文本；不为展示额外请求模型或查询历史。
- Telegram 提供群名、发起人、消息链接；事件没有话题名时展示话题 ID。其他平台按已取得元数据提供名称，不能把未知 ID 冒充显示名。
- 来源放在确认正文，并随原正文保留到允许/拒绝/作废后；桌面/手机的自动审批故障提示单独读取 `metadata.imSourceDescription`，不覆盖来源。受保护消息不复制正文摘要。
- 服务端在 dispatch 时持久化来源快照（trigger message id、chat/topic、requester、link、摘要），供后续 permission 卡使用；不能在确认时重新拉“最新消息”。
- 渠道 owner 检查、桌面本机专属确认、既有权限策略不变；渠道破坏性操作检查在展示桌面镜像前执行。

### 6.4 渠道差异与 fail-closed

- Slack：thread 回帖卡片，按钮 style 映射 Block Kit；可在 thread 内更新。
- Telegram：私聊发本 lane；群/topic 的权限卡按现行规则改投 owner 私聊，并在正文前加服务端本地化的来源说明与原消息链接；按钮回调由服务端铸 `callback_data`、记 `(requestId, interactionId, buttonId)` 映射，点按后回 `interaction.decision`；`interactionClosed=true` 后作废映射。
- X：只有一条公开回帖名额，无交互控件。权限确认不能经 X 完成；服务端应把确认留给桌面/手机，X 侧只回一句“需要确认”的说明（或按产品规则不派发需要确认的任务）。这是**设计约束**，不是缺口；不得为了“能点按钮”在 X 上自动允许。
- 所有渠道：点击回调必须校验归属（requestId 是当前设备正在跑的任务、interactionId 未收口、buttonId 属于该卡）；不匹配一律静默拒绝。

## 7. 多团队、多渠道、provider 绑定与偏好

### 7.1 Slack 绑定与 multi-team

- 绑定流程：desktop `bind.start`（可带 `teamId` pin）→ 服务端签发 Sign in with Slack OIDC `authorizeUrl` → 用户在浏览器授权 → 服务端回调校验 state、换取 Slack 用户身份、检查 App 是否安装 → `bind.update(confirmed, slackUserId, slackUserName, teamName, teamId)`。
- 未安装 App：`bind.update(failed, reason='not-installed', message, installUrl)`；desktop 显示“安装 App”引导并带 team 参数。
- 被同用户另一台设备顶替：`bind.update(revoked/failed, reason='superseded')`。
- multi-team：`bind.state` 是权威全量快照；`bind.update` 只承载过程事件。连接建立与任何绑定变化后推送 `bind.state`；旧 desktop 丢帧不断连。
- 单绑定旧语义：服务端未宣告 multi-team 时，按单绑定处理；`bind.revoke` 只接受空对象。desktop 仅在协商后发带 `teamId` 的形态。
- `teamId` 用于消歧：`prefs.set/state` 条目、`tool.request`、`task.dispatch.source.teamId/teamName`。多绑定且缺 `teamId` 时，服务端必须拒绝猜测并回结构化错误（`AMBIGUOUS_TEAM`），不能默认第一个 team。
- `slack-communications`：`bind.start/bind.update.purpose='communications'`、`bind.state.communications[{...binding,enabled}]`；只授权本机通讯，不修改 Bot 接收设备。服务端只在宣告该能力时消费。

### 7.2 provider-neutral 绑定（Telegram / X）

- 状态机：`none → pending → awaiting_confirmation? → confirmed`；终态 `denied/expired/failed/revoked/superseded`。状态单调，不允许从终态回到 pending。
- 必填联动：pending/awaiting_confirmation 必带 HTTPS `connectUrl`、`attemptId`、`expiresAt`；confirmed 必带 `bindingId/principalId/scopeId`；失败终态必须非空 `reason` 并清空 binding/principal/expiry；revoked/superseded 保留 bindingId 但清空 attempt/expiry。
- Telegram：`connectUrl` 必须是严格 `https://t.me/<botUsername>?start=<43-char-base64url-token>`；只允许 `start` 一个参数、无端口/凭证/fragment/重复参数；bot username 必须匹配 `^[A-Za-z][A-Za-z0-9_]{1,28}[Bb][Oo][Tt]$`；token 为 32 随机字节的 canonical unpadded base64url。
- X：`connectUrl` 必须精确 `https://x.com/i/oauth2/authorize`，参数集恰为 `response_type=code、client_id、redirect_uri、scope、state、code_challenge、code_challenge_method=S256`；无重复/未知参数；服务端生成 PKCE verifier 并保存到 attempt，回调校验 state + verifier。
- `actions` 是 UI 能力提示，当前值 `open_connect_url/copy_connect_url/cancel/retry/revoke/open_provider/add_to_group`；消费者忽略未来未知 action。服务端仍需按 provider 做 host allowlist，不能把任意 URL 塞进 `connectUrl/remediationUrl`。
- `provider.bind.state` 是某 scope 的权威时点快照，所有可空字段显式给出；`provider.bind.update` 是事件/应答。desktop 对 `bindingId` 不匹配、已被替换的 binding、过期回执一律丢弃。
- `provider.bind.cancel` 只取消指定 attempt；`provider.bind.revoke` 只解除指定 binding。两者都必须带 `requestId` 以配对应答。

### 7.3 目录偏好

| 域 | 键 | 语义 | 写入方 |
|---|---|---|---|
| Slack `prefs.*` | `(slack user, teamId, workspace alias)` | agent/model/effort/permission；本地是正本，server `user_prefs` 是 `/model` 卡镜像 | desktop 设置页 / 渠道 `/model` 卡 |
| `provider.prefs.*` | `(provider, bindingId 或 scopeId, workspace alias)` | 同上，provider-neutral；条目刻意不含 teamId | desktop / 渠道卡 |
| `provider.behavior.*` | `(provider=telegram, bindingId)` | 表情/引用/群激活 | desktop / 设置页 |

- 选择器：`provider.prefs.*` 必须 `bindingId` 与 `scopeId` 恰一非空；`provider.behavior.*` 只认 `bindingId`，不接受 `scopeId`。
- workspace 只允许别名；绝对路径拒绝。`undefined`=不动、`null`=显式清空；`bound=false` 时 `prefs` 必空、behavior 回默认。
- 服务端只做 shape 校验，不校验值合法性；过期模型/档位由 desktop 派发侧 defaults 兜底。
- `prefs.state.replyTo=null` 是服务端主动推送（渠道 `/model` 写入后）；desktop 只在 `replyTo` 非空时配对在途 get/set，不拿 server 快照覆盖本地写入。
- multi-team 迁移后，Slack 目录偏好的本机正本是 `owners/<hash>/hook-workspace-prefs.json`，键 `(channel, teamId, workspace)`；server `user_prefs` 不再是派发正本，只作 `/model` 卡镜像。服务端仍须接受 `prefs.*` 读写以兼容旧客户端和渠道卡。

### 7.4 Telegram 行为配置

- 默认值：`emojiReactions='minimal'`、`replyQuoteDm='off'`、`replyQuoteGroup='first'`；群激活默认 `mention`，只有显式 `always` 才进入 map。
- `set` 三态：缺省=不动；枚举=显式 override（即使等于当前默认也保留用户意图）；`null`=清除 override、回落默认。`groupActivation` 一次只 patch 一个 chatId，`value='always'` 或 `null`；空 set 拒收。
- `state` 必须是已解析有效值（不是 patch）；`groupActivation` 只列偏离默认的 chatId；`bound=false` 时强制默认 + 空 map。
- `chatId` 必须是 Bot API 52-bit 范围内规范负整数（群/频道），不能是正数/前导零/超范围。

## 8. Telegram 两类 bot 架构差异与命令面

### 8.1 两套架构

| 维度 | 官方 bot | 个人 bot |
|---|---|---|
| 连接持有者 | 服务端 `telegram-hook-server` | desktop `packages/lizi-im/src/telegram/**` |
| lane 模型 | per-principal（发言人自己的 Telegram user id） | per-chat（本地单 owner 模型） |
| 绑定 | 服务端 deep-link/principal/bindingId | 用户自填 bot token；桌面设置页解绑 |
| 入站命令分发 | 服务端解析并分发 | desktop 本地 `botCommands.ts` |
| 命令菜单 | 协商 `telegram-commands-v1` 后由 desktop 经 `provider.commands.set` 下发；服务端只执行 `setMyCommands` | desktop 直接 `setMyCommands` |
| 过程消息 | 默认服务端渲染；协商后 desktop 经 `msg.op` 发布 | desktop 直接 Bot API 编辑 |
| 终稿 | 服务端发布；协商 `telegram-final-ops-v1` 且普通成功、无附件时 desktop 发布 | desktop 发布 |
| 群权限 | 按用户配置，完全访问即完全访问 | 群轮次保留逐轮策略；owner 的完全访问只对 owner 触发轮次生效 |
| 群历史 | 私聊不能跨群检索；群轮次 lane-only | 私聊 owner 可跨群检索；群轮次 lane-only |
| 离线积压过期 | 10 分钟；超龄整条静默消费 | 60 分钟；私聊跳过，群消息仍入本地窗口 |
| typing 上限 | 5 分钟 + 设备在线门控 | 10 分钟 |
| Agent 直调 Bot API | 无（连接在服务端） | 有 `cindy_telegram_bot` MCP |
| `/start` | 无（深链绑定代替） | 有 |
| `/status`、`/unlink` | 有 | 无（有意差异） |
| `/unbind`、`/effort`、`/agent` | 有 | 无（当前缺口） |

### 8.2 官方命令面

| 命令 | 用途 | 备注 |
|---|---|---|
| `/new` | 立即创建并绑定新任务 | 必须已创建并出现在任务列表；不得只清上下文；旧任务保留历史；新任务从当前默认配置解析 agent/model/source/effort/permission |
| `/help` | 帮助 | 菜单文案与注册表同源 |
| `/stop` | 停止当前轮 | 只对正在跑的 requestId 发 `task.cancel`；队列中的同任务请求默认继续 |
| `/session` | 选择最近任务 | `query.kind=sessions`，≤20 条脱敏；选中后以 `sessionId` 接管并重绑 externalKey |
| `/project`、`/workspace` | 选择工作目录 | 两条菜单文案逐字相同；`/workspace` 官方独立命令，个人侧是 `/project` 别名 |
| `/model` | 选择模型 | `query.kind=models`；写目录偏好；影响后续消息 |
| `/effort` | 思考强度 | 同上；官方有、个人缺 |
| `/agent` | 切换 Agent | Claude Code / Codex / Pi；官方有、个人缺 |
| `/permission` | 权限档 | 写任务内权限；不改变共享权限确认的最终决定 |
| `/settings` | 设置入口 | 链接/说明 |
| `/ctr`、`/exctr` | 接管/解除接管 | `/exitctr` 仅个人别名为兼容；官方只精确匹配 `exctr` |
| `/unbind` | 清除当前 chat 的项目映射 | 官方有、个人缺 |
| `/status` | 关联状态 | 官方有（链路可断，才有“关联状态”）；个人无 |
| `/unlink` | 解除 Telegram 关联 | 官方有；个人在设置页解绑 |

- 服务端命令分发必须保留原始用户文本（`source.userText`），不得把服务端装饰后的 prompt 当作用户原话；Pi 管理命令在官方 hook 的 origin 是 `hook`，不得伪装成本机命令直接执行。
- 命令别名是跨仓镜像缺口：`/exitctr` 只有个人 bot 认；官方服务端只认 `exctr`。新增/修改别名必须双仓核对。
- 命令菜单：desktop 按 en（默认）/zh/ja/ko 各渲染一份，经 `provider.commands.set` 下发；服务端只管理默认菜单与 zh/ja/ko，每次全量重写，缺席语言写空，其它语言码（含显式 `en`）忽略；菜单只存内存、不落库：desktop 每次握手重发，服务端重启后到 desktop 重连前用自己的默认菜单。

### 8.3 官方 Telegram 的消息载体

- 进度：默认服务端按 `turn.progress` 渲染占位消息；协商 `telegram-progress-ops-v1` 后 desktop 用与个人 bot 同一份 `startTelegramProgressCarrier` 渲染 HTML，首帧 `send`、后续 `edit`，服务端只执行。私聊草稿模式（`TELEGRAM_DM_DRAFT_ENABLED`）仍由服务端承载，desktop 进度 op 回 `PROGRESS_UNAVAILABLE`。
- 终稿：默认服务端新发正文，分段与附件发送完成、保存终态 route 后才尽力删除进度消息；删除失败允许两条并存。协商 `telegram-final-ops-v1` 的普通成功、无附件轮次由 desktop 发布：Rich → HTML → plain、分段、失败回落；`turn.end.clientFinal.complete` 决定服务端是否接管。带附件、失败、取消、续跑轮、NO_REPLY、私聊草稿仍由服务端发布。
- 卡片：默认服务端渲染；协商 `telegram-card-ops-v1` 后 desktop 用 `layoutTelegramCard` 渲染并经 `msg.op(purpose=interaction-card)` 发布；服务端铸 callback_data、记映射、点按回 `interaction.decision`；群/topic 卡改投 owner 私聊并加来源说明。客户端卡片 op 明确失败时回落 `interaction.request` 旧路径。
- 私聊终稿首段可挂完成特效 `messageEffectId`；装饰失败只降级装饰，不重发正文。

## 9. 群组中继、通知、附件与表情
### 9.1 群消息中继（group-relay）

- 设计边界（2026-07-28 决策）：**群聊内容不得驻留在 server**（内存也不行）。server 收到群消息后，对已声明 `group-relay-v1` + `group-relay-recipient-v1` 的成员桌面转发即弃；server 只可存 `chatId ↔ principal` 成员元数据用于路由；滚动窗口、增量游标、上下文拼装全在 desktop 本地。
- `group.message` fire-and-forget，无 ack，桌面离线即丢；每个扇出帧的 `recipient{bindingId,principalId}` 必须来自发送当刻再次确认的 binding，不得复用消息作者或客户端当前状态推断。desktop 只在 recipient 与当前 confirmed binding 精确匹配时入窗。
- server 必须过滤一次性凭证（如 Telegram 绑定深链 `/start <token>`），不转发；群消息里的 bot 命令、受保护内容、超龄消息按产品规则处理。群激活默认 `mention`；`always` 时未被召唤的消息也进 turn 并打 `ambient` 标，不 typing、不表情，模型可用 `NO_REPLY` 闭嘴；ambient 失败默认静默，但错误必须落桌面日志/任务记录。
- `threadId=null` 表示主群流；topic lane 用服务端下发的 threadId 分桶。注意 `message_thread_id` 的双语义：普通群 reply 链也会带该值（= reply root）；**投递位置**用裸值，**归属**必须用 `is_topic_message` 门控（该字段只有持有 Telegram 连接的一侧拿得到，官方在服务端判定）。
- desktop 本地窗口按 `(provider, chatId, threadId, messageId)` 存；`task.dispatch.source.triggerMessageId` 用于把“当前消息”从上下文精确剔除；旧 server 不发时降级为不剔重。窗口保留按容量：每 provider 命名空间 1 GiB 正文 + 500 万行安全阀，回收低水位 0.9；读取窗口默认 500 条。
- 官方 Telegram 命名空间 `telegram:<principalId>`；个人 `telegram-personal:<botId>`，两账号额度与内容隔离。群历史检索的 FTS/LIKE 必须把 lane 条件写进 SQL，不能先全局搜再过滤。
- X 不声明 group relay；Slack 的群/频道上下文走平台历史拉取，不走 group.message。X 的 `task.dispatch.source` 必须按祖先→当前顺序给 `threadContext`，末项 `messageId=triggerMessageId` 且 `authorId=requesterId`；`xContext.truncated` 标明截断，服务端只提供事实，prompt 由 desktop 组装。

### 9.2 上下线与任务通知

- `hello.lifecycleAnnouncement` 上报当前设备偏好；服务端缺省按 true（兼容旧 desktop）。服务端在 `welcome.features` 宣告 `lifecycle-announcement-v1` 后，desktop 才可发 `lifecycle.preference{enabled}` 即时覆盖。
- 新 desktop → 旧 server：旧 server 忽略 hello 字段、不宣告能力，desktop 不发 preference，服务端保持既有开启行为。
- 旧 desktop → 新 server：缺省 hello 字段按 true，保持升级前通知行为。
- 上下线通知只针对当前设备；账号切换/断连后不得用旧偏好影响新连接。
- 任务完成/失败通知就是渠道终稿消息，不是独立的推送通道；device-link 的 APNs/FCM 推送属于 device-link relay 分册，不在本分册范围。

### 9.3 附件与媒体

| 方向 | 限额/规则 |
|---|---|
| 入站 `task.dispatch.attachments` | 最多 16 条；`{name,mimeType,dataBase64}`；base64 内联复用已鉴权 WS，不另开 HTTP 端点 |
| 图片白名单 | `image/png`、`image/jpeg`、`image/gif`、`image/webp`；`image/jpg` 归一为 `image/jpeg`；BMP/SVG/HEIC/AVIF/TIFF 不在白名单 |
| 非图片 | 服务端可放开全 MIME 转发为“文件”；desktop 按媒体白名单选择媒体总仓或普通文件目录；空字节跳过并报告附件不完整 |
| 出站 `turn.end.attachments` | desktop 从最终文本 `xdt-image://`/`xdt-file:///` 与 tool_result 旁路收集；单图 5 MiB、单文件 10 MiB、总数 8、总字节 30 MiB；超限/越界跳过并在正文提示 |
| 单帧 | 48 MiB 字符上限；附件精细限额由 provider 负责 |
| Telegram | Bot API 下载上限 20 MiB；相册 `media` 一次多条；客户端终稿 op 的 `media` 被服务端拒收，附件始终随 `turn.end` 由服务端发 |
| Slack | 文件上传/分享需 bot token 权限；thread 内回复；Block Kit 图片块/文件块 |
| X | 媒体上传单独接口；一条回复的媒体数量/大小受 X 套餐限制；超限降级为文本说明 |
| 持久出箱 | 只存终态文本，不存附件字节；崩溃重放的兜底帧没有附件，服务端接管后附件会丢——这是已声明限制，带附件轮次不走客户端终稿发布 |

### 9.4 表情与 typing

- 排队：👀；开始处理：从 👨‍💻/🤔/🤓/✍ 随机选一个，处理中不变；成功/取消清除；失败保留 👎（生动档用失败变体）。表情关闭、ambient、群不允许时静默。
- 两种开启档位：`off/minimal/expressive`；`react` 空 emoji 撤销；`big=true` 对应夸张效果。
- 新版服务端统一管理状态反馈：鉴权后确认但不执行 Desktop 的自动 `:ack/:final/:final-fallback`；`:processing` 只触发一次处理中状态，避免旧客户端重复加 👍。旧 Desktop 连新 server 时，其 accepted/首帧 progress 仍会触发处理中；新 Desktop 连旧 server 保留 `msg.op` 回落，旧 server 的独立成功表情需升级 server 才能消除。
- 断线时终态表情在重连补发；受理的 👀 过期不补（迟到“正在做”更奇怪）。服务端按 `opId` 幂等，补发不会打出第二个。
- typing：官方 5 分钟上限 + 设备在线门控；个人 10 分钟。`msg.op typing` 当前未接线（缺口），服务端不要假设客户端会发。
- Slack 用 reactions API；Telegram 用 `setMessageReaction`；X 没有等价表情状态，最多对原帖点赞（当前不实现）。

## 10. 可靠性：重试、去重、幂等、顺序、离线补偿

### 10.1 幂等键

| 键 | 生成方 | 作用域 | 语义 |
|---|---|---|---|
| `requestId` | 普通任务由 server；续跑由 desktop（UUID v4） | 全局生命周期路由表 | 同一 requestId 重投只回放 ack/终态，不重跑 agent |
| `opId` | desktop | 全局 | `msg.op` 幂等；同一 opId + 同内容重发返回首次结果；同 opId 不同内容回 `IDEMPOTENCY_CONFLICT` |
| `queryId` | server | 单次 query | 配对 query.response；server 自行超时 |
| `interactionId` | desktop | 单次交互 | 配对 decision/cancel；卡片收口后作废 |
| `pairingId` / 事件 id | server | 渠道事件 | Slack event_id、Telegram update_id、X mention id 去重，防重放产生第二个 dispatch |
| `bindingId` / `attemptId` | server | 绑定 | 绑定代际；旧 binding 的迟到回执丢弃 |
- `msg.op.result.errorCode` 稳定集合：`PROGRESS_UNAVAILABLE`、`UNSUPPORTED_PARAMETERS`、`MESSAGE_NOT_OWNED`、`BINDING_REVOKED`、`RATE_LIMITED`、`IDEMPOTENCY_CONFLICT`、`CAPACITY_REACHED`、`OUTCOME_UNKNOWN`、`PERSIST_FAILED`、`TURN_UNAVAILABLE`；客户端只按码分支，未知码按明确失败。
### 10.2 重投与补发矩阵

| 帧 | 丢失后果 | 恢复策略 |
|---|---|---|
| `task.dispatch` 未 ack | 任务不执行 | server 重投同一 requestId；desktop 回放上次 ack；若已有终态则回放终态（超过 24h 只回放 ack） |
| `task.ack` 丢失 | server 不知道已受理 | server 重投 dispatch；desktop ackHistory/终稿账本兜底 |
| 已 ack 为 queued 后 desktop 重启 | 队列丢失 | server 靠 lease 超时收口（建议 10 分钟），回一条“任务已过期/请重发” |
| `turn.end` 丢失 | 渠道永远等不到结果 | desktop 持久出箱（24h 内）重发；协商 `turn-delivery-v1` 时等 server accepted 才停 |
| `turn.progress` 丢失 | 进度不更新 | latest-wins，丢帧无害；不补发 |
| `turn.reopen` 丢失 | 续跑不回流 | 无 ack；desktop 只在 server 宣告能力且首事件到达后发；失败则渠道停在原终态 |
| `msg.op` 回执丢失 | 不知道是否落地 | 同 opId 同内容原样重发对账；`OUTCOME_UNKNOWN` 不得换 opId |
| `group.message` 丢失 | 群上下文缺口 | fire-and-forget，不补；桌面离线即丢 |
| `interaction.decision` 丢失 | 卡片挂着 | 用户可再点；服务端卡片保留按钮；超时后 desktop 发 `interaction.cancel` 收口 |
| `bind.update` 丢失 | 绑定 UI 停在 pending | 重连时服务端推 `bind.state`/`bind.update(none/pending)` 现状；desktop 以快照为准 |

### 10.3 顺序与串行化

- **同 externalKey 串行**：desktop `serializeByKey(connectionId + externalKey)` 把“定位+入队”作为原子段；服务端对同一 lane 的 dispatch 也应串行，避免两条消息同时建任务。
- **同 session 串行受理**：desktop `serializeSessionAdmission(sessionId)`；服务端不直接控制 session，但应按 `externalKey` 保序。
- **同目标消息串行**：`edit/delete/react` 的 `messageId` 必须先核验属于该 lane（本 lane 先前 `send` 的产物或该 lane 的入站消息），否则就是可猜 id 的后门；再按 `(botId, chatId, messageId)` 串行，只按 opId 合并不能阻止较老操作延迟完成后覆盖新状态。
- **同 bot token 全局限速**：`msg.op` 的中心化限速队列，尊重 Telegram `retry_after`；跨租户共享预算，不能下放客户端。
- **SLA 顺序**：同一条消息的进度/终稿必须按“进度 → 终稿”到达；终稿栅栏后丢弃迟到 progress。续跑轮 requestId 的 progress/end 必须排在 reopen 之后。

### 10.4 时效与放弃线

- desktop 终稿投递时效 `HOOK_TERMINAL_DELIVERY_TTL_MS = 24h`：超过该线的终稿一律不再发，**包括 server 显式重投**；只回放 ack。服务端 `OUTBOX_MAX_ATTEMPTS × OUTBOX_MAX_DELAY_MS ≈ 24h` 取同一个数。
- 服务端出箱必须有界：最大尝试次数/最大延迟/最大条目；过期条目丢弃并审计，不能无限重试。
- 交互卡 30 分钟超时；`msg.op.result` 等待 30 秒；连接握手 15 秒；心跳/空闲见 3.4。
- 群消息中继不设 TTL（desktop 本地窗口按容量回收）；服务端只保留成员元数据，按退群/解绑清理。

### 10.5 失败方向

- 未知 `requestId` 的 progress/end 一律静默忽略，不新建消息、不报错。
- 未知 `opId` 的 `msg.op.result` 丢弃；迟到 decision 不覆盖最终正文。
- 未绑定/绑定失效：停止新出站，保留旧 binding 仅用于幂等 ACK/清理；不得把答案写给新主人。
- 换账号/解绑/退群/账号退出：该轮出站 fail-closed；旧过程消息可留在原聊天，新终稿不能被旧帧覆盖。
- 服务端收到不属于自己 provider 的帧（Slack 帧走 Telegram lane 等）立即丢弃并记 warn，不转投。

## 11. 数据模型建议

> 目标：PostgreSQL 主库 + Redis（连接注册/限速/短期幂等）+ 对象存储（附件）。所有表按 `provider` 分区或至少带 provider 列；群聊正文不落库。

| 表 | 关键列 | 约束/索引 | 说明 |
|---|---|---|---|
| `channel_binding` | `id(binding_id), provider, scope_id, scope_name, principal_id, principal_name, device_id, account_id, status, attempt_id, generation, credential_ref, created_at, confirmed_at, revoked_at, metadata` | 唯一 `(provider, binding_id)`；部分唯一 `(provider, scope_id, principal_id, device_id) WHERE status='confirmed'`；索引 `(account_id, provider, device_id)` | Slack team/Telegram bot/X account 的绑定；凭证只存引用，不存明文 |
| `hook_connection` | `id, account_id, device_id, provider, status, protocol_version, features jsonb, workspaces jsonb, default_workspace, lifecycle_announcement, connected_at, last_seen_at, close_code, client_version` | 唯一 `(account_id, provider, device_id) WHERE status='online'`；索引 `last_seen_at` | 连接注册表；Redis 可做热态，PG 做审计 |
| `hook_lane` | `id, provider, external_key, binding_id, principal_id, chat_id, thread_id, lane_kind(dm/group/topic/post), bot_id, generation, default_workspace, reply_anchor_message_id, state, last_inbound_at` | 唯一 `(external_key)`；索引 `(provider, chat_id)`、`(binding_id, state)` | externalKey 不透明；`generation` 对应 `g<n>` |
| `hook_session` | `id, provider, external_key, device_id, session_id, generation, state(active/archived/replaced), last_used_at` | 唯一 `(provider, external_key, device_id) WHERE state='active'`；索引 `(device_id, session_id)` | externalKey→desktop 任务的绑定；server 侧可丢映射，desktop 每次 dispatch 仍会按工作目录现场复核 |
| `hook_request` | `request_id pk, lane_id, device_id, session_id, workspace, status, ack_result, ack_payload jsonb, queue_position, prompt_hash, source jsonb, lease_expires_at, attempt_count, created_at, accepted_at, terminal_at, terminal_status, error_code, reopen_of` | 唯一 `request_id`；索引 `(lane_id, created_at)`、`(device_id, status)`、`(lease_expires_at)` | 一轮任务的生命周期；`source` 保存来源快照 |
| `hook_message` | `id, lane_id, request_id, purpose(trigger/progress/final/interaction/notice), role(user/bot), channel_message_id, channel_message_ids text[], parent_message_id, thread_id, state, content_hash, op_id, created_at, updated_at, deleted_at` | 唯一 `(lane_id, channel_message_id)`（平台消息 id 在 lane 内唯一）；索引 `(request_id, purpose)` | 渠道消息映射；不存完整群正文 |
| `hook_msg_op` | `op_id pk, request_id, lane_id, purpose, action_kind, content_hash, status, channel_message_id, error_code, attempts, next_retry_at, expires_at, created_at, completed_at` | 唯一 `op_id`；索引 `(status, next_retry_at)`、`(request_id)` | msg.op 幂等与重试；内容指纹防换正文 |
| `pending_confirmation` | `interaction_id pk, request_id, lane_id, kind, title, body_hash, buttons jsonb, status, decision_button_id, decision_by(desktop/mobile/im), decided_at, expires_at, message_id, source_message_id, source_chat, source_link, source_excerpt, created_at` | 唯一 `(request_id, interaction_id)`；索引 `(status, expires_at)` | 权限/提问/计划卡；来源快照随卡保留 |
| `turn_delivery` | `request_id pk, state, attempt, retry_at, error_code, error_message, retryable, updated_at` | 索引 `(state, retry_at)` | 交付回执；有界 TTL |
| `reopen_route` | `request_id pk, reopen_of, lane_id, message_id, connection_id, generation, state(active/orphaned/closed), expires_at` | 索引 `(reopen_of)`、`(connection_id, state)` | 续跑锚点；断连收口按 connection+generation 校验 |
| `workspace_prefs` | `id, provider, binding_id, scope_id, team_id nullable, workspace_alias, model, effort, agent_kind, permission_mode, updated_at` | 唯一 `(provider, binding_id, scope_id, workspace_alias)`；索引 `(binding_id)` | Slack 与 provider-neutral 可分开两表；team_id 仅 Slack |
| `telegram_behavior` | `binding_id pk, emoji_reactions, reply_quote_dm, reply_quote_group, updated_at` | — | 只存 override；有效值 = override 或默认 |
| `telegram_group_activation` | `binding_id, chat_id, value('always'), updated_at` | 主键 `(binding_id, chat_id)` | 只存偏离默认的群 |
| `channel_membership` | `provider, chat_id, principal_id, binding_id, first_seen_at, last_seen_at, left_at` | 唯一 `(provider, chat_id, principal_id, binding_id)`；索引 `(provider, chat_id)` | 仅路由元数据，无内容 |
| `inbound_event` | `provider, event_id, received_at, result, expires_at` | 唯一 `(provider, event_id)` | 事件去重，TTL 7–30 天 |
| `delivery_outbox` | `id, kind(turn_end/msg_op/interaction/notice), ref_id, lane_id, payload jsonb, attempts, next_attempt_at, expires_at, state` | 索引 `(state, next_attempt_at)`；唯一 `(kind, ref_id)` | 统一出箱；payload 不含凭证；附件不入 JSON |

关键约束：

- `request_id`、`op_id`、`interaction_id` 全局唯一；跨 provider 也不得复用（同一张路由表）。
- `external_key` 唯一且不可变；`/new` 生成新 key 并递增 generation，旧 key 标记 retired，不删除（保留历史与诊断）。
- 群聊正文不落 `hook_message`/`channel_membership`；只存消息 id、文件名、时间等路由元数据。附件字节进对象存储，按任务/租户隔离，随任务删除清理。
- 所有敏感字段（Slack user token、Telegram bot token、X refresh token）加密存储，密钥走 KMS；日志只打引用/指纹。
- 软删除 + 审计：绑定解除、消息删除、卡片收口都保留 `revoked_at/deleted_at/closed_at`，便于排障。

## 12. Go 实现映射建议

### 12.1 进程与包

```text
cmd/hook-server/                 # 单二进制，-provider=slack|telegram|x 或三 lane 同进程
internal/hookproto/              # 信封、42 种 payload、校验器、构造器、golden fixtures
internal/hookgw/                 # WSS hub：upgrade、鉴权、connection actor、读写泵
internal/channel/                # Adapter 接口 + slack/telegram/x 实现
internal/lane/                   # externalKey、绑定、workspace、命令解析、dispatch 编排
internal/turn/                   # request 生命周期、队列/lease、reopen、delivery
internal/interaction/            # 卡片渲染、callback token、共享权限桥
internal/outbox/                 # 持久出箱 + worker + 限速
internal/prefs/                  # workspace/provider prefs、behavior
internal/store/                  # pgx/sqlc、迁移、事务
internal/ratelimit/              # 每 bot token / 每 chat / 渠道 429
internal/security/               # token vault、URL allowlist、OIDC/PKCE、webhook 验签
internal/obs/                    # 结构化日志、metrics、trace、脱敏
```

### 12.2 核心接口（职责，不写实现）

- **Adapter**：`Start/Stop`、`Inbound() <-chan Event`、`Send/Edit/Delete/React/Typing/Media`、`SetCommands`、`ResolvePrincipal`、`ValidateConnectURL`。每个 provider 一个实现；不允许在 handler 里写 `if provider == ...` 的业务分支。
- **Connection actor**：每个 `(account, provider, device)` 一个 goroutine 串行读写；读泵解析/校验/分发，写泵带缓冲与背压；连接关闭时向 hub 发 `Disconnected{connectionId, generation}`。
- **Lane service**：负责 `externalKey` 生成/解析、绑定查询、命令解析、`task.dispatch` 构造、ack 等待。同一 lane 用 keyed lock 串行。
- **Turn service**：维护 `request` 状态机、lease、queue 等待、`turn.end` 出箱、`turn.delivery` 回执、`turn.reopen` 锚点与孤儿收口。
- **Interaction service**：卡片渲染、`callback_data` 签名（requestId/interactionId/buttonId + 过期时间 + HMAC）、点击校验、多端共享权限桥、迟到点击丢弃。
- **Outbox worker**：按 `next_attempt_at` 拉取，调用 adapter 发布；成功标 delivered，失败按指数退避；超过 24h 或最大尝试次数标 dead；所有发布以 `request_id/op_id` 幂等。
- **Rate limiter**：每 bot token 一个令牌桶/滑动窗口；渠道 429 时按 `retry_after` 暂停该 token 的所有发送；`msg.op` 与 legacy 发送共用预算。

### 12.3 并发与一致性

- 连接读写分离：读泵不阻塞写；写队列满时优先丢 progress/typing，保留 ack/turn.end/interaction；丢帧要记 metric，不能静默。
- lane 级串行：同一 `external_key` 的 dispatch 用分布式锁（Redis SET NX + TTL）或单实例 keyed mutex；跨实例时按 `external_key` 哈希到同一 worker。
- 设备路由：`device_id → pod` 注册在 Redis；dispatch 时查在线 pod，若设备离线则进入“待重投”而不是直接失败。WSS 连接必须落在单 pod，不能跨 pod 共享 socket。
- 事务边界：`hook_request` 创建 + outbox 入队 + lane 绑定更新在同一事务；消息发送在事务外，成功后再写 `hook_message`/`delivery`，失败可重试。
- 幂等落库：`INSERT ... ON CONFLICT DO NOTHING` + 读回；`op_id` 冲突时比较 `content_hash`，不同则返回 `IDEMPOTENCY_CONFLICT`。
- 顺序：同 lane 的 outbox 条目按 `created_at, id` 排序；同一目标消息的 edit/delete/react 用 `(channel, chat_id, message_id)` 锁串行。

### 12.4 与 desktop 的契约测试

- 用 TS 协议包的 `parse.ts` 生成 golden 帧（42 种 + 边界坏帧），Go 校验器逐条对齐：字段名、枚举、联动、上限、错误路径。
- 双向 fuzz：Go 构造器 → TS parse；TS 构造器 → Go parse。任何一侧拒绝都视为契约失败。
- 集成测试：假 desktop 连 WSS，覆盖 hello/welcome、dispatch/ack、queue、cancel、turn.end、turn.delivery、turn.reopen、msg.op、interaction、group.message、provider.commands.set。
- 故障注入：断连/重连、重复 dispatch、ack 丢失、终稿丢失、429、callback 迟到、绑定被顶、滚动发布能力降级。
- 负载：单连接 1000 QPS 小帧；48 MiB 大帧；同 bot token 多租户限速；群中继扇出 1000 设备。

### 12.5 部署与运维

- 三个 lane 可同二进制不同 deployment，也可同进程按 provider 隔离；共享 PG/Redis，但连接注册表按 provider 分区。
- 滚动发布：先升服务端、再升 desktop；服务端必须能对旧 desktop 只发旧帧；新 desktop 落到旧节点时自动降级。回滚同理。
- 观测：每帧打 `provider, connection_id, device_id, external_key, request_id, op_id, type, direction, result`；不要把用户正文/附件内容打进日志；敏感字段脱敏。
- 告警：连接掉线率、ack 超时率、outbox 积压、渠道 429、终稿失败率、交互超时率、群中继扇出失败率、绑定失败率。
- 配额：每设备连接数、每账号 lane 数、每 bot token 发送速率、每群中继扇出上限、每帧大小。

## 13. 未确认项
以下项在客户端仓无法闭环，落地 Go 服务端前必须对照服务端代码/线上行为核验；未核验前按“保守实现 + 可降级”处理。

| # | 未确认项 | 影响 | 建议核验方式 |
|---|---|---|---|
| 1 | Slack 入站用 Events API 还是 Socket Mode；webhook 路径与 signing secret | 决定 ingress 架构与验签 | 查服务端路由/部署配置 |
| 2 | Slack App 的 bot/user scopes 与 token 轮换策略 | 决定 `tool.request` 能力边界 | 查 Slack App manifest 与 token vault |
| 3 | Slack 进度消息由服务端编辑还是只发终稿；编辑间隔 | 影响 `turn.progress` 渲染与限速 | 查服务端 Slack controller 与线上消息 |
| 4 | Telegram 官方 bot 用 long polling 还是 webhook；`allowed_updates` 集合 | 决定 update 去重与 `my_chat_member` 行为 | 查服务端 bot 启动配置 |
| 5 | Telegram 群是否开启 privacy mode；官方 bot 在群里的可见消息范围 | 影响群触发与 group relay 的输入 | 用测试群/BotFather 核验 |
| 6 | `TELEGRAM_DM_DRAFT_ENABLED` 是否在生产开启；草稿 API 可用性 | 决定进度载体与 `PROGRESS_UNAVAILABLE` 分支 | 查服务端开关 |
| 7 | 官方 Telegram 是否对 `turn.delivery` 发送回执 | 客户端只对 X 消费；若服务端发 Telegram 会被丢弃 | 抓包/看服务端 provider 分支 |
| 8 | 服务端 outbox 的精确尝试次数/最大延迟（约 24h 是客户端推断） | 影响终稿放弃线与重投策略 | 查服务端 `OUTBOX_MAX_*` 常量 |
| 9 | 服务端是否实现 `session-new-v1` 与 `session-result-v1` | 未实现时 `/new` 懒创建、后台结果不回传 | 查服务端 query/feature 分支 |
| 10 | 服务端是否实现 `provider.commands.set` | 未实现时菜单回落到服务端默认，客户端新命令可能不可见 | 抓 `welcome.features` 与 `setMyCommands` |
| 11 | `/new` 的 generation 递增规则与旧 externalKey 回收策略 | 决定 lane/绑定表设计 | 查服务端 externalKey 生成器 |
| 12 | `/session` 选中任务后是否复用原 externalKey 还是新 generation | 影响绑定与历史消息归属 | 抓包/查服务端命令实现 |
| 13 | 官方 Telegram 群/topic 卡的 owner 私聊改投规则与来源文案 | 影响权限卡可用性 | 查服务端 controller |
| 14 | X 的提及来源是 filtered stream 还是轮询；公开回复是否只允许一条 | 决定 X 的吞吐与“单条公开消息”约束 | 查 X API 套餐与服务端实现 |
| 15 | X 是否支持媒体上传/多图；上限 | 决定附件降级 | 查 X API 文档与服务端实现 |
| 16 | Slack 是否实现 `slack-communications` 与 `bind.state.communications` | 客户端只在宣告后消费；未实现时该 UI 不出现 | 查 `welcome.features` 与绑定接口 |
| 17 | 交互卡 `/model` 等卡片在 X 上如何降级（纯文本编号回复？） | X 无按钮，命令面可能不可用 | 查服务端 X 渲染器 |
| 18 | 群消息中继的成员元数据保留期限与退群清理 | 影响 `channel_membership` TTL | 查服务端存储与清理任务 |
| 19 | 服务端对 `msg.op` 的 content fingerprint 具体算法 | 影响幂等冲突判定 | 查服务端执行器 |
| 20 | 服务端是否对 Slack 发送 `turn.delivery` | 若发送会被客户端丢弃，不影响功能但浪费帧 | 抓包/查 provider 分支 |

## 14. 落地顺序建议（阶段 1 → 阶段 2）

1. **P0 骨架**：`hookproto` 42 种消息 + 校验器；WSS hub；鉴权/hello/welcome/心跳/重连；连接注册表；`task.dispatch/ack`；Slack OIDC 绑定；Telegram 深链绑定；X OAuth2 绑定。
2. **P0 任务闭环**：lane/externalKey；找/建/接管；队列 20；`task.cancel`；`turn.end` 出箱；Slack/Telegram/X 基础投递；交互卡与共享权限；命令分发（`/new /help /stop /session /model /effort /agent /permission /project /status /unlink`）。
3. **P1 体验与可靠性**：`turn.progress`；`turn.reopen`；`turn.delivery`（X）；`msg.op`（Telegram 进度/终稿/卡片）；`group.message`；附件/表情/typing；`session-new`；后台结果；multi-team；provider prefs/behavior；命令菜单。
4. **P2 增强**：Slack 网关工具；`slack-communications`；生命周期通知；群历史检索元数据；限速/审计/配额完善；压测与混沌。

每阶段都必须保持“旧 desktop 可用、旧 server 可降级”的滚动发布能力；能力协商是唯一开关，不能用部署顺序假设客户端版本。
