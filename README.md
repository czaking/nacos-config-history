# Nacos Config History — 配置变更审计平台

| **English** | **中文** |
|---|---|
| A self-hosted platform for auditing configuration changes in Aliyun MSE Nacos. | 一个用于审计阿里云 MSE Nacos 配置变更的自建平台。 |
| - **Change records** — pick a day and see who changed what (per-person summary + full detail).<br>- **History comparison** — full version timeline per config; compare **any two historical versions**.<br>- **No truncated namespaces** — data pulled via MSE OpenAPI on a schedule; namespaces stored with full ID/name (works around the 2048-char truncation in SLS log analytics).<br>- **Operator auto-mapping** — principalId → real name via RAM ListUsers, refreshed hourly. | - **变更记录** —— 指定某一天,查看哪些人修改了哪些配置(按人汇总 + 明细)。<br>- **历史对比** —— 完整版本时间线,**对比任意两个历史版本**。<br>- **命名空间不再截断** —— 数据经 MSE OpenAPI 定时入库,以完整 ID/名称存储,绕开 SLS 日志分析列 2048 字符截断。<br>- **操作人自动映射** —— principalId → 姓名 经 RAM ListUsers 每小时同步。 |

## Architecture / 架构

```
MSE Nacos ──OpenAPI──▶ Go backend (poll & persist) ──▶ DB (SQLite/Postgres)
                              │
   RAM ListUsers ─────────────┘  (principalId→name, hourly)
                              │
                     REST API + static SPA ──▶ React frontend
```

## Polling strategy / 轮询策略

| **English** | **中文** |
|---|---|
| **Why it doesn't hit rate limits**: the first time a namespace is seen, a **full backfill** runs (enumerate all configs, pull complete version history). Every subsequent round is **incremental**: `ListConfigTrack` (one call per namespace) identifies *which* configs changed; only those are queried with `ListNacosHistoryConfigs` for operator and version info. Steady state = single-digit calls per round, with built-in call spacing (150ms) and gentle backoff retries. | **为什么不会被限流**:第一次见到某命名空间时做一次**全量回填**(枚举所有配置,拉全量历史版本)。之后每轮是**增量**:`ListConfigTrack`(每命名空间一次调用)拿到「哪些配置变了」,只对变化的配置调 `ListNacosHistoryConfigs` 拿操作人和版本号。稳态下每轮只有个位数调用,客户端内置调用间隔节流(150ms)与温和退避重试。 |
| Key point: `ListConfigTrack` only returns dataId/time/md5 — **no operator, no version number**; the operator (`SrcUser` = principalId) only comes from `ListNacosHistoryConfigs`. The two APIs are complementary. | 关键点:`ListConfigTrack` 只有 dataId/时间/md5,**没有操作人也没有版本号**;操作人(`SrcUser`=principalId)只能从 `ListNacosHistoryConfigs` 拿。两者互补。 |

## Layout / 目录结构

```
backend/            Go service / Go 服务
  aliyun/           MSE / RAM OpenAPI wrappers / OpenAPI 封装(通用 RPC 客户端)
  store/            DB abstraction: SQLite locally / Postgres in prod / DB 抽象
  poller/           backfill + incremental polling / 回填 + 增量轮询
  api/              REST endpoints / REST 接口
  main.go           wiring + HTTP server + graceful shutdown / 组装 + HTTP + 优雅退出
  web/              frontend build output (vite) / 前端构建产物
frontend/           React + Vite
Dockerfile          multi-stage build / 多阶段构建(前端 → 后端 → 运行时)
```

## Local development / 本地开发

| **English** | **中文** |
|---|---|
| Prerequisites: Go 1.26+, Node 20+, Aliyun credentials with MSE access. Credential sources (priority order): ① env vars `ALIYUN_ACCESS_KEY_ID` / `ALIYUN_ACCESS_KEY_SECRET` / `ALIYUN_SECURITY_TOKEN`; ② falls back to the `current` profile in `~/.aliyun/config.json` (incl. STS token). | 前置:Go 1.26+、Node 20+,以及一份可访问 MSE 的阿里云凭据。凭据来源(按优先级):① 环境变量 `ALIYUN_ACCESS_KEY_ID` / `ALIYUN_ACCESS_KEY_SECRET` / `ALIYUN_SECURITY_TOKEN`;② 回退到 `~/.aliyun/config.json` 的 `current` profile(含 STS token)。 |

```bash
# Backend with local SQLite / 后端(本地 SQLite)
cd backend
SYNC_ONCE=1 DB_DSN=./nacoshist.db go run .   # first run: full backfill / 首次:全量回填
go run .                                      # then run permanently / 之后常驻:轮询 + HTTP(:8080)

# Frontend / 前端
cd frontend
npm install
npm run dev        # :5173, /api proxied to :8080 / 自动代理到 :8080
# or serve via Go / 或构建后由 Go 托管:
npm run build && (cd ../backend && go run .)   # → :8080
```

## Configuration / 配置项(环境变量)

| Var / 变量 | Default / 默认 | Description / 说明 |
|------|------|------|
| `ALIYUN_ACCESS_KEY_ID/SECRET` | — | Aliyun credentials, falls back to CLI profile / 阿里云凭据(缺省读 CLI profile) |
| `ALIYUN_SECURITY_TOKEN` | — | Token for STS/OAuth temporary credentials / STS/OAuth 临时凭据 token |
| `MSE_ENDPOINT` | `mse.<region>.aliyuncs.com` | Derived from `ALIYUN_REGION` / 由 region 推导 |
| `MSE_INSTANCE_ID` | `mse-your-instance-id` | Your Nacos instance / 你的 Nacos 实例 |
| `ALIYUN_REGION` | `us-west-1` | |
| `DB_DRIVER` | `sqlite` | `sqlite` (local) or `pgx` (prod Postgres) / 本地或生产 |
| `DB_DSN` | `./nacoshist.db` | sqlite path or `postgres://user:pass@host:5432/db?sslmode=require` |
| `POLL_INTERVAL_SECONDS` | `300` | Polling interval / 轮询间隔 |
| `USER_SYNC_INTERVAL_SECONDS` | `3600` | RAM user mapping refresh / RAM 用户映射刷新间隔 |
| `SYNC_ONCE` | — | `1` = sync once and exit (backfill/testing) / 跑一次同步后退出 |
| `ADDR` | `:8080` | HTTP listen address / 监听地址 |
| `STATIC_DIR` | `./web` | Frontend static dir / 前端静态目录 |
| `DISPLAY_TZ` | `Asia/Shanghai` | Timezone for the per-day filter / 按天筛选所用时区 |

## Production Postgres / 生产 Postgres

| **English** | **中文** |
|---|---|
| Just change two env vars — no code change (schema is portable). The first start auto-creates tables and runs a full backfill. | 只需改两个环境变量,无需改代码(表结构通用)。首次启动自动建表并全量回填。 |

```bash
DB_DRIVER=pgx DB_DSN='postgres://user:pass@host:5432/nacoshist?sslmode=require'
```

## REST API / REST 接口

| Path / 路径 | Description / 说明 |
|------|------|
| `GET /api/health` | Health check / 健康检查 |
| `GET /api/namespaces` | Namespace list / 命名空间列表 |
| `GET /api/changes?date=YYYY-MM-DD&ns=&user=&dataId=&limit=` | Change records for a day/namespace / 某天或某命名空间的变更记录 |
| `GET /api/versions?ns=&group=&dataId=` | Version timeline of one config / 单个配置的版本时间线 |
| `GET /api/content?nid=` | Content of a version (lazy-loaded & cached) / 某版本内容(懒加载并缓存) |
| `GET /api/diff?a=<nid>&b=<nid>` | Any two versions for client-side diff / 任意两个版本供前端对比 |

## Deploying to Kubernetes / 部署到 K8s

| **English** | **中文** |
|---|---|
| Recommended on k8s: stateless service, rolling updates, probes, and credential injection via ExternalSecret all work naturally. Notes: inject credentials via ESO/ExternalSecret (AK/SK or RRSA); `DB_DRIVER=pgx` + `DB_DSN` from a Secret; a single replica is enough (only the DB is stateful); readiness probe on `/api/health`; egress to `mse.<region>.aliyuncs.com` and `ram.aliyuncs.com`. | 推荐放 k8s:无状态服务、滚动更新、探针、ExternalSecret 注入凭据都更顺。要点:凭据经 ESO/ExternalSecret 注入(AK/SK 或 RRSA);`DB_DRIVER=pgx` + `DB_DSN` 指向生产 Postgres(DSN 放 Secret);单副本即可(有状态的只是 DB);就绪探针打 `/api/health`;出网访问 `mse.<region>.aliyuncs.com` 和 `ram.aliyuncs.com`。 |

```bash
docker build -t <registry>/nacos-config-history:latest .
docker push <registry>/nacos-config-history:latest
```
