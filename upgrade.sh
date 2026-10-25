#!/usr/bin/env bash
set -euo pipefail
umask 077
error() { printf "%s\n" "$*" >&2; exit 1; }
BINARY= VERSION= SHA256=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --binary|--version|--sha256)
            [ "$#" -ge 2 ] || error "Missing argument value"
            case "$1" in --binary) BINARY=$2 ;; --version) VERSION=$2 ;; --sha256) SHA256=$2 ;; esac
            shift 2 ;;
        -h|--help) echo "Usage: upgrade.sh --binary PATH --version TAG --sha256 TRUSTED_SHA256"; exit 0 ;;
        *) error "Unsupported upgrade argument" ;;
    esac
done
SEMVER='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][A-Za-z0-9-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][A-Za-z0-9-]*))*)?(\+[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*)?$'
[[ "$VERSION" =~ $SEMVER ]] || error "Explicit release SemVer required"
[[ "$SHA256" =~ ^[a-fA-F0-9]{64}$ ]] || error "Trusted candidate SHA256 required"
[ "$(uname -s)" = Linux ] || error "Only Linux is supported"
case "$(uname -m)" in x86_64|amd64|aarch64|arm64) ;; *) error "Unsupported architecture" ;; esac
[ "$(id -u)" = 0 ] || error "Root required"
for command in systemctl flock timeout sha256sum stat cmp install mktemp; do
    command -v "$command" >/dev/null || error "Missing tool: $command"
done
INSTALL_DIR=/opt/swallow-agent
TARGET="$INSTALL_DIR/swallow-agent"
CONFIG="$INSTALL_DIR/config.yml"
KEY="$INSTALL_DIR/agent.key"
SERVICE=/etc/systemd/system/swallow-agent.service
safe() {
    [ ! -L "$1" ] && [ -e "$1" ] || error "Unsafe path: $1"
    [ "$(stat -c %u "$1")" = 0 ] || error "Non-root owner: $1"
    local mode
    mode=$(stat -c %a "$1")
    (( (8#$mode & 0022) == 0 )) || error "Writable path: $1"
}
regular() {
    safe "$1"
    [ -f "$1" ] && [ "$(stat -c %h "$1")" = 1 ] || error "Not a single-link regular file: $1"
}
for path in "$INSTALL_DIR" "$(dirname "$SERVICE")"; do
    while :; do
        safe "$path"
        [ -d "$path" ] || error "Non-directory parent"
        [ "$path" != / ] || break
        path=$(dirname "$path")
    done
done
LOCK="$INSTALL_DIR/.install.lock"
if [ -e "$LOCK" ] || [ -L "$LOCK" ]; then regular "$LOCK"; fi
exec 9>>"$LOCK"
flock -n 9 || error "Another install/migration/upgrade is running"
for path in "$TARGET" "$CONFIG" "$KEY"; do regular "$path"; done
[ "$(stat -c %a "$TARGET")" = 755 ] || error "Unexpected binary mode"
[ "$(stat -c %a "$CONFIG")" = 600 ] && [ "$(stat -c %a "$KEY")" = 600 ] || error "Config/key must be 0600"
UNIT="$SERVICE"
UMASK_LINE="UMask=0077"
MARKER="$INSTALL_DIR/.enrollment-install"
if [ -L "$SERVICE" ]; then
    [ "$(stat -c %u "$SERVICE")" = 0 ] && [ "$(readlink "$SERVICE")" = "$INSTALL_DIR/swallow-agent.service" ] || error "Unknown service link"
    [ "$(stat -c %a "$INSTALL_DIR")" = 755 ] || error "Unknown migration layout"
    UNIT="$INSTALL_DIR/swallow-agent.service"
    MARKER="$INSTALL_DIR/.token-to-key-complete"
    UMASK_LINE=""
else
    [ "$(stat -c %a "$INSTALL_DIR")" = 700 ] || error "Unknown install layout"
fi
regular "$UNIT"
regular "$MARKER"
[ "$(stat -c %a "$MARKER")" = 600 ] || error "Unsafe layout marker"
if [ -n "$UMASK_LINE" ]; then
    [[ "$(head -n 1 "$MARKER")" == "swallow-enrollment-v1 "* ]] || error "Unknown install marker"
else
    [[ "$(head -n 1 "$MARKER")" == "token-to-key "* ]] || error "Incomplete migration"
fi
# Only canonical key references emitted by install/migration are accepted.
[ "$(grep -c privateKeyPath "$CONFIG")" = 1 ] || error "Unknown key configuration"
LINE=$(grep privateKeyPath "$CONFIG")
case "$LINE" in "privateKeyPath: $KEY"|"privateKeyPath: '$KEY'"|"privateKeyPath: \"$KEY\"") ;; *) error "Non-standard private key path" ;; esac
! grep -Eq "^[[:space:]]*token[[:space:]]*:" "$CONFIG" || error "Token installation requires migration"
[ "$(systemctl show -p LoadState --value swallow-agent.service)" = loaded ] || error "Service is not loaded"
[ "$(systemctl show -p NeedDaemonReload --value swallow-agent.service)" = no ] || error "Pending unit changes"
[ -z "$(systemctl show -p DropInPaths --value swallow-agent.service)" ] || error "Service drop-ins are unsupported"
FRAGMENT=$(systemctl show -p FragmentPath --value swallow-agent.service)
[ "$FRAGMENT" = "$UNIT" ] || [ "$FRAGMENT" = "$SERVICE" ] || error "Unknown service fragment"
WORK=$(mktemp -d "$INSTALL_DIR/.upgrade.XXXXXXXXXX")
TOUCHED=false COMMITTED=false ACTIVE=
stop_confirmed() {
    local result=0 properties active= sub= main= control= job=unknown name value
    timeout -k 5 120 systemctl stop swallow-agent.service </dev/null || result=$?
    if [ "$result" != 0 ]; then
        printf "%s\n" "WARNING: stop swallow-agent.service failed or timed out (exit $result); checking actual service state" >&2
    fi
    if ! properties=$(timeout -k 2 15 systemctl show --all -p ActiveState -p SubState -p MainPID -p ControlPID -p Job swallow-agent.service </dev/null); then
        printf "%s\n" "WARNING: cannot confirm service stopped; refusing binary replacement" >&2
        return 1
    fi
    while IFS== read -r name value; do
        case "$name" in
            ActiveState) active=$value ;;
            SubState) sub=$value ;;
            MainPID) main=$value ;;
            ControlPID) control=$value ;;
            Job) job=$value ;;
        esac
    done <<< "$properties"
    if [[ "$active" = inactive || "$active" = failed ]] && [[ "$sub" = dead || "$sub" = failed ]] && [ "$main" = 0 ] && [ "$control" = 0 ] && [ -z "$job" ]; then
        return 0
    fi
    printf "%s\n" "WARNING: service stop unconfirmed (ActiveState=$active SubState=$sub MainPID=$main ControlPID=$control Job=$job); refusing binary replacement" >&2
    return 1
}
cleanup() {
    local status=$?
    trap "" INT TERM HUP
    trap - EXIT
    if [ "$TOUCHED" = true ] && [ "$COMMITTED" = false ]; then
        if stop_confirmed && cp -p "$WORK/original" "$WORK/restore" && mv -f "$WORK/restore" "$TARGET"; then
            printf "%s\n" "Original binary restored" >&2
            if [ "$ACTIVE" = active ]; then
                systemctl reset-failed swallow-agent.service </dev/null || printf "%s\n" "WARNING: could not reset original service start limit" >&2
                systemctl start swallow-agent.service </dev/null && systemctl is-active --quiet swallow-agent.service </dev/null || printf "%s\n" "WARNING: original service could not be restarted" >&2
            fi
        else
            printf "%s\n" "WARNING: rollback failed; recover binary from $WORK/original" >&2
        fi
        printf "%s\n" "Upgrade failed; recovery backup retained: $WORK" >&2
    elif [ "$COMMITTED" = true ]; then
        rm -f "$WORK/expected-unit"
        printf "%s\n" "Original binary retained for manual recovery: $WORK/original" >&2
    else
        rm -rf -- "$WORK"
    fi
    exit "$status"
}
trap cleanup EXIT
trap "exit 130" INT
trap "exit 143" TERM
trap "exit 129" HUP
{
cat <<EOF
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
EOF
[ -z "$UMASK_LINE" ] || printf "%s\n" "$UMASK_LINE"
cat <<EOF

[Install]
WantedBy=multi-user.target
EOF
} > "$WORK/expected-unit"
cmp -s "$WORK/expected-unit" "$UNIT" || error "Non-standard service unit"
[ -f "$BINARY" ] && [ ! -L "$BINARY" ] || error "Candidate must be a regular file"
install -m 755 "$BINARY" "$WORK/candidate"
HASH=$(sha256sum "$WORK/candidate")
[ "${HASH%% *}" = "${SHA256,,}" ] || error "Candidate checksum mismatch"
CANDIDATE_VERSION=$(timeout -k 2 15 "$WORK/candidate" -version </dev/null) || error "Candidate version probe failed"
[ "$CANDIDATE_VERSION" = "$VERSION" ] || error "Candidate version mismatch"
CURRENT=$(timeout -k 2 15 "$TARGET" -version </dev/null) || error "Cannot identify current version"
[[ "$CURRENT" =~ $SEMVER ]] || error "Unknown current version; manual review required"
# Compare SemVer without integer arithmetic overflow or build metadata ordering.
compare_identifier() {
    ORDER=0
    [ "$1" != "$2" ] || return 0
    if [[ "$1" =~ ^[0-9]+$ && "$2" =~ ^[0-9]+$ ]]; then
        if [ "${#1}" -lt "${#2}" ]; then ORDER=-1; return; fi
        if [ "${#1}" -gt "${#2}" ]; then ORDER=1; return; fi
    elif [[ "$1" =~ ^[0-9]+$ ]]; then ORDER=-1; return
    elif [[ "$2" =~ ^[0-9]+$ ]]; then ORDER=1; return
    fi
    if [[ "$1" < "$2" ]]; then ORDER=-1; else ORDER=1; fi
}
export LC_ALL=C
OLD=${CURRENT%%+*} NEW=${VERSION%%+*}
IFS=. read -r -a OLD_CORE <<< "${OLD%%-*}"
IFS=. read -r -a NEW_CORE <<< "${NEW%%-*}"
OLD_CORE[0]=${OLD_CORE[0]#v}
NEW_CORE[0]=${NEW_CORE[0]#v}
ORDER=0
for index in 0 1 2; do
    compare_identifier "${NEW_CORE[$index]}" "${OLD_CORE[$index]}"
    [ "$ORDER" = 0 ] || break
done
if [ "$ORDER" = 0 ] && [ "$OLD" != "$NEW" ]; then
    if [[ "$OLD" != *-* ]]; then ORDER=-1
    elif [[ "$NEW" != *-* ]]; then ORDER=1
    else
        IFS=. read -r -a OLD_PRE <<< "${OLD#*-}"
        IFS=. read -r -a NEW_PRE <<< "${NEW#*-}"
        for ((index=0; index<${#OLD_PRE[@]} || index<${#NEW_PRE[@]}; index++)); do
            if [ "$index" -ge "${#NEW_PRE[@]}" ]; then ORDER=-1; break; fi
            if [ "$index" -ge "${#OLD_PRE[@]}" ]; then ORDER=1; break; fi
            compare_identifier "${NEW_PRE[$index]}" "${OLD_PRE[$index]}"
            [ "$ORDER" = 0 ] || break
        done
    fi
fi
[ "$ORDER" != -1 ] || error "Downgrade refused; manual review required"
cp -p "$TARGET" "$WORK/original"
ACTIVE=$(systemctl show -p ActiveState --value swallow-agent.service)
case "$ACTIVE" in active|inactive) ;; *) error "Unstable/failed service; repair before upgrading" ;; esac
TOUCHED=true
stop_confirmed || error "Service did not stop safely"
mv -f "$WORK/candidate" "$TARGET"
if [ "$ACTIVE" = active ]; then
    systemctl start swallow-agent.service </dev/null
    systemctl is-active --quiet swallow-agent.service </dev/null
    sleep 1
    systemctl is-active --quiet swallow-agent.service </dev/null
fi
COMMITTED=true
printf "%s\n" "Binary replaced with $VERSION; original $ACTIVE state preserved. Active is NOT proof of authentication/report delivery; verify in Monitor."
