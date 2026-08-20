# Nacos Config History — Config Change Audit Platform

**English** | [中文](#中文)

## Features

- **Change records** — pick a day and see who changed what (per-person summary + full detail).
- **History comparison** — for any config, a full version timeline; compare **any two historical versions** (not just "version X vs latest").
- **No truncated namespaces** — data is pulled into a DB via the MSE OpenAPI on a schedule; namespaces are stored with their full ID/name, working around the 2048-character column truncation in SLS log analytics.
- **Operator auto-mapping** — principalId → real name via RAM ListUsers, refreshed hourly; new colleagues show up automatically.

## Architecture

```
MSE Nacos ──OpenAPI──▶ Go backend (poll & persist) ──▶ DB (SQLite/Postgres)
                              │
   RAM ListUsers ─────────────┘  (principalId→name, hourly refresh)
                              │
                     REST API + static SPA
                              │
                        React frontend
```

### Polling strategy (why it doesn't hit rate limits)

The first time a namespace is seen, a **full backfill** runs: enumerate all configs and pull their complete version history.
Every subsequent round is **incremental**: `ListConfigTrack` (one call per namespace) identifies *which* configs changed,
and only those configs are queried with `ListNacosHistoryConfigs` for operator and version info.

In steady state each round is a single-digit number of calls. The client also enforces call spacing (150ms default) plus gentle backoff retries,
fully avoiding the early "enumerate history per config" storm that used to 503 MSE.

> Key point: `ListConfigTrack` only returns dataId/time/md5 — **no operator, no version number**;
> the operator (`SrcUser` = principalId) only comes from `ListNacosHistoryConfigs`. The two APIs are complementary.

## Layout

```
backend/            Go service
  aliyun/           MSE / RAM OpenAPI wrappers (generic RPC client)
  store/            DB abstraction (SQLite locally / Postgres in prod)
  poller/           backfill + incremental polling
  api/              REST endpoints
  main.go           wiring + HTTP server + graceful shutdown
  web/              frontend build output (produced by vite)
frontend/           React + Vite
Dockerfile          multi-stage build (frontend → backend → runtime)
```

## Local development

Prerequisites: Go 1.26+, Node 20+, and Aliyun credentials with access to MSE.

Credential sources (in priority order):
1. Env vars `ALIYUN_ACCESS_KEY_ID` / `ALIYUN_ACCESS_KEY_SECRET` / `ALIYUN_SECURITY_TOKEN`
2. Falls back to the `current` profile in `~/.aliyun/config.json` (including STS token)

### Backend (local SQLite)

```bash
cd backend
# First run: full backfill (minutes, depending on config count) into a sqlite file
SYNC_ONCE=1 DB_DSN=./nacoshist.db go run .
# Then run permanently: scheduled polling + HTTP server
go run .
# → HTTP listening on :8080
```

### Frontend

```bash
cd frontend
npm install
npm run dev        # http://localhost:5173, /api proxied to :8080
# Or build into backend/web and let Go serve it:
npm run build && (cd ../backend && go run .)   # → http://localhost:8080
```

## Configuration (env vars)

| Var | Default | Notes |
|------|------|------|
| `ALIYUN_ACCESS_KEY_ID/SECRET` | — | Aliyun credentials (falls back to CLI profile) |
| `ALIYUN_SECURITY_TOKEN` | — | Token for STS/OAuth temporary credentials |
| `MSE_ENDPOINT` | `mse.<region>.aliyuncs.com` | Derived from `ALIYUN_REGION` |
| `MSE_INSTANCE_ID` | `mse-your-instance-id` | Your Nacos instance |
| `ALIYUN_REGION` | `us-west-1` | |
| `DB_DRIVER` | `sqlite` | `sqlite` (local) or `pgx` (prod Postgres) |
| `DB_DSN` | `./nacoshist.db` | sqlite file path, or `postgres://user:pass@host:5432/db?sslmode=require` |
| `POLL_INTERVAL_SECONDS` | `300` | Polling interval |
| `USER_SYNC_INTERVAL_SECONDS` | `3600` | RAM user mapping refresh interval |
| `SYNC_ONCE` | — | `1` = sync once and exit (for backfill/testing) |
| `ADDR` | `:8080` | HTTP listen address |
| `STATIC_DIR` | `./web` | Frontend static dir |
| `DISPLAY_TZ` | `Asia/Shanghai` | Timezone used by the per-day "change records" filter |

## Switching to Postgres in production

Just change two env vars — no code change (the schema is portable across both):

```bash
DB_DRIVER=pgx DB_DSN='postgres://user:pass@host:5432/nacoshist?sslmode=require'
```

The first start auto-creates the tables and runs a full backfill.

## REST API

| Path | Description |
|------|------|
| `GET /api/health` | Health check |
| `GET /api/namespaces` | Namespace list |
| `GET /api/changes?date=YYYY-MM-DD&ns=&user=&dataId=&limit=` | Change records for a day/namespace |
| `GET /api/versions?ns=&group=&dataId=` | Version timeline of one config |
| `GET /api/content?nid=` | Content of a version (lazy-loaded and cached) |
| `GET /api/diff?a=<nid>&b=<nid>` | Contents of any two versions for client-side diff |

## Deploying to Kubernetes

Recommended on k8s rather than ECS: stateless service, rolling updates, probes, and credential injection via ExternalSecret all work naturally.

```bash
docker build -t <registry>/nacos-config-history:latest .
docker push <registry>/nacos-config-history:latest
```

Deployment notes:
- Inject credentials as env vars via ESO/ExternalSecret (AK/SK or RRSA).
- `DB_DRIVER=pgx` + `DB_DSN` pointing at the production Postgres (DSN in a Secret).
- A single replica is enough (the only stateful part is the DB); readiness probe on `/api/health`.
- Outbound access to `mse.<region>.aliyuncs.com` and `ram.aliyuncs.com`.

---

## 中文

[English](#nacos-config-history--config-change-audit-platform) | **中文**

### 功能

- **变更记录** —— 指定某一天,查看哪些人修改了哪些配置(按人汇总 + 明细)。
- **历史对比** —— 选任意一个配置,展示完整版本时间线,**对比任意两个历史版本**(不再局限于「某版本 vs 最新版」)。
- **命名空间不再截断** —— 数据通过 MSE OpenAPI 定时入库,命名空间以完整 ID/名称存储,绕开了 SLS 日志分析列 2048 字符截断的问题。
- **操作人自动映射** —— principalId → 姓名 通过 RAM ListUsers 定期同步,新入职同事自动出现。

### 架构

```
MSE Nacos ──OpenAPI──▶ Go 后端(轮询入库) ──▶ DB(SQLite/Postgres)
                              │
   RAM ListUsers ─────────────┘  (principalId→姓名, 每小时刷新)
                              │
                     REST API + 静态 SPA
                              │
                        React 前端
```

#### 轮询策略(为什么不会被限流)

第一次见到某命名空间时做一次**全量回填**:枚举所有配置,拉全量历史版本。
之后每轮是**增量**:先用 `ListConfigTrack`(每命名空间一次调用)拿到「这段时间哪些配置变了」,
只对变化的配置调 `ListNacosHistoryConfigs` 拿操作人和版本号。

稳态下每轮只有个位数调用。客户端还内置了调用间隔节流(默认 150ms)+ 温和退避重试,
彻底避免了早期「逐配置枚举历史」打爆 MSE 触发 503 的问题。

> 关键点:`ListConfigTrack` 只有 dataId/时间/md5,**没有操作人也没有版本号**;
> 操作人(`SrcUser`=principalId)只能从 `ListNacosHistoryConfigs` 拿。所以两者是互补的。

### 目录结构

```
backend/            Go 服务
  aliyun/           MSE / RAM OpenAPI 封装(通用 RPC 客户端)
  store/            DB 抽象(SQLite 本地 / Postgres 生产)
  poller/           回填 + 增量轮询
  api/              REST 接口
  main.go           组装 + HTTP 服务 + 优雅退出
  web/              前端构建产物(由 vite 生成)
frontend/           React + Vite
Dockerfile          多阶段构建(前端 → 后端 → 运行时)
```

### 本地开发

前置:Go 1.26+、Node 20+,以及一份可访问 MSE 的阿里云凭据。

凭据来源(按优先级):
1. 环境变量 `ALIYUN_ACCESS_KEY_ID` / `ALIYUN_ACCESS_KEY_SECRET` / `ALIYUN_SECURITY_TOKEN`
2. 回退到 `~/.aliyun/config.json` 的 `current` profile(含 STS token)

#### 后端(连本地 SQLite)

```bash
cd backend
# 先跑一次全量回填(约十几分钟,取决于配置数量),落到 sqlite 文件
SYNC_ONCE=1 DB_DSN=./nacoshist.db go run .
# 之后常驻运行:定时轮询 + HTTP 服务
go run .
# → HTTP listening on :8080
```

#### 前端

```bash
cd frontend
npm install
npm run dev        # http://localhost:5173 ,/api 自动代理到 :8080
# 或构建到 backend/web 由 Go 一起托管:
npm run build && (cd ../backend && go run .)   # 访问 http://localhost:8080
```

### 配置项(环境变量)

| 变量 | 默认 | 说明 |
|------|------|------|
| `ALIYUN_ACCESS_KEY_ID/SECRET` | — | 阿里云凭据(缺省读 CLI profile) |
| `ALIYUN_SECURITY_TOKEN` | — | STS/OAuth 临时凭据的 token |
| `MSE_ENDPOINT` | `mse.<region>.aliyuncs.com` | 由 `ALIYUN_REGION` 推导 |
| `MSE_INSTANCE_ID` | `mse-your-instance-id` | 你的 Nacos 实例 |
| `ALIYUN_REGION` | `us-west-1` | |
| `DB_DRIVER` | `sqlite` | `sqlite`(本地)或 `pgx`(生产 Postgres) |
| `DB_DSN` | `./nacoshist.db` | sqlite 文件路径,或 `postgres://user:pass@host:5432/db?sslmode=require` |
| `POLL_INTERVAL_SECONDS` | `300` | 配置轮询间隔 |
| `USER_SYNC_INTERVAL_SECONDS` | `3600` | RAM 用户映射刷新间隔 |
| `SYNC_ONCE` | — | `1` 表示跑一次同步后退出(用于回填/测试) |
| `ADDR` | `:8080` | HTTP 监听地址 |
| `STATIC_DIR` | `./web` | 前端静态目录 |
| `DISPLAY_TZ` | `Asia/Shanghai` | 「变更记录」按天筛选所用时区 |

### 切换到生产 Postgres

只需改两个环境变量,无需改代码(表结构对两种库通用):

```bash
DB_DRIVER=pgx DB_DSN='postgres://user:pass@host:5432/nacoshist?sslmode=require'
```

首次启动会自动建表并做一次全量回填。

### REST 接口

| 路径 | 说明 |
|------|------|
| `GET /api/health` | 健康检查 |
| `GET /api/namespaces` | 命名空间列表 |
| `GET /api/changes?date=YYYY-MM-DD&ns=&user=&dataId=&limit=` | 某天/命名空间的变更记录 |
| `GET /api/versions?ns=&group=&dataId=` | 单个配置的版本时间线 |
| `GET /api/content?nid=` | 某版本内容(懒加载并缓存) |
| `GET /api/diff?a=<nid>&b=<nid>` | 返回任意两个版本内容供前端对比 |

### 部署到 K8s

推荐放在 k8s 而非 ECS:无状态服务、滚动更新、探针、配合已有的 ExternalSecret 注入凭据都更顺。

```bash
docker build -t <registry>/nacos-config-history:latest .
docker push <registry>/nacos-config-history:latest
```

Deployment 要点:
- 凭据用集群里已有的 ESO/ExternalSecret 注入为环境变量(AK/SK 或 RRSA)。
- `DB_DRIVER=pgx` + `DB_DSN` 指向生产 Postgres(DSN 放 Secret)。
- 单副本即可(有状态的只是 DB,本服务无状态);就绪探针打 `/api/health`。
- 出网访问 `mse.<region>.aliyuncs.com` 和 `ram.aliyuncs.com`。
