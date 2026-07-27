# ===== Stage 1: 构建前端 =====
FROM docker.linkos.org/library/node:20-alpine AS web-builder
WORKDIR /app/web

# 先复制依赖文件，利用 Docker 缓存
COPY web/package.json web/package-lock.json* ./
# 使用淘宝镜像加速（国内网络环境）
RUN npm config set registry https://registry.npmmirror.com \
    && npm ci || npm install

# 复制前端源码并构建
COPY web/ ./
RUN npm run build

# ===== Stage 2: 构建后端 =====
# 使用 Debian 版 golang（glibc），使 CGO 编译的二进制与 glibc 运行时匹配，
# 同时兼容 cursor 等捆绑 glibc node 的预编译 agent。
FROM docker.linkos.org/library/golang:1.25 AS go-builder
WORKDIR /app

# golang:1.25（Debian bookworm）已内置 gcc 等编译工具链，无需额外安装 musl-dev

# 复制 vendor 目录，使用本地依赖
COPY go.mod go.sum vendor/ ./
ENV GOFLAGS=-mod=vendor

# 复制源码并编译（CGO_ENABLED=1 以支持 SQLite）
COPY . .
COPY --from=web-builder /app/web/dist ./web/dist
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /out/opennexus ./cmd/server

# ===== Stage 3: 运行时 =====
# 使用 Debian 版 node 镜像（glibc），因为 agents 通过 npx 调用 claude-agent-acp，
# 且 cursor 等 agent 捆绑的是 glibc 预编译 node，Alpine(musl) 下无法执行。
FROM docker.linkos.org/library/node:20-slim AS runtime
WORKDIR /app

# 换阿里云镜像源加速 apt 安装（兼容 deb822 与传统 sources.list 两种格式）
RUN sed -i 's#deb.debian.org#mirrors.aliyun.com#g' /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
    sed -i 's#deb.debian.org#mirrors.aliyun.com#g' /etc/apt/sources.list 2>/dev/null || true

# 安装运行时依赖：bash（部分 agent / 脚本依赖 bash）、sqlite、ca-cert、git、
# wget（HEALTHCHECK 使用）、tzdata（时区）
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash ca-certificates libsqlite3-0 git wget tzdata \
    && ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone \
    && rm -rf /var/lib/apt/lists/*

# 复制后端二进制
COPY --from=go-builder /out/opennexus /app/opennexus

# 复制前端构建产物（后端 release 模式会服务这些静态文件）
COPY --from=web-builder /app/web/dist /app/web/dist

# 复制默认配置
COPY config.yaml /app/config.yaml

# 数据持久化目录：统一使用默认 ~/.openNexus（root 用户即 /root/.openNexus），
# 数据库、会话、ACP 二进制缓存、调试目录全部落在此目录，挂载单个卷即可全量持久化。
RUN mkdir -p /root/.openNexus/session
VOLUME ["/root/.openNexus"]

# 显式指定 npm 缓存目录，便于通过 docker volume 持久化。
# npx 调用 claude-agent-acp 时下载的包会缓存在 /root/.npm/_npx，
# 持久化后容器重启无需重新从网络下载。
# 不再设置 DATABASE_PATH / AGENTS_WORKSPACE_SESSION_DIR，让程序走默认 ~/.openNexus 路径。
# LANG/LC_ALL 设为 C.UTF-8（Debian 内置），保证终端及子进程正确处理中文等多字节字符。
ENV NPM_CONFIG_CACHE=/root/.npm \
    SERVER_MODE=release \
    SERVER_PORT=8080 \
    WEB_DIST=/app/web/dist \
    NODE_ENV=production \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/health >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/opennexus"]
