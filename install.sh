#!/usr/bin/env bash
#
# SwallowAgent 一键安装脚本 (Linux)
# 用法:
#   curl -fsSL https://raw.githubusercontent.com/M1saka10010/SwallowAgent/main/install.sh | bash -s -- <reportUrl> <token>
#   或
#   bash install.sh <reportUrl> <token>
#   或使用具名参数:
#   bash install.sh --report-url ws://host:8080/report --token xxxx
#
set -euo pipefail

REPO="M1saka10010/SwallowAgent"
BIN_NAME="swallow-agent"
INSTALL_DIR="${INSTALL_DIR:-/opt/swallow-agent}"
LINK_DIR="${LINK_DIR:-/usr/local/bin}"
SERVICE_NAME="swallow-agent.service"
SERVICE_DIR="${SERVICE_DIR:-/etc/systemd/system}"

info()  { printf '\033[1;34m[INFO]\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m[WARN]\033[0m %s\n' "$*"; }
error() { printf '\033[1;31m[ERROR]\033[0m %s\n' "$*" >&2; exit 1; }

# 解析参数：reportUrl 与 token
REPORT_URL=""
TOKEN=""
POSITIONAL=()
while [ $# -gt 0 ]; do
    case "$1" in
        --report-url) REPORT_URL="${2:-}"; shift 2 ;;
        --token)      TOKEN="${2:-}"; shift 2 ;;
        -h|--help)
            echo "用法: bash install.sh <reportUrl> <token>"
            echo "      bash install.sh --report-url <url> --token <token>"
            exit 0 ;;
        *) POSITIONAL+=("$1"); shift ;;
    esac
done
[ -z "$REPORT_URL" ] && [ "${#POSITIONAL[@]}" -ge 1 ] && REPORT_URL="${POSITIONAL[0]}"
[ -z "$TOKEN" ] && [ "${#POSITIONAL[@]}" -ge 2 ] && TOKEN="${POSITIONAL[1]}"

[ -n "$REPORT_URL" ] || error "缺少回报地址，请传入。示例: bash install.sh ws://host:8080/report <token>"
[ -n "$TOKEN" ]      || error "缺少鉴权 Token，请传入。示例: bash install.sh ws://host:8080/report <token>"
case "$REPORT_URL" in
    ws://*|wss://*) ;;
    *) error "回报地址必须以 ws:// 或 wss:// 开头，当前: $REPORT_URL" ;;
esac

# 检测下载工具
if command -v curl >/dev/null 2>&1; then
    DL="curl -fsSL"
elif command -v wget >/dev/null 2>&1; then
    DL="wget -qO-"
else
    error "需要 curl 或 wget，请先安装其中之一。"
fi

# 检测操作系统
OS="$(uname -s)"
[ "$OS" = "Linux" ] || error "此脚本仅支持 Linux，当前系统: $OS"

# 检测架构
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64)         ARCH="amd64" ;;
    aarch64|arm64)        ARCH="arm64" ;;
    *) error "不支持的架构: $ARCH" ;;
esac

ASSET="${BIN_NAME}-linux-${ARCH}"

# 获取最新版本下载地址
info "正在查询最新版本..."
API_URL="https://api.github.com/repos/${REPO}/releases/latest"
RELEASE_JSON="$($DL "$API_URL")" || error "无法访问 GitHub API。"

TAG="$(printf '%s' "$RELEASE_JSON" | grep -oE '"tag_name"\s*:\s*"[^"]+"' | head -n1 | sed -E 's/.*"tag_name"\s*:\s*"([^"]+)".*/\1/')"
[ -n "$TAG" ] || error "无法解析最新版本号。"
info "最新版本: $TAG"

DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"

# 下载二进制
TMP_FILE="$(mktemp)"
trap 'rm -f "$TMP_FILE"' EXIT

info "正在下载: $DOWNLOAD_URL"
if command -v curl >/dev/null 2>&1; then
    curl -fL --progress-bar -o "$TMP_FILE" "$DOWNLOAD_URL" || error "下载失败。"
else
    wget -O "$TMP_FILE" "$DOWNLOAD_URL" || error "下载失败。"
fi

[ -s "$TMP_FILE" ] || error "下载的文件为空。"
chmod +x "$TMP_FILE"

# 提权辅助函数
if [ "$(id -u)" -eq 0 ]; then
    SUDO=""
elif command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
    info "安装需要管理员权限，可能会提示输入密码。"
else
    error "需要 root 权限或 sudo 来写入 ${INSTALL_DIR} 和 ${LINK_DIR}。"
fi

# 安装到 /opt/swallow-agent
TARGET="${INSTALL_DIR}/${BIN_NAME}"
info "正在安装到 $TARGET"
$SUDO mkdir -p "$INSTALL_DIR"
$SUDO mv "$TMP_FILE" "$TARGET"
$SUDO chmod 755 "$TARGET"
trap - EXIT

# 创建软链接到 PATH
LINK="${LINK_DIR}/${BIN_NAME}"
info "正在创建软链接: $LINK -> $TARGET"
$SUDO mkdir -p "$LINK_DIR"
$SUDO ln -sf "$TARGET" "$LINK"

info "安装完成: $TARGET"
info "软链接: $LINK"

# 生成配置文件 config.yml（已存在则跳过，避免覆盖用户配置）
CONFIG_FILE="${INSTALL_DIR}/config.yml"
if $SUDO test -f "$CONFIG_FILE"; then
    info "配置文件已存在，跳过生成: $CONFIG_FILE"
else
    info "正在生成配置文件: $CONFIG_FILE"
    $SUDO tee "$CONFIG_FILE" >/dev/null <<EOF
# 磁盘目录（为空则采集根目录 /）
paths:
# 网卡名称（为空则聚合所有网卡）
interfaceNames:
# 使用情况采集间隔（秒）；系统信息按此间隔的 20 倍上报
interval: 3
# 回报地址，必须使用 ws:// 或 wss://
reportUrl: ${REPORT_URL}
# 鉴权 Token
token: ${TOKEN}
# 调试模式
isDebug: false
EOF
    $SUDO chmod 644 "$CONFIG_FILE"
fi

# 生成 systemd 服务文件
SERVICE_FILE="${INSTALL_DIR}/${SERVICE_NAME}"
info "正在生成服务文件: $SERVICE_FILE"
$SUDO tee "$SERVICE_FILE" >/dev/null <<EOF
[Unit]
Description=Swallow Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
ExecStart=${INSTALL_DIR}/${BIN_NAME} -c ${INSTALL_DIR}/config.yml
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
$SUDO chmod 644 "$SERVICE_FILE"

# 注册并启动 systemd 服务
if command -v systemctl >/dev/null 2>&1; then
    info "正在注册 systemd 服务..."
    $SUDO ln -sf "$SERVICE_FILE" "${SERVICE_DIR}/${SERVICE_NAME}"
    $SUDO systemctl daemon-reload
    info "正在启用并启动服务..."
    $SUDO systemctl enable --now "${SERVICE_NAME}"
    if $SUDO systemctl is-active --quiet "${SERVICE_NAME}"; then
        info "服务已启动并设为开机自启。"
    else
        warn "服务启动失败，请检查日志: sudo journalctl -u ${SERVICE_NAME} -e"
    fi
    info "常用命令:"
    info "  查看状态: sudo systemctl status ${SERVICE_NAME}"
    info "  查看日志: sudo journalctl -u ${SERVICE_NAME} -f"
else
    warn "未检测到 systemctl，已生成服务文件但未注册: $SERVICE_FILE"
fi

# 验证
if command -v "$BIN_NAME" >/dev/null 2>&1; then
    info "可执行文件已就绪，运行 '${BIN_NAME}' 即可使用。"
else
    warn "$LINK_DIR 不在 PATH 中，请手动添加或使用完整路径: $LINK"
fi
