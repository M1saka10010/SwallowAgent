#!/usr/bin/env bash
set -euo pipefail
umask 077
error() { printf '%s\n' "$*" >&2; exit 1; }
VERSION=''
MODE=install
ARGS=()
TOKEN_STDIN=false
while [ "$#" -gt 0 ]; do
    case "$1" in
        --version|--mode|--report-url)
            [ "$#" -ge 2 ] || error 'Missing argument value'
            case "$1" in
                --version) VERSION=$2 ;;
                --mode) MODE=$2 ;;
                --report-url) ARGS+=("$1" "$2") ;;
            esac
            shift 2 ;;
        --enrollment-token-stdin) TOKEN_STDIN=true; ARGS+=("$1"); shift ;;
        --dev-mode) ARGS+=("$1"); shift ;;
        -h|--help)
            echo 'Usage: bootstrap.sh --version TAG [--mode install|migrate-token-to-key] --report-url URL --enrollment-token-stdin [--dev-mode]'
            echo 'Local binary: use install.sh or migrate-token-to-key.sh --binary PATH directly.'
            exit 0 ;;
        *) error 'Unsupported argument' ;;
    esac
done
SEMVER='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][A-Za-z0-9-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][A-Za-z0-9-]*))*)?(\+[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*)?$'
[[ "$VERSION" =~ $SEMVER ]] || error 'An explicit release version is required (not latest)'
case "$MODE" in install|migrate-token-to-key) ;; *) error 'Unsupported mode' ;; esac
[ "$TOKEN_STDIN" = true ] || error 'Required: --enrollment-token-stdin'
[ "$(uname -s)" = Linux ] || error 'Only Linux is supported'
case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) error 'Unsupported architecture' ;;
esac
for command in curl sha256sum mktemp bash; do
    command -v "$command" >/dev/null || error 'Missing required download or verification tool'
done
WORK=$(mktemp -d)
CHILD=''
cleanup() { rm -rf -- "$WORK"; }
forward() {
    trap '' INT TERM HUP
    if [ -n "$CHILD" ]; then
        # Async shells can inherit ignored INT; TERM also runs migration rollback.
        kill -s TERM "$CHILD" 2>/dev/null || true
        wait "$CHILD" 2>/dev/null || true
    fi
    exit "$2"
}
trap cleanup EXIT
trap 'forward INT 130' INT
trap 'forward TERM 143' TERM
trap 'forward HUP 129' HUP
BASE="https://github.com/M1saka10010/SwallowAgent/releases/download/$VERSION"
download() {
    curl --disable --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 15 --max-time 300 --output "$WORK/$1" "$BASE/$1" </dev/null &
    CHILD=$!
    local status=0
    wait "$CHILD" || status=$?
    CHILD=''
    [ "$status" -eq 0 ] || error 'Release download failed; nothing executed'
}
verify() {
    local hash name extra expected='' count=0 actual
    while read -r hash name extra; do
        if [ "$name" = "$1" ]; then
            [[ "$hash" =~ ^[a-fA-F0-9]{64}$ ]] && [ -z "$extra" ] || error 'Invalid checksum entry'
            expected=$hash
            count=$((count + 1))
        fi
    done < "$WORK/checksums.txt"
    [ "$count" -eq 1 ] || error 'Missing or duplicate checksum entry'
    actual=$(sha256sum "$WORK/$1")
    [ "${actual%% *}" = "${expected,,}" ] || error 'Release checksum mismatch; nothing executed'
}
BINARY="swallow-agent-linux-$ARCH"
SCRIPT="$MODE.sh"
download checksums.txt
download "$BINARY"
download "$SCRIPT"
verify "$BINARY"
verify "$SCRIPT"
chmod 700 "$WORK/$BINARY"
exec 3<&0
bash "$WORK/$SCRIPT" --binary "$WORK/$BINARY" "${ARGS[@]}" <&3 3<&- &
CHILD=$!
STATUS=0
wait "$CHILD" || STATUS=$?
CHILD=''
exit "$STATUS"
