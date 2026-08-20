# syntax=docker/dockerfile:1

# ---- frontend build ----
FROM node:22-alpine AS web
WORKDIR /web
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm ci || npm install
COPY frontend/ ./
# Override vite's dev outDir (../backend/web) to a local dist for this stage.
RUN npx vite build --outDir dist --emptyOutDir

# ---- backend build ----
FROM golang:1.26-alpine AS api
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOPROXY=https://goproxy.cn,https://goproxy.io,direct GOSUMDB=off
COPY backend/go.mod backend/go.sum* ./
RUN go mod download
COPY backend/ ./
# bring in the built SPA
COPY --from=web /web/dist ./web
RUN go build -o /out/nacoshist .

# ---- runtime ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=api /out/nacoshist /app/nacoshist
COPY --from=api /src/web /app/web
ENV STATIC_DIR=/app/web ADDR=:8080 DISPLAY_TZ=Asia/Shanghai
EXPOSE 8080
ENTRYPOINT ["/app/nacoshist"]
