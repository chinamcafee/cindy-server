# 数据库设计

> PostgreSQL 18，单库 `cindy`，按域前缀分表。迁移使用 goose（SQL-first）。
> 所有表默认包含 `created_at timestamptz not null default now()`；可变表含 `updated_at`。
> ID 统一 `uuid`（应用生成 UUIDv7）；外部可见 ID 与主键一致，避免额外映射。

---

## 1. 通用约定

| 项 | 约定 |
|---|---|
| 字符集 | UTF-8；`text` 为主，短枚举用 `text + check` 或 PG enum |
| 时间 | `timestamptz`，UTC；对外 ISO-8601 |
| 金额 | `bigint` 最小货币单位 + `currency char(3)`；禁止 float |
| 软删除 | `deleted_at timestamptz`；查询默认过滤 |
| 审计 | `audit_events` 只追加；业务表保留 `created_by/updated_by` 可选 |
| JSON | 仅用于协议快照/可变元数据；高频过滤字段必须独立列 |
| 外键 | 同域强外键；跨域仅逻辑引用（避免跨服务耦合） |
| 索引 | 所有外键列、高频过滤列、排序键；大表使用覆盖索引 |
| 分区 | `chat_messages`、`audit_events`、`ma_usage_events`、`hb_events` 按月/日分区 |
| 迁移 | 只前进；破坏性变更分两步；每个迁移含 `-- +goose Up/Down` |

---

## 2. 平台表（`platform` 前缀或公共）

| 表 | 关键字段 | 说明 |
|---|---|---|
| `schema_migrations` | version, applied_at | goose |
| `outbox_events` | id, aggregate_type, aggregate_id, event_type, payload jsonb, status, attempts, available_at, created_at, processed_at | 事务消息 |
| `idempotency_keys` | scope, key, request_hash, response_code, response_body jsonb, expires_at | 写幂等 |
| `audit_events` | id, occurred_at, region, actor_type, actor_id, actor_ip, user_agent, action, target_type, target_id, before jsonb, after jsonb, request_id, result, reason | 分区 |
| `feature_flags` | key, default_value jsonb, description, updated_at | flags |
| `feature_flag_overrides` | flag_key, scope(account/org/percent), target, value jsonb | 灰度 |
| `announcements` | id, platform, version, locale, title, body, status, published_at | 公告 |
| `app_settings` | key, value jsonb, updated_at | 运营配置 |
| `admin_users` | id, email citext unique, password_hash, totp_secret_encrypted, role, status, last_login_at | 后台账号 |
| `admin_sessions` | id, admin_id, token_hash, expires_at, ip, user_agent, revoked_at | 后台会话 |
| `admin_jobs` | id, type, payload jsonb, status, run_at, attempts, last_error | 后台任务 |
| `file_objects` | id, bucket, key, owner_account_id, scene, mime, size, sha256, acl, status, expires_at | 对象登记 |

---

## 3. 认证与账号（`auth_`）

| 表 | 关键字段 | 说明 |
|---|---|---|
| `auth_accounts` | id, region, status, created_at, deleted_at | 账号根 |
| `auth_identities` | id, account_id, type(email/phone/apple/google/wechat), identifier citext, verified_at, metadata | 唯一(type, identifier, region) |
| `auth_memberships` | id, account_id, kind(personal/org), role(owner/admin/member), display_name, avatar_url, email, org_id, org_name, org_logo_url, status | 资源身份 |
| `auth_organizations` | id, slug unique, name, logo_url, region, status, plan, sso_required, plugin_prefix, settings jsonb | 组织 |
| `auth_org_domains` | id, org_id, domain citext unique, verified_at, verification_token | 域名路由 |
| `auth_sso_connections` | id, org_id, protocol(oidc/saml/cas), name, config_encrypted jsonb, enabled, status | SSO |
| `auth_verification_codes` | id, channel, identifier, purpose, code_hash, expires_at, attempts, consumed_at | 验证码 |
| `auth_login_tickets` | id, account_id, purpose, data jsonb, expires_at, consumed_at | 多账号/补绑/二次验证 |
| `auth_refresh_sessions` | id, account_id, membership_id, device_id, token_hash unique, family_id, rotated_from, expires_at, revoked_at, ip, user_agent | 资源刷新会话 |
| `auth_account_sessions` | id, account_id, token_hash, family_id, expires_at, revoked_at | 账号级会话 |
| `auth_desktop_authorizations` | id, code, status, encrypted_tokens, expires_at, poll_count, last_polled_at | 托管回调 |
| `auth_oauth_states` | id, provider, state, nonce, redirect_uri, code_verifier, expires_at, consumed_at | 社交 OAuth |
| `auth_account_deletions` | id, account_id, status, challenge_hash, scheduled_at, executed_at, restored_at | 删除 |
| `auth_social_identities` | 可合并到 identities | — |
| `auth_org_invitations` | id, org_id, email citext, role, token_hash, expires_at, accepted_at | 后台邀请（推断） |

索引：

- `auth_identities(type, identifier)` unique。
- `auth_refresh_sessions(token_hash)` unique；`(account_id, revoked_at)`。
- `auth_verification_codes(identifier, purpose, consumed_at)`。
- `auth_org_domains(domain)` unique。

---

## 4. Chat（`chat_`）

| 表 | 关键字段 | 说明 |
|---|---|---|
| `chat_actors` | id, account_id, type(user/bot), display_name, avatar_url, metadata jsonb | Bot 与人类统一 actor |
| `chat_rooms` | id, kind(direct/group), owner_account_id, title, avatar_url, status, metadata jsonb, last_seq bigint | 房间 |
| `chat_room_members` | room_id, actor_id, role, joined_at, last_read_seq, notification_level, removed_at | 成员/已读 |
| `chat_messages` | id, room_id, seq bigint, sender_actor_id, client_message_id, kind, content jsonb, reply_to_id, thread_root_id, source jsonb, created_at, edited_at, deleted_at | 分区/唯一(room_id, seq) |
| `chat_message_reactions` | message_id, actor_id, emoji, created_at | 回应 |
| `chat_attachments` | id, message_id, object_id, oss_key, mime, size, sha256, original_name | 附件 |
| `chat_executions` | id, room_id, bot_actor_id, status, lease_expires_at, started_at, ended_at, error jsonb | Bot 执行租约 |
| `chat_plans` | id, room_id, status, created_by, created_at | 分工 |
| `chat_plan_steps` | id, plan_id, assignee_actor_id, title, status, sort, metadata | 分工步骤 |
| `chat_invitations` | id, room_id, token_hash, created_by, expires_at, used_at | 群邀请 |
| `chat_room_events` | id, room_id, seq bigint, type, actor_id, payload jsonb, created_at | 成员/属性变更流 |
| `chat_companions` | id, account_id, bot_actor_id, main_device_id, status, profile_snapshot jsonb, settings jsonb | 云伙伴 |
| `chat_bot_groups` | id, owner_account_id, name, avatar_url, status, settings jsonb | Bot 群 |
| `chat_bot_group_members` | group_id, actor_id, role, joined_at | 群成员 |
| `chat_preferences` | account_id, key, value jsonb | 客户端偏好 |
| `chat_idempotency` | 可复用 `idempotency_keys(scope='chat')` | — |

索引：

- `chat_messages(room_id, seq desc)`；`(thread_root_id)`；`(sender_actor_id, created_at)`。
- `chat_room_members(actor_id, removed_at)`。
- `chat_rooms(owner_account_id, status)`。
- 唯一：`chat_messages(room_id, client_message_id)`（非空时）。

分区：`chat_messages` 按月；保留策略按组织配置。

---

## 5. Device-Link（`dl_`）

| 表 | 关键字段 | 说明 |
|---|---|---|
| `dl_devices` | id, account_id, name, platform, app_version, device_info jsonb, remote_control_enabled, busy, last_seen_at, removed_at, created_at | 设备目录 |
| `dl_push_tokens` | id, account_id, device_id, token, platform, provider, app_variant, apns_env, created_at, updated_at | 唯一(token) |
| `dl_shared_tasks` | id, session_id, owner_account_id, host_device_id, revision, status, created_at, closed_at | 共享任务 |
| `dl_shared_task_guests` | id, shared_task_id, member_id, account_id, version, device_ids jsonb, joined_at, removed_at | 成员 |
| `dl_shared_task_invitations` | id, shared_task_id, token_hash unique, expires_at, used_at, created_by | 43 字符 token |
| `dl_provider_shares` | id, owner_account_id, provider_id, provider_label, host_device_id, status, created_at | 分享 |
| `dl_provider_share_members` | id, share_id, member_id, account_id, display_name, status, version | 成员 |
| `dl_provider_share_requests` | id, share_id, account_id, status, pairing_code, expires_at, created_at | 申请 |
| `dl_provider_share_guest_credentials` | id, share_id, member_id, credential_hash, expires_at, revoked_at | 跨区 |
| `dl_device_identities` | id, account_id, device_id, public_key, algorithm, created_at, revoked_at | ES256 |
| `dl_identity_challenges` | id, device_id, path, token_hash, body_hash, nonce, expires_at, consumed_at | 挑战 |
| `dl_relay_audits` | id, account_id, device_id, event, code, occurred_at | 连接审计（可选） |

在线状态与路由不落 PG，放 Redis：

- `cindy:{env}:dl:route:{deviceId}` → `{accountId, connId, node}` TTL 90s。
- `cindy:{env}:dl:presence:{accountId}` hash deviceId → JSON（online/busy/remoteControlEnabled/lastSeenAt）。
- `cindy:{env}:dl:queue:{deviceId}` 可选离线短队列（不存业务内容）。

---

## 6. 模型与计费（`ma_`）

| 表 | 关键字段 | 说明 |
|---|---|---|
| `ma_providers` | id, slug unique, name, region, status, wire_protocol, metadata jsonb | 供应商 |
| `ma_models` | id, provider_id, model_key, display_name, capabilities jsonb, context_window, max_output, pricing jsonb, currency, media jsonb, status, sort | 模型 |
| `ma_catalog_revisions` | id, version bigint, status, registry jsonb, notes, published_at, created_by | 目录快照 |
| `ma_model_availability` | model_id, scope, target, plan, regions text[] | 可用性 |
| `ma_credentials` | id, account_id, provider_id, label, ciphertext bytea, nonce bytea, key_version, status, last_validated_at | BYOK 加密 |
| `ma_credential_events` | id, credential_id, event, occurred_at | 轮换/撤销审计 |
| `ma_plans` | id, slug, name, price_minor, currency, interval, features jsonb, status | 套餐 |
| `ma_subscriptions` | id, account_id, org_id, plan_id, status, current_period_start, current_period_end, provider_ref | 订阅 |
| `ma_credit_ledger` | id, account_id, delta, reason, balance_after, created_at | 额度流水 |
| `ma_usage_events` | id, account_id, model_id, input_tokens, output_tokens, cost_minor, currency, request_id, occurred_at | 分区 |
| `ma_quota_limits` | id, scope, target, period, limit_value, used_value | 配额 |
| `ma_orders` | id, account_id, plan_id, amount_minor, currency, status, provider_ref | 订单 |
| `ma_invoices` | id, order_id, status, url, issued_at | 发票 |
| `ma_byok_policies` | provider_id, enabled, share_enabled, max_credentials | 策略 |
| `ma_usage_daily` | account_id, day, model_id, tokens, cost_minor | 聚合 |

- 价格 JSONB 结构：`{"inputPerMillion": 3.0, "outputPerMillion": 15.0, "currency": "USD", "tiers": [...]}`。
- 公共目录接口读 `ma_catalog_revisions.registry` 的已发布快照，避免实时 JOIN。

---

## 7. 语音（`voice_`）

| 表 | 关键字段 |
|---|---|
| `voice_sessions` | id, account_id, mode, ticket_hash, ws_url, status, expires_at, created_at |
| `voice_dictionary` | id, account_id, term, weight, updated_at |
| `voice_usage` | id, account_id, session_id, audio_ms, characters, occurred_at |

---

## 8. 插件与 Skill（`plugin_` / `sk_`）

### 8.1 插件

| 表 | 关键字段 |
|---|---|
| `plugin_plugins` | id, ghost_id, scope(public/organization/personal), org_id, namespace, name, description, author, status, default_install, icon_key, created_at, updated_at |
| `plugin_releases` | id, plugin_id, version, manifest jsonb, sha256, size_bytes, icon_key, status, review_status, review_reason, published_at |
| `plugin_download_tickets` | id, plugin_id, release_id, account_id, issued_at, expires_at |
| `plugin_usage_events` | id, event_id unique, plugin_id, release_id, account_id, occurred_at, accepted_at, rejected_code |
| `plugin_usage_daily` | plugin_id, release_id, day, event_count |
| `plugin_org_prefixes` | org_id unique, prefix, created_at |
| `plugin_upload_sessions` | id, account_id, org_id, size_bytes, sha256, object_key, status, put_url_expires_at, failure_code, plugin_id, release_id, ghost_id, version, review_status |
| `plugin_removals` | id, plugin_id, ghost_id, scope, org_id, namespace, action(purge), removed_at |

### 8.2 Skill Hub

| 表 | 关键字段 |
|---|---|
| `sk_skills` | id, slug, owner_account_id, owner_type, display_name, summary, description, visibility(private/shared/public), status, latest_version, icon_key, github_url, created_at, updated_at |
| `sk_versions` | id, skill_id, version, folder_hash, zip_sha256, file_size, status, scan_status, rejection_reason, created_at |
| `sk_files` | id, version_id, path, size, language, sha256, truncated |
| `sk_categories` | id, slug, name, parent_id, source |
| `sk_skill_categories` | skill_id, category_id |
| `sk_tags` | id, slug, name, source |
| `sk_skill_tags` | skill_id, tag_id |
| `sk_teams` | id, org_id, slug, name, type, source, my_role 逻辑在成员表 |
| `sk_team_members` | team_id, account_id, role |
| `sk_departments` | id, org_id, slug, name, parent_id |
| `sk_visibility_shares` | skill_id, team_id, dept_id |
| `sk_auto_install` | id, org_id, skill_id, version, audience jsonb |
| `sk_download_tickets` | id, skill_id, version_id, account_id, expires_at |
| `sk_publish_uploads` | id, account_id, slug, version, oss_key, status |

---

## 9. Hook / IM（`hook_`）

| 表 | 关键字段 |
|---|---|
| `hook_channels` | id, provider(slack/telegram/x), external_id, name, status, config_encrypted jsonb |
| `hook_bindings` | id, account_id, membership_id, channel_id, external_user_id, status, preferences jsonb |
| `hook_sessions` | id, binding_id, task_id, status, last_message_at |
| `hook_messages` | id, session_id, direction, type, payload jsonb, created_at |
| `hook_confirmations` | id, session_id, request_id, status, decision, expires_at, decided_at |
| `hook_group_relays` | id, channel_id, group_id, binding_id, status |
| `hook_delivery_attempts` | id, message_id, attempt, status, error |

---

## 10. 更新/心跳/GitHub（`upd_` / `hb_` / `gh_`）

| 表 | 关键字段 |
|---|---|
| `upd_desktop_releases` | id, platform, arch, channel(release/beta/canary), version, manifest jsonb, files jsonb, rollout, status, published_at |
| `upd_mobile_releases` | id, platform, channel, version, build_number, runtime_version, install_url, itms_url, min_version, release_notes, status |
| `upd_announcements` | id, platform, version, locale, title, body, status, published_at |
| `upd_endpoint_manifests` | id, region, version, payload jsonb, status, published_at |
| `upd_wallpapers` | id, kind(video/webp), sha256, url, size, status |
| `hb_daily` | account_id, day, platform, version, first_seen, last_seen |
| `hb_events` | id, account_id, platform, version, occurred_at（可选分区） |
| `gh_issues` | id, account_id, type(bug/feature), title, description, github_number, url, status, created_at |

---

## 11. 索引与性能清单

- 列表默认 `ORDER BY updated_at DESC, id DESC`，索引 `(updated_at DESC, id DESC)`。
- 搜索：`pg_trgm` GIN（用户 email/name、插件 name、Skill name）。
- 多租户：所有 org 域表带 `org_id`，索引 `(org_id, created_at DESC)`。
- 消息：`(room_id, seq)` 唯一；游标 `seq > ?`。
- 插件列表：`(scope, organization_id, status)` + `(default_install)`。
- 用量：`(account_id, occurred_at)` 分区裁剪。
- 审计：`(occurred_at)`、`(actor_id)`、`(target_type, target_id)`。

---

## 12. 保留与清理

| 数据 | 保留 |
|---|---|
| 验证码/票据 | 过期后 24h 清理 |
| Refresh 会话 | 过期后 30d 清理 |
| 设备/推送 token | 失效后 90d |
| 消息/附件 | 按组织策略（默认长期） |
| 用量/事件 | 24 个月 |
| 审计 | ≥ 12 个月 |
| 幂等键 | 24h–7d |
| outbox | 成功后 7d |
| 日志上传原文 | 不落库（外部 SLS） |

清理任务由 worker 定时执行，分批删除，避免长事务。

---

## 13. 迁移顺序

1. `0001_platform`（extensions、outbox、audit、admin、flags）
2. `0002_auth`
3. `0003_chat`
4. `0004_devicelink`
5. `0005_modelaccess`
6. `0006_voice`
7. `0007_plugin`
8. `0008_skillhub`
9. `0009_oss_updates`
10. `0010_hooks_github_heartbeat`

每个迁移可独立重放；种子数据（模型目录、套餐、feature flags、管理员）通过 `cmd/seed` 或迁移中的 `INSERT ... ON CONFLICT`。
