# Device-Link 设备互联中继与协作

> 能力编号前缀：`DL-`
> 证据等级：**契约**（协议包 + 严格 parser）、**文档**（产品/开发规则）、**推测**（服务端内部实现）。
> 协议版本：`PROTOCOL_VERSION = 1`，append-only，靠 capability 协商演进。

---

## 1. 能力总览

| 编号 | 能力 | 消费端 | 优先级 |
|---|---|---|---|
| DL-01 | WSS 中继：hello/hello-ack、presence、ping/pong、路由转发 | desktop/mobile | P0 |
| DL-02 | 设备目录 REST：列表/改名/移除 | desktop/mobile | P0 |
| DL-03 | 路由授权：`src` 覆写、`remoteControlEnabled` 校验、同账号隔离 | relay | P0 |
| DL-04 | 连接治理：顶号 4409、超限 4429、移除 4410、版本 4400、鉴权 4401、拥塞 1013 | relay | P0 |
| DL-05 | 媒体签票：presign-put/get/delete，私有对象 | desktop/mobile | P0 |
| DL-06 | ICE/TURN 配置下发 | mobile/desktop viewer | P0 |
| DL-07 | 推送 token 注册与 `notify` 下发（APNs/FCM） | mobile/desktop | P1 |
| DL-08 | 共享任务 REST + relay 跨账号 scope | desktop/mobile | P1 |
| DL-09 | 供应商分享 REST + 跨区 guest 凭证 | desktop/mobile | P1 |
| DL-10 | 设备身份挑战/登记（ES256） | mobile（预留） | P2 |
| DL-11 | heartbeat 账号在线心跳 | desktop | P1 |

---

## 2. 传输与端点

| 项 | 值 |
|---|---|
| WS 路径 | `/api/device-link/ws` |
| 鉴权 | `Authorization: Bearer <accessToken>`；或 `Authorization: ProviderShareGuest <credential>` |
| URL | base `http→ws` / `https→wss` |
| 帧 | 单帧 JSON；协议上限 2 MiB，ws 层硬上限 4 MiB |
| 关闭码 | 1000 正常、1006 传输/心跳/握手超时、1013 relay 拥塞、4400 版本/非法帧、4401 鉴权、4409 顶号、4410 设备已移除、4429 连接超限 |
| 心跳 | 客户端 ping 10–20 s；pong 缺失 + 无入站帧超时判死；服务端应刷新 route TTL |
| 重连 | 指数退避 1 s→30 s，抖动 0.7–1.0；稳定 10 s 后清零；同一 deviceId 新连接顶掉旧连接 |

### 2.1 REST 端点

| 方法 | 路径 | 用途 | 请求/响应 |
|---|---|---|---|
| GET | `/api/device-link/devices` | 设备列表 | → `DeviceView[]` |
| PATCH | `/api/device-link/devices/{id}` | 改名 | `{name}` |
| DELETE | `/api/device-link/devices/{id}` | 移除 | 保留移除标记；后续 hello → 4410 |
| PUT | `/api/device-link/push-token` | 注册推送 | `{token, platform:'ios', provider:'apns', appVariant:'cn'|'global', apnsEnv}` |
| DELETE | `/api/device-link/push-token` | 注销推送 | — |
| GET | `/api/device-link/ice-servers` | ICE 配置 | `{iceServers:[{urls,username,credential}],expiresAt}`，≤4 条 |
| POST | `/api/device-link/media/presign-put` | 媒体上传签票 | `{size, ext, contentType, sharedTaskId?}` → `{putUrl,key,expiresAt}` |
| POST | `/api/device-link/media/presign-get` | 媒体下载签票 | `{key}` → `{getUrl,expiresAt}` |
| DELETE | `/api/device-link/media` | 删除对象 | `{key}` → `{deleted}` |
| GET/POST | `/api/device-link/shared-tasks…` | 共享任务 | 见 §5 |
| GET/POST | `/api/device-link/provider-shares…` | 供应商分享 | 见 §6 |
| POST | `/api/device-link/identities/challenges` | 设备身份挑战 | 见 §7 |
| POST | `/api/device-link/identities` | 登记设备公钥 | 见 §7 |
| POST | `/api/device-link/identities/relay-challenges` | relay 挑战 | 见 §7 |
| POST | `/api/device-link/identities/push-challenges` | push 挑战 | 见 §7 |
| GET | `/shared-task/join#<token>` | 公开邀请页 | 43 字符 token 放 fragment |
| GET | `/chat-invite/join#<token>` | 群邀请页 | token 放 fragment |

---

## 3. 中继协议

### 3.1 信封

```ts
type Envelope = {
  v: 1;
  kind: EnvelopeKind;
  id?: string;            // 请求配对；routed 帧缺省自动补
  src?: string;           // 服务端写入，客户端传值被覆盖
  dst?: string;           // 目标物理 deviceId
  sharedTask?: { sharedTaskId; target:{role:'host'}|{role:'guest',memberId}; source? };
  providerShare?: { shareId; target:{role:'host'}|{role:'guest',memberId}; source? };
  payload?: unknown;
};
```

范围字段二选一；`source` 由服务端填写，客户端不采信。

### 3.2 kind 全集

| 集合 | kind | 服务端行为 |
|---|---|---|
| 连接层 | `hello` | 首帧必须；注册设备并回 `hello-ack` |
| | `hello-ack` | 服务端→客户端，含 `serverProtocolVersion`、`deviceId`、`userId`、`capabilities` |
| | `presence-set` | 更新 busy/remoteControlEnabled 等 |
| | `presence-changed` | 向同账号在线设备广播 |
| | `ping` / `pong` | 刷新 lastSeen/route TTL；原样回 pong |
| | `notify` | 服务端消费，转 APNs/FCM，不转发 |
| 隧道层 | `link-open`、`link-accept`、`link-close`、`invoke`、`invoke-result`、`push` | 按 dst 转发 |
| 错误 | `relay-error` | 回带原帧 id |

- `ROUTED_KINDS`：`link-open/link-accept/link-close/invoke/invoke-result/push`。
- `CONTROL_KINDS`：`link-open/invoke`；转发前校验目标 `remoteControlEnabled === true`，否则 `REMOTE_DISABLED`。
- 回程豁免：`link-accept/invoke-result/push/link-close` 不受开关限制。
- 未知 kind 静默丢弃（因此新能力必须 capability 协商）。

### 3.3 握手 payload

```json
// hello
{
  "deviceName": "...", "platform": "darwin|win32|linux|ios|android",
  "appVersion": "...", "remoteControlEnabled": false, "busy": false,
  "deviceInfo": {"cpuLabel":"...","memoryGb":16,"osVersion":"...","modelLabel":"..."},
  "capabilities": ["shared-task-v2","provider-share-v1"]
}
// hello-ack
{
  "serverProtocolVersion": 1, "deviceId": "srv-assigned", "userId": "...",
  "capabilities": ["notify","shared-task-v2","provider-share-v1"]
}
```

服务端能力集 append-only；客户端据此 gate 新帧。

### 3.4 错误码

`RelayErrorCode`：`DEVICE_OFFLINE`、`REMOTE_DISABLED`、`VERSION_MISMATCH`、`PAYLOAD_TOO_LARGE`、`RATE_LIMITED`、`BAD_REQUEST`、`INTERNAL`。

- `DEVICE_OFFLINE` 同时覆盖“不在线”与“不属于本账号”，防探测。
- 错误载荷 `{code,message,dst?}`。

---

## 4. 服务端职责边界

### 4.1 必须做

1. 鉴权与账号命名空间隔离。
2. 设备注册/在线状态/路由表：`hello` 写库，`ping` 刷 TTL，断开置离线。
3. `src` 覆写；`CONTROL_KINDS` 开关校验；`dst` 同账号或授权 scope 校验。
4. `presence-changed` 广播；`notify` 消费与推送。
5. 连接治理：顶号、上限、移除、版本、背压。
6. 共享任务/供应商分享的授权与成员版本校验，scope `source` 重写。
7. 媒体签票与对象归属校验（key 内嵌 userId）。
8. ICE/TURN 短期凭证。
9. 公开邀请落地页（token 在 fragment）。

### 4.2 明确不做

- 不解析 `invoke.channel/args`、`push.channel/payload`。
- 不保存会话/消息/任务内容。
- 不做 IPC 权限（allowlist、撤权、本地开关在设备侧）。
- 不代理媒体字节。
- 不转发/解析远程桌面 SDP、输入、剪贴板（均为普通 invoke payload）。
- 不落盘 notify 正文。

---

## 5. 共享任务

### 5.1 数据模型

```ts
SharedTask {
  sharedTaskId, sessionId, ownerAccountId, hostDeviceId,
  revision, status: 'active'|'closed', createdAt, closedAt?
}
SharedTaskGuest { memberId, accountId, version, deviceIds[], joinedAt }
SharedTaskInvitation { token(43), sharedTaskId, expiresAt, usedAt?, createdBy }
```

- 创建时最多 2 个 guest；读历史快照容忍 3。
- 附件路径强绑 `sharedTaskId`。
- 服务端每 30 s 补偿扫描，关闭超时/失效任务。

### 5.2 REST

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/device-link/shared-tasks` | 我参与的列表 |
| POST | `/api/device-link/shared-tasks` | 创建（绑定 sessionId） |
| GET | `/api/device-link/shared-tasks/{id}` | 快照 |
| POST | `/api/device-link/shared-tasks/{id}/invite` | 生成 43 字符邀请 token |
| POST | `/api/device-link/shared-tasks/join` | 凭 token 加入 |
| GET | `/api/device-link/shared-tasks/{id}/members` | 成员 |
| DELETE | `/api/device-link/shared-tasks/{id}/members/{memberId}` | 移除 |
| POST | `/api/device-link/shared-tasks/{id}/leave` | 退出 |
| POST | `/api/device-link/shared-tasks/{id}/close` | 关闭（owner） |

### 5.3 relay scope 授权

带 `sharedTask` 的帧：

- 校验 sharedTask 状态 active、双方为成员、`target` 与真实设备绑定匹配、双方声明 `shared-task-v2`。
- 服务端重写 `source` 为调用方角色；同帧不得携带 `providerShare`。
- 非成员一律 `DEVICE_OFFLINE`/`REMOTE_DISABLED`，不泄露任务存在性。

---

## 6. 供应商分享

### 6.1 模型

```ts
OwnerShare {
  shareId, providerId, providerLabel, hostDeviceId, createdAt, status,
  members: [{memberId, accountId, displayName, status, version}],
  requests: [{requestId, accountId, status, pairingCode, createdAt, expiresAt}]
}
ReceivedShare {
  shareId, memberId, providerId, providerLabel, hostDeviceId, deviceName,
  owner: {accountId, displayName, avatarUrl?}, status:'active'|'paused',
  hostOnline, hostCapable
}
```

- 申请状态：`pending|approved|rejected|withdrawn|expired`。
- 4 位配对码仅供人对人核对，服务端随机生成补零。
- 跨区分享：名片 + requestToken + `ProviderShareGuest` 凭证（24 h，只存摘要，轮换即失效）。

### 6.2 REST（owner/guest 两套）

| 方法 | 路径 | 用途 |
|---|---|---|
| GET/POST | `/api/device-link/provider-shares` | 我的分享/创建 |
| GET | `/api/device-link/provider-shares/{id}` | 详情 |
| PATCH | `/api/device-link/provider-shares/{id}` | 暂停/恢复/改成员 |
| DELETE | `/api/device-link/provider-shares/{id}` | 撤销 |
| GET | `/api/device-link/provider-shares/received` | 我收到的 |
| POST | `/api/device-link/provider-shares/{id}/requests` | 申请访问 |
| POST | `/api/device-link/provider-shares/{id}/requests/{requestId}/approve|reject` | 审批 |
| GET/POST | `/api/device-link/provider-shares/x/*` | 跨区名片/requestToken/credential |

relay 只允许带 `providerShare` scope 的 guest 连接；guest 不进入 presence、不能订阅 push。写操作按账号/guestKey/IP 限流。

---

## 7. 设备身份（P2）

- P-256 密钥 + ES256 挑战签名。
- 挑战绑定：`path`（`/api/device-link/ws` 或 `/api/device-link/push-token`）、`tokenHash`、`bodyHash`、`authDeviceId`、`expiresAt`（1–120 s）。
- 头：`x-cindy-identity-*`。客户端 TS 侧尚未全量接线，服务端可先实现并与 Bearer 并存。

---

## 8. 推送 notify

hello 声明 `notify` 能力后，同账号设备可发送：

```json
{
  "kind": "notify",
  "targetDeviceId?": "...",
  "title": "<=?  ", "body": "<=?  ",
  "collapseId?": "...",
  "sender": {"displayName":"...","avatarBytes?":"<=2048"}
}
```

服务端：查目标推送 token（排除发送方）→ APNs/FCM；频控超限 `RATE_LIMITED`；字段长度上限；不落盘正文。

---

## 9. ICE / TURN

- `GET /api/device-link/ice-servers` 返回 ≤4 条 ICE server，凭证有效期需留 2 分钟余量。
- 客户端 8 s 超时，失败回退公共 STUN。
- 服务端可自建 coturn（TURN REST 凭证：`username = expiry:accountId`，`credential = HMAC-SHA1(secret, username)`）。

---

## 10. 媒体签票

- 对象上限 2 GiB；私有 ACL；key 内嵌 `userId`，服务端校验请求方与 key 归属。
- 上传头固定 `Content-Type` + `x-oss-object-acl: private`。
- 大于 64 MiB 客户端流式 PUT；无 multipart。
- 删除为 best-effort；生命周期由服务端/桶策略负责。
- 共享任务附件 key 必须含 `sharedTaskId`。

---

## 11. 心跳

- `POST {heartbeatUrl}/heartbeat`，body `{uid, platform?, version?}`，响应 `{ok:true}`。
- 无鉴权；桌面每 60 s，仅 cloud 登录态；5 s 超时；失败仅 warn。
- 建议在线 TTL 90 s（60 s 心跳 + 30 s 容错）；仅用于 DAU/在线统计，不参与 relay 路由。

---

## 12. Go 实现要点

- `internal/devicelink/hub.go`：每账号设备连接表（`map[accountID]map[deviceID]*Conn`），连接互斥与顶号。
- 路由转发不反序列化 payload（`json.RawMessage`），仅解析信封头。
- presence/route 用 Redis Hash + TTL；DB 只在 hello/改名/移除时写。
- `internal/devicelink/sharedtask`、`providershare` 独立 service/repository。
- 媒体签票封装 S3 presigner；key 生成校验归属。
- 推送 worker 消费 outbox，按 token/platform 分发。
- 关闭码、错误码必须与客户端常量一致。

---

## 13. 未确认项

1. presence/route TTL 精确值与 Redis key 结构。
2. notify 频控阈值、单账号连接上限。
3. 共享任务补偿扫描的具体失效条件。
4. 跨区 guest 凭证的签名算法与轮换周期（契约只给 24 h 语义）。
5. 设备身份是否已线上启用。
