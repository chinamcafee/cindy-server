# 模型访问与计费实施方案

> 对应能力：MODEL-01…MODEL-32（见 `docs/rebuild/05-model-access.md`）
> 依赖：Foundation、Auth

---

## 1. 实现范围

| 优先级 | 能力 |
|---|---|
| P0 | ListModels v5、推理网关凭据、组织配额、余额、匿名公共 Catalog v5、区域路由 |
| P1 | 凭据轮换、媒体 Guide、BYOK 目录/凭据、计费商品/订单/订阅/套餐变更 |
| P2 | 语音、Embedding、OAuth Broker、供应商分享（device-link 域） |

---

## 2. 包结构

```
internal/modelaccess/
├── handler.go
├── service_models.go      # ListModels v5
├── service_credentials.go # gateway key 下发/轮换
├── service_guides.go      # 媒体 invocation guide
├── service_quota.go       # quota/balance/credit usage
├── service_byok.go
├── billing/               # catalog/orders/subscriptions/plans
├── catalog/               # 匿名公共目录 + revision 发布
├── repo_*.go
└── models.go
internal/voice/            # 会话/ticket/ASR 代理/润色
internal/oauthbroker/      # feature flags、token exchange/refresh
internal/embedding/        # /v1/embeddings 代理（可选）
```

---

## 3. 端点

### 3.1 model-access

| 方法 | 路径 |
|---|---|
| GET | `/api/model-access/models?schemaVersion=5` |
| GET | `/api/model-access/credentials` |
| POST | `/api/model-access/credentials/rotate` |
| GET | `/api/model-access/invocation-guide?modelId=` |
| GET | `/api/model-access/invocation-guides` |
| GET | `/api/model-access/quota` |
| GET | `/api/model-access/balance` |
| GET | `/api/model-access/credit-usage` |
| GET | `/api/model-access/byok/providers?schemaVersion=1` |
| GET | `/api/model-access/byok/credentials?schemaVersion=1` |
| POST/DELETE | `/api/model-access/byok/credentials`（创建/撤销，字段按分册） |

### 3.2 匿名 catalog

| 方法 | 路径 |
|---|---|
| GET | `/api/model-catalog/catalog?registrySchemaVersion=5&registryMedia=1&registryLocalRuntimes=1` |

- 支持 v1–v5 冻结快照；每版本独立 ETag。
- `updatedAt` 规范 UTC；同时间必须同 canonical JSON。
- 只允许顶层 `version/providers/presets/modelRegistry`。

### 3.3 billing

| 方法 | 路径 |
|---|---|
| GET | `/api/billing/catalog` |
| POST | `/api/billing/credit-topup/orders` |
| GET | `/api/billing/credit-topup/orders/{id}` |
| GET/POST | `/api/billing/subscriptions`、`/api/billing/subscription` |
| POST | `/api/billing/subscription/resume` |
| POST | `/api/billing/plan-change-quotes` |
| POST | `/api/billing/portal` |

> 请求/响应字段以分册 §8 与客户端 `shared/billing.ts` 为准。

### 3.4 voice / broker / embedding

| 方法 | 路径 |
|---|---|
| POST | `/api/voice/sessions`（`mode:"dictation"` → ticket + ASR WS URL） |
| WS | ASR（三协议适配：OpenAI realtime / Volcengine / 兼容） |
| POST | `/api/voice/refine`（SSE） |
| GET | `/api/user/feature-flags` |
| POST | `/api/integrations/{slug}/oauth/exchange`、`/refresh` |
| GET | `/api/integrations/oauth/{slug}/bounce` |
| POST | `/v1/embeddings` |

---

## 4. 模型目录

### 4.1 存储

- `ma_providers`、`ma_models`、`ma_catalog_revisions`、`ma_model_availability`。
- 公共 Catalog 由 `ma_catalog_revisions.registry` 快照提供；发布时生成 canonical JSON 与 ETag。
- ListModels v5 从账号可用性 + Gateway 实价 + Registry 元数据投影生成，不直接返回快照。

### 4.2 投影规则

- `availability`：`available` / `requires_payment`（后者展示但不可路由）。
- 能力字段：`modalities`、`efforts`、`defaultEffort`、`supportsFastMode`、`perAgent`。
- 价格：Gateway 实价（per-token）权威；`displayPricing` 仅在请求头 `X-Cindy-Model-Capabilities` 声明时下发。
- `schemaVersion` 只接受 5；错误版本拒绝并保留客户端 LKG。
- 顶层只允许 `schemaVersion/accountTier/models`。
- 空数组表示无模型；5xx 让客户端保留旧快照。

### 4.3 发布流程

draft → 校验（重复 id、价格区间、别名、引用）→ 新 revision（`updatedAt` 前向）→ 生成快照 → 灰度 → 全量。禁止回拨 `updatedAt`。

---

## 5. 凭据与 BYOK

- 网关凭据：服务端持有上游 gateway 的账号 key；`GET /credentials` 返回短期/可轮换的 `{endpoint,apiKey}`；`rotate` 撤销旧 key。
- 加密：BYOK 使用信封加密（DEK + KEK/KMS），只写不读；响应只返回状态与掩码。
- 轮换：`key_version`；旧版本可解密直到数据迁移完成。
- BYOK 组织策略：provider 白名单、数量上限、分享开关。
- 校验：异步校验上游（pending → ready/error），30s 自愈最多 3 次。

---

## 6. 计费与配额

- 金额：最小货币单位 + `currency`；API 金额为十进制字符串。
- 商品目录、套餐、订阅状态机：`active/past_due/canceled/trialing`。
- 订单：`pending/paid/failed/refunded`；幂等键。
- 额度：`ma_credit_ledger` 只追加；`balance` 由流水计算或快照 + 增量。
- 用量：`ma_usage_events` 分区；按账号/模型/日聚合；超额返回业务码。
- 支付：dev 使用 mock provider；生产接 Stripe/支付宝等（不在本期）。

---

## 7. 语音与 Embedding

- 语音会话：`POST /api/voice/sessions` 鉴权 → 分配上游 → 返回一次性 ticket + WS URL；ticket 绑定账号/区域/模式/TTL。
- ASR 代理：WS 双向转发；限制音频帧大小/时长；记录用量。
- 润色：SSE 代理；prompt cache 预热端点。
- 词典：按账号存储，跨设备同步走 device-link 或独立接口（按分册）。
- Embedding：OpenAI 兼容 `/v1/embeddings`；模型白名单；批量/重试上限；不记录输入正文。

---

## 8. OAuth Broker

- feature flags：`GET /api/user/feature-flags` → `{isCanary?, defaultEnableBeta?}`。
- token exchange：`POST /api/integrations/{slug}/oauth/exchange|refresh`，slug 仅 `feishu`、`jira`；client secret 在服务端。
- 上游 401 仅在明确拒绝码时判定 `invalidGrant`；5xx/404 → `SERVICE_UNAVAILABLE`。
- 回弹：`/api/integrations/oauth/{slug}/bounce` 302 到 `127.0.0.1:<redirectPort>`。

---

## 9. 测试

- Catalog v5 结构、ETag、版本快照、revision 冲突。
- ListModels：能力头协商、价格币种、availability、空列表/5xx。
- 凭据轮换：旧 key 失效、并发 GET 稳定。
- BYOK：加密存取、越权、状态机。
- 配额/余额/用量：边界、超额、幂等。
- 订阅/订单：状态机、幂等、退款。
- 语音 ticket 一次性、过期；Embedding 批量上限。
- OAuth：slug 白名单、上游错误映射。

---

## 10. 验收

- 客户端模型选择器可加载 v5 目录并展示价格。
- BYOK 凭据不可读明文；轮换生效。
- 余额/配额/用量与账本一致。
- 公共 Catalog 可被匿名拉取并通过客户端 parser。
