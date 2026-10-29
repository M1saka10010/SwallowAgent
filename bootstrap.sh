#!/usr/bin/env bash
set -euo pipefail
umask 077
error() { printf '%s\n' "$*" >&2; exit 1; }
VERSION=''
MODE=install
GITHUB_PROXY=''
ARGS=()
TOKEN_STDIN=false
while [ "$#" -gt 0 ]; do
    case "$1" in
        --version|--mode|--report-url|--github-proxy)
            [ "$#" -ge 2 ] || error 'Missing argument value'
            case "$1" in
                --version) VERSION=$2 ;;
                --mode) MODE=$2 ;;
                --github-proxy) GITHUB_PROXY=$2 ;;
                --report-url) ARGS+=("$1" "$2") ;;
            esac
            shift 2 ;;
        --enrollment-token-stdin) TOKEN_STDIN=true; ARGS+=("$1"); shift ;;
        --dev-mode) ARGS+=("$1"); shift ;;
        -h|--help)
            echo 'Usage: bootstrap.sh --version TAG [--mode install] --report-url URL --enrollment-token-stdin [--dev-mode]'
            echo 'Local binary: use install.sh or upgrade.sh --binary PATH directly.'
            echo 'Upgrade: bootstrap.sh --version TAG --mode upgrade [--github-proxy HTTPS_PREFIX]'
            exit 0 ;;
        *) error 'Unsupported argument' ;;
    esac
done
SEMVER='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][A-Za-z0-9-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][A-Za-z0-9-]*))*)?(\+[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*)?$'
[[ "$VERSION" =~ $SEMVER ]] || error 'An explicit release version is required (not latest)'
case "$MODE" in
    install) [ "$TOKEN_STDIN" = true ] || error 'Required: --enrollment-token-stdin' ;;
    upgrade) [ "${#ARGS[@]}" -eq 0 ] || error 'Upgrade does not accept enrollment/report/dev arguments' ;;
    *) error 'Unsupported mode' ;;
esac
if [ -n "$GITHUB_PROXY" ]; then
    PROXY_PATTERN='^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?(/[A-Za-z0-9._~-]+)*/?$'
    [[ "$GITHUB_PROXY" =~ $PROXY_PATTERN ]] || error 'Invalid HTTPS GitHub proxy prefix'
    [[ "/$GITHUB_PROXY/" != *'/../'* && "/$GITHUB_PROXY/" != *'/./'* ]] || error 'Invalid proxy path'
fi
[ "$(uname -s)" = Linux ] || error 'Only Linux is supported'
case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) error 'Unsupported architecture' ;;
esac
for command in curl sha256sum mktemp bash; do
    command -v "$command" >/dev/null || error 'Missing required download or verification tool'
done
WORK=''
CHILD=''
LAUNCHING=0
PENDING=''
cleanup() { if [ -n "$WORK" ]; then rm -rf -- "$WORK"; fi; }
forward() {
    PENDING=${PENDING:-$2}
    # Do not ignore signals before fork: the child would inherit SIG_IGN.
    [ "$LAUNCHING" -eq 0 ] || return 0
    trap '' INT TERM HUP
    if [ -n "$CHILD" ]; then
        # Async shells can inherit ignored INT; TERM lets the child clean up and roll back.
        kill -s TERM "$CHILD" 2>/dev/null || true
        wait "$CHILD" 2>/dev/null || true
    fi
    exit "$PENDING"
}
trap cleanup EXIT
trap 'forward INT 130' INT
trap 'forward TERM 143' TERM
trap 'forward HUP 129' HUP
for PARENT in "${TMPDIR:-/tmp}" /var/tmp /opt; do
    if ! WORK=$(mktemp -d -- "${PARENT%/}/swallow-bootstrap.XXXXXXXXXX" 2>/dev/null); then
        WORK=''
        continue
    fi
    if { printf '#!/bin/sh\nexit 0\n' > "$WORK/probe" &&
         chmod 700 "$WORK/probe" &&
         "$WORK/probe" </dev/null; } 2>/dev/null; then
        rm -f -- "$WORK/probe"
        break
    fi
    cleanup
    WORK=''
done
[ -n "$WORK" ] || error 'No writable executable temporary directory available (TMPDIR or /tmp, /var/tmp, /opt); nothing downloaded'
BASE="https://github.com/M1saka10010/SwallowAgent/releases/download/$VERSION"
[ -z "$GITHUB_PROXY" ] || BASE="${GITHUB_PROXY%/}/$BASE"
download() {
    LAUNCHING=1
    curl --disable --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 15 --max-time 300 --output "$WORK/$1" "$BASE/$1" </dev/null &
    CHILD=$!
    LAUNCHING=0
    [ -z "$PENDING" ] || forward TERM "$PENDING"
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
if [ "$MODE" = upgrade ]; then
    HASH=$(sha256sum "$WORK/$BINARY")
    ARGS=(--version "$VERSION" --sha256 "${HASH%% *}")
fi
chmod 700 "$WORK/$BINARY"
exec 3<&0
LAUNCHING=1
bash "$WORK/$SCRIPT" --binary "$WORK/$BINARY" "${ARGS[@]}" <&3 3<&- &
CHILD=$!
LAUNCHING=0
[ -z "$PENDING" ] || forward TERM "$PENDING"
STATUS=0
wait "$CHILD" || STATUS=$?
CHILD=''
exit "$STATUS"
