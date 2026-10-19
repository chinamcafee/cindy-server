# 基础平台（Foundation）实施方案

> 对应能力：SVC-01…、横切基础设施
> 依赖：无（最先实现）

---

## 1. 目标

提供所有业务域共享的运行底座：配置、日志、HTTP、鉴权、数据库、缓存、对象存储、幂等、审计、限流、迁移、后台任务。业务域不得自行创建这些基础设施。

---

## 2. 包结构

```
internal/platform/
├── config/        # env 解析、校验、默认值
├── obs/           # slog、request id、metrics、otel
├── db/            # pgxpool、TxManager、goose
├── cache/         # Redis client、key 规范、锁
├── objectstore/   # S3 client、bucket、presigner
├── authn/         # JWT 签发/验签、JWKS
├── authz/         # 角色/权限判定（admin）
├── httpapi/       # router、错误、中间件、分页、参数解析
├── idempotency/   # Redis + PG 幂等
├── ratelimit/     # Redis 滑动窗口
├── audit/         # 审计写入
├── outbox/        # 事务消息
├── mailer/        # SMTP
├── clock/         # 可注入时钟（测试）
└── crypto/        # 信封加密、随机 token、哈希
```

---

## 3. 配置

- `config.Load()` 读取环境变量并校验；返回不可变 `Config`。
- 关键校验：URL 格式、TTL > 0、生产禁止固定验证码、密钥文件存在、区域枚举。
- 支持 `_FILE` 后缀读取密钥文件。
- 测试使用 `config.ForTest()`。

---

## 4. HTTP 中间件顺序

1. `RequestID`（`X-Request-Id` 透传或生成 ULID）。
2. `RealIP`（信任代理白名单）。
3. `Recoverer`（panic → 500 + 日志 + 指标）。
4. `AccessLog`（slog，敏感头剔除）。
5. `Metrics`（路由模板，避免高基数）。
6. `Timeout`（WSS/上传除外）。
7. `RateLimit`（按路由策略）。
8. `Auth`（Bearer / admin session；匿名路由跳过）。
9. `Version/Accept` 校验（按域）。
10. Handler。

错误：

```go
func WriteError(w, r, err) {
  var api *APIError
  if errors.As(err, &api) { ... } else { 500 INTERNAL }
}
```

统一 JSON：`{"error":{"code","message","details"}}`。

分页：

```go
type Page struct { Cursor string; Limit int }
type PageResult[T any] struct { Items []T; NextCursor string }
```

游标使用 base64(JSON{sortValue,id})，防篡改可选 HMAC。

---

## 5. 数据库

- `db.NewPool(ctx, cfg)`：pgxpool，`MaxConns`、`MinConns`、`MaxConnLifetime`、`HealthCheckPeriod`。
- `TxManager.WithTx(ctx, fn)`：`pgx.BeginFunc`，支持嵌套（savepoint）。
- goose 迁移嵌入 `migrations/*.sql`，`db.Migrate(ctx)`；启动时校验版本。
- 健康检查：`Ping` + 迁移版本。
- 所有 repository 接收 `pgx.Tx` 或 `*pgxpool.Pool` 接口。
- SQL 使用 `$1` 参数；禁止拼接；复杂查询写注释说明索引。

---

## 6. 缓存与限流

Key 规范：`cindy:{env}:{domain}:{purpose}:{id}`。

| 用途 | Key | TTL |
|---|---|---|
| presence | `dl:presence:{accountId}` | 90s |
| 路由 | `dl:route:{deviceId}` | 90s |
| 验证码 | `auth:code:{purpose}:{identifier}` | 10min |
| 幂等 | `idem:{scope}:{key}` | 24h |
| 限流 | `rl:{route}:{subject}:{window}` | 窗口长度 |
| 会话吊销 | `auth:revoked:{sessionId}` | 至过期 |
| 目录缓存 | `catalog:revision:{region}` | 5min |

限流算法：Redis Lua 滑动窗口（ZSET 或计数器分桶），返回 `allowed, remaining, resetAt`；超限 `429 + Retry-After`。

---

## 7. 对象存储

- `NewClient`：MinIO SDK，自定义 endpoint、region、SSL。
- 桶检查：启动时 `BucketExists`（可配置跳过）。
- 预签名：PutObject/GetObject，TTL 5–15 min；上传约束 `Content-Type`、`Content-Length`、ACL。
- 私有对象不提供公开 URL；公开桶只读。
- 对象登记表 `file_objects`，用于归属校验与生命周期。
- 开发环境 endpoint 必须可被客户端访问；文档提示真机调试改局域网 IP。

---

## 8. 鉴权（客户端）

- JWT EdDSA；claims：`iss, aud, sub(accountId), mid(membershipId), region, typ(access), jti, exp, iat`。
- `authn.SignAccess/Parse`；`JWKS` 端点 `/.well-known/jwks.json`。
- 中间件把 `Principal{AccountID, MembershipID, Region, Scopes}` 注入 context。
- 401 码：`AUTH_REQUIRED`、`INVALID_TOKEN`、`TOKEN_EXPIRED`。
- 区域校验：token region 必须等于请求部署 region。

## 9. 鉴权（Admin）

- 独立 `admin_sessions` cookie + CSRF header。
- RBAC：`role -> permissions`；路由声明所需权限。
- 高风险操作二次确认与审计。

---

## 10. 幂等

```go
type Store interface {
  Do(ctx, scope, key, requestHash string, fn func() (status int, body any, err error)) (status int, body any, replayed bool, err error)
}
```

- 首次执行：占位 → 执行 → 存响应（TTL 24h）。
- 同 key 不同 hash → `409 IDEMPOTENCY_CONFLICT`。
- 同 key 同 hash → 返回缓存响应。
- 并发：Redis SETNX 占位 + 短轮询/等待。

---

## 11. 审计

- `audit.Log(ctx, Event)` 写入 `audit_events`。
- 自动采集 actor（客户端/管理员/系统）、IP、UA、requestId。
- `before/after` 只记录持久化字段，敏感字段过滤。
- 写入失败不阻断业务，但记录错误指标（高风险操作可配置为阻断）。

---

## 12. 后台任务

- `outbox_events` 事务内写入；`cmd/worker` 轮询 `FOR UPDATE SKIP LOCKED`。
- 消费者按 `event_type` 注册；至少一次；幂等处理。
- 定时任务：共享任务补偿（30s）、过期清理、用量聚合、心跳聚合、推送重试。
- 重试：指数退避，最大 10 次；死信表/状态。

---

## 13. 测试

- 配置解析表驱动。
- 中间件：request id、recover、限流、鉴权（无 token/过期/错误 region）。
- 幂等：并发同 key 只执行一次。
- 迁移：空库 → 最新 → 重复执行无副作用。
- 健康检查：依赖断开的 readyz 行为。

---

## 14. 验收

- `make run` 启动无警告；`/healthz`、`/readyz`、`/metrics` 可用。
- 所有业务域通过 `platform` 获取依赖，无重复初始化。
- 错误信封、requestId、限流头、审计字段统一。
