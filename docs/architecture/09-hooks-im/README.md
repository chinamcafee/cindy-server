# Hook Server 与 IM 渠道实施方案

> 对应能力：HOOK-01…HOOK-34（见 `docs/rebuild/09-hooks-im.md`）
> 依赖：Foundation、Auth、Chat（可选）

---

## 1. 实现范围

| 优先级 | 能力 |
|---|---|
| P0 | 三条 WSS lane 接入、hello/welcome、Bearer 鉴权、设备互斥、task.dispatch/ack、turn.end 出箱、命令分发 |
| P1 | 交互卡、权限确认、进度、turn.reopen/delivery、绑定（Slack OIDC / Telegram 深链 / X OAuth2）、偏好、附件、群中继 |
| P2 | Slack multi-team、工具代理、上下线通知、审计看板 |

> 实现顺序建议：先 Telegram（协议简单、易本地验证），再 Slack，最后 X。

---

## 2. 包结构

```
internal/hooks/
├── handler.go          # /ws 升级 + 平台 webhook
├── hub.go              # lane/账号/设备连接表
├── envelope.go         # v/type/id/ts/payload
├── protocol.go         # 42 类型常量与解析
├── dispatch.go         # task.dispatch/ack、幂等
├── turns.go            # progress/end/reopen/delivery
├── interactions.go     # ask/plan/permission
├── bindings.go
├── prefs.go
├── commands.go
├── outbox.go           # 终稿/进度出箱
├── channels/
│   ├── slack/          # Events API + Web API
│   ├── telegram/       # Bot API
│   └── x/              # X API
└── repo.go
```

---

## 3. 传输与握手

- 三条独立 WSS：`slack-hook` / `telegram-hook` / `x-hook`；服务端可同进程按 Host 或路径区分。
- 路径：兼容 `/` 与 `/ws`；升级头 `Authorization: Bearer <accessToken>`。
- 首帧 `hello`：声明 `deviceId`、`features`、`workspaces`；服务端回 `welcome`（`features` 是能力子集）。
- 同账号同 deviceId：first-wins；后到连接 close `4000`。
- 心跳 30s；静默 90s 判死并释放槽位；断线后 `turn.end` 入出箱。

信封：

```json
{"v":1,"type":"task.dispatch","id":"uuid","ts":1760000000000,"payload":{}}
```

---

## 4. 消息类型（42）

按分册逐项实现。最小可运行集：

- 连接：`hello`、`welcome`、`ping`、`pong`、`error`。
- 任务：`task.dispatch`、`task.ack`（accepted/queued/rejected）、`task.cancel`。
- 轮次：`turn.progress`、`turn.end`、`turn.reopen`、`turn.delivery`。
- 会话：`session.new`/`session-new`、`session.result`、`session.archive`。
- 交互：`interaction.request`、`interaction.response`、`permission.request`、`permission.response`。
- 绑定：`bind.request`、`bind.state`、`provider.bind.*`。
- 偏好：`prefs.get/set`、`provider.prefs.*`、`telegram.prefs.*`。
- 工具：`tool.request`、`tool.response`。
- 查询：`workspaces.list`、`models.list`、`sessions.list`。
- 群中继：`group.message`。
- 生命周期：`lifecycle.preference`、`status`。

未知 type：丢弃不断连；新能力必须 feature gate。

---

## 5. 数据模型

见 `11-database` §9：`hook_channels`、`hook_bindings`、`hook_sessions`、`hook_messages`、`hook_confirmations`、`hook_group_relays`、`hook_delivery_attempts`。

要点：

- `externalKey` 不透明；同一 key 映射同一 session（desktop 可重建）。
- 群聊内容不落库；只存 `chatId ↔ principal` 元数据。
- `hook_messages` 只存协议帧与状态，用于重放/审计；可按保留期清理。
- `hook_confirmations` 记录每次权限请求与最终决定；不可代答。

---

## 6. 核心流程

### 6.1 入站消息 → 任务

1. 平台 webhook/长轮询接收 → 去重（event_id/update_id/mention id）。
2. 解析渠道/租户/用户 → 查 binding → 找在线设备连接。
3. 命令？→ 服务端执行（`/new`、`/stop`、查询等）。
4. 否则 `task.dispatch`（requestId 幂等）→ 等 `task.ack`。
5. 超时/拒绝 → 用户可见错误；不重复派发。

### 6.2 轮次

- desktop `turn.progress`：latest-wins；服务端更新/发送进度消息。
- `turn.end`：写 outbox → 渠道发布 → 记录终态；失败重试（退避，24h 放弃）。
- X：`turn.delivery` 两阶段回执；每次公开回帖计费。
- 断线：未结束轮次孤儿收口；恢复后补发。

### 6.3 权限确认

- desktop 发 `interaction.request`（ask/plan/permission）→ 服务端渲染渠道卡片。
- 用户点击 → 服务端校验来源/时效/成员 → 回 `interaction.response`。
- 多端同一决定：以 desktop 最终决定为准；渠道回答不代表执行已恢复。
- 30 min 超时；迟到点击拒绝。

### 6.4 绑定

- Slack：OIDC（Sign in with Slack）→ 绑定账号；multi-team 用 `bind.state`。
- Telegram：深链 token（签名、短 TTL）→ 绑定。
- X：OAuth2 PKCE。
- 绑定是设备级；同一账号多设备按 principal 路由。

---

## 7. 渠道适配

| 渠道 | 入站 | 出站 |
|---|---|---|
| Slack | Events API webhook（验签、URL challenge） | Web API：postMessage/update/delete/reactions/files |
| Telegram | Bot API update（webhook 或长轮询） | sendMessage/editMessageText/deleteMessage/setMessageReaction/sendChatAction/sendMediaGroup/setMyCommands |
| X | 提及/回复事件（filtered stream 或轮询） | 创建回复/帖子、媒体上传 |

- 凭证只在服务端；dev 用 mock channel 记录出站。
- 429 退避；平台错误映射为稳定码。
- 官方 bot 与个人 bot 的命令面差异按产品台账，不强行统一。

---

## 8. 可靠性

- `requestId`/`opId` 贯穿；dispatch/ack/终态幂等。
- 出站 outbox 至少一次；渠道去重（幂等键/消息 ID 映射）。
- 设备槽位回收；重连 standby 探测。
- 限流：每账号/每渠道/每群。
- 审计：绑定、权限决定、命令、终稿投递。

---

## 9. 测试

- 握手/鉴权/设备互斥（4000）。
- dispatch 幂等、ack 三态、超时。
- turn.end 出箱、重试、24h 放弃。
- 交互卡与权限确认（多端、迟到、超时）。
- 绑定/解绑/多团队。
- 群中继不落内容。
- 渠道 mock：出站格式、429、失效凭证。
- 未知 type/字段宽容。

---

## 10. 验收

- 本地 mock 渠道可完成“消息 → 任务 → 进度 → 终稿”闭环。
- 同一设备重复连接被拒；断线后终稿不丢。
- 权限确认在桌面与 IM 得到同一决定。
- 42 类型解析器与客户端测试样例一致。
