# Nacos 配置变更审计平台

| 中文说明 | English |
|---|---|
| [🇨🇳 中文文档](./README_zh-CN.md) | [🇬🇧 English](./README.md) |
| 一个用于审计阿里云 MSE Nacos 配置变更的自建平台:按天看谁改了什么、任意两个历史版本对比、操作人自动映射真名。 | A self-hosted platform for auditing configuration changes in Aliyun MSE Nacos: who changed what per day, diff of any two historical versions, operators auto-mapped to real names. |

## 功能

- **变更记录** —— 指定某一天,查看哪些人修改了哪些配置(按人汇总 + 明细)。
- **历史对比** —— 选任意一个配置,展示完整版本时间线,**对比任意两个历史版本**(不再局限于「某版本 vs 最新版」)。
- **命名空间不再截断** —— 数据通过 MSE OpenAPI 定时入库,命名空间以完整 ID/名称存储,绕开了 SLS 日志分析列 2048 字符截断的问题。
- **操作人自动映射** —— principalId → 姓名 通过 RAM ListUsers 定期同步,新入职同事自动出现。

## 架构

```
MSE Nacos ──OpenAPI──▶ Go 后端(轮询入库) ──▶ DB(SQLite/Postgres)
                              │
   RAM ListUsers ─────────────┘  (principalId→姓名, 每小时刷新)
                              │
                     REST API + 静态 SPA
                              │
                        React 前端
```

### 轮询策略(为什么不会被限流)

第一次见到某命名空间时做一次**全量回填**:枚举所有配置,拉全量历史版本。
之后每轮是**增量**:先用 `ListConfigTrack`(每命名空间一次调用)拿到「这段时间哪些配置变了」,
只对变化的配置调 `ListNacosHistoryConfigs` 拿操作人和版本号。

稳态下每轮只有个位数调用。客户端还内置了调用间隔节流(默认 150ms)+ 温和退避重试,
彻底避免了早期「逐配置枚举历史」打爆 MSE 触发 503 的问题。

> 关键点:`ListConfigTrack` 只有 dataId/时间/md5,**没有操作人也没有版本号**;
> 操作人(`SrcUser`=principalId)只能从 `ListNacosHistoryConfigs` 拿。所以两者是互补的。

## 目录结构

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

## 本地开发

前置:Go 1.26+、Node 20+,以及一份可访问 MSE 的阿里云凭据。

凭据来源(按优先级):
1. 环境变量 `ALIYUN_ACCESS_KEY_ID` / `ALIYUN_ACCESS_KEY_SECRET` / `ALIYUN_SECURITY_TOKEN`
2. 回退到 `~/.aliyun/config.json` 的 `current` profile(含 STS token)

### 后端(连本地 SQLite)

```bash
cd backend
# 先跑一次全量回填(约十几分钟,取决于配置数量),落到 sqlite 文件
SYNC_ONCE=1 DB_DSN=./nacoshist.db go run .
# 之后常驻运行:定时轮询 + HTTP 服务
go run .
# → HTTP listening on :8080
```

### 前端

```bash
cd frontend
npm install
npm run dev        # http://localhost:5173 ,/api 自动代理到 :8080
# 或构建到 backend/web 由 Go 一起托管:
npm run build && (cd ../backend && go run .)   # 访问 http://localhost:8080
```

## 配置项(环境变量)

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

## 切换到生产 Postgres

只需改两个环境变量,无需改代码(表结构对两种库通用):

```bash
DB_DRIVER=pgx DB_DSN='postgres://user:pass@host:5432/nacoshist?sslmode=require'
```

首次启动会自动建表并做一次全量回填。

## REST 接口

| 路径 | 说明 |
|------|------|
| `GET /api/health` | 健康检查 |
| `GET /api/namespaces` | 命名空间列表 |
| `GET /api/changes?date=YYYY-MM-DD&ns=&user=&dataId=&limit=` | 某天/命名空间的变更记录 |
| `GET /api/versions?ns=&group=&dataId=` | 单个配置的版本时间线 |
| `GET /api/content?nid=` | 某版本内容(懒加载并缓存) |
| `GET /api/diff?a=<nid>&b=<nid>` | 返回任意两个版本内容供前端对比 |

## 部署到 K8s

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
