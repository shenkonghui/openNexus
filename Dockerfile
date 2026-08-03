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

# 先复制依赖清单并下载模块（module 模式，vendor/ 已 gitignore 不入库，
# 因此不能 COPY vendor/ / 不能依赖 -mod=vendor，否则干净检出构建会失败）。
# 使用国内 goproxy 加速（与 base 镜像/npm 镜像一致）。
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download

# 仅复制 Go 编译所需源码：cmd/ 与 internal/（含 internal/acp/registry.json 的 go:embed）。
# 不再 COPY . .，避免把 .git / web / electron / dist / data 等无关大目录带入构建层。
# 注意：COPY 多个目录到 ./ 会平铺内容，必须分别 COPY 到对应子目录以保留包路径。
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=web-builder /app/web/dist ./web/dist
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /out/opennexus ./cmd/server

# ===== Stage 3: 运行时 =====
# 使用 Debian 版 node 镜像（glibc），因为 agents 通过 npx 调用 claude-agent-acp，
# 且 cursor 等 agent 捆绑的是 glibc 预编译 node，Alpine(musl) 下无法执行。
# 选用 node:22-slim：@agentclientprotocol/claude-agent-acp@0.64.0 要求 node>=22，
# node:20-slim 会导致 EBADENGINE 警告并可能握手失败。
FROM docker.linkos.org/library/node:22-slim AS runtime
WORKDIR /app

# 换阿里云镜像源加速 apt 安装（兼容 deb822 与传统 sources.list 两种格式）
RUN sed -i 's#deb.debian.org#mirrors.aliyun.com#g' /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
    sed -i 's#deb.debian.org#mirrors.aliyun.com#g' /etc/apt/sources.list 2>/dev/null || true

# 安装运行时依赖：bash（部分 agent / 脚本依赖 bash）、sqlite、ca-cert、git、
# wget（HEALTHCHECK 使用）、curl、make、vim（常见工具）、tzdata（时区）、
# bubblewrap（agent OS 沙箱，sandbox.enabled 时使用）
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash ca-certificates libsqlite3-0 git wget curl make vim tzdata bubblewrap \
    && ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone \
    && rm -rf /var/lib/apt/lists/*

# 安装新版 docker CLI（docker-ce-cli，阿里云镜像源）。
# 背景：宿主机 Docker Server 29.x（API>=1.44），Debian 12 的 docker.io(20.10，API 1.41) 过旧，
# 连宿主机 socket 会报 "client version too old"。故从 docker-ce 官方仓库（阿里云加速）安装
# 最新 docker-ce-cli。容器内经挂载的宿主机 /var/run/docker.sock 管理宿主容器（供 agent 使用）。
RUN install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://mirrors.aliyun.com/docker-ce/linux/debian/gpg -o /etc/apt/keyrings/docker.asc \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://mirrors.aliyun.com/docker-ce/linux/debian bookworm stable" > /etc/apt/sources.list.d/docker.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends docker-ce-cli \
    && rm -rf /var/lib/apt/lists/* \
    # 创建 docker 组（gid 与宿主机 122 对齐），使 compose 的 group_add: docker 能生效；
    # 不依赖 adduser/passwd 包，直接写入 /etc/group（幂等：已存在则跳过）
    && (grep -q '^docker:' /etc/group || echo 'docker:x:122:' >> /etc/group)

# 复制后端二进制
COPY --from=go-builder /out/opennexus /app/opennexus

# 复制前端构建产物（后端 release 模式会服务这些静态文件）
COPY --from=web-builder /app/web/dist /app/web/dist

# 复制默认配置
# COPY config.yaml /app/config.yaml

# 创建 entrypoint 脚本：在主程序启动前按运行时 HOME 重定位目录、清理 npx 残留。
# 背景：容器内 HOME 由 docker-compose 注入为宿主机 HOME（如 /Users/xxx），与镜像构建期的 /root 不同。
# 该 HOME 在纯净镜像里通常不存在，必须在运行时按 $HOME 现场创建。
# 平台隔离：容器用 *.npm-linux / *.npm-global-linux 缓存（与宿主机 macOS 的 ~/.npm 物理隔离），
# 否则 npx（claude-agent-acp → claude-code 捆绑的原生二进制）跨平台复用会导致 agent 崩溃无法启动。
# 清理 npx rename 失败残留（背景：npx 先下载到 . 开头临时目录再 rename，容器被 kill 会留残骸，
# 下次 rename 目标非空 → ENOTEMPTY → agent 握手失败）。
# 校验 npx 缓存 bin 链接完整性（背景：npm exec 复用缓存时若 node_modules/.bin 缺失不会重建，
# 导致 "cbc: not found" / exit 127。清掉该缓存目录强制下次重新安装）。
# 注意：不再在主程序前串行预热 npx 缓存。此前 npx -y 下载 @agentclientprotocol/claude-agent-acp
# 在部分环境（registry 不可达/网络受限）会挂起不退出，阻塞后续 exec 导致应用无法启动。改为按需懒加载。
RUN printf '#!/bin/sh\n\
set -e\n\
# 运行时 HOME（由 compose 注入）可能不存在，现场创建关键子目录\n\
mkdir -p "$HOME/.openNexus/session" "$HOME/.npm-global-linux/bin" "$HOME/.npm-linux/_npx" 2>/dev/null || true\n\
# 运行时重定位 npm 缓存/全局目录与 PATH 到真实 $HOME 的 *-linux 隔离目录（覆盖镜像里 /root 的默认值）\n\
export NPM_CONFIG_CACHE="$HOME/.npm-linux"\n\
export NPM_CONFIG_PREFIX="$HOME/.npm-global-linux"\n\
export PATH="$HOME/.npm-global-linux/bin:$PATH"\n\
# 清理 npx 缓存中 npm rename 失败残留的临时目录（以 . 开头）\n\
find "$HOME/.npm-linux/_npx" -mindepth 1 -name ".*" -type d -exec rm -rf {} + 2>/dev/null || true\n\
# 校验 npx 缓存完整性：node_modules 存在但 .bin 缺失 → 包已装但 bin 链接丢失，清掉强制重装。\n\
# 否则 npm exec 命中缓存跳过安装，找不到 cbc/codebuddy 等命令（exit 127）。\n\
for d in "$HOME/.npm-linux/_npx"/*/; do\n\
  [ -d "$d/node_modules" ] && [ ! -d "$d/node_modules/.bin" ] && rm -rf "$d" 2>/dev/null || true\n\
done\n\
exec /app/opennexus\n' > /app/entrypoint.sh && chmod +x /app/entrypoint.sh

# 构建期仍以 /root 为兜底主目录（纯净镜像默认 HOME=/root）。
# VOLUME 仅声明匿名卷挂载点，实际路径以 compose 中 ${HOME} 挂载为准；
# 关键子目录由 entrypoint 在运行时按真实 HOME 现场创建。
RUN mkdir -p /root/.openNexus/session /root/.npm-global-linux/bin
VOLUME ["/root/.openNexus"]

# 镜像默认值以 /root 为基准；运行时由 entrypoint 按真实 $HOME 重置 NPM_CONFIG_CACHE /
# NPM_CONFIG_PREFIX / PATH 到 *-linux 隔离目录。LANG/LC_ALL=C.UTF-8（Debian 内置）保证多字节字符正确。
ENV NPM_CONFIG_CACHE=/root/.npm-linux \
    NPM_CONFIG_PREFIX=/root/.npm-global-linux \
    PATH="/root/.npm-global-linux/bin:${PATH}" \
    SERVER_MODE=release \
    SERVER_PORT=8008 \
    WEB_DIST=/app/web/dist \
    NODE_ENV=production \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

EXPOSE 8008

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8008/health >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/entrypoint.sh"]
