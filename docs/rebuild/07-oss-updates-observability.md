# OSS、桌面/移动更新、远程配置与可观测链路

> 文档编号：07（阶段 1 分册）
> 覆盖能力：`OSS-01…OSS-10`、`UPD-01…UPD-14`、`OBS-01…OBS-14`，共 38 个编号
> 定位：基于开源客户端 `cindy` 逆向重建闭源服务端的可直接实现规格；只描述客户端已消费的契约与服务端必须提供的行为，不含实现代码。
> 源码快照：`cindy`（2026-10-19 工作区）、`cindy-protocol`（2026-08-10 最后提交）。
> 配套：[00-overview](00-overview.md)、[01-service-topology](01-service-topology.md)、[04-device-link](04-device-link.md)、[06-plugin-skillhub](06-plugin-skillhub.md)、[08-observability-security](08-observability-security.md)。

---

## 0. 证据等级与阅读约定

| 等级 | 含义 | 典型来源 |
|---|---|---|
| 【契约】 | 客户端严格解析、测试固化或调用点逐字符写死的 wire 契约；不符即拒绝。 | `clientEndpointSchema.ts`、`memberUpload.ts`、`updateService.ts`、`mobileAttachmentUpload.ts` |
| 【文档】 | 仓内开发/产品文档明确写出的行为、上限、状态机。 | `cindy-updater.md`、`client-log-upload-*.md`、`wallpaper-on-demand-plan.md` |
| 【推测】 | 由字段、错误码、降级行为反推；可自定实现，但必须满足客户端可观察结果。 | 对象键、配额、私有发布流水线 |

约定：客户端默认同时指 Desktop 与 Mobile；HTTPS/TLS；除匿名端点外带 `Authorization: Bearer <accessToken>`；错误信封统一 `{"error":{"code","message","details"?}}`；时间优先 ISO-8601 UTC。

---

## 1. 源文件索引

| 源文件 | 提取内容 |
|---|---|
| `apps/desktop/src/main/ossPublicUpload.ts` + `profileEdit.ts` | 头像 presign-put、PUT 头、5 MB/512 px/PNG-JPEG 校验 |
| `apps/desktop/src/main/device-link/mediaTransfer.ts` + `apps/mobile/src/session/mobileAttachmentUpload.ts` | 设备媒体 presign-put/get/delete、2 GiB、流式、Range、私有 ACL、移动重试 |
| `apps/desktop/src/main/plugin-publisher/api.ts` + `packages/plugin-protocol/src/memberUpload.ts` | 插件上传路由、状态机、失败码、128 MiB/256 MiB/2048 |
| `apps/desktop/src/main/skillhub/publishService.ts` / `zipPacker.ts` | Skill init/commit、版本竞态、<10 MB、确定性 zip |
| `apps/desktop/src/main/updateService.ts` + `updateChannelStore.ts` / `canaryFlagStore.ts` / `canaryFlagSync.ts` | 桌面检查/下载/应用/重启、patch-info、渠道代际、beta/canary/feature-flags |
| `apps/desktop/src/main/manifestService.ts` / `releaseNotesService.ts` | manifest 渠道 URL、缓存、notice/index |
| `apps/desktop/src/main/downloader/**` + `updateScriptMacOS.ts` / `updateScriptLinux.ts` | SHA-256、断点续传、重试、macOS/Linux 应用脚本 |
| `apps/desktop/cindy-updater/**` + `docs/dev-rules/cindy-updater.md` / `docs/product-rules/agent-app-update.md` | Windows 更新器 CLI/阶段/回滚/UAC/锁；更新器门禁与 Agent 路径 |
| `packages/maker-shared/src/updateChannel.ts` | canary > beta > release |
| `apps/mobile/src/update/fetchLatestRelease.ts` / `bundleUpdate.ts` | `/latest`、记录字段、runtimeVersion、minVersion |
| `apps/mobile/src/update/otaRequestCoordinator.ts` / `startupOtaUpdate.ts` / `otaReloadGuard.ts` | OTA URL、channel header、EAS-Client-ID、超时、防循环 |
| `apps/mobile/app.config.js` / `app.json` / `apps/mobile/docs/selfhost-native-ota-recovery.md` | fingerprint runtimeVersion、checkAutomatically、原生恢复 |
| `packages/maker-shared/src/clientEndpointSchema.ts` / `clientEndpointResilience.ts` / `clientEndpointOrigins.ts` + `config/endpoint*.json` | endpoint 全字段、主源/镜像/缓存/随包、受信任域、两区正本 |
| `packages/heartbeat-client/**` + `apps/desktop/src/main/heartbeatService.ts` | 心跳端点、周期、失败语义、仅 cloud |
| `apps/desktop/src/main/log-upload/**` + `docs/client-log-upload-*.md` | 采集/脱敏/批次/SLS/标记/上限 |
| `apps/desktop/src/main/github-issue/**` | issue 创建/查询、平台身份、错误映射 |
| `apps/desktop/src/main/wallpaper-video.ts` + `wallpaper-video-manifest.json` | 壁纸 CDN 路径、16 套、校验、两区 |
| `apps/desktop/src/renderer/analytics/tapdbClient.ts` + `apps/mobile/src/analytics/mobileTapdb.ts` | TapDB 区域配对、客户端直报 |
| `docs/dev-rules/cindy-updater.md` / `docs/product-rules/agent-app-update.md` | 更新器门禁、Agent 更新路径 |

---

## 2. 能力总览

| 编号 | 能力 | 客户端证据 | 服务归属 | 优先级 |
|---|---|---|---|---|
| OSS-01 | 头像公开资产 presign-put | `ossPublicUpload.ts`、`profileEdit.ts` | oss-server | P0 |
| OSS-02 | 公开对象可读性与生命周期 | `ossPublicUpload.ts` 注释 | oss + 对象存储/CDN | P0 |
| OSS-03 | 设备媒体私有 presign-put | `mediaTransfer.ts`、`mobileAttachmentUpload.ts` | device-link + oss | P0 |
| OSS-04 | 设备媒体 presign-get / Range | `mediaTransfer.ts`、`remoteMediaProtocol.ts` | device-link + oss | P0 |
| OSS-05 | 删除与归属校验 | `mediaTransfer.ts`、`sharedTaskMediaContext.ts` | device-link + oss | P0 |
| OSS-06 | 大小、配额、限流 | `mediaTransfer.ts`、08 分册 | oss / 网关 | P1 |
| OSS-07 | 插件包 prepare/commit/status | `plugin-publisher/api.ts`、`memberUpload.ts` | plugin-server | P1 |
| OSS-08 | Skill 包 init/commit | `skillhub/publishService.ts` | skill-hub | P1 |
| OSS-09 | 对象键、ACL、生命周期 | 客户端注释 + 发布脚本 | oss / 运维 | P1 |
| OSS-10 | 上传完整性 size+sha256 | `attachmentOssRef.ts` | oss / device-link | P1 |
| UPD-01 | 桌面 CDN 目录与 manifest 全字段 | `manifestService.ts`、发布脚本 | CDN / 发布 | P0 |
| UPD-02 | 桌面 release/beta/canary | `manifestService.ts`、`updateChannelStore.ts` | CDN / feature-flags | P1 |
| UPD-03 | 桌面灰度与 feature flags | `canaryFlagSync.ts`、`authManager.ts` | oauth-broker | P1 |
| UPD-04 | 桌面下载校验/续传/重试 | `downloader/**` | CDN（客户端校验） | P0 |
| UPD-05 | 桌面 apply/relaunch/回滚 | `updateService.ts`、`cindy-updater/**` | 客户端本地 | P1 |
| UPD-06 | 桌面 Agent 二进制与 pi | `manifestService.ts`、`agent-binaries/**` | CDN | P1 |
| UPD-07 | 桌面自动更新与 Agent 更新 | `updateAutoRelaunchPolicy.ts`、产品规则 | 客户端本地 | P2 |
| UPD-08 | 更新说明/公告 CDN | `releaseNotesService.ts` | CDN | P1 |
| UPD-09 | 移动整包 `/latest` | `fetchLatestRelease.ts` | mobile-update-server | P0 |
| UPD-10 | 移动 OTA manifest/runtimeVersion | `otaRequestCoordinator.ts`、`app.config.js` | mobile-update + CDN | P0 |
| UPD-11 | 移动发布渠道 | `canaryChannelStore.ts`、`betaChannelStore.ts` | mobile-update + flags | P1 |
| UPD-12 | 移动强更 minVersion | `bundleUpdate.ts`、`forcedUpdateStore.ts` | mobile-update | P1 |
| UPD-13 | 移动审核/TestFlight/Play 门禁 | endpoint `review`、`androidInstallSource.ts` | endpoint 清单 | P1 |
| UPD-14 | 壁纸与静态资源 CDN | `wallpaper-video.ts`、壁纸方案 | CDN | P2 |
| OBS-01 | endpoint.json 发布与 schema v1 | `clientEndpointSchema.ts` | CDN / 配置服务 | P0 |
| OBS-02 | endpoint 信任模型与离线回退 | `clientEndpointResilience.ts`、`clientEndpointOrigins.ts` | 客户端 + CDN | P0 |
| OBS-03 | feature flags 服务 | `canaryFlagSync.ts`、`AuthContext.tsx` | oauth-broker | P1 |
| OBS-04 | 远程运营开关 | endpoint 空值、`review`、hook 灰度 | oauth-broker / CDN | P1 |
| OBS-05 | heartbeat 端点与周期 | `heartbeat-client/client.ts` | heartbeat-server | P0 |
| OBS-06 | 在线 TTL 与 DAU 口径 | `heartbeat-client/types.ts` | heartbeat + Redis | P1 |
| OBS-07 | 日志上传目标与鉴权 | `logUploadTarget.ts`、`logSink.ts` | 外部 SLS（客户端直写） | P1 |
| OBS-08 | 日志批次、压缩、字段格式 | `logSink.ts`、`limits.ts` | 外部 SLS | P1 |
| OBS-09 | 日志脱敏与隐私边界 | `redact.ts`、`sourceAllowlist.ts` | 客户端（服务端只接收） | P0 |
| OBS-10 | 日志重试、状态、补传 | `uploadRunner.ts`、`pendingMarkers.ts` | 客户端 + 运维 | P1 |
| OBS-11 | 反馈 issue 创建 | `githubIssueSubmitService.ts` | github-server | P1 |
| OBS-12 | 我的 issue 查询 | `myIssuesRuntime.ts`、`myIssuesService.ts` | github-server | P2 |
| OBS-13 | TapDB 遥测边界 | `tapdbClient.ts`、`mobileTapdb.ts` | 客户端直报 + model-access | P2 |
| OBS-14 | 观测、审计、指标横切 | 08 分册 | 全部服务 | P1 |

---

## 3. OSS / 媒体服务

### 3.1 边界

oss-server 只做签名、归属校验、对象登记；字节由客户端 presign 后直传/直下 OSS。Desktop/Mobile → presign-put（Bearer + 归属校验）→ PUT 到 OSS/S3 → GET/HEAD 签名 URL 或 CDN；服务端不代理字节。

【契约】relay 帧上限 2 MiB，大媒体不得内联；签名 URL 自带权限，客户端不转发 Bearer；预签名 query 含可复用签名，服务端日志与回给对端的错误只能出现 host；对象键对客户端不透明，只要求非空、稳定、可再次 presign。

### 3.2 头像公开上传（OSS-01/02）

```
POST {ossApiBaseUrl}/api/oss/presign-put   Authorization: Bearer <token>
{ "scene":"avatar", "contentType":"image/png", "size":123456 }
→ { "putUrl":..., "publicUrl":..., "key":..., "headers":{"Content-Type":"image/png"}, "expiresAt":... }
```

客户端【契约】：

- 未登录本地失败 `NOT_AUTHENTICATED`；presign 超时 15 s，非 2xx 读 `error.code`，返回 `stage:'presign'`；PUT 超时 60 s。
- PUT **原样携带响应 `headers`**；`Content-Type` 已签进 URL，不一致会被 OSS 拒绝。
- PUT 返回 2xx 后 `publicUrl` 才可访问（此前 404）；之后把 `publicUrl` 交给 `PATCH /api/me/profile`（auth-server）落库；成功返回 `{publicUrl,key}`，失败按 stage 区分 presign/put。

服务端约束：

- `scene` 只允许 `avatar`；`contentType` 白名单 png/jpeg/webp；`size` ≤ **5 MiB**（`AVATAR_MAX_BYTES`）；白名单外 presign 400。
- 实际头像是 renderer 裁剪的 ≤512 px PNG/JPEG；main 还按文件头与完整解码校验，服务端不得放宽。
- 公开 bucket 内容寻址或版本化：换头像生成新 key，旧对象由生命周期回收；不要原地覆盖，否则 CDN 缓存旧字节。
- `publicUrl` 走 CDN；`headers` 至少含 `Content-Type`；若用 `x-oss-object-acl: public-read` 也必须放进 `headers`（客户端只照抄）。推荐 key【推测】`v1/avatar/<userId>/<sha256>.<ext>`；读取匿名，上传/覆盖鉴权，删除头像只需把 profile avatar 置空。

### 3.3 设备媒体私有对象（OSS-03/04/05/10）

| 方法 | 路径 | 请求 | 响应 | 备注 |
|---|---|---|---|---|
| POST | `/api/device-link/media/presign-put` | `{size,ext,contentType,sharedTaskId?}` | `{putUrl,key,expiresAt}` | Bearer；`ext` 不带点，未知填 `bin` |
| POST | `/api/device-link/media/presign-get` | `{key}` | `{getUrl,expiresAt}` | 同账号；共享任务需成员校验 |
| DELETE | `/api/device-link/media` | `{key}` | `{deleted:boolean}` | 幂等；对象不存在也成功语义 |

客户端行为：

- `contentType` 缺失统一 `application/octet-stream`，presign 与 PUT 必须逐字节一致且都不能缺，否则 `SignatureDoesNotMatch` 403；移动 presign 12 s、最多 2 次（重复无副作用），PUT 单次 120 s 或 60 s 无进度超时，同 URL 最多 2 次，仅网络/408/429/5xx 重试，403 不重试。
- 桌面 ≤64 MiB 读 Buffer 整体 PUT，>64 MiB 磁盘流式 PUT（`duplex:'half'`、显式 `Content-Length`）；2 GiB 在客户端自校。
- 下载小媒体整文件；视频/音频 Range 流式返回原始 OSS Response（200/206、`Content-Range`/`Content-Length`/`Content-Type` 透传）；出方向附件落盘校验 size+sha256，先 `.part` 再 rename。

对象键/ACL【契约】：

- key **内嵌 userId** 承载同账号鉴权；共享任务 key 必须含 `sharedTaskId`；服务端校验请求方与 key 归属。推荐 `v1/device-media/<userId>/<yyyy>/<mm>/<uuid>.<ext>` 与 `v1/device-media/shared/<sharedTaskId>/<userId>/<uuid>.<ext>`【推测】。
- PUT 必须带 `Content-Type` + `x-oss-object-acl: private`；ACL 是 OSS V1 canonical header，放 query 会签名不匹配；device-media 一律 private，即使桶默认 public-read 也覆盖，下载只能经 presign-get。
- `presign-put`：Bearer == key 中 userId；带 sharedTaskId 时校验 host/guest 上传权限。`presign-get`：同账号；guest 只能读该任务范围；越权一律 404（防枚举）。`DELETE`：owner 或 host；不存在幂等。relay 传递的 ossKey 不得让服务端信任客户端自报 owner。

上限/生命周期：

- 单对象 **2 GiB**；OSS V1 预签名 PUT 无法绑定 `Content-Length`，客户端按真实字节自校，服务端再校验声称 size；无 multipart，>64 MiB 流式。
- 客户端用后即删，失败只 warn，由桶生命周期兜底；建议未引用中转 7 天、共享任务 30 天、流式播放对象不少于 24 h，任务结束主动清前缀；上传失败/取消 best-effort 删除，`onLateSuccess` 回收迟到成功对象。

### 3.4 插件包上传（OSS-07）

| 方法 | 路径 | 请求 | 响应 |
|---|---|---|---|
| POST | `{pluginApiBaseUrl}/api/publisher/uploads` | `{sizeBytes,sha256}` | `{uploadId,putUrl,headers,expiresAt,status:"awaiting_upload"}` |
| PUT | `putUrl` | `.cindy` 字节 | 2xx；409 视为已存在可 commit |
| POST | `/api/publisher/uploads/{uploadId}/commit` | `{}` | **202**，body `{uploadId,status}` |
| GET | `/api/publisher/uploads/{uploadId}` | — | `{status,pluginId,releaseId,ghostId,version,reviewStatus,failure}` |
| GET | `/api/publisher/releases/mine?cursor=` | — | `{releases,nextCursor}` |

- 状态机：`awaiting_upload → validating → publishing → succeeded|failed|expired`；审核态独立 `pending|approved|rejected`。
- 身份来自 Connection JWT（audience `<orgSlug>:cindy-publisher`）；body 只带 size/sha256，`ghostId`/`version`/actor 不得由 body 覆盖。
- PUT 原样带 `headers` 并精确 `content-length`；流式不跟 3xx；409（`x-oss-forbid-overwrite`）直接 commit；5xx 归 `commit_same_upload`，其余 4xx 同 URL 重试。
- 会话 TTL **60 分钟**；距过期 <90 s 不再开始 PUT；并发 2；commit 后 2.5 s 轮询，瞬时错误最多 8 次、退避 2 s。
- 上限：归档 **128 MiB**、解压 **256 MiB**、条目 **2048**；服务端重复校验 size/sha256/zip 结构/解压炸弹/ghostId 前缀/版本存在。
- 稳定失败码：`UPLOAD_OBJECT_MISSING`、`UPLOAD_SIZE_MISMATCH`、`UPLOAD_SHA256_MISMATCH`、`PLUGIN_PACKAGE_INVALID`、`MEMBERSHIP_INACTIVE`、`PUBLISH_NOT_AUTHORIZED`、`PLUGIN_GHOST_ID_CONFLICT`、`PUBLISH_PREFIX_UNREGISTERED`、`PUBLISH_GHOST_ID_PREFIX_MISMATCH`、`PUBLISH_GHOST_ID_INVALID`、`PLUGIN_GHOST_ID_RESERVED`、`PLUGIN_NAMESPACE_CONFIG_REQUIRED`、`PLUGIN_NAMESPACE_CLIENT_REQUIRED`、`PUBLISH_VERSION_EXISTS`、`PUBLISH_STORAGE_UNAVAILABLE`、`PUBLISH_INTERNAL_ERROR`。
- 202/expired 都不是 HTTP 错误，客户端读 body `status`；推荐暂存 `v1/plugin-upload/<uploadId>/package.cindy`，成功后提升为 `v1/plugins/<orgSlug>/<ghostId>/<version>/<sha256>.cindy`。

### 3.5 Skill 包上传（OSS-08）

| 方法 | 路径 | 请求 | 响应 |
|---|---|---|---|
| POST | `/api/skills-hub/skills/publish/init` | `{slug,version?}` | `{nextVersion,ossKey,uploadUrl}` |
| PUT | `uploadUrl` | zip 字节，`Content-Type: application/zip` | 2xx；403=`OSS_PUT_EXPIRED` |
| POST | `/api/skills-hub/skills/publish/commit` | `{ossKey,slug,version,changelog?,displayName?,summary?,description?,tags?,visibility?,deptTeamSlug?,teamSlug?,visibleSlugs?}` | 成功/业务错误 |

- 服务端 init 分配 `nextVersion`；commit 版本被并发占用返回 `VERSION_RACE`，客户端最多重新 init 2 次；`uploadUrl` 必须接受 `Content-Type: application/zip`，客户端不额外带 ACL 头。
- Skill 产品红线 **<10 MB**；zip 根直接是 `SKILL.md`（无顶层目录），确定性打包（固定时间戳、排除规则与 folderHash 一致）。服务端解压检查 `SKILL.md`、frontmatter `name`/`description`、slug 作者权限。
- 新客户端固定 `cindySkillHubApiBaseUrl`；空 = 云端 Skill Hub 明确关闭，绝不回退旧 `skillhubApiBaseUrl`。
- 推荐 key【推测】：`v1/skill-upload/<uploadId>/package.zip` → `v1/skills/<ownerScope>/<slug>/<version>/<sha256>.zip`。

### 3.6 上限、配额与限流

| 对象 | 客户端上限 | 服务端建议 |
|---|---|---|
| 头像 | 5 MiB；png/jpeg/webp；≤512 px | 20 次/天；旧对象 30 天回收 |
| device-media | 2 GiB/对象；>64 MiB 流式 | 100 presign/小时；日 10 GiB；7/30 天生命周期 |
| 插件包 | 128 MiB/256 MiB/2048 | 20 次/天/组织；暂存 24 h，失败 7 天 |
| Skill 包 | <10 MiB | 20 次/天；暂存 24 h；版本永久 |
| 日志批次 | 500 条/1 MiB/20 s | SLS 单批约 3 MB |
| issue | 标题 200/描述 5000 | 同左；账号/IP 限流 |
| 心跳 | 60 s、5 s 超时 | 1 次/分钟；Redis TTL 90 s |

【推测】配额需产品/风控确认；超限 413/429 + `Retry-After`，不要 500。

---

## 4. 桌面更新

### 4.1 CDN 目录

`cdnBaseUrl` 来自 endpoint（CN `https://hotfix.cindy.com.cn/cindy`，Global `https://hotfix.cindy.app/cindy`），客户端所有清单/资产都拼在其下：

```
{cdnBaseUrl}/
  endpoint.json
  manifest-<platformKey>.json | -beta.json | -canary.json
  app/<platformKey>/<file>                     # app.hotfix / app.installer 的 file
  claude-code/<ver>/<platformKey>/claude[.exe].gz
  codex/<ver>/<platformKey>/codex[.exe].gz     # 旧字段兼容
  codex-package/<ver>/<platformKey>/...tar.gz
  ripgrep/<ver>/<platformKey>/rg[.exe].gz
  pi/<ver>/<platformKey>/...tar.gz
  notice/<platformKey>/<version>.json | index.json
  wallpapers/<sha256>.webp | .mp4
```

- `platformKey = ${process.platform}-${process.arch}`（`darwin-arm64`/`win32-x64`/`linux-x64`）；manifest 与 notice 共用该轴。
- `file` 是相对 baseUrl 的路径，客户端只取 basename，服务端可调整前缀；`codex-package`/`pi` 精确目录属私有流水线【推测】，以 manifest `file` 为准。
- 版本化路径（`claude-code/<ver>/...`）不可变；发布脚本以 `x-oss-meta-gz-sha256`/`binary-sha256` 防覆盖，同版本不同内容默认拒绝。manifest 必须描述远端实际对象的 sha256/size。
- 发布顺序：先传资产并回读校验 → 再原子写 manifest/notice 指针；回滚只改 manifest，不删旧对象。

### 4.2 manifest 全字段

```json
{
  "app": {"version":"0.0.124","releaseNotes":"optional legacy","hotfix":{"file":"app/darwin-arm64/xdt-maker-0.0.124.zip","sha256":"64hex","size":123},"installer":{"file":"app/linux-x64/cindy-0.0.124-amd64.deb","sha256":"64hex","size":123},"requireRelogin":false},
  "claudeCode":{"version":"2.1.108","file":"...","sha256":"...","size":1,"binarySha256":"..."},
  "codex":{"version":"0.0.0","file":"...","sha256":"...","size":1,"binarySha256":"..."},
  "codexPackage":{"version":"0.0.0","file":"codex-package/...tar.gz","sha256":"...","size":1,"binarySha256":"..."},
  "ripgrep":{"version":"14.1.0","file":"...","sha256":"...","size":1,"binarySha256":"..."},
  "pi":{"version":"0.0.0","file":"pi/...tar.gz","sha256":"...","size":1}
}
```

| 字段 | 必填 | 行为 |
|---|---|---|
| `app.version` | 是 | 与 `app.getVersion()` 比较；非法拒绝；只升不降 |
| `app.releaseNotes` | 否 | 旧 UI 兼容；权威是 notice |
| `app.hotfix` | mac/win 是 | 热更 ZIP，`file/sha256/size` 缺一不可 |
| `app.installer` | Linux 是 | 必须 `.deb`；非 `.deb` 拒绝应用内更新 |
| `app.requireRelogin` | 否 | 下载后写 `relogin-required.flag`，新版本启动消费一次 |
| `claudeCode` | Linux 可缺 | 单文件 `.gz`；`binarySha256` 为解压后摘要 |
| `codex` | 否 | 旧客户端兼容；新客户端读 `codexPackage` |
| `codexPackage` | 可缺 | 整目录 tar.gz，根即运行时目录，入口 `bin/codex` |
| `ripgrep` | 可缺 | 单文件 `.gz`；Linux 包内自带可省略 |
| `pi` | 否 | 可选实验 Agent；缺失/失败不阻塞启动 |

- `PlatformAsset.file` 相对 baseUrl；`sha256` 64 hex；`size` 字节；未知字段忽略。
- manifest 响应 ≤8 MiB、超时 30 s；启动检查 8 s；非 200 静默视为无更新；客户端内存缓存按渠道代际失效，不落盘。
- Linux manifest 不带 `hotfix`/`requireRelogin`/agent 资产；打包 Linux 用系统 CLI 或官方回退。

### 4.3 渠道、灰度与 feature flags

`canary > beta > release`（`resolveUpdateChannel`）。

| 渠道 | 来源/持久化 | manifest URL | 失败行为 |
|---|---|---|---|
| release | 默认 | `manifest-<platform>.json` | 无更新 |
| beta | 设备开关 `userData/update-channel-settings.json`（`enableBeta`/`orgDefaultEnableBeta`） | `-beta.json` | 不回落 release；开启前必须 `probeBetaManifest` |
| canary | 账号 flag `userData/canary-flag.json`，登出清除 | `-canary.json` | 不回落 release，避免静默降级 |

- beta 仅部分平台：Linux 只有 x64 发布 beta；不支持时忽略落盘值。
- probe 要求 200 且可解析 `app.version`；Linux 还要求 installer 是 `.deb` 且 sha/size 合法，否则拒绝开启。
- `defaultEnableBeta` 是 feature-flags 可选字段：非 xd 组织可默认打开 beta；用户手动关过后不得再自动打开；xd 组织另有本地默认逻辑。
- 无百分比/corhort 客户端实现；灰度只能由服务端按账号返回 `isCanary`。
- beta/canary manifest 必须含完整 agent 资产，否则 Claude Code/Codex/ripgrep/pi 不可用。
- 下载中切渠道的产物丢弃，不写 `patch-info.json`。

### 4.4 下载、校验、续传

- URL `${cdnBaseUrl}/${asset.file}`，不带 Bearer/自定义鉴权；必须校验 manifest 的 sha256（小写）与 size，不匹配删除临时文件并失败。
- 最多 6 次；退避 1/2/4/8/16/30 s ±25%；仅 NETWORK/HTTP_5XX/单次 TIMEOUT 重试；4xx（416 除外）不重试。
- `.part` + `.meta.json` 记录 URL、ETag/Last-Modified、expectedSize、sha256；`Range` + `If-Range`；服务端忽略 Range 返回 200 时从零重下；416 丢弃重下。
- 重定向手动、最多 5 跳，每跳重新校验 URL 策略；不允许跨 host 带凭据。
- 进度单调、0–100、≤5 次/秒；`update-status` 与旧 `app-update-progress` 双通道。
- 下载后写 `patch-info.json`（`version/fileName/sha256/requireRelogin/enableBeta`），只有 sha 校验通过才写。

CDN 要求：支持 Range/ETag/Last-Modified/Content-Length/Content-Type；`sha256` 描述对象字节而非解压后；版本化资产不可变、不可转码；大文件 `Cache-Control: public,max-age=31536000,immutable`，manifest 短 TTL/no-cache；客户端已带 `?t=`。

### 4.5 apply / relaunch / 回滚（UPD-05）

状态：`idle → checking → downloading → ready → superseding → relaunching`；失败 `error` 或回 `ready`。

启动决策（`checkExistingPatch`）：

- `patch-info.json` 缺失/损坏/文件不存在 → 正常检查；`enableBeta` 与当前渠道不一致 → 丢弃（防共库另一实例切渠道后装错包）。
- 版本相同 → 已应用清理；更旧 → 丢弃；`applyAttempts >= 3` → 放弃并清除，避免更新器崩溃后无限重试；否则 `relaunch`。

| 平台 | 执行 | 关键点 |
|---|---|---|
| Windows | `cindy-updater.exe`（Tauri/Rust） | CLI `--zip --app-dir --exe-name --pid --log --lock [--theme] [--workdir]`；等 PID 60 s；解压 `%TEMP%`；选择性备份将被覆盖文件；复制重试；启动 3 s 内验证；失败回滚；`ShellExecuteExW(runas)` 自提权；锁 `updating`；失败留 `retry.zip`；>7 天临时目录清理 |
| macOS | `/bin/bash` 更新脚本 | 等旧 PID 最多 120 s，超时 SIGKILL；等 2 s；`ditto -x -k`；清残留进程；`mv` 替换 `.app`；`xattr -cr`；`open` 验证；同一更新锁 |
| Linux | `bash -c` + `pkexec` | 只接受 `.deb`；复制后用 `sha256sum` 再校验；`dpkg --install`/`apt-get`；校验安装布局；锁心跳 5 s；非受管/架构不符拒绝 |

共享保护：macOS App Translocation 禁止热更；更新前回收 Pi Subagent（最多 3 轮/6 s），不能确认停止则取消；Windows 缺 app-local VC Runtime 时保留补丁、不消耗重试；自动重启默认关闭，条件为系统 idle ≥10 min、无活跃任务、60 s 无 busy/resume、屏幕状态可读，Linux 需交互授权时禁止无人值守；`requireRelogin` 标记由新版本 auth 初始化消费一次，版本必须匹配。

服务端不参与 apply；不要设计“服务端触发重启/远程安装”。Agent 更新只能走同一 `checkForUpdate → executeRelaunch` 且经宿主确认卡。

### 4.6 更新说明与公告（UPD-08）

```
{cdnBaseUrl}/notice/<platformKey>/<version>.json?t=<ms>
{cdnBaseUrl}/notice/<platformKey>/index.json?t=<ms>
```

`index.json` 是升序版本数组；渲染层据此展示跨版本升级后的所有未读版本。

单版本允许 `version/date/githash?/contributors?`、`contentByLocale`（5 locale：`zh-CN`/`zh-TW`/`en`/`ja`/`ko`）以及根级 `sections/topics/intro` 旧格式。`contentByLocale` 每项可含 `intro`、`topics[{id?,emoji?,title,text,contributors?}]`、`sections[{title,items[{name,list}]}]`。

- `topic` 需 title+text 非空；`section` 至少一个带 name 且 list 非空的 item，否则视为不可渲染。
- 客户端最多 4 次尝试（1/2/4 s 退避）；非 200/JSON 失败不重试；响应 ≤8 MiB、15 s；200 但无渲染内容不缓存并按失败处理。
- 客户端经 main `net.request` 访问，CDN 无需 CORS；没有独立全局公告 API，公告面就是更新说明。
- `app.releaseNotes` 仅旧版兼容；权威是 CDN notice。

### 4.7 桌面错误码
`manifest_failed`（404/网络/JSON/版本非法，静默 idle）、`download_failed`（下载/校验失败，error 可重试）、`manual_download`（环境不支持，预留）、`idle`（最新/无适用资产/版本无关包）、`translocated`（macOS 未移入 Applications，error）、`linux_installation_unsupported`（非受管/区域不符，保留 ready）、`subagent_reclaim_unconfirmed`（无法确认 Subagent 停止，取消重启并保留补丁）。

---

## 5. 移动更新

### 5.1 整包 `/latest`（UPD-09）

```
GET {mobileUpdateBaseUrl}/latest?platform=ios|android[&channel=canary|beta]&t=<ms>
Accept: application/json; Cache-Control: no-cache
```

- release 不带 channel；canary/beta 带 `channel=`；8 s 超时；404 = 服务端确认无记录（无更新）；5xx/网络/超时 = 检查失败，不能误报“已是最新”。
- 一律 cache-buster + no-cache：`/latest` 是可变指针，`minVersion` 可能原地改，边缘旧副本会误挡/错放。
- 仅自建变体消费；`mobileUpdateBaseUrl` 为空不请求。

记录：`{version, buildNumber?, runtimeVersion, installUrl, itmsUrl, releaseNotes?, minVersion?}`。

- `version`/`minVersion` 点分纯数字（每段安全整数）；`runtimeVersion` 非空；`installUrl` 与 `itmsUrl` 至少一个；`buildNumber` 字符串/数字可缺。
- 任一无效应整体视为无更新，不要部分使用；推荐每平台每渠道一个可变指针 `mobile-ota/<platform>/<channel>.json`，对象不可变、指针原子替换。

### 5.2 OTA manifest 与 runtimeVersion（UPD-10）

- `runtimeVersion` 由 Expo fingerprint 生成，不透明；服务端精确匹配，不解析。
- 自建 OTA URL `${mobileUpdateBaseUrl}/manifest`，由 endpoint 解析后通过 `Updates.setUpdateURLAndRequestHeadersOverride` 覆写，不写进 JS 包。
- 请求头：`expo-platform`、`expo-runtime-version`、`expo-protocol-version`、`expo-api-version`、`EAS-Client-ID: 00000000-0000-4000-8000-000000000000`（自建线共享）、`x-cindy-update-channel: canary|beta`（release 不带）。
- 响应必须与 `expo-updates@57` 原生解析器兼容。推荐官方自定义更新服务器协议：200 + `multipart/mixed`，至少 `manifest` part（可带 `extensions`）；manifest 含 `id`、`createdAt`、`runtimeVersion`、`launchAsset`、`assets`、`metadata`、`extra`；launchAsset/assets 元素含 `hash`、`key`、`contentType`、`url`；无更新的精确表示以原生层为准（列为未确认项），优先复用官方实现，不要自创 JSON。
- 资产按 `hash` 缓存；同 runtime 发新 bundle 生成新 `id`，不同 runtime 绝不复用旧 bundle。
- 自建线 `checkAutomatically: NEVER`，JS 启动/resume/手动主动检查；bridge 模式 `disableAntiBrickingMeasures: true`，native v1 为 false（原生事务恢复保护）。
- 启动 check 2.5 s/fetch 8 s；resume check 10 s/fetch 60 s；全部 fail-open，不阻塞进 App。
- 防循环：同 `updateId` 最多 reload 2 次；emergency launch 不 reload，后台只下载不重启；落盘成功才计数。
- 两阶段 header 协调器：旧 header 启动当前 bundle → 共享 UUID 下载新 `updateId` → 成功落盘才持久切换；失败恢复旧 header，避免下次冷启动筛不到当前 bundle。

### 5.3 移动渠道（UPD-11）

- canary：账号级 `/api/user/feature-flags.isCanary`；仅自建线；AsyncStorage `cindy.mobile.update.canary`；登出/换号清除；失败保留旧值。
- beta：设备级 AsyncStorage `cindy.mobile.update.beta.meta.v1`（兼容旧 key）；登录后可按组织默认预打开；探测 `/latest?channel=beta`，404/5xx 均不可用。
- 优先级 `canary > beta > release`，canary 命中忽略 beta。
- 渠道同时影响 `/latest?channel=` 与 OTA 头 `x-cindy-update-channel`，服务端必须一致；release 不带 channel，不得把缺失解释成 beta/canary。

### 5.4 强更 minVersion（UPD-12）

- 服务端 `version` 必须高于原生 `APP_BINARY_VERSION`（不能用可能被 OTA 覆盖的 `expoConfig.version`）。
- `forced = currentVersion < minVersion && record.version >= minVersion`；门槛必须可满足，否则退化为普通可跳过更新，避免无出口阻断屏。
- 强更**不经过 runtimeVersion 门闸**：同 runtime 的旧构建也能强更；非强更且 runtime 相同 → 交给 JS OTA；runtime 不同 → 提示整包。
- 命中后 root 不挂业务树，只渲染唯一“去更新”出口；状态仅内存，不持久化。
- 拉取失败/无安装地址/门槛不可满足 → fail-open，不进入阻断。
- 阻断屏回前台 + 30 s 节流重查；只有成功拉到 `/latest` 且不再强更才解除，断网不能绕过；安装地址优先 `itmsUrl`，缺失回退 `installUrl`。
- 撤回 `minVersion` 必须改同一个 `/latest` 指针，客户端只读这一处。

### 5.5 审核模式、TestFlight、Google Play（UPD-13）

- endpoint `review`：手机送审版本号，客户端用自身二进制版本严格相等匹配，且 iOS 非 TestFlight 才进入审核模式；关闭全部 JS 显式更新检查（启动热更/整包/resume/设置页）。
- 管不到 expo-updates 原生冷启动后台检查；审核窗口内必须同时冻结 OTA 发布；过审后清空 `review`，否则同版本线上用户永久收不到更新检查。
- TestFlight 不进入审核模式；其 JS OTA 走独立通道。
- Google Play 安装不检查自建整包（安装来源 `com.android.vending`），由 Play 管理；自建 APK/AAB 共用原生配置，不能只用 `isSelfHosted` 区分。
- 非自建 EAS/TestFlight 手动 OTA 仍带隐私同意闸；自建 OTA 已改共享 UUID，不再复用 analytics consent。

---

## 6. 远程配置

### 6.1 endpoint.json 全字段

区域级公开 JSON，`schemaVersion` 当前为 **1**；客户端先拉清单再做更新检查。

| 字段 | 协议 | 消费方 | 说明 |
|---|---|---|---|
| `schemaVersion` | number | 两端 | 必填正整数；>1 整份拒绝 |
| `authApiBaseUrl` | https | 两端 | auth-server：登录/刷新/`/api/me/profile` |
| `authDesktopCallbackUrl` | https | desktop | 服务端托管回调；非空走托管回调，空回退 loopback；清空即远程回滚 |
| `chatApiBaseUrl` | https | 两端 | chat-server；允许跨区（两区均 `cindy.app`） |
| `deviceLinkApiBaseUrl` | https | 两端 | relay REST + WS |
| `oauthBrokerApiBaseUrl` | https | 两端 | OAuth broker + `/api/user/feature-flags` |
| `ossApiBaseUrl` | https | 两端 | oss-server |
| `heartbeatUrl` | https | desktop | heartbeat-server |
| `telegramHookWsUrl` | wss | desktop | 空 = 未上线，设置卡隐藏 |
| `xHookWsUrl` | wss | desktop | 同上 |
| `slackHookWsUrl` | wss | desktop | 同上；允许跨区 |
| `websiteUrl` | https | 两端 | 官网/帮助 |
| `modelAccessApiBaseUrl` | https | 两端 | 模型目录/凭据/配额/充值 |
| `voiceApiBaseUrl` | https | 两端 | 语音输入 |
| `githubApiBaseUrl` | https | desktop | 反馈 issue + 我的 issue |
| `skillhubApiBaseUrl` | https | desktop 旧 | 已发布客户端继续消费，不得改指新服务 |
| `cindySkillHubApiBaseUrl` | https | desktop 新 | 空 = 云端 Skill Hub 明确关闭，不回退旧地址 |
| `pluginApiBaseUrl` | https | desktop | 插件市场/组织发布 |
| `cdnBaseUrl` | https | 两端 | 更新/壁纸/公告 |
| `mobileUpdateBaseUrl` | https | mobile 自建 | `/latest` 与 `/manifest`；仅自建变体 |
| `review` | string 可选 | mobile | 送审版本号；缺失/空 = 关闭审核模式 |
| `region` | `cn`/`global` 可选 | 两端 | 自报区域；与 expectedRegion 不一致整份拒绝 |

解析【契约】：缺失/空白归一 `''` 不阻断；非空必须字符串；所有 URL https、hook wss，生产/CDN 不放宽 http/ws（仅 dev 本地文件 `allowHttp`）；去尾斜杠；拒绝 URL 凭据；未知字段忽略；`schemaVersion` 缺失/非正整数/过大整份拒绝；`review` 非 string 整份拒绝；`region` 非法或不匹配整份拒绝。

### 6.2 信任模型与离线回退

1. **主源**：构建期烘焙 `<cdnBaseUrl>/endpoint.json`，TLS 认证；`app.ready` 内阻塞解析。
2. **镜像**：仅当主源等于官方随包 `cdnBaseUrl/endpoint.json` 时启用 `config/endpoint-manifest-mirrors.json`（GitHub raw）；最多 1 个，总预算 2 次 × 2.5 s。
3. **缓存**：`userData/endpoint-manifest-cache[.region].json` ≤128 KiB；记录 `savedAt/sourceUrl/manifestText`；`sourceUrl` 必须完全一致；只接受严格解析且落在受信任域的端点。
4. **随包兜底**：`config/endpoint.json`/`endpoint.global.json`；仅当 sourceUrl 等于随包 `cdnBaseUrl/endpoint.json`；自定义/自托管源绝不回退 Cindy 生产。

- 网络失败、5xx、407/408/425/429 → 可镜像/缓存/随包；其他 4xx、JSON/schema/非法 URL/区域不符 → **配置事故，直接阻断**。
- 不做逐字段合并：一次启动只用一份完整快照。
- 受信任域写死：`cn → cindy.com.cn`，`global → cindy.app`；跨区 key 仅 `chatApiBaseUrl`、`slackHookWsUrl`、`telegramHookWsUrl`、`xHookWsUrl` 允许 global 域，其余（auth/device-link/oauth-broker/model-access/voice）锁本区，防跨区 token 误发。
- 缓存无 TTL，`savedAt` 仅诊断；修改 CDN 清单后重启生效，没有推送。
- 清单公开可读；CDN 写权限、TLS、受信任域、缓存来源绑定共同构成信任边界。

### 6.3 feature flags（OBS-03）

```
GET {oauthBrokerApiBaseUrl}/api/user/feature-flags   Authorization: Bearer <token>
→ { "isCanary": true, "defaultEnableBeta": true }
```

- `isCanary` 必须 boolean；缺失/非 boolean 视为非法，客户端**保留本地旧值**，避免瞬时故障静默降级。
- `defaultEnableBeta` 可选，只有 true 有意义；未知字段忽略，可 append-only 扩展。
- 请求失败/非法响应保留旧值；只有成功且 auth 代际/uid 仍匹配才落盘。
- 登出清 canary（桌面 `canary-flag.json`，移动 AsyncStorage）；换号不得继承上一账号通道。
- 桌面登录态落地后异步请求，不阻塞主界面；移动仅自建线请求。
- 服务端按账号返回；没有百分比/cohort；1%/5% 灰度只能服务端按用户哈希决定 `isCanary`。

### 6.4 公告与更新说明

- 当前客户端没有独立全局公告/横幅 API；公告面只有桌面 notice 更新说明（4.6）。
- 不要臆造 `/api/announcements`；若产品确需运营公告，需新增客户端版本与协议，并明确展示位置、已读态、locale 回退、撤回语义。
- 既有远程开关可承担部分运营：hook URL 置空 = 渠道未上线；`authDesktopCallbackUrl` 置空 = 回调回滚 loopback；`review` = 审核模式；`app.releaseNotes` = 旧版说明。
- 更新说明权威源是 CDN，不是数据库；保证原子发布、index 升序、版本齐全。

### 6.5 壁纸与静态资源（UPD-14）

- 当前清单 16 套，全部 `delivery:"cdn"`；每套 WebP 预览 + MP4；key `wallpapers/<sha256>.webp` / `.mp4`，内容寻址，内容变则新 hash。
- 客户端从 `cdnBaseUrl` 拼 URL；HTTPS、禁 URL 凭据、禁重定向（`redirect:'error'`）、`credentials:'omit'`；校验 sha256/bytes、单次 120 s、最多 3 次；缓存键是内容哈希，升级/换账号不清。
- 两区相同内容哈希；发布时 cn/global 分别上传并复核字节数、SHA-256、Content-Type；不删旧清单仍引用的对象。
- 纯静态，无服务端 API；Go 最多提供发布校验工具，不在请求路径代理字节。

### 6.6 运营开关

| 开关 | 位置 | 影响 | 回滚 |
|---|---|---|---|
| `cdnBaseUrl` | endpoint | 更新/壁纸/公告切域 | 改回旧值并重启 |
| `authDesktopCallbackUrl` | endpoint | 托管回调 vs loopback | 置空 |
| hook URL | endpoint | 渠道显示/连接 | 置空 |
| `review` | endpoint | 手机审核模式 | 清空 |
| `isCanary` | feature flags | 账号级 canary | 返回 false |
| `defaultEnableBeta` | feature flags | 组织默认 beta | false/缺省 |
| `minVersion` | `/latest` | 移动强更 | 改回 null/删除 |
| `channel` | `/latest` + OTA header | beta/canary 分支 | 改回 release 指针 |
| `app.version` | manifest | 桌面更新目标 | 改回旧 manifest |

---

## 7. heartbeat 服务（OBS-05/06）

```
POST {heartbeatUrl}/heartbeat   Content-Type: application/json
{ "uid":"<user.id>", "platform":"darwin", "version":"0.0.124" } → 200 { "ok":true }
```

- 无鉴权；`uid` 为 cloud 登录态 user.id；未登录/本地模式不启动。
- 启动立刻一次，之后每 **60 s**；单次超时 5 s；无 jitter/backoff/retry；任何非 2xx/网络失败只 warn，不抛、不影响 App；`getUid()` 为 null 跳过本次。
- `platform`=`process.platform`，`version`=`app.getVersion()`；服务端接受缺失/未知字段。
- 桌面仅在已验证 cloud 会话启动；uid/realm 变化重启，退出显式 stop；本地/匿名/未登录不得向云端上报在线状态。

服务端：Redis `presence/hb:<uid>` → `{platform,version,lastSeenAt}`，TTL **90 s**（60 s 心跳 + 30 s 容错），每次刷新；在线数=未过期 key；DAU 用 HLL/日 rollup，不在高频心跳写 PG；仅用于在线/DAU，不参与 device-link 路由/presence/调度；单 uid 限流 1 次/分钟；响应 `{ok:true}`；不长期存 IP/UA。

---

## 8. 日志上传（OBS-07/08/09/10）

### 8.1 目标与鉴权

客户端**直接写阿里云 SLS**，服务端不代理、不持 AccessKey：

```
POST https://<project>.<endpointHost>/logstores/<logstore>/track
```

| 区域 | project | logstore | endpointHost |
|---|---|---|---|
| cn | `cindy-sh-prod` | `cindy-sh-prod-client-log` | `cn-shanghai.log.aliyuncs.com` |
| global | `cindy-sgp-prod` | `cindy-sgp-prod-client-log` | `ap-southeast-1.log.aliyuncs.com` |

- 免签 web tracking，无签名、无用户 token；靠 project+logstore 定位。
- 目标值**构建期注入**（`XDT_LOG_UPLOAD_TARGET`），不进 endpoint：远程可改的免签写入地址等于允许把日志改投他人 logstore。
- 运行期再校验 `endpointHost` 匹配 `<region>.log.aliyuncs.com`、project/logstore 符合 SLS 命名；非法则整体静默关闭，不降级。
- 只烘焙本区目标；cn 包不含 global，反之亦然；未配置/未同意隐私政策/自动开关关闭时不产生任何字节。

### 8.2 采集与脱敏

四层防线（客户端唯一防线，服务端无机会再过滤）：

1. **源白名单**：只读 `main-*.log` 与 `agent-*.ndjson`（崩溃仅 proxy）；`sessions/**`（对话正文）与 `*cc-debug.raw.log` 永不打开。
2. **记录白名单**：deny-by-default，按 scope 根/精确匹配；只放行生命周期、崩溃、网络、更新、数据库、鉴权、设备互联连接层；未知来源丢弃。
3. **正则红线**：Authorization/Cookie 整头、Bearer、JWT、`sk-`、GitHub token、云 AccessKey、PEM 私钥、敏感字段名值（含转义 JSON）、URL query、邮箱（留首字母+域名）、家目录用户名。
4. **字段白名单+截断**：最终只保留 `ts/level/src/scope/msg`，正文 ≤2000 字符；`tz/seq/sessionId` 等丢弃。

### 8.3 wire 格式与批次

```json
{
  "__topic__":"cindy-client-log","__source__":"darwin-arm64",
  "__tags__":{"uploadCode":"ABCD-EFGH","deviceId":"...","appVersion":"0.0.124","region":"cn",
    "platform":"darwin","arch":"arm64","osVersion":"...","uiLanguage":"zh-CN",
    "reason":"manual|crash-immediate|crash-backfill","userId":"optional","crashToken":"optional","crashAtMs":"optional"},
  "__logs__":[{"ts":"...","level":"info","src":"main","scope":"updateService","msg":"...","uploadCode":"ABCD-EFGH"}]
}
```

- 必选头：`Content-Type: application/json`、`x-log-apiversion: 0.6.0`、`x-log-bodyrawsize: <未压缩 UTF-8 字节数>`。
- **不压缩**：不发 `x-log-compresstype`；`bodyrawsize` 按字节数，不用 `body.length`。
- 批次 ≤500 条、≤1 MiB（预留 4 KiB envelope），按 UTF-8 精确切；单条超预算仍单独成批，让服务端明确拒绝。
- 批次串行；任一批失败整次失败并停止后续，避免部分成功清掉补传标记；单批超时 20 s；SLS 单批约 3 MB。
- `__tags__` 全字符串；未登录不写 userId；不写空串。

### 8.4 上限、重试与状态

| 维度 | 值 |
|---|---|
| 默认回溯 / 崩溃补传 | 2 天 / 最早未传崩溃放宽，上限本地保留 30 天 |
| 单文件 / 单次总读取 | 8 MiB / 24 MiB |
| 合并条数 / 单条正文 | 4000 / 2000 字符 |
| 锚点预卷 / 自动节流 | 崩溃前 2 分钟 / 同进程 5 分钟 |
| 启动补传延迟 / 手动并发 | ready-to-show 后 15 s / 1 次 |
| 上传编号 | 8 位 `XXXX-XXXX`，字符集去 0/1/I/L/O/U |

状态：`uploaded`、`skipped-not-configured`、`skipped-no-consent`、`skipped-crash-auto-off`、`skipped-consent-unknown`、`skipped-throttled`、`empty`、`failed(status)`、`error`。

- 崩溃即时只同步写 <400 B 标记，然后 fire-and-forget 上传，不阻塞退出。
- 标记一崩溃一文件 `pending-<crashAtMs>-<token>.json`，rename 原子认领；成功且非空才清；失败/0 条还原；授权关闭清空全部；>30 天直接删；跨进程用代次令牌防误删；陈旧 claim 1 h 后可重领。
- 断网/5xx 保留标记，下次启动补传；部分批次成功也按整次失败，宁可重传不丢。

### 8.5 服务端重建边界

- 继续用 SLS：服务端**没有**日志上传端点；只维护 project/logstore、web tracking 开关、30 天保留、权限与看板；不要新增代理层。
- 若必须自建接收：兼容上述 `track` URL/头/envelope，或发新客户端改地址；服务端仍二次脱敏（token/Authorization/URL query）与字段白名单；严禁记录请求体；加密存储、30 天、按 uploadCode/region/userId 索引；账号删除时按 userId 清理/匿名化。
- 免签写入是滥用面：按 project/logstore/IP 限流，拒绝非 `cindy-client-log` topic，设单批/日配额；不要把写入地址做成可任意指定 logstore 的开放代理；`uploadCode` 只用于支持定位，不是身份凭证。

---

## 9. 反馈 / GitHub issue（OBS-11/12）

### 9.1 创建 issue

```
POST {githubApiBaseUrl}/api/github/issues   Authorization: Bearer <token>
{ "title":"≤200", "description":"≤5000", "type":"bug|feature", "appVersion":"0.0.124", "userName":"可选公开署名" }
→ { "githubIssue": { "number":1234, "url":"https://github.com/makecindy/cindy/issues/1234" } }
```

- 服务端以平台 Bot 在 `makecindy/cindy` 创建；`type` 映射 `bug`/`feature` 标签。
- `userName` 是用户确认的公开署名；缺失时按 membership id 回退；服务端校验长度/字符集，防 Markdown 注入与 impersonation。
- 上限与客户端一致：标题 200、描述 5000；超限 400，不截断后静默成功。
- 描述末尾有“提交时的任务环境”块（区域/OS/arch/osVersion/Harness/Model ID/界面语言）与可选相关日志；服务端原样保留 Markdown，不改写；日志只在用户明确同意时附带。
- 错误映射：401→`AUTH_NOT_READY`，网络 0→`NETWORK_ERROR`，其他→`SERVER_ERROR`；服务端返回稳定 `error.code`。
- 客户端无幂等键；建议按 `(membershipId,title,60s)` 软去重，或后续版本加 `Idempotency-Key`（当前不可依赖）。

### 9.2 我的 issue

```
GET {githubApiBaseUrl}/api/github/issues/mine   Authorization: Bearer <token>
```

- 只返回当前 membership 平台代发的 issue；不要求用户有 GitHub 账号。
- 外层兼容 `{issues:[]}`/`{items:[]}`/裸数组；可选 `total_count`/`totalCount`/`total`/`hasMore`。
- 每条至少 `number/title/html_url|htmlUrl|url/created_at|createdAt`；`state` 只有 `closed` 识别为关闭；`labels` 可为字符串数组/对象数组/单字符串；`comments`/`commentCount` 可选。
- 客户端 10 s 超时；404/501 → `platform-unavailable`，降级为只展示本机账本；接口上线后零改动生效。
- 服务端可聚合 GitHub API，但必须按当前用户过滤；绝不返回他人 issue。本地账本只是补充，平台就绪时合并去重。

### 9.3 身份与隐私

- 平台路径：服务端持有 GitHub App/机器账号代发；客户端常量 `{kind:'platform', login:'cindy-issue'}`，真实登录名由服务端决定。
- 用户 GitHub 身份路径：客户端通过 `cindy-github` 插件 PAT 直接 `create_issue`，不经过 github-server；服务端不持用户 PAT，也不提供代替用户 PAT 的接口。
- 确认卡是唯一提交入口；客户端确认前已高置信度脱敏，用户编辑视为确认；服务端不得绕过或自动提交。
- 服务端日志不记录完整正文（可能含用户粘贴日志/路径）；只记 issue number、membershipId、type、结果。按 membership+IP 限流，GitHub 二级限流时排队/退避并返回明确错误。

---

## 10. TapDB 遥测边界（OBS-13）

客户端直报 TapDB，不经过 Cindy 服务端：

| 区域 | appId | 采集端点 |
|---|---|---|
| cn | `gczef0ey3e8ogpmizs` | `https://e.tapdb.com/event` |
| global | `h08anxdfrvfocfs894` | `https://e.tapdb.ap-sg.tapapis.com/event` |
| dev | `gczef0ey3e8ogpmizs` | `https://e.tapdb.com/event` |

- 桌面 renderer SDK 上报 `app_start`/`app_engaged`（30 分钟节流、每天最多 48 条）/`page_hide`/`setUser`；移动由 `xdt-tapdb` 原生模块上报。
- consent 决定是否上报；`optOutTracking` 后客户端闸立即停止，SDK 侧失败重试；日志上传与 analytics consent 是两件事。
- 服务端**不承担**：客户端事件采集/存储、SDK 初始化、consent 判定、设备标识生成。
- 服务端**承担**：model-access 的 charge 事件上报到同区同项目；必须与客户端 appId/端点配对。TapDB 只统计同一项目中通过 SDK 上报过 `user_login` 的 user_id 的充值，因此要保证客户端登录事件已发生（曾有 global 报进国内项目导致国际收入全丢）。
- 不要新增 `/api/telemetry` 代理：客户端无此契约，代理增加 PII 面且无法改变 SDK 行为；服务端日志/审计不得记录 distinctId/设备标识/事件 payload。

---

## 11. 数据模型与对象键

### 11.1 建议表

| 表 | 关键列 | 用途 |
|---|---|---|
| `oss_objects` | `id,region,key,owner_user_id,scene,visibility,content_type,size,sha256,state,created_at,expires_at` | 对象登记/presign/删除/配额对账 |
| `device_media_objects` | `key,owner_user_id,shared_task_id,size,content_type,sha256,state,created_at,last_access_at` | 媒体归属与生命周期（可并入上表） |
| `upload_quota_usage` | `user_id,region,day,bytes_used,object_count` | 日配额；Redis 计数 + 异步 PG |
| `plugin_upload_tasks` / `skill_versions` | 插件：`upload_id,org_id,membership_id,size,sha256,status,failure_code,release_id,expires_at`；Skill：`slug,version,owner_scope,zip_sha256,object_key,visibility,review_status` | 上传状态机与版本（06 主责） |
| `app_releases` / `app_release_notes` | `region,platform_key,channel,version,manifest_json/content_json,published_at,published_by` | manifest/公告发布审计；CDN 仍是权威 |
| `update_channel_assignments` / `feature_flag_org_defaults` | `user_id,channel,source`；`org_id,flag,value` | canary 名单与组织默认 |
| `heartbeat_daily` | `day,region,platform,version,dau` | DAU rollup；明细只在 Redis |
| `github_issue_links` | `membership_id,github_number,title,type,public_name,identity,created_at` | mine 查询与审计 |
| `audit_events` | 见 08 | manifest/minVersion/flag/对象删除审计 |

### 11.2 对象键（建议）

统一 `v1/` 前缀；只允许 `[A-Za-z0-9._/-]`，禁 `..`、前导 `/`、空段：

```
v1/avatar/<userId>/<sha256>.<ext>
v1/device-media/<userId>/<yyyy>/<mm>/<uuid>.<ext> | v1/device-media/shared/<sharedTaskId>/<userId>/<uuid>.<ext>
v1/plugin-upload/<uploadId>/package.cindy → v1/plugins/<orgSlug>/<ghostId>/<version>/<sha256>.cindy
v1/skill-upload/<uploadId>/package.zip → v1/skills/<ownerScope>/<slug>/<version>/<sha256>.zip
v1/releases/desktop/<platformKey>/<version>/<filename> | v1/releases/mobile/<platform>/<version>/<filename>
v1/wallpapers/<sha256>.webp | .mp4
```

- device-media 的 userId 段必须可解析并与 Bearer 比对；客户端从不解析。
- 公开（头像/壁纸/发布资产）与私有（device-media/插件暂存/Skill 暂存）分桶或分前缀；不靠对象名保密。
- 插件/Skill 提升优先 copy + 新 key，不在 commit 后引用可覆盖的 upload 前缀；发布资产不可变，回滚保留旧对象。

### 11.3 ACL、生命周期、配额

| 类别 | ACL | 生命周期 | 备注 |
|---|---|---|---|
| 头像 | public-read/CDN | 旧版本 30 天 | 新 key 内容寻址 |
| device-media | private | 未引用 7 天/共享 30 天 | 客户端用后删，生命周期兜底 |
| 插件暂存 | private | 24 h；失败 7 天 | commit 后提升 |
| 插件 Release | private（签票下载） | 永久/按版本 | 市场下载由 plugin-server 签票 |
| Skill 暂存/版本 | private | 24 h / 永久 | 旧版本继续可装 |
| 桌面/移动发布资产 | public/CDN | 永久或 N 版 | 不可变 |
| 壁纸 | public/CDN | 永久 | 内容寻址 |
| 日志 | 外部 SLS | 30 天 | 服务端不存原文 |

配额见 3.6；Redis 原子计数，超限 429/413，异步落 PG 便于账单/审计。

---

## 12. Go 实现映射

### 12.1 包结构

```
cmd/cindy-server/main.go
internal/ossapi/          presign、对象登记、配额、归属
internal/media/           device-link 媒体（或并入 devicelink/media）
internal/pluginpublish/   插件上传（06）
internal/skillhub/        Skill init/commit（06）
internal/updates/         桌面 manifest/notice 发布与校验
internal/mobileupdate/    /latest、expo OTA manifest
internal/remoteconfig/    endpoint、feature flags、运营开关
internal/heartbeat/       /heartbeat、Redis TTL、日 rollup
internal/logingest/       可选：自建 SLS 兼容接收
internal/githubissue/     平台 issue / mine
internal/telemetry/       仅 model-access charge
internal/cdnpublish/      资产/manifest 发布 CLI/worker
internal/presign/         S3/MinIO presigner
```

### 12.2 路由

| 方法 | 路由 | 模块 | 鉴权 |
|---|---|---|---|
| POST | `/api/oss/presign-put` | ossapi | Bearer |
| POST | `/api/device-link/media/presign-put` | media | Bearer |
| POST | `/api/device-link/media/presign-get` | media | Bearer |
| DELETE | `/api/device-link/media` | media | Bearer |
| POST/GET | `/api/publisher/uploads`、`/api/publisher/uploads/{id}`、`.../commit`、`/api/publisher/releases/mine` | pluginpublish | Connection JWT |
| POST | `/api/skills-hub/skills/publish/init`、`.../commit` | skillhub | Bearer |
| POST | `/heartbeat` | heartbeat | 匿名 |
| GET | `/api/user/feature-flags` | remoteconfig | Bearer |
| POST | `/api/github/issues` | githubissue | Bearer |
| GET | `/api/github/issues/mine` | githubissue | Bearer |
| GET | `/latest` | mobileupdate | 匿名 |
| GET | `/manifest` | mobileupdate | 匿名（按 runtimeVersion/channel） |

### 12.3 关键实现点

- **Presigner**：AWS SDK v2/MinIO；签名版本与客户端 PUT 头严格一致，返回 `headers` 原样；设备媒体签 `Content-Type` + `x-oss-object-acl: private`；头像签 `Content-Type`，public ACL 由桶/CDN 策略决定。
- **归属**：从 key 前缀解析 owner，不信 body userId；共享任务查成员表；越权统一 404。
- **配额**：Redis `INCRBY` + 日 key 过期；大对象先扣再 presign，失败释放。
- **幂等**：插件 uploadId/commit 幂等；Skill 唯一索引 `(slug,version)` 返回 `VERSION_RACE`；issue 可软去重。
- **发布**：`cdnpublish` 先传不可变资产，再 HEAD 校验 size/sha256/content-type，最后原子替换 manifest/notice；支持 `--dry-run` 与两区并行。
- **移动 OTA**：优先复用 Expo 官方 self-hosted updates server 协议；服务端只存 `runtimeVersion → bundle` 与 channel 指针；资产走 CDN，不经 Go。
- **feature flags**：按 user_id 查 canary 名单/组织默认；不缓存超过 auth 生命周期；失败 5xx 让客户端保留旧值。
- **heartbeat**：Redis pipeline，TTL 90 s；异步日 rollup；不在请求路径写 PG。
- **日志**：默认保持 SLS；自建 `logingest` 时实现 `track` 兼容层 + 二次脱敏 + 严格配额。
- **GitHub**：App token 只在服务端；创建与 mine 共用 membership 映射；不记正文。
- **TapDB**：只在 model-access 充值链路调用；appId/端点按区域配对。
- **配置**：`REGION`、`OSS_ENDPOINT/BUCKET/ACCESS_KEY`、`CDN_BASE_URL`、`SLS_*`、`GITHUB_APP_*`、`TAPDB_*`、`MOBILE_UPDATE_*` 全部环境变量/Secret Manager，不写仓库。
- **可观测**：RED 指标；重点 `presign_total{scene,result}`、`oss_upload_bytes`、`update_manifest_requests{channel,result}`、`heartbeat_online`、`github_issue_total{result}`、`mobile_latest_total{channel,result}`；审计 manifest 发布、minVersion 变更、flag 名单变更、对象删除。

---

## 13. 未确认项

1. 设备媒体对象键精确格式/桶名与生命周期天数；客户端只要求不透明、可归属、内嵌 userId，7/30 天为建议。
2. 头像公开桶 ACL、CDN 缓存头、旧头像回收策略；各 presign 的 `expiresAt` 实际 TTL（客户端不解析，建议 5–15 分钟）。
3. 上传配额阈值与计费口径（3.6 为建议）。
4. 共享任务 `sharedTaskId` 在 key 中的编码、guest 读权限精确规则；需与 04 对齐。
5. 私有发布流水线中 `app.hotfix.file`、`app.installer.file`、`codex-package`、`pi` 的稳定目录命名，以及 beta/canary manifest 是否对所有 platformKey 发布完整 agent 资产；客户端只信 manifest。
6. 桌面 manifest 无签名，只有 SHA-256 与安装包代码签名；CDN 写权限即代码执行权限，必须靠发布审计/不可变对象/代码签名防护。
7. 移动 OTA 无更新响应、multipart 边界、`extensions` 内容的官方表示；优先复用 expo-updates 官方协议并做兼容测试。
8. `runtimeVersion` 具体指纹值与发布流水线如何保证同 runtime 复用；服务端只按字符串匹配。
9. `/latest` 后端存储与 CDN 缓存头、`minVersion` 撤回的运维命令与审计；客户端强制 no-cache，只观察 `/latest`。
10. feature flags 除 `isCanary`/`defaultEnableBeta` 外是否有已下发字段，以及是否存在未公开的全局公告接口；客户端忽略未知字段，当前只发现更新说明通道。
11. 壁纸 16 套清单的发布归属、两区缓存头、旧对象保留策略。
12. github-server 的 GitHub App 身份、issue 模板/标签、`/mine` 是否透传 GitHub 对象，以及 issue 创建幂等与防刷阈值；客户端两种命名都兼容且无幂等键。
13. heartbeat TTL 线上实际值（客户端注释建议 90 s）。
14. SLS project/logstore 是否按 dev/staging/prod 区分，以及 TapDB 服务端 charge 事件名/属性与客户端 `user_login` 的时序保证；文档只给 cn/global 生产值。
15. 移动 `review` 与 expo-updates 原生冷启动检查的交互；审核期需冻结 OTA 发布。
