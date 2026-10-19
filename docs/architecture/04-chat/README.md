# Chat Server 实施方案

> 对应能力：CHAT-01…CHAT-27（见 `docs/rebuild/03-chat-server.md`）
> 依赖：Foundation、Auth

---

## 1. 实现范围

| 优先级 | 能力 |
|---|---|
| P0 | `/me`、actor 目录、建群/1:1 房、房间快照、消息读写、媒体、WSS 订阅/补读、执行租约、健康检查 |
| P1 | 群设置/成员管理/归档/解散、Thread、邀请、列表态、变更头、执行失败、分工、云伙伴偏好 |
| P2 | 回应分页、Thread 贴回、继续讨论、镜像导入、跨账号群邀请完整流程 |

---

## 2. 包结构

```
internal/chat/
├── handler.go        # /v1 路由
├── ws.go             # /v1/ws 升级、订阅、ack
├── hub.go            # 房间/账号订阅与扇出
├── service_rooms.go
├── service_messages.go
├── service_actors.go
├── service_exec.go   # 执行领取/心跳/终态
├── service_plans.go
├── service_media.go
├── repo_*.go
├── models.go
└── events.go         # 事件类型与序列化
```

---

## 3. 数据模型要点

见 `11-database` §4。补充：

- `chat_messages.seq` 每房间单调递增；用 `UPDATE chat_rooms SET last_seq = last_seq + 1 RETURNING last_seq` 在事务内分配，或 `INSERT ... SELECT coalesce(max(seq),0)+1` 加房间行锁。
- `seq` 对外字符串（可超 JS 安全整数）。
- `chat_room_events` 记录成员/设置变更，推进房间 `head`；消息本身也推进 `head`。
- `chat_room_members.last_read_seq` 用于未读与 `reads`。
- `chat_executions.epoch` 每次领取/接管/撤权递增；`lease_expires_at` 建议 60s。
- `chat_companions` 与 `chat_preferences` 存云伙伴资料键值；键值 CAS。
- 媒体附件先 `chat_media`（或 `file_objects` + `chat_attachments`）登记，消息引用 `mediaId`。

---

## 4. HTTP 路由（精确）

所有路径前缀 `/v1`。写请求必须 `operationId`。

| 方法 | 路径 |
|---|---|
| GET | `/v1/me` |
| POST | `/v1/profile/refresh` |
| GET/POST | `/v1/actors` |
| PATCH | `/v1/actors/{actorId}` |
| POST | `/v1/actors/{actorId}/profile` |
| GET/POST | `/v1/preferences` |
| GET/POST | `/v1/conversations` |
| PATCH | `/v1/conversations/{roomId}` |
| POST | `/v1/conversations/{roomId}/dissolve` |
| GET | `/v1/conversations/{roomId}/snapshot` |
| GET/POST | `/v1/conversations/{roomId}/members` |
| POST | `/v1/conversations/{roomId}/bot-access` |
| POST | `/v1/conversations/{roomId}/nickname` |
| POST | `/v1/conversations/{roomId}/transfer` |
| POST | `/v1/conversations/{roomId}/list-state` |
| GET | `/v1/conversation-list-state` |
| GET/POST | `/v1/conversations/{roomId}/messages` |
| GET | `/v1/conversations/{roomId}/messages/{messageId}` |
| POST | `/v1/conversations/{roomId}/messages/{messageId}/continue` |
| POST | `/v1/conversations/{roomId}/messages/{messageId}/cancel-planning` |
| POST/GET | `/v1/conversations/{roomId}/messages/{messageId}/reactions` |
| POST | `/v1/executions/claim` |
| POST | `/v1/conversations/{roomId}/executions/{executionId}` |
| POST | `/v1/conversations/{roomId}/executions/{executionId}/control` |
| GET | `/v1/conversations/{roomId}/executions` |
| GET | `/v1/conversations/{roomId}/execution-failures` |
| GET/POST | `/v1/conversations/{roomId}/plans` |
| POST | `/v1/conversations/{roomId}/plans/{planId}` |
| POST | `/v1/conversations/{roomId}/plans/{planId}/messages` |
| POST | `/v1/media/prepare`、`/v1/media/{id}/complete`、`GET /v1/media/{id}` |
| POST | `/v1/conversations/{roomId}/invites`、`DELETE /v1/invites/{id}`、`GET /v1/invites/{token}`、`POST /v1/invites/{token}/accept` |
| GET | `/v1/changes` |
| GET | `/v1/ready` |
| WSS | `/v1/ws` |

> 媒体/邀请路径以分册为准；实现前回查 `docs/rebuild/03-chat-server.md` 的对应小节。

---

## 5. WSS

### 5.1 连接

- 升级前校验 Bearer；从 token 取 account/membership/device。
- 首帧 `subscribe`：`{scopes:[...], after?:{roomId:seq}, ack?}`。
- 服务端推送 `{type, seq?, roomId?, payload, changeId?}`。
- 客户端回 `ack {changeId}`；服务端保留每连接待确认窗口（建议 100），超窗断开重连。
- 断线重连用 `after` 补读；游标低于保留下界 → `RESET_REQUIRED` + `head`。

### 5.2 事件类型（最小集）

`message.created`、`message.updated`、`message.deleted`、`reaction.updated`、`room.updated`、`room.dissolved`、`member.joined`、`member.left`、`member.updated`、`execution.claimed`、`execution.updated`、`execution.finished`、`plan.updated`、`read.updated`、`change.reset`。

事件持久化到 `chat_room_events`/消息表，`changeId` 单调；hub 只负责在线扇出，离线靠补读。

### 5.3 多实例

- 本地 hub：`map[roomID]map[connID]*Conn` + `map[accountID]...`。
- Redis Pub/Sub 频道 `chat:events`；每实例订阅并按本地连接分发。
- 事件先写 PG 再发布，避免丢失。

---

## 6. 关键语义

### 6.1 消息发送

1. 鉴权 → actor 归属/角色 → 房间状态（joined、未归档/解散）。
2. 幂等（account+operationId）。
3. 校验 mentions（全部 joined）、quote 可见性、threadRoot/replyTo 一致性、附件归属。
4. 事务：分配 seq → 插入消息 → 更新 room.head → 写 outbox（事件）。
5. 返回 `{id,seq,createdAt}`。
6. hub 扇出。

### 6.2 执行租约

- `claim`：找该 bot 的待执行（按 room/seq 排序），校验设备=homeDeviceId；`FOR UPDATE SKIP LOCKED`；设置 epoch、lease、context_seq；返回。
- `heartbeat`：校验 epoch，续租。
- `complete`：校验 epoch/租约；写 bot 消息（幂等键 `chat:<execId>:<epoch>:complete`）；清租约。
- `fail`：记录失败码，不写消息。
- 过期 worker：租约过期 → 标记可重领。

### 6.3 媒体

- `prepare`：校验大小（≤100MB/附件，≤20 个）、MIME → 生成对象 key → 预签名 PUT → 返回 `{mediaId, uploadUrl, headers, expiresAt}`。
- `complete`：HEAD 校验 size/etag → 标记已封存 → 返回媒体元数据。
- `GET`：校验房间成员 → 预签名 GET 或重定向。
- 消息引用未封存媒体 → 400 `INVALID_ATTACHMENT`。

### 6.4 1:1 房

- `(owner_account_id, kind='direct', source_id)` 唯一；并发创建返回同一房间。
- 镜像导入按 `(room_id, source_id)` 去重。

---

## 7. 错误码

按分册 §2.5 全表实现。重点：

- `OPERATION_CONFLICT`、`REVISION_CONFLICT`、`HOME_DEVICE_CONFLICT`、`PLAN_OPEN`、`PLAN_CLOSED`、`LAST_OWNER`、`CONVERSATION_ARCHIVED`、`CONVERSATION_DISSOLVED`、`UPLOAD_EXPIRED`、`RESET_REQUIRED`、`MENTION_UNAVAILABLE`、`QUOTE_UNAVAILABLE`、`REPLY_THREAD_MISMATCH`、`ATTACHMENT_*`。

---

## 8. 测试

- 幂等：同 operationId 并发只写一条；不同参数 409。
- seq：并发 100 条无重复、无空洞。
- mentions 失效整条拒绝。
- Thread 回复只有一层。
- 执行：并发 claim 只一个成功；epoch 过期拒绝；心跳续租；终态幂等。
- WSS：订阅、顺序、ack、断线补读、RESET_REQUIRED。
- 权限：非成员 404；角色不足 403。
- 1:1 房并发创建唯一。

---

## 9. 验收

- 桌面/移动端可完成建群、发消息、收到 WSS、断线补读。
- Bot 执行领取→心跳→完成→消息落库。
- 所有写请求幂等；所有列表有游标。
