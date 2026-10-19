# Device-Link 实施方案

> 对应能力：DL-01…DL-11（见 `docs/rebuild/04-device-link.md`）
> 依赖：Foundation、Auth

---

## 1. 实现范围

| 优先级 | 能力 |
|---|---|
| P0 | WSS 握手/路由/presence/ping、设备目录、关闭码、媒体签票、ICE |
| P1 | 共享任务、供应商分享、推送 notify、心跳 |
| P2 | 设备身份 ES256、跨区 guest 完整流程、背压策略 |

---

## 2. 包结构

```
internal/devicelink/
├── handler.go        # REST + WS 升级
├── hub.go            # 账号/设备连接表、路由、广播
├── conn.go           # 单连接读写泵、限速、关闭
├── envelope.go       # 信封解析/构造（RawMessage payload）
├── presence.go       # Redis presence/route
├── service_devices.go
├── service_media.go
├── service_ice.go
├── sharedtask/       # 共享任务 service/repo
├── providershare/    # 供应商分享 service/repo
├── push/             # APNs/FCM 适配与 outbox worker
└── identities.go     # ES256 挑战（P2）
```

---

## 3. WSS 设计

### 3.1 连接生命周期

```
Upgrade → 鉴权（Bearer 或 ProviderShareGuest）
→ 15s 内必须 hello → 校验协议版本/能力
→ 写 hello-ack（分配 deviceId）→ 注册 hub + presence
→ 读泵（限制帧 2MiB、速率、未知 kind 丢弃）
→ 写泵（有界队列，满则 1013/断开）
→ 断开：清理路由、广播离线、写 lastSeenAt
```

- 顶号：同账号同 deviceId 新连接 → 旧连接 4409。
- 账号连接上限 → 4429。
- 设备被移除 → 后续 hello 4410；在线连接主动关闭 4410。
- 版本不符/非法首帧 → 4400；鉴权失败 → 4401。

### 3.2 信封

```go
type Envelope struct {
  V             int             `json:"v"`
  Kind          string          `json:"kind"`
  ID            string          `json:"id,omitempty"`
  Src           string          `json:"src,omitempty"`
  Dst           string          `json:"dst,omitempty"`
  SharedTask    *SharedTaskScope `json:"sharedTask,omitempty"`
  ProviderShare *ProviderShareScope `json:"providerShare,omitempty"`
  Payload       json.RawMessage `json:"payload,omitempty"`
}
```

- `src` 始终由服务端写入；忽略客户端值。
- `payload` 不反序列化（`RawMessage`）。
- 转发前校验：`dst` 在线、同账号或有效 scope、`CONTROL_KINDS` 的 `remoteControlEnabled`。

### 3.3 路由

- `hub.Route(accountID, envelope)`：查本地连接；若目标在其他实例，通过 Redis 定向频道 `dl:node:{nodeId}` 转发（或一致性哈希）。
- 不做消息持久化；不在线直接 `DEVICE_OFFLINE`。
- `presence-changed` 广播同账号所有连接。
- `notify` 不转发：解析目标 token → 入 outbox → APNs/FCM。

### 3.4 背压

- 每连接写队列上限（如 256 帧/4 MiB）；超限关闭 1013。
- 读速率限制（如 200 帧/s）；超限 `RATE_LIMITED` 或断开。

---

## 4. Presence / 路由

Redis：

| Key | 值 | TTL |
|---|---|---|
| `dl:presence:{accountId}` (hash) | deviceId → `{online,busy,remoteControlEnabled,lastSeenAt,nodeId}` | 90s |
| `dl:route:{deviceId}` | `{accountId,nodeId,connId}` | 90s |
| `dl:conn_count:{accountId}` | 计数 | 窗口 |

- hello 写库 + Redis；ping 刷新 TTL；断开删 route 并更新 presence online=false。
- 设备列表 = DB 档案 ∪ presence 三态（在线/离线/未知）。
- 多实例：presence 以 Redis 为准，本地 hub 只负责本实例连接。

---

## 5. REST

按分册 §2.1 实现。要点：

- 设备列表返回 `DeviceView[]`（含 `isSelf`、`online`、`lastSeenAt`）。
- PATCH 改名；DELETE 写 `removed_at` 并断开（4410）。
- 推送 token upsert；注销删除。
- 媒体签票：校验 `key` 归属（解析 key 内 userId），私有 ACL。
- ICE：TURN REST 凭证（`username=<expiry>:<accountId>`，HMAC-SHA1），≤4 条。

---

## 6. 共享任务

- 表：`dl_shared_tasks`、`dl_shared_task_guests`、`dl_shared_task_invitations`。
- 邀请 token：43 字符随机；只存哈希；一次性；TTL 7d。
- 路由 scope 校验：active、成员、target 设备绑定、双方 capability；`source` 重写。
- 补偿 worker：每 30s 扫描超时/失效任务并关闭。
- 附件 key 必须包含 `sharedTaskId`。

---

## 7. 供应商分享

- 表：`dl_provider_shares`、`_members`、`_requests`、`_guest_credentials`。
- 申请：4 位配对码；审批/拒绝；状态机。
- 跨区：名片 + requestToken → guest credential（24h，存哈希）。
- guest 连接：只允许 `providerShare` scope；不进 presence；禁止订阅。
- 写操作限流。

---

## 8. 推送

- `notify` payload 校验长度；查目标 token（排除发送方）；按 `appVariant/apnsEnv` 选择 APNs 环境。
- 入 outbox；worker 调 APNs/FCM；失败退避；失效 token 删除。
- dev 模式写日志不真发。

---

## 9. 心跳

- `POST /heartbeat`：`{uid, platform?, version?}` → `{ok:true}`。
- 无鉴权；按 IP 限流；写 `hb_daily` upsert；不参与 presence。

---

## 10. 设备身份（P2）

- `POST /identities/challenges`：生成 nonce，绑定 path/tokenHash/bodyHash/deviceId，TTL 120s。
- `POST /identities`：校验 ES256 签名 → 存公钥。
- relay/push 挑战：在 WS 升级/推送注册时校验签名头；与 Bearer 并存。

---

## 11. 错误与关闭码

严格对齐客户端常量：

- `RelayErrorCode`：`DEVICE_OFFLINE`、`REMOTE_DISABLED`、`VERSION_MISMATCH`、`PAYLOAD_TOO_LARGE`、`RATE_LIMITED`、`BAD_REQUEST`、`INTERNAL`。
- 关闭码：4400/4401/4409/4410/4429/1013。
- 错误帧回带原 `id`。

---

## 12. 测试

- 两个 WS 客户端：hello、路由、presence、ping。
- 跨账号不可达；`remoteControlEnabled=false` 时控制帧拒绝、回程可通。
- 顶号/移除/超限/版本/大帧/未知 kind。
- 共享任务：邀请一次性、非成员拒绝、关闭后拒绝。
- 供应商分享：审批后 guest 可连、撤销即时失效。
- 媒体 key 越权；ICE 凭证有效期。
- 推送：notify 频控、失效 token 清理。

---

## 13. 验收

- 客户端两台设备可互发 invoke/push。
- 关闭被控开关后无法控制，但会话收尾可通。
- 共享任务/供应商分享端到端可用。
- 所有关闭码与错误码可被客户端正确分类。
