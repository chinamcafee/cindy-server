# Cindy 服务端逆向重建 · 03 Chat Server / 云伙伴 / 群聊 / 实时同步

> 文档编号：RB-03　｜　阶段：阶段 1（合同重建）　｜　版本：v1.0　｜　日期：2026-10-19
>
> 目标：把客户端已实现的 Chat Server 合同还原为可直接指导 Go 服务端实现的规格。覆盖账号 actor、群房间、消息与 Thread、执行租约、分工 plan、媒体、邀请、个人列表、云伙伴资料快照与 1:1 房间、WSS 变更流。
>
> 证据根目录：`/Users/changzechuan/InnovationProjects/CindyProjects/cindy`
>
> 重要边界：Cindy 的“聊天”由两套独立后端承载。**Chat Server**（`chatApiBaseUrl`，HTTP `/v1/*` + WSS `/v1/ws`）负责账号级 IM、群房间、执行租约和伙伴资料键值库；**device-link / shared-task / task-copy** 负责设备在线状态、同账号远控、跨账号共享任务和跨电脑任务复制。云伙伴用 Chat Server 存“对话层 + 资料快照”，用 device-link 决定“哪台电脑是主力、谁接管、谁交出”。本文只定义 Chat Server；device-link 仅在边界处点名。

## 0. 元信息与证据等级
| 项 | 内容 |
|---|---|
| 文档定位 | 服务端重建合同，不是客户端实现说明；服务端实现以本文的“必须”语义为准 |
| 读者 | Go 服务端研发、协议联调、测试与运维 |
| 证据基线 | 客户端仓当前工作区（2026-10-19 读取）；服务端代码仅作旁证 |
| 覆盖范围 | `/v1/*` HTTP、`/v1/ws` WSS、PostgreSQL 数据模型、执行调度与权限 |
| 不覆盖 | device-link 隧道与 relay、shared-task REST、task-copy、对象存储内部实现、账号认证服务内部实现 |
| 交付形态 | 单文件 Markdown；不含可执行代码；字段名即 wire 合同 |

证据等级：【源码】客户端实际发出的请求/解析逻辑，视为硬合同；【文档】仓内产品/开发规则明确描述的服务端行为，视为权威语义；【推测】客户端未直接证明、由字段或状态机反推，实现时保留兼容余量并列入第 11 节确认。

### 0.1 源文件索引
> 路径均相对证据根目录；服务端实现时可直接回查客户端行为。

| 区域 | 文件 | 作用 |
|---|---|---|
| Mobile | `apps/mobile/src/chat/chatServerClient.ts` | HTTP 合同、房间/消息/成员/执行失败/伙伴 actor/1:1 房/发送/媒体 |
| Mobile | `apps/mobile/src/chat/chatServerSubscription.ts` | WSS 建连、auth、订阅、ack、`RESET_REQUIRED`、退避 |
| Mobile | `apps/mobile/src/chat/useChatServer.ts` | 客户端工厂（owner/realm 栅栏）、列表轮询+订阅、群页读写 |
| Mobile | `apps/mobile/src/chat/cloudCompanion.ts` | 云端伙伴行去重、候选电脑、切换状态机、消息排序 |
| Mobile | `apps/mobile/src/chat/useCloudCompanion.ts` | 能力/actors/home 读取、`hasProfileSnapshot` 后才能 `setHome` |
| Mobile | `apps/mobile/src/chat/useCloudCompanionRoom.ts` | 1:1 云端会话只读分页 |
| Desktop | `apps/desktop/src/main/maker-ipc/chatServer.ts` | 生产适配器：HTTP/WS、注册 actor、群/消息/计划/执行/邀请/媒体/Thread/贴回 |
| Desktop | `apps/desktop/src/main/maker-ipc/chatServerMedia.ts` | 媒体 prepare→PUT→complete / 下载缓存 |
| Desktop | `apps/desktop/src/main/maker-ipc/chatServerThreads.ts` | Thread 预览与贴回源解析、缓存/重试 |
| Desktop | `apps/desktop/src/main/maker-ipc/chatServerMigration.ts` | 旧本地群→服务器群迁移、导入水位 |
| Desktop | `apps/desktop/src/main/maker-ipc/chatServerErrors.ts` | 错误码投影（对外稳定类别） |
| Desktop | `apps/desktop/src/main/maker-ipc/botGroupChatService.ts` | 群专线/分工 Session、隐藏输入、终态、超时、附件预算 |
| Desktop | `apps/desktop/src/main/maker-ipc/botGroupToolAuthorization.ts` | 群工具授权边界（活执行+心跳） |
| Desktop | `apps/desktop/src/main/companion-cloud/runtime.ts` | 同步循环、home 注册/接管/交出/切换/合并/停用 |
| Desktop | `apps/desktop/src/main/companion-cloud/{mirror,materialize,uploader,homes,directRoom,merge}.ts` | 镜像、接管、资料上传、主力电脑、1:1 投递、合并 |
| Shared | `packages/maker-shared/src/botGroupChat.ts` | 群聊 wire 合同、视图、错误码、路由 key、`ChatServerApi` |
| Shared | `packages/maker-shared/src/chatConversationList.ts` | 个人列表态同步/CAS |
| Shared | `packages/maker-shared/src/{botGroupPresentation,botGroupQuote,botGroupTask}.ts` | 失败投影/批量 100、引用卡、任务卡 |
| Shared | `packages/maker-shared/src/clientEndpointSchema.ts` | `chatApiBaseUrl` 与协议白名单 |
| Docs | `docs/product-rules/cloud-companions.md` | 云伙伴权威规则（home/离线/切换/镜像/兼容） |
| Docs | `docs/product-rules/bot-group-chat.md` | 群聊权威规则（编排/额度/分工/手机/跨账号/邀请/Thread/贴回） |
| Docs | `docs/dev-rules/chat-server-feature-parity.md` | 服务端能力对照（字段/能力位/迁移） |
| Docs | `docs/dev-rules/chat-server-local-test.md` | 端点/本地联调/数据与执行边界 |
| Docs | `docs/product-rules/shared-task-mode.md` | 共享任务/访客/跨账号授权（非 Chat Server） |

## 1. 能力总览
> `CHAT-NN` 为稳定能力编号，后续拆任务、排期与验收均引用。优先级：P0 = 无此能力客户端不可用；P1 = 主流程可用但群管理/云伙伴不完整；P2 = 体验增强。

| 编号 | 能力 | Desktop | Mobile | 优先级 | 关键合同 |
|---|---|---|---|---|---|
| CHAT-01 | 认证、`/me` 与能力协商 | 是 | 是 | P0 | Bearer、`capabilities`、`deviceId` |
| CHAT-02 | 账号 actor 目录与资料刷新 | 是 | 是 | P1 | `GET /actors`、`profile/refresh` |
| CHAT-03 | Bot actor 注册与资料发布 | 是 | 否 | P0 | `externalId`、`homeDeviceId`、`avatarSource` |
| CHAT-04 | 主力电脑绑定/停用（CAS） | 是 | 间接 | P0 | `expectedHomeDeviceId`、`HOME_DEVICE_CONFLICT` |
| CHAT-05 | 伙伴云端资料键值库 | 是 | 只读 | P0 | `preferences`、`expectedRevision`、墓碑 |
| CHAT-06 | 会话列表分页 | 是 | 是 | P0 | `after=<roomId>`、`state=joined` |
| CHAT-07 | 建群/建 1:1 房（确定性房间） | 是 | 建群否 | P0 | `operationId`、`sourceId`、`kind` |
| CHAT-08 | 群设置/归档/解散 | 是 | 经电脑 | P1 | `revision`、`groupLifecycleVersion` |
| CHAT-09 | 房间快照 | 是 | 是 | P0 | `room/members/messages/cursor/reads` |
| CHAT-10 | 成员读取与管理 | 是 | 读/经电脑 | P0 | 角色、`guestAccess`、`accessRevision` |
| CHAT-11 | 消息分页/Thread/单条 | 是 | 是 | P0 | `before=<seq>`、`threadRootId`、`all` |
| CHAT-12 | 发消息/@/引用/卡片/附件块 | 是 | 文字与 @ | P0 | `operationId`、`mentions`、`mentionsAll` |
| CHAT-13 | 表情回应 | 是 | 否 | P2 | 幂等、回应者分页 |
| CHAT-14 | Thread 贴回主群 | 是 | 否 | P2 | `cindy.thread-share`、固定幂等键 |
| CHAT-15 | 继续讨论/取消分工判断 | 是 | 否 | P1 | `discuss:<sourceId>`、`cancel-plan:` |
| CHAT-16 | 执行领取/心跳/终态租约 | 是 | 否 | P0 | `epoch`、15 秒心跳、终态幂等 |
| CHAT-17 | 执行停止/列表/失败状态 | 是 | 读失败 | P1 | `execution-failures`、白名单错误 |
| CHAT-18 | 分工 plan（创建/动作/评论） | 是 | 经电脑 | P1 | `plan:<sourceId>`、`expectedRevision` |
| CHAT-19 | 媒体 prepare/upload/complete/fetch | 是 | 读 | P0 | 签名 URL、100MB、20 附件 |
| CHAT-20 | 邀请链接创建/撤销/预览/接受 | 是 | 接受否 | P1 | 43 位 token、长期/单次语义 |
| CHAT-21 | 个人列表状态 | 是 | 是 | P1 | `hidden`、`revision` |
| CHAT-22 | WSS 订阅/重放/ack/reset | 是 | 是 | P0 | `after`、`changes`、`RESET_REQUIRED` |
| CHAT-23 | 变更头 `/changes` | 否 | 是 | P1 | `scope`、`head` |
| CHAT-24 | 入群系统事件 | 是 | 读 | P2 | `cindy.membership`、`member.joined` |
| CHAT-25 | 1:1 房间镜像与离线投递 | 是 | 只读 | P1 | 镜像去重、`directRoomVersion` |
| CHAT-26 | 云伙伴接管/交出/合并支撑 | 是 | 发起接管 | P0 | CAS + 偏好快照 + 房间历史 |
| CHAT-27 | 健康检查与可观测 | 运维 | 运维 | P0 | `GET /ready`、结构化日志 |

## 2. 传输与鉴权
### 2.1 基址、路径与 TLS
| 项 | 合同 | 证据 |
|---|---|---|
| 基址来源 | 区域端点清单的 `chatApiBaseUrl`；正式包必须 `https:`；客户端不硬编码生产地址 | 【源码】`clientEndpointSchema.ts:77,155` |
| HTTP 前缀 | `${baseUrl}/v1${route}`；路由不带尾斜杠 | 【源码】`chatServer.ts:464-468` |
| WSS | 同基址派生 `/v1/ws`，协议 `wss:`；没有 SSE/EventSource | 【源码】`chatServer.ts:807-811` |
| 测试例外 | 仅显式隔离测试允许 `http://127.0.0.1:3018`；正式包只允许 HTTPS | 【源码】`chatServer.ts:230-238` |
| 重定向 | 客户端不跟随 3xx，报 `CHAT_REDIRECT_REFUSED` | 【源码】`chatServer.ts:487` |
| 响应上限 | 8 MiB；超过即视为不可信响应 | 【源码】`chatServer.ts:480` |
| 请求超时 | 15 秒；服务端必须在 15 秒内返回，禁止长轮询 | 【源码】`chatServer.ts:476,510` |

### 2.2 认证、设备与代演身份
```
Authorization: Bearer <access token>
X-Chat-Actor: <actorId>        # 可选；Bot 发言/管理动作时使用
```

- access token 由账号认证服务签发（RS256/JWKS），服务端必须校验签名、`iss`、`exp`、受众与账号状态。国内外/个人与企业账号可互邀，身份必须包含 **issuer**，防止不同认证域编号碰撞。【文档】`chat-server-local-test.md:34`
- `/me` 返回 `deviceId`（device-link 的设备标识）。服务端以 token 中的设备身份作为“当前设备”，不信任请求体自报的设备 ID。
- `X-Chat-Actor` 只允许两类：① token 账号拥有的 `bot` actor；② 当前房间内 `owner/admin` 成员 actor。服务端必须验证归属与角色，不能只看 actor 是否存在。【源码】`chatServer.ts:474-475`；`chatServerErrors.ts:29`
- 作者身份永远由 token + `X-Chat-Actor` 决定。客户端从不在请求体里自报作者；服务端也不接受 `authorId` 字段。【文档】`bot-group-chat.md:418,430`
- 账号切换、换区、换端点即销毁旧请求、旧 socket、旧执行；服务端以 token 的 issuer/账号/realm 为准。

### 2.3 401、429 与错误信封
| 场景 | 客户端行为 | 服务端合同 |
|---|---|---|
| 401 `TOKEN_EXPIRED` / `INVALID_TOKEN` / `AUTH_REQUIRED` | 仅续期一次；并发共用一次刷新；60 秒内不重复刷新 | 返回标准 401；不得用 200 包裹错误 |
| 429 | 读取 `Retry-After`，至少等待该时长 | 必须带 `Retry-After` 秒数 |
| 5xx/网络 | 退避重试；群发言失败不自动重发 | 5xx 可重试；其余 4xx 不要求重试 |
| 3xx | 拒绝跟随 | 不应返回 3xx；媒体签名 URL 除外 |

错误信封固定为 `{"error":{"code":"REVISION_CONFLICT","message":"...","details":{"revision":12}}}`。`code` 是稳定合同；`message` 仅用于日志/兜底；`details` 在冲突类必须带当前值（`revision`、`homeDeviceId`、`head`）。【源码】`chatServer.ts:492-505`

### 2.4 幂等、修订与请求约束
| 机制 | 合同 |
|---|---|
| `operationId` | 所有写请求必须携带；按“账号 + operationId”去重。同键同参数返回首次结果（含资源 ID）；同键不同参数返回 `409 OPERATION_CONFLICT` |
| 幂等窗口 | 建议至少 24 小时；客户端进程重启后仍依赖服务端幂等，不依赖本地 outbox【文档】`chat-server-local-test.md:140-142` |
| 修订 CAS | `expectedRevision` 必须匹配当前 `revision`；失败返回 `409 REVISION_CONFLICT`，`details.revision` 给当前值 |
| 主力电脑 CAS | `expectedHomeDeviceId` 必须匹配当前 `home_device_id`（`null` 表示只抢空位）；失败返回 `409 HOME_DEVICE_CONFLICT`，`details.homeDeviceId` 给当前值 |
| 心跳例外 | 执行心跳每次使用新 `operationId`；服务端不得把心跳当普通写请求做幂等短路，必须每次续租 |
| 终态幂等 | `complete/fail` 使用 `chat:<execId>:<epoch>:<action>`；同键重复返回同一结果，不能重复写消息 |
| 请求体上限 | 建议 256 KiB；单条 content 建议 64 KiB；单条正文 8000 字由客户端切分，服务端仍需校验【文档】`bot-group-chat.md:424,645-646` |
| 校验顺序 | 认证 → 账号/actor 归属 → 房间成员与状态 → 参数 → 幂等 → CAS → 写库 |

### 2.5 错误码与 HTTP 状态映射
> 客户端显式识别的码；`INVALID_CHAT_*` 是客户端归一化码，不是服务端原始码。

| HTTP | 服务端码 | 触发 |
|---|---|---|
| 400 | `INVALID_INPUT` / `INVALID_PARAMS` | 字段缺失/类型错误/未知字段（旧服务端拒绝新字段也用此码） |
| 400 | `MENTION_UNAVAILABLE` / `QUOTE_UNAVAILABLE` / `REPLY_THREAD_MISMATCH` | 点名失效；引用不可见/跨群/跨 Thread；`replyToId` 跨 Thread |
| 400 | `INVALID_ATTACHMENT` / `ATTACHMENT_TOO_LARGE` / `ATTACHMENT_UNAVAILABLE` / `SHARE_UNAVAILABLE` | 附件参数/大小/读取失败；Thread 回复不可贴回 |
| 401 | `TOKEN_EXPIRED` / `INVALID_TOKEN` / `AUTH_REQUIRED` | token 过期、无效、缺少认证 |
| 403 | `OWNER_REQUIRED` / `ROLE_REQUIRED` / `ACTOR_NOT_OWNED` | 权限不足、代演 actor 非本人所有 |
| 404 | `CONVERSATION_NOT_FOUND` / `NOT_FOUND` / `MEDIA_NOT_FOUND` | 房间不存在、已退出、媒体不存在 |
| 409 | `REVISION_CONFLICT` / `OPERATION_CONFLICT` / `HOME_DEVICE_CONFLICT` | 修订 CAS、幂等键不同参数、主力电脑 CAS 失败 |
| 409 | `PLAN_OPEN` / `PLAN_CLOSED` / `LAST_OWNER` / `CONVERSATION_ARCHIVED` | 未结束计划、计划已结束、最后群主、归档群拒绝写 |
| 410 | `CONVERSATION_DISSOLVED` / `UPLOAD_EXPIRED` / `RESET_REQUIRED` | 解散群、媒体签名过期、订阅游标低于保留下界 |
| 429 | `RATE_LIMITED` | 限流；必须带 `Retry-After` |
| 500/503 | `INTERNAL` / `UNAVAILABLE` / `UPLOAD_QUOTA` | 未分类错误、依赖不可用、对象存储配额 |

客户端归一化码：`INVALID_CHAT_RESPONSE`、`INVALID_CHAT_PAGE`、`INVALID_CHAT_LIST_STATE`、`CHAT_REDIRECT_REFUSED`、`REQUEST_TIMEOUT`、`MEDIA_UPLOAD_FAILED`、`MEDIA_DOWNLOAD_FAILED`。【源码】`chatServer.ts:487-510`

## 3. HTTP 端点全表
> 所有路径省略 `${chatApiBaseUrl}/v1` 前缀；`/ready` 例外。请求/响应只列客户端实际消费的字段；服务端可追加字段但不得改变语义。写请求均需 `operationId`，表中不重复。

| 域 | 方法/路径 | 用途 | 请求字段 | 响应字段 | 分页/幂等 | 主要错误 |
|---|---|---|---|---|---|---|
| 身份 | `GET /me` | 当前人类 actor、设备、能力位 | — | `actor{id,kind}`、`deviceId?`、`capabilities{groupDiscussionParity?,companionHome?,directRoomClaim?,groupLifecycleVersion?,conversationListStateVersion?}` | — | 401；`INVALID_CHAT_IDENTITY`（actor 非 human） |
| 身份 | `POST /profile/refresh` | 刷新认证资料投影 | 空 | 200/204，响应忽略 | 客户端 60 秒节流 | 401 |
| 身份 | `GET /actors` | 账号可见 actor 目录 | — | `Actor[]`：`id,kind,externalId,name,avatar,avatarSource,homeDeviceId,disabled` | — | 401 |
| 身份 | `POST /actors` | 把本机 Bot Profile 登记为 actor | `kind='bot'`、`externalId`、`name` | `Actor` | 按 `operationId`；`unique(owner,externalId)` 返回既有 actor | 409 `OPERATION_CONFLICT`；400 |
| 身份 | `PATCH /actors/:actorId` | 绑定/交接/接管主力电脑；停用 | `homeDeviceId`、`expectedHomeDeviceId`；停用时另带 `disabled:true` | `{homeDeviceId}` | 天然 CAS；停用同样带 CAS | 409 `HOME_DEVICE_CONFLICT`（`details.homeDeviceId`）；403 |
| 身份 | `POST /actors/:actorId/profile` | 发布伙伴名字/头像 | `name`、`avatar`、`avatarSource` | `Actor` 或 `{name,avatar,avatarSource}` | 以 `avatarSource` 去重；仅主力电脑可写 | 403（非主力，具体码待确认）；400 |
| 资料 | `GET /preferences`（`X-Chat-Actor`=bot） | 读取该伙伴全部资料键 | — | `[{key,value,revision}]` | — | 401/403 |
| 资料 | `POST /preferences` | 写入/墓碑一个键 | `key`、`expectedRevision`、`value`（`null` 为墓碑） | `{revision}` | 按 `operationId` + CAS；值 ≤60KB、键 ≤160 字符 | 409 `REVISION_CONFLICT`（`details.revision`）；400 |
| 房间 | `GET /conversations` | 列出当前账号已加入的群 | `limit`（≤100，默认 100）、`after=<roomId>` | `Room[]`，行带 `state`、`head` | `after` 取上一页最后一个 `id`；短页结束 | 400；401 |
| 房间 | `POST /conversations` | 建群或建 1:1 伙伴房 | `kind:'group'/'direct'`、`name`、`participants[]`、`sourceId?`、`responseMode?`、`speakingMode?`、`history?` | `{id}` | `operationId`；direct 房按 `(owner,sourceId)` 唯一 | 409 `OPERATION_CONFLICT`；400；403 |
| 房间 | `PATCH /conversations/:roomId` | 群设置、负责人、归档 | `expectedRevision`、`name?`、`responseMode?`、`speakingMode?`、`organizerId?`、`archived?`、`topic?`、`description?` | `Room`（含新 `revision`） | CAS | 409 `REVISION_CONFLICT`；403；410 `CONVERSATION_DISSOLVED`；409 `CONVERSATION_ARCHIVED` |
| 房间 | `POST /conversations/:roomId/dissolve` | 解散群（不可撤销） | `expectedRevision` | `{revision,dissolvedAt,dissolvedBy}` | CAS；需 `groupLifecycleVersion=1` | 403 `OWNER_REQUIRED`；409 `REVISION_CONFLICT`；400 `LIFECYCLE_UNAVAILABLE` |
| 房间 | `GET /conversations/:roomId/snapshot` | 打开房间的一致读起点 | — | `{room,members[],messages[],cursor,reads?:[{thread_key,read_seq}]}` | `cursor` 为 1–30 位十进制字符串；`messages` 为最近 100 条（含所有 Thread 回复） | 403/404 |
| 房间 | `GET /conversations/:roomId/members` | 成员列表（含历史成员） | — | `Member[]` | — | 403/404 |
| 房间 | `POST /conversations/:roomId/members` | 邀请/退出/移除/封禁/解封/改角色 | `actorId`、`action:'invite'/'leave'/'remove'/'ban'/'unban'/'role'`、`role?` | `Member` 或 `{ok:true}` | `operationId` + 状态 CAS | 403 `OWNER_REQUIRED`/`ROLE_REQUIRED`；409 `LAST_OWNER`；400 `ACTOR_NOT_OWNED` |
| 房间 | `POST /conversations/:roomId/bot-access` | 伙伴主人设置本群授权档 | `actorId`、`access:'none'/'chat'/'tools'`、`expectedRevision` | `{accessRevision}` | CAS；仅该 bot 主人 | 403；409 `REVISION_CONFLICT` |
| 房间 | `POST /conversations/:roomId/nickname` | 改群昵称 | `actorId`、`nickname` | `Member` | `operationId`；本人或 bot 主人 | 403；400 |
| 房间 | `POST /conversations/:roomId/transfer` | 转让群主 | `actorId` | `{revision}` | 仅 owner；目标必须 joined | 403 `OWNER_REQUIRED`；400 |
| 房间 | `POST /conversations/:roomId/list-state` | 从我的列表隐藏/恢复 | `hidden`、`expectedRevision` | `{conversationId,hidden,revision}` | CAS；需 `conversationListStateVersion=1` | 409 `REVISION_CONFLICT`；400 `LIFECYCLE_UNAVAILABLE` |
| 房间 | `GET /conversation-list-state` | 拉取本人列表态 | `limit`（≤100）、`after=<conversationId>` | `[{conversationId,hidden,revision}]` | `after` 游标 | 400 |
| 消息 | `GET /conversations/:roomId/messages` | 主时间线/Thread 消息页 | `limit`（1–100，默认 100）、`before=<seq>`、`threadRootId?`、`all?` | `Message[]`，新→旧 | `before` 为十进制字符串；必须严格变旧 | 400；403/404 |
| 消息 | `POST /conversations/:roomId/messages` | 发消息/Thread 回复/贴回/任务卡 | `content[]`、`mentions[]`、`mentionsAll?`、`threadRootId?`、`replyToId?`、`deferExecution?`、`planningMode?`、`sourceId?`、`createdAt?` | `{id,seq?,createdAt?}` | 按 `operationId`；`clientId` 即幂等键 | 400 `MENTION_UNAVAILABLE`/`QUOTE_UNAVAILABLE`/`REPLY_THREAD_MISMATCH`/`ATTACHMENT_*`；409 `OPERATION_CONFLICT`；409 `CONVERSATION_ARCHIVED`；410 `CONVERSATION_DISSOLVED` |
| 消息 | `GET /conversations/:roomId/messages/:messageId` | 单条读取（引用/贴回源） | — | `Message` | — | 403/404/410 均视为不可见 |
| 消息 | `POST /conversations/:roomId/messages/:messageId/continue` | 继续讨论/自动判断后继续 | `operationId='discuss:<sourceId>'`、`automatic?` | `{roundId?}` 或 `{ok:true}` | 固定幂等键 | 409 `PLAN_OPEN`；400；403 |
| 消息 | `POST /conversations/:roomId/messages/:messageId/cancel-planning` | 取消服务端延迟兜底的分工判断 | `operationId='cancel-plan:<sourceId>:<failed>'`、`failed` | `{ok:true}` | 固定幂等键 | 400；403 |
| 消息 | `POST /conversations/:roomId/messages/:messageId/reactions` | 添加/取消表情 | `emoji`、`present` | `{ok:true}` | 按 `operationId`；同一 actor+emoji 唯一 | 400；403 |
| 消息 | `GET /conversations/:roomId/messages/:messageId/reactions` | 回应者列表 | `emoji`、`limit=100`、`after=<actorId>` | `[{actor_id}]` | `after` 为 actor UUID；短页结束 | 400；403 |
| 执行 | `POST /executions/claim` | Bot 领取一个执行 | `executorId`、`accessPolicyVersion:1`、`planVersion:1`、`directRoomVersion?:1`、`memberJoinedVersion?:1` | `{execution: Execution\|null}` | 每次随机 `operationId`；每 bot 同时最多一个 | 400 `INVALID_INPUT`（旧服务端拒绝新字段）；403 `ACTOR_NOT_OWNED` |
| 执行 | `POST /conversations/:roomId/executions/:executionId` | 心跳/完成/失败 | `executorId`、`epoch`、`action:'heartbeat'/'complete'/'fail'`；成功带 `content[]`、`continueDiscussion`；失败带 `detail:'cindy-runtime-error:<CODE>'` | `{status}` | 终态固定键 `chat:<execId>:<epoch>:<action>`；心跳每次新键 | 409（epoch 过期/租约失效，具体码待确认）；403 |
| 执行 | `POST /conversations/:roomId/executions/:executionId/control` | 停止普通轮次 | `action:'stop'` | `{status}` | 按 `operationId` | 403/404 |
| 执行 | `GET /conversations/:roomId/executions` | 侧栏“谁在说”、失败兜底、继续讨论源 | — | `Execution[]` | 最近 100 条 | 403 |
| 执行 | `GET /conversations/:roomId/execution-failures` | 按源消息批量读失败状态 | `sourceIds=a,b,...`（≤100/批） | 失败行：`id,conversation_id?,source_message_id,bot_id,epoch,status:'failed',failure_code?,plan_id?,updated_at?` | 无游标；按已加载消息 ID | 404（旧服务端）→ 客户端回退 `/executions`；400 |
| 分工 | `GET /conversations/:roomId/plans` | 读取计划 | `ids?`（逗号分隔） | `ServerPlan[]` | — | 403 |
| 分工 | `POST /conversations/:roomId/plans` | 创建分工 | `operationId='plan:<sourceId>'`、`sourceMessageId`、`organizerId`、`request`、`steps[{botId,task,status?}]`、`sourceId?`、`status?`、`currentStep?` | `ServerPlan` | 固定幂等键 | 409 `PLAN_OPEN`；400；403 |
| 分工 | `POST /conversations/:roomId/plans/:planId` | 计划动作 | `expectedRevision`、`action:'start'/'dismiss'/'stop'/'continue'/'retry'/'reassign'/'remove'`、`position?`、`botId?` | `ServerPlan` | CAS | 409 `REVISION_CONFLICT`/`PLAN_CLOSED`；403 |
| 分工 | `POST /conversations/:roomId/plans/:planId/messages` | 进行中/等待时补充意见 | `content[]` | `Message` 或 `{ok:true}` | 按 `operationId` | 409 `PLAN_CLOSED`；400 |
| 媒体 | `POST /conversations/:roomId/media` | 申请上传 | `operationId='media:<sha256(source+attachmentId+bytes)>'`、`name`、`type`、`size` | `{id,uploadUrl}` | 按 `operationId`；同内容同键 | 400 `ATTACHMENT_TOO_LARGE`/`INVALID_ATTACHMENT`；410 `UPLOAD_EXPIRED` |
| 媒体 | `PUT <uploadUrl>` | 直传对象存储 | 原始字节；`Content-Type` 为 MIME | 2xx | 签名 URL；60 秒超时；不带账号 token | 403 签名失败；客户端归一化 `MEDIA_UPLOAD_FAILED` |
| 媒体 | `POST /conversations/:roomId/media/:mediaId/complete` | 封存对象 | `operationId='seal:<mediaId>'` | `{state}` 或 `{ok:true}` | 固定幂等键 | 404 `MEDIA_NOT_FOUND`；410 `UPLOAD_EXPIRED` |
| 媒体 | `GET /conversations/:roomId/media/:mediaId` | 取短期下载地址 | — | `{name,type,size,url}` | 签名 GET；size 为正整数且 ≤100MB | 403/404 |
| 邀请 | `POST /conversations/:roomId/invite-links` | 创建邀请链接 | — | `{token,expiresAt:string\|null,reusable?}` | 按 `operationId`；token 43 位 base64url | 403 `OWNER_REQUIRED`/`ROLE_REQUIRED` |
| 邀请 | `POST /conversations/:roomId/invite-links/revoke` | 撤销单条链接 | `token` | `{revoked:true}` | 按 `operationId` | 404（旧服务端不支持）；403 |
| 邀请 | `POST /invite-links/preview` | 预览邀请 | `token` | `{groupId,name,inviterName,expiresAt,reusable?,joined}` | — | 404/410；不开放历史 |
| 邀请 | `POST /invite-links/accept` | 接受邀请 | `token` | `{groupId}` | 按 `operationId`；重复接受幂等 | 403 封禁；410 过期/撤销；409 已满 |
| 变更 | `GET /changes` | 取 scope 当前头/变更 | `scope=actor:<id>` 或 `conversation:<id>`、`after`、`limit` | `{head,changes?}` | `limit=1` 取头；`after` 为 scope 游标 | 400；410 `RESET_REQUIRED` |
| 运维 | `GET /ready` | 上线/联调健康检查 | — | `{status:'ok'}` 或 200 | — | 503 |

补充合同：

- `GET /actors` 必须返回 `disabled:true` 的行，客户端自行丢弃；服务端不要替客户端隐藏，否则“删除后其他设备降级”无法传播。【源码】`runtime.ts:72-76`
- `externalId` 是本地 Bot Profile id，跨电脑稳定；同一账号下同一 `externalId` 只对应一个 bot actor，重复登记返回既有 actor。【源码】`chatServer.ts:588-592,710-713`
- `POST /actors/:id/profile` 只在“本机是 homeDeviceId、本安装已认领、未降级”时由客户端调用；服务端仍必须校验 token 设备等于 `home_device_id`。【文档】`cloud-companions.md:148`
- `GET /conversations` 不返回 direct 房；所有 direct 房都不出现在群聊列表。`Room` 字段：`id,kind,name,topic,description,response_mode,speaking_mode,organizer_id,revision,head,archived,dissolved_at,state,created_at,updated_at`。
- `Member` 字段：`id,kind,name,displayName,nickname,avatar,state,role,ownerActorId,ownerName,guestAccess,accessRevision`。客户端自行过滤 `state==='joined'`；服务端必须返回非 joined 历史成员，否则回复数/回应者投影会缺行。
- 建群：群名 1–40 字；`participants` 为 bot actor ID 数组，客户端限制 2–6 位【源码】`botGroupChat.ts:11-13`；服务端校验 2–6 且 bot 属于当前账号。成员顺序即 `participants` 顺序，必须持久化 `position/joined_at`，不能依赖 UUID 排序。【文档】`chat-server-feature-parity.md:33`
- 1:1 房：`kind='direct'`、`participants:[botActorId]`、`sourceId=botActorId`、`operationId='companion-room:<actorId>'`。服务端以 `(owner_account_id, kind='direct', source_id)` 唯一约束保证任意设备并发创建得到同一房间；名字冲突时客户端改用 `companion-room:<actorId>:<sha256(name) 前12位>` 重试，服务端仍命中同一房间。【源码】`chatServerClient.ts:325-336`
- 旧群迁移：额外 `sourceId:<localGroupId>`、`responseMode`、`speakingMode`、`history:'shared'`；服务端用 `sourceId` 做迁移水位，重复请求返回同一房间。`history:'shared'` 是否所有群都接受列入未确认。【源码】`chatServerMigration.ts:44-48`
- `Message` 字段：`id,seq,authorId,author{kind,name},content[],origin?,createdAt,deleted,threadRootId,revision?,replyCount?,reactions?`。`seq` 是房间内全序，wire 为十进制字符串，必须允许超过 JS 安全整数（客户端用 `BigInt`）。【源码】`chatServerClient.ts:44-47`
- `content` 块：`text`（≤8000 字）、`media{mediaId,caption?}`（媒体属于本房间、上传者为发送者、已封存）、`card{namespace,schemaRevision,data,fallback}`（服务端视为不透明 JSON，限制大小/深度）。引用存 `cindy.quote` 卡片；贴回存 `cindy.thread-share`；任务卡存 `cindy.task`。
- `mentions` 中每个目标必须仍是 joined 成员，任一失效整条拒绝 `MENTION_UNAVAILABLE`，不得静默过滤为空或只发给有效目标。【文档】`bot-group-chat.md:420`。`mentionsAll=true` 由服务端按最新成员展开为全部 joined 成员，再排除作者；与 `mentions` 命名列表并存。【文档】`chat-server-feature-parity.md:14,34`
- `threadRootId` 必须指向本房间主时间线消息；回复的回复一律拒绝（Thread 只有一层）。`replyToId` 必须与消息在同一 Thread，否则 `REPLY_THREAD_MISMATCH`。
- `cindy.quote` 的摘要由发送端生成；服务端校验来源消息 ID 在当前读者权限内可见、未删除、非系统事件、非跨 Thread，失败 `QUOTE_UNAVAILABLE`。【文档】`bot-group-chat.md:501-506`
- Thread 贴回固定幂等键 `thread-share:<replyId>`，`mentions` 只含分享者本人；服务端定向消息不得放宽到其他人，否则会唤醒伙伴。重复贴回同一版本返回同一条消息；来源已改动或参数不同返回 `OPERATION_CONFLICT`。【文档】`bot-group-chat.md:626-628`
- 导入消息（云伙伴镜像）需要允许 `sourceId` 与 `createdAt`：服务端按 `(room_id, source_id)` 唯一去重，并按客户端提供的原始时间保存。【推测】但“对话保留原时间”是产品要求【文档】`cloud-companions.md:115,141`。
- 已解散/已归档房间拒绝新消息；客户端据此显示只读。`GET /actors`、`GET /conversations` 等读接口在服务不可达时保留旧数据，客户端自行处理。

## 4. WSS `/v1/ws`
### 4.1 握手与认证
```
C→S  {"type":"auth","token":"<access token>"}          // open 后立即发送
S→C  {"type":"ready","actorId":"<humanActorId>"}        // 认证成功
```

- 手机 15 秒握手 deadline，未收到 `ready` 即关闭重连；桌面无显式 deadline，靠 15 秒 HTTP 超时与重连。【源码】`chatServerSubscription.ts:26-35`
- 服务端必须在 `auth` 前拒绝任何 `subscribe`；认证失败关闭连接，建议 close code 4401（码待确认）。
- `ready` 后客户端会清空快照缓存、重新订阅所有 scope、同步个人列表并强制重读；服务端不得假设客户端保留旧订阅。
- 建议服务端每 30 秒发 WS ping；客户端不回 pong 时关闭。该心跳与执行租约心跳无关。

### 4.2 订阅 scope
```
C→S  {"type":"subscribe","scope":"conversation:<roomId>","after":"<decimal cursor>"}
```

| scope | 含义 | 初始 cursor | 证据 |
|---|---|---|---|
| `actor:<humanActorId>` | 账号级事件：列表、成员关系、actor 主页 | 手机取 `/changes` 的 head；桌面用 `'0'` 回放 | 【源码】`useChatServer.ts:134-140` |
| `conversation:<roomId>` | 单房间事件 | `snapshot.cursor` | 【源码】`chatServer.ts:803-806,830` |

- 每 socket 最多 16 个 scope（客户端注释）；超过时服务端返回 `scope_error` 并拒绝新 scope，具体码待确认。
- `actor` scope 只允许本人 human actor ID；`conversation` scope 只允许 joined 成员。非成员返回 `scope_error`（`NOT_MEMBER` 类），客户端停止该 scope，不无限重试。
- 手机群页可同时订阅 `conversation:<id>` 与 `actor:<id>`（当服务端支持列表移除能力时）。【源码】`useChatServer.ts:124-125,223-224`

### 4.3 服务端事件
| `type` | 载荷 | 客户端处理 |
|---|---|---|
| `ready` | `actorId` | 重置缓存、重新订阅全部 scope、同步个人列表、强制重读 |
| `changes` | `scope`、`cursor`、`changes:[{type,entityId?,data?}]` | 逐条失效；处理完发 `ack` |
| `scope_error` | `scope`、`error{code,details?}`、可选 `changes` | `RESET_REQUIRED` 用 `details.head` 重订；其他错误撤权/停止 |
| `error` | 任意 | 关闭 socket |

`changes[].type` 清单：

| change type | 出现 scope | `data`/`entityId` | 客户端动作 |
|---|---|---|---|
| `conversation.list.changed` | actor | `{conversationId,hidden,revision}` | 合并个人列表态 |
| `conversation.updated` | actor / conversation | `{dissolvedAt?}` | 失效缓存；解散则停止分工 |
| `membership.changed` | actor | `entityId` | 失效该群 |
| `actor.changed` | actor | `{homeDeviceId?}` | 云伙伴开启时强制重读 actor/home |
| `execution.changed` | conversation | `{status,botId}` | 租约复核；`queued` 且属于本机 Bot 时 250ms 内 claim |
| `message.created` | conversation | `entityId` | 失效并重读 |
| `message.updated` / `message.deleted` | conversation | `entityId` | 失效 Thread 预览/贴回源；重读 |
| `message.system.created` | conversation | `{executionTrigger,message}`（测试 fixture） | 仅触发重读；生产载荷待确认 |
| `reaction.changed` | conversation | `entityId`（messageId） | 记消息级回应版本，重读 |
| `pin.changed` / `read.changed` | conversation | — | 计入“闲聊类”变更 |

- “闲聊类”（`message.created/updated`、`reaction.changed`、`pin.changed`、`read.changed`）不触发租约复核；未知/其它类型触发 `checkLease`，防止撤权后继续跑。【源码】`chatServer.ts:163-165,844-850`
- 服务端推送**不带消息体**（个人列表行除外）；客户端统一“失效 + 重读”。hub 只需发送 `entityId`，不能依赖客户端从事件拼状态。
- `execution.changed` 的 `status` 至少包含 `queued/claimed/running/completed/failed/stopped/expired`。

### 4.4 seq、ack、断线补读与游标
- **scope 游标**：每个 scope 单调递增的十进制字符串；客户端用 `BigInt` 比较，迟到事件不能回退。**消息序号**：房间内 `seq` 严格单调；与 scope 游标是两套序号，客户端不假设相等。
- **ack**：`{"type":"ack","scope":"...","cursor":"..."}`；仅在处理完 `changes` 后发送；丢失只会导致重放，不丢数据。服务端不得因 ack 删除 change log。
- **重连**：不重放客户端写；客户端用最后 ack 的游标重新 `subscribe`，服务端从 change log 补读。
- **保留下界**：服务端必须定义 change log 保留期（建议 7 天）；游标低于下界返回 `scope_error{RESET_REQUIRED,details.head}`。
- **重置**：conversation scope 重读 snapshot，用新 `snapshot.cursor` 重订；actor scope 同步个人列表后用 `head` 重订。手机 actor scope 非 reset 错误直接停止订阅；conversation scope 非 reset 错误关闭 socket 并重读授权。【源码】`chatServerSubscription.ts:55-62`
- **批量**：服务端可合并 150ms 内的变更；一批最多建议 100 条。桌面退避 500ms 起倍增、上限 15 秒；手机 1000ms 起倍增、上限 15 秒；`ready` 后重置。【源码】`chatServer.ts:413-414,894-899`

### 4.5 与 HTTP 读模型的关系
打开房间的标准顺序：`GET snapshot` 拿 `cursor` → `subscribe(conversation, cursor)` → 并行读 messages/executions/plans/failures。新房间必须先拿 snapshot 游标再读其余，否则会漏事件。【源码】`chatServer.ts:1116-1129`

- 快照复用：仅当订阅仍 live、generation 未变、30 秒内；发送前也用 30 秒内快照。
- 侧栏摘要复用：房间 `head`/`revision`/`updated_at`/本地 generation/registryKey 不变且 60 秒内不重读。
- 附件下载完成按群合并为一次刷新；不要为每个附件触发一次全页重读。

## 5. 核心数据模型
> 建议 PostgreSQL 15+；时间用 `timestamptz`；ID 用 UUID（建议 UUIDv7）；JSONB 存 content/card/data。`seq`、`revision`、scope 游标用 `BIGINT`，wire 一律字符串。

### 5.1 表清单与索引
| 表 | 关键字段 | 索引/约束 | 说明 |
|---|---|---|---|
| `accounts` | `id`、`issuer`、`subject`、`display_name`、`avatar`、`created_at` | `unique(issuer,subject)` | 认证账号；human actor 关联此表 |
| `actors` | `id`、`account_id`、`kind`、`issuer`、`external_id`、`name`、`avatar`、`avatar_source`、`home_device_id`、`disabled`、`created_at`、`updated_at` | `unique(account_id,external_id) where kind='bot'`；`index(account_id)`；`index(home_device_id)` | human/bot/integration；停用只置位不删 |
| `actor_preferences` | `actor_id`、`key`、`value jsonb`、`revision`、`deleted_at`、`updated_at` | `pk(actor_id,key)`；`index(actor_id)` | 伙伴资料 KV；`value` 为 null 表示墓碑 |
| `rooms` | `id`、`kind`、`name`、`topic`、`description`、`response_mode`、`speaking_mode`、`organizer_id`、`owner_account_id`、`source_id`、`revision`、`head`、`last_seq`、`archived`、`dissolved_at`、`dissolved_by`、`created_at`、`updated_at` | `unique(owner_account_id,source_id) where kind='direct'`；`index(kind,updated_at desc)` | direct/group/channel；`head` 是变更头 |
| `room_members` | `room_id`、`actor_id`、`state`、`role`、`nickname`、`position`、`joined_at`、`invited_by`、`guest_access`、`access_revision`、`updated_at` | `pk(room_id,actor_id)`；`index(actor_id,state)`；`index(room_id,state,position)` | 成员顺序用于轮次；历史成员保留 |
| `messages` | `id`、`room_id`、`seq`、`author_id`、`author_kind`、`author_name`、`content jsonb`、`origin`、`thread_root_id`、`reply_to_id`、`revision`、`deleted_at`、`source_id`、`share_source_id`、`created_at`、`edited_at` | `unique(room_id,seq)`；`index(room_id,seq desc)`；`index(room_id,thread_root_id,seq)`；`unique(room_id,source_id) where source_id is not null` | 墓碑保留；作者名快照不随改名 |
| `message_reactions` | `message_id`、`actor_id`、`emoji`、`created_at` | `pk(message_id,actor_id,emoji)`；`index(message_id)` | 聚合由查询计算或缓存 |
| `media` | `id`、`room_id`、`uploader_id`、`name`、`mime`、`size`、`sha256`、`object_key`、`state`、`upload_expires_at`、`sealed_at`、`created_at` | `index(room_id,created_at)`；`unique(room_id,sha256)` 可选 | 状态 `prepared/uploaded/sealed/gc` |
| `executions` | `id`、`room_id`、`source_message_id`、`bot_id`、`requester_id`、`status`、`epoch`、`plan_id`、`plan_step`、`trigger_type`、`trigger jsonb`、`context_seq`、`attachment_after_seq`、`access_mode`、`access_revision`、`lease_expires_at`、`failure_code`、`result_message_id`、`turn_id`、`cycle_no`、`created_at`、`updated_at` | `index(bot_id,status,created_at)`；`index(room_id,source_message_id)`；`index(lease_expires_at) where status in ('claimed','running')` | 执行=租约；历史保留 |
| `room_turns` | `id`、`room_id`、`source_message_id`、`responders jsonb`、`status`、`cycle_no`、`published_count`、`reserved_count`、`created_at`、`ended_at` | `index(room_id,created_at)` | 一轮（最多 3 圈、10 条 Bot 消息） |
| `plans` | `id`、`room_id`、`source_message_id`、`request_text`、`organizer_id`、`creator_id`、`status`、`current_step`、`revision`、`note_message_ids uuid[]`、`created_at`、`updated_at` | `unique(room_id) where status in ('proposed','running','waiting')` | 一个群最多一张未结束计划 |
| `plan_steps` | `plan_id`、`position`、`bot_id`、`task`、`status`、`result_message_id`、`updated_at` | `pk(plan_id,position)` | 步骤顺序与交接消息 |
| `attachment_watermarks` | `room_id`、`bot_id`、`access_mode`、`access_revision`、`plan_id`、`plan_step`、`after_seq`、`updated_at` | `unique(room_id,bot_id,access_mode,access_revision,plan_id,plan_step)` | 附件成功投递水位 |
| `invitations` | `id`、`room_id`、`token_hash`、`created_by`、`expires_at`、`reusable`、`revoked_at`、`used_count`、`created_at` | `unique(token_hash)`；`index(room_id)` | 只存摘要；不存明文 token |
| `conversation_list_states` | `account_id`、`conversation_id`、`hidden`、`revision`、`updated_at` | `pk(account_id,conversation_id)` | 只影响本人账号所有设备 |
| `read_states` | `room_id`、`actor_id`、`thread_key`（`main` 或 Thread 根 ID）、`read_seq`、`updated_at` | `pk(room_id,actor_id,thread_key)` | 服务端只读给客户端；无写接口 |
| `membership_events` | `id`、`room_id`、`actor_id`、`message_id`、`display_name`、`seq`、`created_at` | `unique(room_id,actor_id,message_id)` | 入群系统事件的可信来源 |
| `change_log` / `change_scopes` | `change_log(scope,seq,type,entity_id,data,created_at)`；`change_scopes(scope,head,updated_at)` | `pk(scope,seq)`；`pk(scope)`；`index(created_at)` | 每 scope 单调；定期分区/清理 |
| `operation_keys` | `account_id`、`operation_id`、`route`、`request_hash`、`status`、`response jsonb`、`created_at`、`expires_at` | `pk(account_id,operation_id)`；`index(expires_at)` | 幂等账本；同键不同 hash 报冲突 |

### 5.2 不建表的对象
| 对象 | 处理 |
|---|---|
| device | Chat Server 不存设备表。设备身份来自 token；主力 `deviceId` 存在 `actors.home_device_id`，安装认领存在 `companion.install.v1` |
| presence / typing | Chat Server 没有 presence/typing 通道；“在线”来自 device-link。不要为本协议新增在线表 |
| share（Thread 贴回） | 不建 share 表；贴回是一条带 `cindy.thread-share` 卡片的消息，`share_source_id` 指向原回复，唯一键由 `operationId` 保证 |
| attachment | 统一用 `media`；消息 content 里只放 `mediaId` |
| read state 写入 | 客户端只读 `reads`，不提交已读；不要新增 `POST /reads`。产品规则明确“读取列表不向服务器提交已读” |
| invite token 明文 | 只存 hash；日志、预览、错误消息都不得回显明文 |

### 5.3 事务与一致性
1. **消息写入**：`BEGIN` → 锁 `rooms` 行（或 `UPDATE rooms SET last_seq=last_seq+1 RETURNING last_seq`）→ 插入 `messages` → 更新 `rooms.head` → 追加 `change_log(conversation:<id>)` → 追加 actor scope 的必要事件（成员/列表变化）→ `COMMIT`。同一房间写入必须串行，保证 `seq` 无空洞、无重复。
2. **变更通知**：事务提交后再 `NOTIFY`/发布到 hub；不能在事务内推送，否则客户端可能读到未提交数据。建议 hub 额外每 1 秒轮询 `change_scopes.head` 作为 NOTIFY 丢包的兜底。
3. **快照一致性**：在 `REPEATABLE READ` 事务内读 room、members、最近 100 条消息（含 Thread 回复）、reads、当前 conversation scope head；`cursor` 取该 head。服务端必须保证 cursor 之后的事件可补读。
4. **CAS**：`UPDATE ... WHERE revision=$expected`；0 行时再查当前值，返回 409 + `details.revision`。不要用“先读后写”代替原子 CAS。
5. **主力电脑 CAS**：`UPDATE actors SET home_device_id=$new WHERE id=$id AND home_device_id IS NOT DISTINCT FROM $expected`；成功后递增该 bot 的执行 epoch、取消 queued/claimed 执行、追加 `actor.changed`。0 行返回 409 + 当前 `homeDeviceId`。
6. **幂等账本**：在业务事务内插入 `operation_keys`（唯一键）；冲突时若 `status=done` 返回 `response`，若 `status=in_progress` 短暂等待/返回 409。`request_hash` 覆盖规范化后的请求体，避免同键不同参数被误判。
7. **计划/轮次**：`room_turns` 与 `executions` 的创建必须与用户消息写入同一事务，防止消息已可见但调度丢失；进程重启后由 worker 扫描 `status='queued'` 续派。

### 5.4 关系图
```
accounts 1 ──< actors (human/bot) ──< actor_preferences
                   ├── home_device_id ──> device-link device（不建表）
human actor 1 ──< room_members >── N rooms ──< messages ──< message_reactions / media
                                      ├──< room_turns ──< executions ──> bot actor
                                      ├──< plans ──< plan_steps
                                      ├──< invitations / membership_events
                                      └──< conversation_list_states / read_states
scope ──< change_log / change_scopes
```

## 6. 云伙伴生命周期
### 6.1 启用条件
- 只有 `GET /me` 的 `capabilities.companionHome === true` 且 `deviceId` 非空才启用；旧服务器保持“按电脑”行为。
- `directRoomClaim` 决定是否领取 1:1 房间执行；`groupDiscussionParity` 决定是否使用 `mentionsAll`、`cancel-planning` 等新字段；`groupLifecycleVersion` 决定解散/列表移除；`conversationListStateVersion` 决定个人列表态。
- 能力位缺失时客户端关闭对应功能，不猜测；服务端新增能力只能追加，不能改变旧字段语义。

### 6.2 创建与绑定主力电脑
1. 本机活跃 Bot Profile 在 `register()` 中逐个映射为 actor：不存在则 `POST /actors {kind:'bot', externalId:profile.id, name}`；存在则复用。`externalId` 是跨电脑稳定键。
2. `PATCH /actors/:id {homeDeviceId:self, expectedHomeDeviceId:null}`：只抢“空主力电脑”。冲突时采纳服务端返回的 `details.homeDeviceId`，不重试抢占。
3. 主力电脑登记成功后才 `POST /actors/:id/profile` 发布名字/头像；服务端必须校验 token 设备等于 `home_device_id`。
4. 同一台电脑上的两个安装用 `companion.install.v1` 区分；读到“别的安装”即视为降级（隐藏/不领取/不上传/不自动接管）。服务端不解析该值，但必须原样保存并支持 CAS。
5. 服务端对空 `homeDeviceId` 保持旧行为：不按主力电脑过滤领取；客户端在能力开启后会持续重试登记直到成功。

### 6.3 接管（在这台电脑继续）
1. 客户端先检查云端 `companion.profile.v1` 存在且 `name/description/soul/systemPrompt/user` 都是字符串；不完整则拒绝接管，主力电脑不变。
2. `PATCH /actors/:id {homeDeviceId:self, expectedHomeDeviceId:旧值}`。CAS 成功后服务端立即递增 epoch、取消旧设备的 queued/claimed 执行、追加 `actor.changed`；旧设备下一次 claim 因设备不匹配被拒绝。
3. 客户端先降级本机副本（发送拦截、例行任务、列表隐藏立即生效），再认领安装，然后 `materializeCompanion`：profile→memories→skills→todos→routines→workbench→房间全部历史→rebuild context。
4. 全部成功才解除降级；任一步失败保持降级并约每分钟重试。服务端只需保证 `GET /preferences` 与消息分页在接管期间可重复读，不要求“接管事务”。
5. 接管读取房间历史要读到第一条；读不完整则本次接管失败、稍后重试。服务端必须提供稳定分页（`before=<seq>` 严格变旧）和附件下载。

### 6.4 交出（本机是主力）
1. 客户端 busy 检查：`wait` 最长 4 分钟或 `stop` 30 秒；暂停的伙伴不交出。
2. flush：镜像 drain + 名字头像 + 所有资料重复上传直到零写入。
3. 再次 busy 检查，然后 `PATCH /actors/:id {homeDeviceId:target, expectedHomeDeviceId:self}`。
4. 服务端校验调用设备确实是当前 `homeDeviceId`；成功后递增 epoch、取消本机执行、追加 `actor.changed`，返回新 home。
5. 本机随后降级。若 CAS 失败（被别处接走），客户端立即重读 server 的 home，按实际值显示，不覆盖。
6. 从其他电脑/手机发起交出经 device-link `maker:companion-cloud:hand-over`，最终仍由主力电脑执行同一 CAS；Chat Server 不新增远程交出端点。

### 6.5 降级与停用
- 降级条件：`homeDeviceId` 不是本机；actor `disabled`；同一设备另一安装持有；`companion.install.v1` 指向别的安装。
- 效果：不领取执行、不上传、不导入、不发布资料、列表隐藏、主任务只读、发送拦截。客户端持久化在 `companion-cloud/shadowed.json`（账号隔离），启动前/服务器不可达/功能关闭时仍生效。
- 服务端职责：home 变化后旧设备 claim 一律 403/空；旧 epoch 终态拒绝；`disabled:true` 后仍返回 actor 行（客户端据此降级），但不再派发执行、不再接受 profile 发布。降级状态本身不写入 Chat Server。
- 删除伙伴：主力电脑 `PATCH /actors/:id {disabled:true, homeDeviceId:self, expectedHomeDeviceId:self}`。冲突说明已被接走，只删本机副本；失败记 `retirePending` 每轮重试。其他设备看到 `disabled` 后转降级：隐藏、只读、不运行。

### 6.6 合并多个 Cindy
- 只认 Cindy 预设/`cindy-default`（资料快照 `templateId`、默认身份），从不按名字。
- 各来源先 flush；合并记忆/技能（同名不同内容改名为“名字-来源”）、待办/例行（派生稳定 ID）、工作台、全部对话；对话保留原时间，附件重新上传。
- 全部完成后停用来源 actor：`PATCH /actors/:src {disabled:true, homeDeviceId:来源电脑, expectedHomeDeviceId:来源电脑}`。CAS 失败说明已被接走，跳过停用。
- 合并幂等可重跑；服务端只需保证消息 `sourceId` 去重、偏好键可按 ID 覆盖、`disabled` CAS 正确。合并期间来源有新内容时本次不停用，提示稍后再合并。

### 6.7 资料上传与墓碑
- 只上传“本机有”的键；本机消失且曾由本机拥有的键写 `null` 墓碑；服务器上仅存在的键从不被墓碑。
- 每次最多 30 次写；值 ≤60KB，键 ≤160 字符；冲突时先确认自己仍是主力电脑，再读服务器当前值，最多重写一次。
- 有不可读部分（如 routine 引擎未就绪）时本轮跳过该前缀，不写墓碑；交出时 `requireAll` 直接失败。
- 服务端必须支持“读单键/读全部/按修订写/墓碑”，不要做批量覆盖；否则接管、合并、冲突重试都会丢失并发修改。

### 6.8 1:1 房间与对话镜像
- 房间：`direct` + `participants:[botActor]` + `sourceId=botActor`；房间 ID 由 `(owner, sourceId)` 确定，任意设备并发创建得到同一房间。
- 镜像只由主力电脑执行：只导入“用户可见”的用户消息与伙伴最终回复；隐藏 `botPrivateReply`、`assistantPhase:'commentary'`、子 Agent（`parentToolUseId`/`parent_tool_use_id`/`parentUuid`）、`autoResume`、`botTaskCoordination`、委派续跑；其他来源加与界面同口径的来源前缀。
- 客户消息 ID 去重；游标 `(createdAt,id)`；每轮回扫 10 分钟/200 行；首批只传最近 200 条，后台按时间从旧到新补传（每轮最多 10 页 ×200）。
- 服务端：消息 `sourceId` 唯一去重；允许导入指定 `createdAt`；附件 ≤25MB；导入不触发执行、不发通知。消息 `origin` 建议 `import`。
- 1:1 执行：只对“自己的 direct 房间”派发；校验源消息作者是主人、未删除、非 system；claim 要求 `directRoomVersion=1`；`access_mode='owner'`。成功终态 `{continueDiscussion:false}` 不重复写消息，回复由镜像导入。
- 投递前再确认本机仍是主力 + 安装已认领；受“允许同账号设备控制本机”开关约束（`REMOTE_CONTROL_DISABLED`）。【文档】`cloud-companions.md:118-119`

## 7. Bot 群聊与发言编排
### 7.1 一轮的定义
- 只有**人类成员**的新消息开启一轮；Bot 消息不会自己开启下一轮。
- 一轮最多 3 圈、最多 10 条 Bot 消息；每位 Bot 每圈最多 1 条；只有 1 位回答者时只跑 1 圈。
- 整圈没有任何 Bot 发言，本轮提前结束。自然结束后时间线出现“伙伴们说完了 · 继续讨论”；用户点继续才开启新一轮，回答者与顺序与上一轮相同，一律轮流，额度重新计算。
- 用户在一轮进行中发新消息：本轮立即作废，正在发言的 Bot 被停止，未发出的回复丢弃；按新消息开启新一轮（插话）。
- 用户点停止：本轮结束，正在发言的 Bot 被停止；不产生失败提示。

### 7.2 回答者与顺序
| 消息情况 | 回答者 | 顺序 |
|---|---|---|
| 显式 @ 了 Bot | 仅被 @ 的 Bot | 按 @ 的先后；始终轮流 |
| `mentionsAll=true` / @所有人 | 全部 joined Bot（排除作者） | 成员顺序；首圈可并行 |
| 没有 @ 任何 Bot，`response_mode='all'` | 全部 joined Bot（排除作者） | 成员顺序；首圈可并行 |
| 没有 @ 任何 Bot，`response_mode='mentioned'` | 无 | — |
| 只 @ 了人类 | 不视为点名 Bot；按 `response_mode` 处理（待确认） | — |

- 成员顺序：建群时的 `participants` 顺序；后续加入按 `joined_at`/`position` 追加。第二圈起起点依次后移一位，避免总是同一位先说。
- `speaking_mode='auto'`：没有 @ 具体 Bot（含 @所有人）且回答者 ≥2 时，第一圈所有回答者同时想，彼此看不到本圈回答；第二圈起轮流。`sequential`：始终轮流。显式 @ 具体 Bot 始终轮流。
- 服务端是调度权威：决定谁在何时领取、圈次/额度；客户端只 claim 并执行。客户端不能自报“轮到我”。

### 7.3 并行、轮流与额度
- 第一圈并行：为每个回答者创建一条 `queued` 执行，全部使用同一个 `context_seq`（源消息 seq）；它们互不可见。谁先完成谁先发；额度用完后晚到的回复丢弃。
- 顺序：同一时刻只允许一条 `claimed/running` 执行；前一条终态成功写入消息后，用新的 `context_seq`（最新消息 seq）创建下一条。若前一位 `NO_REPLY`/失败，继续下一位。
- 第二圈起：即使 `auto` 也改为轮流；每位都能看到之前所有人的话。
- 服务端必须在 claim 时原子预占额度：`published_count + reserved_count < 10` 才发租约；完成写消息时 `published_count+1, reserved_count-1`；`NO_REPLY`/失败只释放 reservation。否则并发 claim 会突破 10 条。
- 重启恢复：`room_turns` 与 `executions` 持久化；进程重启后扫描 `queued` 续派，`claimed/running` 等租约过期后转为可重派或失败，不自动重跑已经提交终态的执行。

### 7.4 失败、停止与插话
| 事件 | 服务端行为 |
|---|---|
| 成功且有正文 | 以 bot actor 写消息，推进 `seq`，计入额度；`continueDiscussion` 在群聊路径不自动开新一轮 |
| 成功但正文为空或 `NO_REPLY` | 不写消息，不占额度，继续下一位/下一圈 |
| 失败 | 记录 `failure_code`，不写消息，不自动重试；其他 Bot 继续 |
| 超时 | 单轮 2 小时（等待用户确认不计）；租约过期由 worker 标记失败，建议 `RUNTIME_TIMEOUT` |
| 停止 | `control{action:'stop'}` 取消当前执行与本轮；状态 `stopped`，不算失败 |
| 插话 | 取消本轮全部执行；旧 epoch 的终态拒绝；按新消息建新轮 |
| 撤权/离群/移除 | 递增 `access_revision` + epoch，取消执行，拒绝旧结果 |

失败码白名单：`IMAGE_INPUT_UNSUPPORTED,MODEL_UNAVAILABLE,AUTH_REQUIRED,RATE_LIMITED,UPSTREAM_OVERLOADED,QUOTA_EXCEEDED,NETWORK_ERROR,RUNTIME_TIMEOUT,RUNTIME_ERROR`。【源码】`botGroupChat.ts:47-55`。服务端只存白名单类别，不存原始错误、路径、响应正文或凭证。`/execution-failures` 只返回当前读者可见、源消息未删除的失败行。

### 7.5 @ 提及
- 客户端发送前会读 `GET members`，校验每个显式目标仍是 joined；任一失效整体拒绝并保留输入。服务端在提交时必须用最新成员再校验一次，不能信任客户端快照。
- `@所有人` 由服务端按最新 joined 成员展开，再排除作者；手机直连也先读成员再展开，但服务端仍是最终权威。
- 支持 `mentionsAll`（新服务端）与 `mentions` 命名列表并存；旧服务端只认后者。服务端不得因为 `mentionsAll` 而忽略显式 `mentions` 的顺序。
- Thread 回复不自动点名原消息作者；引用（`cindy.quote`）不增加点名，也不唤醒被引用的人或 Bot。
- Thread 贴回的 `mentions` 只含分享者本人；服务端定向消息从不放宽到其他人，因此不会启动 Bot 回合。
- 草稿中显式目标的 ID/顺序/所有人标记任一变化，客户端都会换新 `clientId`；服务端按新消息处理，不做跨草稿去重。

### 7.6 分工（plan）
- **谁出安排**：每个群一位负责人（`organizer_id`）；未指定或负责人不可用时，成员顺序第一位可用 Bot 担任。负责人可以给自己派一步。
- **自动判断**：用户新消息没有 @ 具体 Bot（@所有人也算没有指定），且群里没有未结束计划时，客户端用辅助模型判断“要不要分工”（≤8 秒）；要则 `POST plans` 且消息 `deferExecution:true`；不要则 `POST continue` 进入普通接话。服务端只负责创建计划/取消兜底，不做模型判断。
- **手动分工**：客户端 `planningMode` 标记；消息一定交给负责人出安排，失败时提示重试，不静默降为普通讨论。
- **计划卡**：一条 `cindy.plan` 卡片消息，最多 6 步；同一位 Bot 相邻步骤由客户端合并。服务端按 `steps` 顺序执行，一步一行。
- **执行**：`start` 后按顺序为每一步创建执行，`plan_id/plan_step` 写入；同一时间只有一位 Bot 在做；每步结束进入 `waiting`，用户 `continue` 才下一步；最后一步完成 `done`。
- **补充/返工**：进行中或等待时用户不 @ 任何人直接说话，客户端发 `plans/:id/messages`；服务端把它作为该计划的评论/返工输入，不新建普通轮次。
- **换人/删步**：`proposed` 可换人/删步；`waiting` 只能换 `pending/failed` 步骤；已完成步骤不能换。`dismiss` 在 `waiting` 映射为 `stop`。计划步骤不占群聊 10 条额度。

### 7.7 群专线 Session
- 每位 Bot 在每个群有一条隐藏的群专线 Session；客户端路由键：`group:<groupId>:access:<mode>:<rev>`（普通）或 `group:<id>:plan:<planId>`（分工）。
- 群专线不进入任务列表、伙伴主时间线、历史列表、未读和系统通知；与伙伴主任务共享 Profile/Home/记忆/Skill/模型候选链，但权限档跟随群授权。
- 服务端不保存 Session，只保存 `access_mode`、`access_revision`、`plan_id`、`plan_step`；执行领取时把这些写入 `Execution`，客户端据此选择/创建专线。
- `access_mode='chat'`：独立群上下文，不装载主人 SOUL/记忆/Bot Home/Skill/工具，`toolsDisabled=true`；`tools`：可用既有工具，但每个工具边界必须回查活执行/授权版本/租约并顺带心跳。
- 群执行上下文由客户端组装：`currentRequest`（源消息 + `location:'thread'|'main'`）、Thread 根与 ≤100 条回复、主群最近 30 条背景、参与者、附件、低信任数据块。服务端只提供消息与执行字段，不组装提示词。

## 8. 权限模型、投递与离线
### 8.1 房间角色矩阵
| 能力 | owner | admin | member | guest | bot actor |
|---|---|---|---|---|---|
| 读消息/成员/快照 | 是 | 是 | 是 | 是 | 仅执行上下文 |
| 发言/Thread/回应/引用 | 是 | 是 | 是 | 是 | 仅终态写消息 |
| 改群名/主题/简介/回复方式/发言方式 | 是 | 是 | 否 | 否 | 代主人角色 |
| 邀请/移除/封禁/改角色 | 是 | 是 | 否 | 否 | 代主人角色 |
| 归档/取消归档 | 是 | 是 | 否 | 否 | 代主人角色 |
| 解散 / 转让群主 | 是 | 否 | 否 | 否 | 否 |
| 创建/撤销邀请链接 | 是 | 是 | 否 | 否 | 代主人角色 |
| 设置自己带来的 Bot 的 `guestAccess` | 是（作为主人） | 是（作为主人） | 是（作为主人） | 是（作为主人） | 否 |
| 领取执行 | 否 | 否 | 否 | 否 | 是（仅自己的 Bot） |

- 群主/管理员负责群资料与成员；**伙伴主人**单独决定该伙伴在本群的授权档，两者不可互相代替。群主身份不能替代伙伴主人身份。【文档】`bot-group-chat.md:402`
- 角色为 `owner/admin/member/guest`；最后一位群主不能退出（`LAST_OWNER`），需先转让或解散。
- 人类退出/被移除时，他带来的 Bot 同时退出，清空群授权并停止相关执行；重新加入恢复默认角色与默认授权，不继承旧授权。
- `X-Chat-Actor` 代演时，服务端既要校验 bot 归属，也要校验 bot 的主人在房间内的角色；bot 自身不能“管理”群。

### 8.2 跨账号访客与授权档
- 邀请链接可跨账号、跨区域；预览只给群名/邀请人/到期/复用/是否已加入，不开放历史；加入后默认共享既往历史。
- `guestAccess` 三档：`none`（仅主人）、`chat`（仅群内交流，默认）、`tools`（工具与资料）。授权变化递增 `accessRevision`，旧执行失效。
- `chat` 档：独立群上下文，不加载主人 SOUL、私人 Profile/Home、已学 Skill、工具集与私人 MCP；保留公开名字与简介。
- `tools` 档：可用已有工具，但每个工具边界重新校验成员状态、授权版本、活执行/租约；已完成的外部操作不可撤销。
- 撤权/离群/移除/租约失效：停止执行、拒绝旧结果；客户端不能自报授权，服务端从消息请求者与 Bot 真实所有者计算。
- 非主人获得的群工具授权不包含“从群启动独立任务/发送私聊”的能力；只有伙伴主人且当前授权有效才可。

### 8.3 组织、区域与账号隔离
- 服务端按区域分开部署，伙伴数据不跨境；国内外/个人与企业账号可互邀，邀请按 issuer 身份校验，跨区接受后按房间历史策略读取。
- 除房间成员关系外，没有“组织级可见性”：actor 目录只返回当前账号相关 actor；跨账号访问必须经邀请成为成员或访客。
- 所有资源查询必须同时按 `account_id` 与 `room_members` 过滤；不能只按 UUID 查找，否则跨账号 ID 猜测会泄露数据。
- 账号切换/换区/换端点即销毁旧请求/旧 socket/旧执行；服务端以 token 的 issuer/账号为准，不接受客户端自报账号。

### 8.4 授权版本与租约
- `room_members.access_revision` 在授权档变化、成员移除/退出、封禁、解散、归档时递增。
- 执行领取时把 `access_mode/access_revision` 写入租约；终态提交时服务端必须比较当前成员状态与授权版本，不匹配即拒绝。
- 撤权时取消旧执行并递增 `epoch`；客户端心跳会收到拒绝，必须立即停止工具调用。
- 群归档：拒绝新消息与新执行，历史保留；取消归档恢复。已解散群不能取消归档。
- 解散：停止所有成员发言、进行中执行与分工，邀请失效；历史仍按原权限保留；已退出者不会因解散重新取得访问权。

### 8.5 消息投递三条路径
| 路径 | 触发 | 服务端动作 | 客户端动作 | 结果 |
|---|---|---|---|---|
| 群 Bot 发言 | 群内用户消息/继续讨论/@ | 建轮次与执行，按额度派发 | 主力电脑 claim → 群专线 → 终态回写 | 服务端以 bot actor 写消息；不自动续轮 |
| 伙伴 1:1 房间 | 主人在 direct 房发消息 | 建 owner 执行，仅派给主力电脑 | claim → canonical 主任务 → 终态 | 终态不写消息，镜像导入回复 |
| 伙伴独立任务卡 | Bot 从群启动独立任务 | 普通消息接口保存 `cindy.task` 卡片 | 以 bot actor 发卡，超长/超附件按 `:N` 续发 | 服务端拒绝（不在群/归档/旧本地群）时退回主人私聊 |

- 群 Bot 发言：服务端决定谁在何时领取；客户端只执行。执行上下文由客户端组装，服务端不保存提示词。
- 1:1 房：只有“自己的 direct 房间”（已记录房间 ID，或成员恰为主人+该 Bot 的 direct）才领取；其它 direct 房不按群聊回答，也不出现在群列表。
- 任务卡：按调用方 `clientId` 幂等；超长正文/超 20 附件在同族 `:N` 消息续发；服务端拒绝时客户端退回私聊，不丢结果。
- 三条路径共用同一消息/成员/权限合同，但执行授权不同：群聊受 `guestAccess`，1:1 固定 owner，任务卡不唤醒群专线/私聊。

### 8.6 离线与推送
- Chat Server **没有**离线判定、presence 或 push 订阅端点；“主力电脑是否在线”来自 device-link，桌面 15 秒宽限，手机看设备列表/roster。
- 离线 UI：手机“消息还没送到主力电脑…”+“换一台电脑继续”；桌面改只读云端对话+横幅；从没列到主力电脑的行直接进云端对话。
- 推送：分工步骤停下时，桌面经 device-link notify（类别 `session-needs-reply`）推送，深链 `/companions/groups/<groupId>?deviceId=<电脑>`；只推手机可见的群；系统层按“同电脑同群”合并。
- 离线排队：文档明确“离线时在其他端直接发送并排队”未覆盖；手机云端会话只读。服务端不要实现隐式离线队列；1:1 房消息在主力电脑上线后由 claim 领取，但 UI 当前只读。
- 若重建版需要服务端推送，应新增独立 push worker/adapter，不要塞进 `/v1/ws` 合同；客户端现有合同不消费 push 端点。

## 9. 客户端行为假设
### 9.1 幂等
1. 所有写请求带 `operationId`；同键重复必须返回首次结果，客户端依赖此点重试。
2. 终态可重复提交（同 operationId）；心跳必须换新 operationId，服务端必须每次续租。
3. 媒体 prepare 同内容同键；`UPLOAD_EXPIRED` 才换键；封存键 `seal:<id>`。
4. 群历史导入以 `sourceId` 去重；客户端用 `(role,createdAt)` 识别“本机镜像行”（服务端不回传来源 ID）。
5. 发送回执缓存 128 条成功结果；进程重启后只依赖服务端 `operationId` 幂等，不是持久 outbox。
6. 群发言失败**绝不自动重发**；放回输入框，重试沿用同一 `clientId`；正文/点名/引用/附件任一变化即换 `clientId`。

### 9.2 顺序
1. 房间内 `seq` 严格单调、十进制字符串，可超过 JS 安全整数；客户端全程 `BigInt`。
2. `snapshot.cursor` 是“可从这里继续订阅”的一致点；`snapshot.messages` 是最新 100 条（含 Thread 回复）。
3. `before` 分页严格变旧；不前进即视为服务端错误（客户端归一化 `INVALID_CHAT_PAGE`）。
4. 房间列表 `after` 游标稳定、可推进；重复/倒退即错误。`head` 是单调变更头；不变即“无变化”，可跳过重读。
5. 任何变更事件只用于“失效+重读”；客户端不依赖事件顺序重建状态。
6. 执行 claim 每次最多一个；客户端串行 claim，同一 Bot 同时只跑一个 run。

### 9.3 重试与退避（客户端参数，服务端需按此设计超时）
| 行为 | 参数 |
|---|---|
| HTTP 请求超时 / 响应上限 | 15 秒 / 8 MiB |
| 401 续期间隔 | ≥60 秒 |
| WSS 握手 deadline（手机） | 15 秒 |
| WSS 重连 | 500ms→15s（桌面）；1s→15s（手机） |
| 执行 poll / 实时 queued 触发 claim | 2 秒 / 250ms |
| 心跳/租约 | 15 秒 |
| 终态重交 | 15 秒；401/429 时 ≥60 秒 + `Retry-After` |
| poll 失败退避 | 2s→60s（5xx）；401/429 ≥60s |
| 变更合并 / 快照复用 / 摘要复用 | 150ms / 30 秒 / 60 秒 |
| Thread 预览失败重试 / 附件下载重试 | 30s×2^n 最多 5 次 / 30 秒后 |
| Bot 单轮上限 | 2 小时（等待用户确认不计） |
| 群发送（手机→电脑）超时 | 180 秒 |
| 媒体 PUT/GET | 60 秒 |
| 手机换电脑等待 | 90 秒；5 秒轮询 |
| 桌面云端宽限 / 自动接管重试 | 15 秒 / 60 秒 |
| 云伙伴上传/镜像周期 | 60 秒 / 15 秒 |
| 旧群迁移失败重试 / 分工取消失败重试 | 60 秒 / 1s×2^n 上限 60 秒 |

### 9.4 容量与协议上限
| 项 | 上限 |
|---|---|
| 单条消息正文 | 8000 字 |
| 单请求体 / 单条 content | ~256 KiB / ~64 KiB |
| 每条消息附件 / 每轮给 Bot 附件 | ≤20 / ≤40 |
| 单文件 / 镜像上传 | ≤100 MB / ≤25 MB |
| 群成员（创建时 Bot 数） | 2–6 |
| 群名 / 主题 / 简介 | 40 / 250 / 2000 字 |
| 每 socket scope | ≤16 |
| `/execution-failures` 源 ID | ≤100/批 |
| 导入批量 / 单条导入 content | ≤50 条且 ≤200KB / ≤32 块且 ≤64KiB |
| 偏好在途写 | 30 次/轮；值 ≤60KB；键 ≤160 字符 |
| 群计划步骤 | 1–6 |
| 邀请 token | 43 位 base64url |

### 9.5 其它假设
- 客户端不把本地状态当授权：群专线路由键不是授权，工具调用必须回查活执行/租约。
- 客户端不消费服务端事件里的消息体；服务端不要把完整消息塞进 WS 帧。
- 客户端不依赖 presence/typing；不要为本协议新增。
- 客户端只读 `reads.read_seq`，不提交已读；服务端不要暴露写已读接口。
- 客户端把 403/404/410 视为“失去访问”，清空页面并停止订阅，不重试。
- 旧服务端缺路由时：`execution-failures` 404 → `/executions`；`invite-links/revoke` 404 → 禁用撤销；`memberJoinedVersion` 400 → 降级；能力位缺失 → 关闭功能。

## 10. Go 实现映射建议
### 10.1 模块划分
| 包/目录 | 职责 | 关键依赖 |
|---|---|---|
| `cmd/chat-server` | 启动、配置、优雅退出、`/ready` | 配置、日志、HTTP server |
| `internal/httpapi` | 路由、中间件、请求解码/校验、错误信封 | `auth`、`store`、`domain` |
| `internal/auth` | JWKS 缓存、JWT 校验、issuer/账号/设备解析、`X-Chat-Actor` 归属 | 账号认证服务 |
| `internal/domain` | 实体、值对象、错误码、权限判定、容量常量 | 无 |
| `internal/store` | pgx/sqlc、事务、seq 分配、change log、幂等账本 | PostgreSQL |
| `internal/realtime` | WS hub、scope 注册、change log 补读、ack、reset | `store` |
| `internal/scheduler` | 轮次、执行队列、租约回收、计划推进、延迟兜底 | `store`、`realtime` |
| `internal/media` | S3 预签名、prepare/complete、下载 URL、配额 | 对象存储 |
| `internal/push`（可选） | device-link push adapter；不属于 `/v1` 合同 | device-link |
| `internal/observability` | 结构化日志、指标、trace、operationId 哈希 | OTel/Prometheus |

### 10.2 Handler 映射
| Handler | 端点 |
|---|---|
| `IdentityHandler` | `GET /me`、`POST /profile/refresh` |
| `ActorHandler` | `GET/POST /actors`、`PATCH /actors/:id`、`POST /actors/:id/profile` |
| `PreferenceHandler` | `GET/POST /preferences` |
| `RoomHandler` | `GET/POST /conversations`、`PATCH`、`dissolve`、`snapshot`、`members`、`bot-access`、`nickname`、`transfer`、`list-state` |
| `MessageHandler` | 消息分页/发送/单条/continue/cancel-planning/reactions |
| `PlanHandler` | plans CRUD/动作/评论 |
| `ExecutionHandler` | claim/心跳/终态/control/list/failures |
| `MediaHandler` | prepare/complete/get（PUT 由对象存储承担） |
| `InviteHandler` | 创建/撤销/预览/接受 |
| `ChangeHandler` | `GET /changes` |
| `WSHandler` | `/v1/ws` 握手、auth、subscribe、ack |

中间件顺序建议：`recover` → `requestID` → `accessLog` → `auth` → `bodyLimit` → `rateLimit` → `idempotency` → handler。`idempotency` 只包裹写路由；GET 不做幂等账本。

### 10.3 Hub 设计
- 每个连接一个读 goroutine + 一个写 goroutine；写 channel 有界（如 256），满时关闭连接，避免慢客户端拖垮服务。
- 连接状态：`accountID`、`actorID`、`deviceID`、`subs map[scope]cursor`、`authed bool`、`closed`。
- 订阅校验：`actor:<self>` 或 `conversation:<room>` 且 `room_members.state='joined'`；校验失败发 `scope_error`，不关闭整个 socket（除非 `error`）。
- 补读：`subscribe` 时从 `change_log` 读 `seq > after` 的批次，按 100 条/批推送；读完推进内存 cursor。游标低于保留下界时发 `RESET_REQUIRED`。
- 扇出：写事务提交后 `NOTIFY chat_changes, '<scope>:<seq>'`；hub 收到通知后从 `change_log` 拉取并推给该 scope 的订阅者。多实例用 PostgreSQL `LISTEN/NOTIFY` 或 Redis/NATS；单实例 MVP 可只用内存通知 + 1 秒轮询兜底。
- ack：只更新内存 cursor 与指标，不落库；持久化游标由客户端自己保存。服务端不得因 ack 删除 change_log。
- 背压：如果某连接落后超过阈值（如 10k 条），直接发 `RESET_REQUIRED`，让客户端重新快照，禁止无限补读。

### 10.4 Worker
| Worker | 周期/触发 | 职责 |
|---|---|---|
| `RoundScheduler` | 新消息/继续讨论/计划动作 | 创建 `room_turns` 与 `executions`；并行首圈/顺序推进；额度预约 |
| `ClaimDispatcher` | `execution.changed`/轮询 | 只做通知与指标；实际 claim 由客户端发起，服务端不主动推执行体 |
| `LeaseReaper` | 5–15 秒 | 扫描 `lease_expires_at < now()` 的 claimed/running，标记 expired/failed，释放 reservation，推进轮次 |
| `PlanScheduler` | 计划动作/步骤终态 | 推进 `current_step`，创建下一步执行，维护 `waiting/done` |
| `DeferredPlanningWorker` | 延迟队列 | 处理 `deferExecution` 的 2 分钟兜底；收到 plan/cancel 后取消 |
| `InviteExpiryWorker` | 每小时 | 标记过期邀请；清理 used/revoked 的 token 摘要 |
| `MediaGC` | 每小时 | 清理 prepared 超时、未封存、无引用对象；对象存储生命周期兜底 |
| `ChangeLogGC` / `IdempotencyGC` | 每天 | 按保留期分区删除 change_log；删除过期 operation_keys |
| `RetireWorker` | 不强制 | 客户端重试停用，服务端只需支持 CAS |

### 10.5 关键实现顺序
1. **M0 合同骨架**：认证、错误信封、`/me`、`/actors`、`/conversations`、`snapshot`、`messages` 读、`/ready`。
2. **M1 写入与实时**：发消息、`seq`、change_log、WS hub、ack/补读、幂等账本。
3. **M2 群聊执行**：claim/心跳/终态、轮次与额度、`execution-failures`、停止/插话。
4. **M3 媒体与邀请**：媒体三段式、邀请创建/预览/接受/撤销。
5. **M4 分工与 Thread**：plans、计划动作/评论、Thread/贴回、引用卡。
6. **M5 云伙伴**：preferences、home CAS、1:1 确定性房间、镜像导入、接管/交出/合并支撑。
7. **M6 兼容与运维**：旧服务端降级、能力位、限流、指标、GC、压测。

### 10.6 事务与并发要点
- `rooms.last_seq` 是房间内唯一的序号分配器；所有消息/系统事件写入都必须先拿它。不要用数据库序列，否则跨房间会出现空洞且难以按房间分页。
- `change_scopes.head` 是 scope 游标分配器；每个 scope 一行，`UPDATE ... RETURNING head` 分配。actor scope 与 conversation scope 分开。
- 幂等账本与业务写入同事务：先插入 `operation_keys(status='in_progress')`，业务完成后更新为 `done` 并写 `response`；冲突请求等待行锁后读结果。不要先做业务再写账本，否则崩溃会重复。
- claim 用 `UPDATE executions SET status='claimed', epoch=epoch+1, lease_expires_at=now()+60s WHERE id=(SELECT ... FOR UPDATE SKIP LOCKED) RETURNING *`；同一 bot 只允许一个 active，用部分唯一索引 `unique(bot_id) where status in ('claimed','running')` 兜底。
- 终态写入消息与更新执行必须同事务；消息 `operationId` 与执行终态键分开，避免客户端重试终态时重复写消息。
- 所有列表查询必须带 `room_id`/`account_id` 前缀索引；禁止无界 `OFFSET` 分页。

## 11. 未确认项
| 编号 | 问题 | 当前处理 | 需要确认 |
|---|---|---|---|
| U-01 | `GET /v1/changes` 是否返回 `changes[]`、`limit` 语义 | 只保证 `head`；可返回 `changes` 但客户端不消费 | 服务端实现前确认 |
| U-02 | 房间列表排序与 `after` 语义 | 假设按稳定顺序（创建/活跃时间）且 `after` 为最后一个 ID | 是否允许按 `id` 排序 |
| U-03 | `head` 是否严格单调、是否按房间重置 | 客户端当作单调字符串；服务端应保证只增不减 | 保留/重置策略 |
| U-04 | `reads.read_seq` 的写入路径 | 只读；不新增写接口 | 是否有独立已读服务 |
| U-05 | `message.system.created` 生产载荷 | 测试 fixture 为 `{executionTrigger,message}`；客户端只触发重读 | 生产是否同形 |
| U-06 | `history:'shared'` 是否所有群接受 | 仅迁移路径使用 | 新建群是否允许 |
| U-07 | `deferExecution` 延迟时长 | 文档写 2 分钟；客户端只当布尔 | 精确值/是否可配置 |
| U-08 | 1:1 房离线发送 | 协议支持 claim/deliver，但 UI 只读 | 是否有其他入口 |
| U-09 | `execution-failures` 完整字段集 | 客户端只解析安全子集 | 是否还有更多字段 |
| U-10 | `memberJoinedVersion` 能力协商 | 客户端以 400 `INVALID_INPUT` 作为回退信号 | 是否另有能力位 |
| U-11 | presence/typing | Chat Server 明确未使用 | 是否需要独立服务 |
| U-12 | `/ready` 是否在 `/v1` 下 | 文档写 `https://chat.cindy.app/ready` | 网关路由 |
| U-13 | 执行租约 TTL | 建议 60 秒；客户端 15 秒心跳 | 线上实际值 |
| U-14 | 偏好墓碑的 GET 表示 | 建议返回 `value:null` + revision | 是否省略墓碑行 |
| U-15 | 入群欢迎执行的目标集合 | 客户端只声明能力；服务端派发 | 派给全部 Bot/负责人/按设置 |
| U-16 | 入群/欢迎事件的去重键 | 建议 `(room_id, actor_id, message_id)` | 服务端现有 migration 口径 |
| U-17 | 管理动作错误码 | 已列 `OWNER_REQUIRED/ROLE_REQUIRED/ACTOR_NOT_OWNED` | 非主力电脑发布资料的码 |
| U-18 | 计划 `reassign` 在 waiting 的允许范围 | 按 feature parity 允许 pending/failed；源码注释写仅 proposed | 以服务端现网为准 |
| U-19 | 每 socket 16 scope 超限码 | 建议 `scope_error{TOO_MANY_SCOPES}` | 实际码 |
| U-20 | 导入消息是否允许客户端指定 `createdAt` | 产品要求保留原时间；按允许实现 | 服务端字段名/校验 |

---

**一句话结论**：Chat Server 是“账号级 IM + 执行租约调度器 + 伙伴资料键值库”；HTTP 负责权威读写与分页，WSS 只负责“哪个 scope 变了 + 游标”并强制 ack/重读；群聊的发言编排与额度在服务端，Agent 执行与工具权限在执行端；云伙伴的对话层与资料快照在 Chat Server，主力电脑、接管/交出/合并与在线状态在 device-link。
