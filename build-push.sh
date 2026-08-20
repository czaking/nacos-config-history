#!/usr/bin/env bash
# Build the nacos-config-history image and push to Aliyun ACR (us-west-1).
#
# 为什么不在容器里多阶段编译:在 arm64 Mac 上用 QEMU 模拟 amd64 跑 Go 工具链会
# 崩溃(go mod download / go build 进程 panic)。Go 交叉编译在宿主机是零成本的,
# 所以这里宿主机原生产出 linux/amd64 二进制 + 前端 dist,再用 Dockerfile.release
# 只做打包(打包阶段唯一执行的 amd64 代码是 apk,busybox 在 QEMU 下稳定)。
#
# 前置:
#   1. 宿主机装了 Go(交叉编译)和 node(vite build)。
#   2. 容器运行时可用(colima/docker),且已 docker login 到公网 push endpoint:
#        TOKEN=$(aliyun cr GetAuthorizationToken --InstanceId cri-nrrmsu66vjaat73n \
#                  --region us-west-1 | jq -r .AuthorizationToken)
#        echo "$TOKEN" | docker login your-registry.us-west-1.cr.aliyuncs.com \
#                  -u cr_temp_user --password-stdin
#   3. ACR 里已建仓库 backend/nacos-config-history。
#
# 用法:  ./build-push.sh [tag]      # tag 默认 0.1.0
set -euo pipefail

TAG="${1:-0.1.0}"
PUSH_HOST="your-registry.us-west-1.cr.aliyuncs.com"       # 公网,推送
PULL_HOST="your-registry-vpc.us-west-1.cr.aliyuncs.com"   # VPC,集群拉取
REPO="backend/nacos-config-history"

cd "$(dirname "$0")"
ROOT="$(pwd)"
BC="$(mktemp -d)"
trap 'rm -rf "$BC"' EXIT

echo ">> [1/4] frontend build (host native)"
# 仅在依赖缺失或 lockfile 变化时才重装:npm ci 会先删光 node_modules 再全量拉,
# 冷缓存下要几十秒。有 node_modules 且 lock 未变时直接跳过,构建只跑 vite。
( cd frontend
  if [ ! -d node_modules ] || [ package-lock.json -nt node_modules/.package-lock.json ]; then
    npm ci >/dev/null 2>&1 || npm install >/dev/null 2>&1
  fi
  npx vite build --outDir dist --emptyOutDir >/dev/null )

echo ">> [2/4] backend cross-compile linux/amd64 (host native)"
# -s -w 去掉符号表和 DWARF 调试信息:二进制 23MB → 15MB,push 到 us-west-1 的
# 上传量少约 35%(网络是整条流水线的瓶颈)。不影响运行,只是不能 gdb/pprof 符号化。
( cd backend && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOFLAGS=-trimpath \
  go build -ldflags="-s -w" -o "$BC/nacoshist-linux-amd64" . )

echo ">> [3/4] assemble build context + package (amd64)"
cp -R "$ROOT/frontend/dist" "$BC/web"
cp "$ROOT/Dockerfile.release" "$BC/Dockerfile"
docker build --platform linux/amd64 -t "${PUSH_HOST}/${REPO}:${TAG}" "$BC"

echo ">> [4/4] pushing"
docker push "${PUSH_HOST}/${REPO}:${TAG}"

echo
echo "✅ pushed ${PUSH_HOST}/${REPO}:${TAG}"
echo "   集群 deployment.yaml 引用 VPC 地址(同一实例同一镜像,tag 一致即可):"
echo "   ${PULL_HOST}/${REPO}:${TAG}"
