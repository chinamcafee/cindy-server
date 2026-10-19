# 插件市场与 Skill Hub 实施方案

> 对应能力：PLUGIN-01…、SKILL-01…（见 `docs/rebuild/06-plugin-skillhub.md`）
> 依赖：Foundation、Auth、OSS

---

## 1. 实现范围

| 优先级 | 能力 |
|---|---|
| P0 | 插件列表/详情/下载签票、用量上报；Skill 列表/详情/文件/版本/下载 |
| P1 | 成员上传（预签名→commit→审核）、组织前缀、默认安装、清理通告；Skill 发布/可见性/审核/自动安装 |
| P2 | 统计聚合、推荐、批量操作、签名校验 |

---

## 2. 包结构

```
internal/plugin/
├── handler_market.go     # /api/plugins
├── handler_publisher.go  # /api/publisher
├── service.go
├── manifest/             # ghost.json 校验器
├── usage.go              # 用量事件与幂等
├── repo.go
└── models.go
internal/skillhub/
├── handler.go
├── service_market.go
├── service_publish.go
├── service_review.go
├── service_autoinstall.go
├── ziputil/              # zip 安全检查与文件清单
├── repo.go
└── models.go
```

---

## 3. 插件端点

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/plugins?scope=all&limit=100&query=&cursor=` | 列表；`schemaVersion=2`；`removals[]`、`currentOrganization` |
| GET | `/api/plugins/{pluginId}` | 详情（含完整 manifest） |
| GET | `/api/plugins/{pluginId}/releases/{releaseId}/download` | 下载签票 `{url,expiresAt,sha256,sizeBytes}` |
| POST | `/api/plugins/{pluginId}/usage-events` | 批量用量 `{events[]}` → `{accepted,duplicate,rejected}` |
| POST | `/api/publisher/uploads` | `{sizeBytes,sha256}` → `{uploadId,putUrl,headers,expiresAt,status}` |
| POST | `/api/publisher/uploads/{id}/commit` | 空 body → 202 `{uploadId,status}` |
| GET | `/api/publisher/uploads/{id}` | 状态/审核 |
| GET | `/api/publisher/releases/mine?cursor=` | 我的发布 |

鉴权：普通 Access Token；发布者使用 Connection JWT（aud `<orgSlug>:cindy-publisher`）。所有请求带 `x-cindy-version`。

---

## 4. manifest 校验

- 只接受 `schemaVersion` 2/3；v3 必填 `minCindyVersion`。
- 顶层字段校验：id 正则/Windows 保留名、name/version 长度、entry/icon 安全相对路径、locales 必含 en、panel/mainView、tools（1–16、唯一、参数 ≤16 KiB）、command 依赖 tools。
- 网络/凭证：hosts 小写域名、禁 IP/端口/路径、通配仅最左；secrets key 正则、source 枚举；inject 头白名单；oauth/oidc/gh-cli 约束。
- node：entry `.js/.cjs`、protocol 枚举、lifecycle/idleTimeout、secretBindings。
- skill/manual/setup/preview 上限与路径安全。
- zip：≤128 MiB、解压 ≤256 MiB、entry ≤2048、ghost.json ≤256 KiB；拒绝 `..`、绝对路径、符号链接、压缩炸弹。
- 校验失败码映射到发布失败码（`PLUGIN_PACKAGE_INVALID` 等）。

> 黄金集：直接复用客户端 `manifest.test.ts` 的合法/非法样例，确保服务端与客户端一致。

---

## 5. 插件发布状态机

```
awaiting_upload → validating → publishing → succeeded
                         ↘ failed / expired
release.reviewStatus: pending → approved / rejected
```

- commit 后异步入队校验：对象存在/大小/SHA-256 → zip 安全 → manifest → 组织前缀/ghostId 冲突 → 创建 release（pending）。
- 审核通过后更新 `currentRelease`；下架写 `removals`。
- 上传会话 TTL（建议 1h）；commit 幂等；expired 仍 202。
- 失败码全表见分册；未知码保留。

---

## 6. 插件用量

- `eventId` 唯一；同 ID 同内容 → duplicate；不同内容 → `EVENT_ID_CONFLICT`。
- `occurredAt` 时间窗校验；`releaseId` 必须已交付给该账号。
- 批量上限 100；ACK 只覆盖本批；聚合到 `plugin_usage_daily`。
- 返回 413 时客户端减半重试。

---

## 7. Skill Hub 端点

| 方法 | 路径 |
|---|---|
| GET | `/api/skills-hub/skills?page&pageSize&sort&order&q&category&scope` |
| POST | `/api/skills-hub/skills/list` |
| GET | `/api/skills-hub/users/published` |
| GET | `/api/skills-hub/skills/{name}` |
| GET | `/api/skills-hub/skills/{name}/files?version&includeHashes` |
| GET | `/api/skills-hub/skills/{name}/file?path&version` |
| GET | `/api/skills-hub/skills/{name}/versions` |
| PATCH/DELETE | `/api/skills-hub/skills/{name}` |
| POST | `/api/skills-hub/skills/{name}/unpublish` |
| POST | `/api/skills-hub/skills/{name}/set-visibility` |
| GET | `/api/skills-hub/skills/{name}/visibility` |
| GET | `/api/skills-hub/users/departments` |
| GET | `/api/skills-hub/categories?scope&includeEmpty` |
| GET | `/api/skills-hub/users/teams` |
| GET | `/api/skills-hub/skills/{name}/scan?version` |
| POST | `/api/skills-hub/skills/batch-detail` |
| GET | `/api/skills-hub/skills/{name}/download?version` |
| POST | `/api/skills-hub/skills/publish/init` |
| PUT | `{uploadUrl}`（S3） |
| POST | `/api/skills-hub/skills/publish/commit` |
| GET | `/api/skills-hub/auto-install?cursor` |

- 列表响应 `{items,total}`；分类两级树；批量详情每批 100。
- 详情 `{deleted:true}` 表示已删除。
- 文件清单含 `path,size,language,truncated,sha256?`；单文件内容有大小/截断。
- 发布：init → PUT zip（`application/zip`）→ commit；`VERSION_RACE` 重取 init 最多 2 次；403 → `OSS_PUT_EXPIRED`。
- 审核状态：`pending/scanning/quarantine/rejected/published`；`pendingVersion` 优先。
- 可见性：organization 仅 `public|shared`；personal 仅 `public|private`。
- 自动安装：`{skills:[{name,version,catalogScope:"team"}]}`，游标 ≤128 字符，服务端按成员/部门受众计算。

---

## 8. 存储与对象键

| 对象 | 键 |
|---|---|
| 插件包 | `plugins/{pluginId}/{releaseId}.cindy` |
| 插件图标 | `plugins/{pluginId}/{sha256}.{ext}` |
| Skill zip | `skills/{skillId}/{version}/{zipSha256}.zip` |
| Skill 图标 | `skills/{skillId}/icon.{ext}` |

- 私有桶；下载只发短期预签名。
- 上传使用 `Content-Length` 与 SHA-256 校验；禁止覆盖（`x-oss-forbid-overwrite`）。
- 对象登记到 `file_objects`，便于生命周期与审计。

---

## 9. 测试

- 列表 scope/organization/namespace 约束；游标不前进报错。
- manifest 黄金集（合法/非法/权限扩张/未知字段）。
- 上传：大小/SHA 不符、zip 穿越、entry 超限、重复 commit。
- 审核：pending→approved→下载；rejected 原因。
- 用量：重复、冲突、越界、超量。
- Skill：发布→扫描→可见性→自动安装；版本竞争；文件路径安全。
- 越权：非组织成员不能发布；非 owner 不能改 Skill。

---

## 10. 验收

- 客户端可浏览、下载并安装一个测试插件（SHA/大小校验通过）。
- 成员上传→审核→发布闭环。
- Skill 发布→审核→组织自动安装清单可见。
- 所有下载 URL 短期有效且不可越权。
