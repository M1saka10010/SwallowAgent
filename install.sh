#!/usr/bin/env bash
set -euo pipefail
umask 077
error() { printf '%s\n' "$*" >&2; exit 1; }
BINARY=""
REPORT_URL=""
TOKEN_STDIN=false
DEV_MODE=false
while [ "$#" -gt 0 ]; do
    case "$1" in
        --upgrade) error "通用升级不受支持；Token 迁移请使用 migrate-token-to-key.sh" ;;
        --enrollment-token-stdin) TOKEN_STDIN=true; shift ;;
        --dev-mode) DEV_MODE=true; shift ;;
        --binary|--report-url)
            [ "$#" -ge 2 ] || error "缺少参数值: $1"
            case "$1" in
                --binary) BINARY="$2" ;;
                --report-url) REPORT_URL="$2" ;;
            esac
            shift 2 ;;
        -h|--help)
            echo '用法: printf %s TOKEN | sudo bash install.sh --binary ./swallow-agent --report-url wss://example.com/report --enrollment-token-stdin [--dev-mode]'
            echo '仅安装本地新版本；注册后自动启动服务，不下载 release。'
            exit 0 ;;
        *) error "未知参数: $1" ;;
    esac
done
[ "$(uname -s)" = Linux ] || error '仅支持 Linux'
[ "$(id -u)" -eq 0 ] || error '请使用 root 或 sudo 执行'
[ -f "$BINARY" ] && [ -x "$BINARY" ] || error '请指定本地构建且可信的可执行文件 --binary'
URL_PATTERN='^wss://([a-zA-Z0-9.-]+|\[[a-fA-F0-9:]+\])(:[0-9]+)?/report$'
[ "$TOKEN_STDIN" = true ] || error '必须指定 --enrollment-token-stdin'
if [ "$DEV_MODE" = true ]; then
    URL_PATTERN='^wss?://([a-zA-Z0-9.-]+|\[[a-fA-F0-9:]+\])(:[0-9]+)?/report$'
fi
[[ "$REPORT_URL" =~ $URL_PATTERN ]] || error '地址必须为 wss://host[:port]/report，不支持查询参数'
INSTALL_DIR=/opt/swallow-agent
TARGET="$INSTALL_DIR/swallow-agent"
KEY="$INSTALL_DIR/agent.key"
CONFIG="$INSTALL_DIR/config.yml"
SERVICE=/etc/systemd/system/swallow-agent.service
STATE="$INSTALL_DIR/.enrollment-install"
HELP="$("$BINARY" -h </dev/null 2>&1)"
[[ "$HELP" == *-enrollment-token-stdin* ]] || error '二进制不支持自动注册，请先从当前源码构建'
EXPECTED_STATE="swallow-enrollment-v1 $REPORT_URL $DEV_MODE"
if [ -e "$INSTALL_DIR" ] || [ -L "$INSTALL_DIR" ]; then
    [ -d "$INSTALL_DIR" ] && [ ! -L "$INSTALL_DIR" ] && [ "$(stat -c '%u:%a' "$INSTALL_DIR")" = '0:700' ] || error '安装目录权限不安全'
    [ -f "$STATE" ] && [ ! -L "$STATE" ] && [ "$(stat -c '%u:%a' "$STATE")" = '0:600' ] || error '既有安装并非本脚本管理，拒绝覆盖'
    [ "$(<"$STATE")" = "$EXPECTED_STATE" ] || error '重试必须使用相同地址与开发模式'
else
    [ ! -e "$SERVICE" ] && [ ! -L "$SERVICE" ] || error '服务文件已存在，拒绝覆盖'
    mkdir -m 700 "$INSTALL_DIR"
    printf '%s\n' "$EXPECTED_STATE" > "$STATE"
fi
for file in "$TARGET" "$CONFIG" "$SERVICE" "$INSTALL_DIR/.install.lock"; do
    [ ! -L "$file" ] || error "拒绝符号链接: $file"
    [ ! -e "$file" ] || [ -f "$file" ] || error "不是普通文件: $file"
done
exec 9>"$INSTALL_DIR/.install.lock"
flock -n 9 || error '另一个安装进程正在运行'
ARGS=(-enroll "$REPORT_URL" -private-key "$KEY" -enrollment-token-stdin)
[ "$DEV_MODE" = false ] || ARGS+=(-dev-mode)
CHILD=""
cleanup_install() {
    local status=$?
    trap - EXIT INT TERM HUP
    if [ -n "$CHILD" ]; then kill -TERM "$CHILD" 2>/dev/null || true; wait "$CHILD" 2>/dev/null || true; fi
    exit "$status"
}
trap cleanup_install EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
"$BINARY" "${ARGS[@]}" <&0 &
CHILD=$!
wait "$CHILD"
CHILD=""
if [ ! -e "$TARGET" ]; then
    TEMP_BINARY="$(mktemp "$INSTALL_DIR/.binary.XXXXXX")"
    install -m 755 "$BINARY" "$TEMP_BINARY"
    mv "$TEMP_BINARY" "$TARGET"
fi
if [ ! -e "$CONFIG" ]; then
TEMP_CONFIG="$(mktemp "$INSTALL_DIR/.config.XXXXXX")"
cat > "$TEMP_CONFIG" <<EOF
paths:
interfaceNames:
interval: 3
reportUrl: '$REPORT_URL'
privateKeyPath: '$KEY'
devMode: $DEV_MODE
isDebug: false
EOF
mv "$TEMP_CONFIG" "$CONFIG"
fi
TEMP_SERVICE="$(mktemp "$INSTALL_DIR/.service.XXXXXX")"
cat > "$TEMP_SERVICE" <<EOF
[Unit]
Description=Swallow Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=$TARGET -c $CONFIG
Restart=on-failure
RestartSec=5s
UMask=0077

[Install]
WantedBy=multi-user.target
EOF
if [ -e "$SERVICE" ]; then
    cmp -s "$TEMP_SERVICE" "$SERVICE" || error '既有服务内容不同，拒绝覆盖'
else
    SYSTEM_TEMP="$(mktemp /etc/systemd/system/.swallow-agent.XXXXXX)"
    install -m 644 "$TEMP_SERVICE" "$SYSTEM_TEMP"
    mv "$SYSTEM_TEMP" "$SERVICE"
fi
rm -f "$TEMP_SERVICE"
systemctl daemon-reload
systemctl enable --now swallow-agent.service
printf '%s\n' '注册与安装完成，服务已启动。无需额外批准。'
