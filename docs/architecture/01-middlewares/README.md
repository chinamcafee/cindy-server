# cindy-middlewares 本地中间件容器组

> 统一 Compose 项目名：`cindy-middlewares`
> 文件：`deploy/compose/docker-compose.yml`
> 目标：一条命令起齐 dev 环境所有中间件；开箱即用、数据可持久、健康检查完整。

---

## 1. 启动与停止

```bash
cd cindy-server

# 启动
docker compose -p cindy-middlewares -f deploy/compose/docker-compose.yml up -d

# 查看状态/健康
docker compose -p cindy-middlewares -f deploy/compose/docker-compose.yml ps

# 日志
docker compose -p cindy-middlewares -f deploy/compose/docker-compose.yml logs -f postgres redis rustfs

# 停止（保留数据）
docker compose -p cindy-middlewares -f deploy/compose/docker-compose.yml down

# 彻底清空（谨慎）
docker compose -p cindy-middlewares -f deploy/compose/docker-compose.yml down -v
```

> 本机若已有 5432/6379/9000 占用，先用 `lsof -i :5432` 确认，再改 ports 映射；不要误杀他人进程。

---

## 2. 组件与端口

| 组件 | 镜像 | 端口 | 用途 | 数据卷 |
|---|---|---|---|---|
| PostgreSQL | `postgres:18-alpine` | 5432 | 主数据库 | `cindy_postgres_data` |
| Redis | `redis:8-alpine` | 6379 | 缓存/presence/限流/会话 | `cindy_redis_data` |
| RustFS | `rustfs/rustfs:latest` | 9000/9001 | S3 兼容对象存储 + 控制台 | `cindy_rustfs_data` |
| coturn | `coturn/coturn:latest` | 3478, 49160-49200 | WebRTC TURN | — |
| Mailpit | `axllent/mailpit:latest` | 1025/8025 | 开发收码与邮件预览 | — |
| Prometheus | `prom/prometheus:latest` | 9090 | 指标 | `cindy_prometheus_data` |
| Grafana | `grafana/grafana:latest` | 3000 | 看板 | `cindy_grafana_data` |

网络：`cindy-middlewares`（bridge）。

---

## 3. 初始化

### 3.1 PostgreSQL

- 用户/密码：`cindy` / `cindy_dev_password`；库：`cindy`。
- 首次初始化执行 `initdb/00-extensions.sql`：`pgcrypto`、`citext`、`pg_trgm`、`btree_gin`。
- 业务表由 goose 迁移创建（见 `internal/platform/db` 与 `migrations/`）。

```bash
# 连接
psql "postgres://cindy:cindy_dev_password@127.0.0.1:5432/cindy?sslmode=disable"
```

### 3.2 RustFS 桶（服务启动时自动创建）

| 桶 | 访问 | 用途 |
|---|---|---|
| `cindy-public` | 匿名只读 | 头像、公开图标 |
| `cindy-private` | 私有 | 设备媒体、会话附件 |
| `cindy-plugins` | 私有 | 插件包与图标 |
| `cindy-skills` | 私有 | Skill 包 |

- 控制台：http://127.0.0.1:9001（`cindy` / `cindy_dev_password`）。
- 预签名 URL 由服务端使用 `minio-go`（S3 SDK）生成；dev 的 endpoint 必须可被客户端访问（移动真机需改成局域网 IP）。
- 生命周期建议：`cindy-private` 未引用对象 7 天清理；插件/Skill 历史版本保留。

### 3.3 Redis

- AOF 开启；`maxmemory 512mb`，`allkeys-lru`。
- Key 命名空间：`cindy:{env}:{domain}:...`，例如 `cindy:dev:presence:<accountId>`。
- 用途：presence、幂等键、限流、refresh 会话索引、验证码、pub/sub 扇出。

### 3.4 coturn

- dev 使用静态用户 `cindy` / `cindy_dev_password`。
- 生产改用 TURN REST 凭证：`username = <expiry>:<accountId>`，`credential = base64(HMAC-SHA1(secret, username))`；服务端与 coturn 共享 `CINDY_TURN_SECRET`。
- 本地 WebRTC 通常直连；TURN 用于验证中继路径。

### 3.5 Mailpit

- SMTP：`127.0.0.1:1025`；Web：http://127.0.0.1:8025。
- 服务端 dev 默认把验证码同时写入固定码 `123456`，Mailpit 用于验证邮件模板。

---

## 4. 健康检查与依赖

- Postgres/Redis/RustFS 均自带 healthcheck；桶由服务启动时自动创建（`objectstore.New`）。
- 服务端 `/readyz` 检查 PG ping、Redis ping、S3 bucket 存在（可配置跳过）。
- Prometheus 通过 `host.docker.internal:19828/metrics` 抓取本机服务；Grafana 自动配置 Prometheus 数据源。

---

## 5. 常见问题

| 现象 | 处理 |
|---|---|
| 5432 被占用 | 改映射端口并同步 `CINDY_DATABASE_URL` |
| RustFS 控制台 403 | 确认 `RUSTFS_ACCESS_KEY/SECRET_KEY` 与 rustfs 服务一致 |
| 预签名 URL 客户端无法访问 | 把 `CINDY_S3_ENDPOINT` 改为局域网地址，或配置反代 |
| coturn 无法中继 | 检查 UDP 端口范围与防火墙；容器需 host network 或显式端口映射 |
| 数据想重置 | `down -v` 后重新 `up -d`，再执行迁移 |
| Apple Silicon 镜像 | 全部使用多架构官方镜像；如遇问题加 `platform: linux/amd64` |

---

## 6. 生产映射

| dev | 生产 |
|---|---|
| 本地 PG | 托管 PostgreSQL（同版本） |
| 本地 Redis | 托管 Redis |
| RustFS | S3/OSS/COS 等 S3 兼容 |
| 本地 coturn | 独立 TURN 集群 |
| Mailpit | SES/Postmark 等 SMTP |
| Prometheus/Grafana | 托管可观测 |

所有中间件连接均通过环境变量注入，代码不依赖具体厂商。
