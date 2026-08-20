# Nacos Config History — Config Change Audit Platform

| 中文说明 | English |
|---|---|
| [🇨🇳 中文文档](./README_zh-CN.md) | [🇬🇧 English](./README.md) |
| 一个用于审计阿里云 MSE Nacos 配置变更的自建平台:按天看谁改了什么、任意两个历史版本对比、操作人自动映射真名。 | A self-hosted platform for auditing configuration changes in Aliyun MSE Nacos: who changed what per day, diff of any two historical versions, operators auto-mapped to real names. |

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
