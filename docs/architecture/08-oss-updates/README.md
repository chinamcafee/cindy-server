# OSS、更新与远程配置实施方案

> 对应能力：OSS-01…、UPD-01…、OBS-01…（见 `docs/rebuild/07-oss-updates-observability.md`）
> 依赖：Foundation、Auth

---

## 1. 实现范围

| 优先级 | 能力 |
|---|---|
| P0 | 头像预签名、设备媒体签票、桌面/移动更新清单、endpoint.json、heartbeat |
| P1 | 公告、feature flags、强制更新、日志上传对接、GitHub issue |
| P2 | 灰度、壁纸、统计看板 |

---

## 2. 包结构

```
internal/oss/
├── handler.go        # /api/oss, /api/device-link/media
├── objects.go
└── lifecycle.go
internal/updates/
├── handler.go        # /latest, /manifest, /notice
├── desktop.go
├── mobile.go
└── flags.go
internal/heartbeat/
internal/githubissue/
```

---

## 3. OSS

### 3.1 端点

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/oss/presign-put` | `{scene:"avatar",contentType,size}` → `{putUrl,publicUrl,key,headers}` |
| POST | `/api/device-link/media/presign-put` | `{size,ext,contentType,sharedTaskId?}` |
| POST | `/api/device-link/media/presign-get` | `{key}` |
| DELETE | `/api/device-link/media` | `{key}` |

- 头像：≤5 MiB，公开桶，PUT 后 `publicUrl` 可读。
- 媒体：≤2 GiB，私有桶；key 内嵌 userId；`x-oss-object-acl: private`。
- 上传 headers 原样返回；签名包含 Content-Type。
- 预签名 TTL：头像 10 min；媒体 15 min；下载 10 min。
- 对象登记与归属校验；删除幂等。

### 3.2 对象键

```
public/avatars/{accountId}/{uuid}.{ext}
media/{accountId}/{yyyy}/{mm}/{uuid}.{ext}
media/shared/{sharedTaskId}/{uuid}.{ext}
plugins/...
skills/...
```

---

## 4. 桌面更新

### 4.1 CDN 布局

```
{cdnBaseUrl}/endpoint.json
{cdnBaseUrl}/manifest-{platform}-{arch}[-beta|-canary].json
{cdnBaseUrl}/notice/{platformKey}/index.json
{cdnBaseUrl}/notice/{platformKey}/{version}.json
{cdnBaseUrl}/wallpapers/{sha256}.{mp4|webp}
```

### 4.2 manifest

```json
{
  "app": {
    "version": "1.2.3",
    "releaseNotes": "...",
    "hotfix": {"file":"...","sha256":"...","size":123},
    "installer": {"file":"...","sha256":"...","size":456},
    "requireRelogin": true
  },
  "claudeCode": {}, "codex": {}, "codexPackage": {},
  "ripgrep": {}, "pi": {}
}
```

- 渠道：release/beta/canary；canary 由账号 feature flag；beta 设备开关。
- Linux 只接受 `.deb`；版本 `0.0.0*` 禁用应用内更新。
- 服务端只生成清单与提供 CDN 文件；下载校验在客户端。

---

## 5. 移动更新

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/latest?platform=ios|android&channel=beta|canary` | 整包 `{version,buildNumber,runtimeVersion,installUrl,itmsUrl,releaseNotes?,minVersion?}` |
| GET | `/manifest` | Expo OTA manifest（按 `x-cindy-update-channel`） |

- `minVersion` 触发强制更新（客户端仅在 `currentVersion < minVersion ≤ version` 时阻断）。
- 审核模式按 iOS 版本；Android 豁免。
- OTA 使用 runtimeVersion 兼容；anti-bricking 由客户端控制。

---

## 6. 远程配置

- `endpoint.json`：按区域生成；严格字段；信任锚 `cindy.app` / `cindy.com.cn`；多 CDN 镜像。
- `GET /api/user/feature-flags`：`{isCanary?, defaultEnableBeta?}`。
- 公告：`/notice` 索引 + 单版本 JSON；15s/8MiB/3 次重试。
- 壁纸：静态 CDN，hash 命名，不覆盖。
- 管理后台发布时生成并校验。

---

## 7. heartbeat

- `POST /heartbeat`：`{uid, platform?, version?}` → `{ok:true}`。
- 无鉴权；IP 限流；60s 周期；在线 TTL 90s。
- 写 `hb_daily` upsert；不参与 presence。

---

## 8. 日志上传

- 客户端直传阿里云 SLS（构建期配置），**服务端不代理、不落原文**。
- 服务端提供支持工具：按 `uploadCode` 查询（对接 SLS API），仅后台可见。
- 若改为自建：提供 `POST /api/logs/upload`，批次 ≤500/1MiB，服务端再脱敏并写对象存储；默认不启用。

---

## 9. GitHub issue

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/github/issues` | `{title,description?,type,appVersion,userName?}` → `{githubIssue:{number,url}}` |
| GET | `/api/github/issues/mine` | 我的 issue 列表 |

- 平台身份 `cindy-issue`（GitHub App）；无 App 配置时 dev 返回 mock。
- 标题/正文长度限制；正文已由客户端脱敏；服务端再过滤 token。
- 按账号限流。

---

## 10. 测试

- 预签名：归属、TTL、大小、ACL；越权 GET 403。
- 桌面 manifest：渠道选择、Linux `.deb`、sha/size。
- 移动：minVersion、runtimeVersion、渠道头。
- endpoint：区域字段、非法 URL 拒绝。
- heartbeat：幂等 upsert、限流。
- GitHub issue：鉴权、长度、错误映射。

---

## 11. 验收

- 客户端可上传头像并公开访问。
- 媒体上传→下载→删除闭环，越权不可读。
- 桌面/移动更新检查可返回清单；强更字段生效。
- endpoint.json 可被客户端严格解析。
