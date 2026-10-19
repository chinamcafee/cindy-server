# 测试与验收

> 目标：证明服务端满足客户端 wire 契约、权限边界、幂等与恢复语义，而不仅是“代码能跑”。

---

## 1. 测试分层

| 层级 | 范围 | 工具 | 是否必需 |
|---|---|---|---|
| 单元 | service 纯逻辑、parser、签名、限流 | `go test` + testify | 是 |
| 仓储 | SQL/迁移/约束 | 真实 PostgreSQL（compose） | 是 |
| HTTP 集成 | router→service→DB | `httptest` + 真实 PG/Redis | 是 |
| 契约 | 响应可被客户端 parser 接受 | 客户端 zod/parser 或等价 JSON Schema | 是（P0） |
| WebSocket | 握手、路由、重连、关闭码 | `gorilla/websocket` 客户端 | 是 |
| 端到端 | 登录→模型→设备→消息→上传→后台 | shell/Go e2e | 是（P0） |
| 前端 | 组件/页面 | Vitest + Testing Library | 推荐 |
| 负载 | WSS/消息/转发 | k6 或自定义 | P1 |
| 安全 | 越权、重放、注入 | 用例集 | 是 |

---

## 2. 契约测试（关键）

客户端仓的 parser 是事实验收标准。重建版至少对以下端点做“响应→客户端 parser”回放：

- `GET /api/auth/providers`、`POST /api/auth/discovery`、登录/刷新响应。
- `GET /api/model-catalog/catalog?registrySchemaVersion=5`、`GET /api/model-access/models`。
- `GET /api/plugins`、详情、下载、用量 ACK。
- `GET /api/skills-hub/skills`、详情、文件、版本、下载。
- Chat 房间/消息/事件 JSON。
- Device-link 信封与 `hello-ack`（可用 `scripts/device-link/relayFixture.ts` 对照）。
- 插件 manifest 校验：用客户端 `manifest.test.ts` 的合法/非法样例跑服务端校验器。

实现方式：

1. 在 `test/contract/fixtures/` 存放客户端期望 JSON。
2. Go 测试启动 handler，断言输出与 fixture 同构（字段存在、类型、枚举）。
3. 可选：Node 侧脚本导入客户端 parser 对 Go 响应做真实解析（CI 双语言）。

---

## 3. 核心用例清单

### 3.1 认证

- 邮箱验证码：发送→验证→token；错误码/过期/次数上限。
- 刷新轮换：旧 refresh 重用 → 全家族吊销。
- 多账号选择、补绑、SSO 二次验证。
- 区域不匹配 → `REGION_MISMATCH`。
- 登出吊销；账号删除挑战/恢复。
- 限流 429 + Retry-After。

### 3.2 Chat

- 创建房间/邀请/加入；成员越权 404。
- 消息 `seq` 单调；`clientMessageId` 幂等；游标补读。
- WSS 订阅、事件顺序、断线重连补读。
- 执行租约：并发 Bot 发言互斥/过期。
- 附件归属与签名 URL 过期。

### 3.3 Device-link

- hello/hello-ack；重复 hello；协议版本不符 4400。
- 同账号路由；跨账号 `DEVICE_OFFLINE`。
- `remoteControlEnabled=false` 时 `link-open/invoke` → `REMOTE_DISABLED`；回程仍可通。
- 顶号 4409；移除 4410；超限 4429；大帧拒绝。
- presence 广播与 TTL；notify 频控。
- 共享任务邀请/加入/权限/关闭；供应商分享审批/跨区凭证。
- 媒体 key 归属校验；预签名过期。

### 3.4 模型

- 目录 schemaVersion 校验；缓存头；revision 回滚。
- BYOK 加密存取（不可读明文）；轮换/撤销。
- 配额/余额/用量；超额错误码。
- 计费幂等（订单/订阅）。

### 3.5 插件/Skill

- 列表 scope/org/namespace 校验；游标不前进报错。
- manifest 合法/非法样例；权限扩张。
- 上传→commit→审核→下载：sha256/size 不一致拒绝。
- 用量事件去重与 ACK；未知拒绝码不崩溃。
- Skill 发布/可见性/审核/自动安装分页。

### 3.6 后台

- RBAC：每个角色最小权限；越权 404。
- 审计：每个写操作有记录，敏感字段不入库。
- 插件审核/下架触发 removals；模型发布 revision。
- 封禁/吊销即时生效。

---

## 4. WebSocket 测试

- 使用真实 `httptest.Server` + `websocket.Dialer`。
- 断言关闭码：4400/4401/4409/4410/4429/1013。
- 心跳：服务端超时清理；客户端重连后状态恢复。
- 背压：慢消费者触发断开或丢弃策略（device-link 1013）。
- 并发：100 连接 × 100 消息，验证顺序与无泄漏。

---

## 5. 性能与稳定性

| 场景 | 目标 |
|---|---|
| 登录 | 200 QPS，P95 < 150ms |
| 消息写入 | 1k msg/s，P95 < 100ms |
| device-link 转发 | 5k 帧/s，P95 < 20ms |
| WSS 连接 | 单实例 10k |
| 目录读取 | 5k QPS（缓存） |

- 使用 `pprof` 定位热点；`go test -race`。
- 泄漏检测：goleak（可选）。
- 长稳：24h 压测，观察内存/连接/队列。

---

## 6. 前端测试

- `pnpm tsc --noEmit`、`pnpm build` 必须通过。
- Playwright（可选）：登录、列表、审核、主题切换。
- 视觉：Light/Dark 截图对比；空状态/错误状态。
- 可访问性：键盘导航、焦点、ARIA（表格/对话框）。

---

## 7. CI 门禁

```yaml
- gofmt -l .            # 必须为空
- go vet ./...
- go test ./... -race
- go build ./cmd/...
- docker build
- cd cindy-admin && pnpm install --frozen-lockfile && pnpm build
```

集成测试需要 compose 中间件；CI 用 service containers 或 `docker compose up -d`。

---

## 8. 验收清单（阶段 3）

- [ ] `cindy-middlewares` 一键启动且全部 healthy。
- [ ] 迁移可从空库执行；seed 可重复执行。
- [ ] 客户端核心链路（登录、目录、设备、消息、上传）本地通过。
- [ ] 契约测试覆盖 P0 端点。
- [ ] 后台可登录并完成用户/设备/插件/模型的基本运营操作。
- [ ] `go test ./...`、`go build ./...`、`pnpm build` 全绿。
- [ ] 安全用例（越权/重放/限流）通过。
- [ ] 文档与实现一致（路由、字段、错误码）。
