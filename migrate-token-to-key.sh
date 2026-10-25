#!/usr/bin/env bash
# One-time Token authentication breaking migration from the standard Linux layout:
# https://github.com/M1saka10010/SwallowAgent/releases/tag/v0.0.2
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
            echo '用法: printf %s TOKEN | sudo bash migrate-token-to-key.sh --binary ./swallow-agent --report-url wss://example.com/report --enrollment-token-stdin [--dev-mode]'
            echo '仅迁移 v0.0.2 Token 标准安装，不提供通用升级，不下载 release。'
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
if [ -n "$REPORT_URL" ]; then
    [[ "$REPORT_URL" =~ $URL_PATTERN ]] || error '地址必须为 wss://host[:port]/report，不支持查询参数'
fi
INSTALL_DIR=/opt/swallow-agent
TARGET="$INSTALL_DIR/swallow-agent"
KEY="$INSTALL_DIR/agent.key"
CONFIG="$INSTALL_DIR/config.yml"
SERVICE=/etc/systemd/system/swallow-agent.service
STATE="$INSTALL_DIR/.token-to-key-complete"
safe() {
    [ ! -L "$1" ] && [ -e "$1" ] || error "不安全路径: $1"
    [ "$(stat -c %u "$1")" = 0 ] || error "非 root 所有: $1"
    local mode
    mode=$(stat -c %a "$1")
    (( (8#$mode & 0022) == 0 )) || error "组或其他用户可写: $1"
}
for path in "$INSTALL_DIR" "$(dirname "$INSTALL_DIR")" "$(dirname "$SERVICE")"; do safe "$path"; done
for path in "$(dirname "$INSTALL_DIR")" "$(dirname "$SERVICE")"; do
    while [ "$path" != / ]; do
        if [ "$path" != /tmp ]; then safe "$path";
        else [ ! -L "$path" ] && [ "$(stat -c '%u:%a' "$path")" = 0:1777 ] || error '不安全临时目录'; fi
        path=$(dirname "$path")
    done
done
[ -d "$INSTALL_DIR" ] || error '安装目录不存在'
for path in "$TARGET" "$CONFIG"; do safe "$path"; [ -f "$path" ] || error '安装文件不是普通文件'; done
[ "$(stat -c %a "$CONFIG")" = 600 ] || error '配置必须为 600'
UNIT="$SERVICE"
if [ -L "$SERVICE" ]; then
    [ "$(stat -c %u "$SERVICE")" = 0 ] && [ "$(readlink "$SERVICE")" = "$INSTALL_DIR/swallow-agent.service" ] || error '拒绝不匹配服务符号链接'
    UNIT="$INSTALL_DIR/swallow-agent.service"
fi
safe "$UNIT"
[ -f "$UNIT" ] || error 'unit 必须为普通文件'
[ -L "$SERVICE" ] && [ "$(stat -c %a "$INSTALL_DIR")" = 755 ] || error '非标准 v0.0.2 安装'
[ ! -e "$INSTALL_DIR/.install.lock" ] && [ ! -L "$INSTALL_DIR/.install.lock" ] || safe "$INSTALL_DIR/.install.lock"
exec 9>"$INSTALL_DIR/.install.lock"
flock -n 9 || error '另一个安装进程正在运行'
if [ -e "$STATE" ] || [ -L "$STATE" ]; then
    safe "$STATE"
    [ -f "$STATE" ] && [ "$(stat -c %a "$STATE")" = 600 ] || error '迁移标记权限不安全'
    [ "$(head -n 1 "$STATE")" = "token-to-key $REPORT_URL $DEV_MODE" ] || error '迁移参数与完成标记不一致'
    tail -n +2 "$STATE" | sha256sum --check --status || error '已完成迁移的文件发生变化；拒绝更新'
    ACTIVE=$(systemctl show -p ActiveState --value swallow-agent.service </dev/null)
    case "$ACTIVE" in active) systemctl is-active --quiet swallow-agent.service </dev/null ;; inactive) ;; *) error '迁移已完成，但服务不健康；请手动检查' ;; esac
    printf '%s\n' 'Token 迁移已完成；未替换二进制、配置或私钥。'
    exit 0
fi
BACKUP=$(mktemp -d "$INSTALL_DIR/.token-migration.XXXXXX")
HELP="$("$BINARY" -h </dev/null 2>&1)"
[[ "$HELP" == *-prepare-token-migration* ]] || error '二进制不支持 Token 迁移，请先从当前源码构建'
chmod 700 "$BACKUP"
STOPPED=false
WAS_ACTIVE=false
COMMITTED=false
CHILD=""
cleanup_migration() {
    local status=$?
    trap - EXIT INT TERM
    if [ -n "$CHILD" ]; then kill -TERM "$CHILD" 2>/dev/null || true; wait "$CHILD" 2>/dev/null || true; fi
    rm -f "$BACKUP/enrollment-token"
    if [ "$STOPPED" = true ] && [ "$COMMITTED" = false ]; then
        rm -f "$STATE"
        systemctl stop swallow-agent.service </dev/null || true
        if ! cp -p "$BACKUP/binary" "$BACKUP/restore-binary" || ! chmod 755 "$BACKUP/restore-binary" || ! mv -f "$BACKUP/restore-binary" "$TARGET" || ! cp -p "$BACKUP/config" "$BACKUP/restore-config" || ! mv -f "$BACKUP/restore-config" "$CONFIG"; then
            printf '%s\n' "本地回滚失败，请从 $BACKUP 恢复" >&2
        elif [ "$WAS_ACTIVE" = true ]; then
            systemctl restart swallow-agent.service </dev/null && systemctl is-active --quiet swallow-agent.service </dev/null || printf '%s\n' '原服务恢复失败，请手动检查' >&2
        fi
        printf '%s\n' '迁移失败；新生成私钥已保留供重试。服务端绑定不可回滚，旧 Token 服务可能无法重新认证。' >&2
    fi
    printf '%s\n' "私有备份保留于 $BACKUP（含旧凭据，请妥善清理）" >&2
    exit "$status"
}
trap cleanup_migration EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cat > "$BACKUP/expected-unit" <<EOF
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

[Install]
WantedBy=multi-user.target
EOF
cp "$UNIT" "$BACKUP/unit-check"
cmp -s "$BACKUP/expected-unit" "$BACKUP/unit-check" || error '服务不是标准 root ExecStart/WorkingDirectory，拒绝迁移'
DROPINS=$(systemctl show -p DropInPaths --value swallow-agent.service </dev/null)
[ -z "$DROPINS" ] || error '不支持服务 drop-in 定制'
FRAGMENT=$(systemctl show -p FragmentPath --value swallow-agent.service </dev/null)
[ "$FRAGMENT" = "$UNIT" ] || [ "$FRAGMENT" = "$SERVICE" ] || error '实际加载的服务路径不匹配'
install -m 600 "$TARGET" "$BACKUP/binary"
install -m 600 "$CONFIG" "$BACKUP/config"
install -m 600 "$UNIT" "$BACKUP/unit"
install -m 755 "$BINARY" "$BACKUP/new-binary"
timeout 15 "$BACKUP/new-binary" -check-enrollment-token <&0 > "$BACKUP/enrollment-token" &
CHILD=$!
wait "$CHILD"
CHILD=""
ARGS=(-prepare-token-migration "$CONFIG" -output-config "$BACKUP/new-config" -default-private-key "$KEY" -enrollment-token-stdin)
[ -z "$REPORT_URL" ] || ARGS+=(-report-url "$REPORT_URL")
[ "$DEV_MODE" = false ] || ARGS+=(-dev-mode)
"$BACKUP/new-binary" "${ARGS[@]}" </dev/null
ACTIVE=$(systemctl show -p ActiveState --value swallow-agent.service </dev/null)
case "$ACTIVE" in active) WAS_ACTIVE=true ;; inactive|failed) ;; *) error '服务状态不稳定，拒绝迁移' ;; esac
STOPPED=true
systemctl stop swallow-agent.service </dev/null
ARGS=(-enroll-token-migration "$BACKUP/new-config" -enrollment-token-stdin)
"$BACKUP/new-binary" "${ARGS[@]}" < "$BACKUP/enrollment-token" &
CHILD=$!
wait "$CHILD"
CHILD=""
rm -f "$BACKUP/enrollment-token"
mv -f "$BACKUP/new-binary" "$TARGET"
mv -f "$BACKUP/new-config" "$CONFIG"
systemctl restart swallow-agent.service </dev/null
systemctl is-active --quiet swallow-agent.service </dev/null
sleep 1
systemctl is-active --quiet swallow-agent.service </dev/null
if [ "$WAS_ACTIVE" = false ]; then systemctl stop swallow-agent.service </dev/null; fi
printf 'token-to-key %s %s\n' "$REPORT_URL" "$DEV_MODE" > "$BACKUP/complete"
sha256sum "$TARGET" "$CONFIG" "$KEY" "$UNIT" >> "$BACKUP/complete"
mv "$BACKUP/complete" "$STATE"
COMMITTED=true
printf '%s\n' '迁移成功；已完成采集、认证及上报写入（协议无持久化回执）；原 unit、链接及服务启用状态保持不变。'
exit 0
