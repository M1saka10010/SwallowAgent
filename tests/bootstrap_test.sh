#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TEST=$(mktemp -d)
trap 'rm -rf -- "$TEST"' EXIT
mkdir "$TEST/bin" "$TEST/assets" "$TEST/tmp" "$TEST/fallback" "$TEST/opt"
export TEST REAL_MKTEMP=$(command -v mktemp) REAL_CHMOD=$(command -v chmod)
export FIXTURE="$TEST/assets" RESULT="$TEST/result" TMPDIR="$TEST/tmp"
export PATH="$TEST/bin:$PATH"
cat > "$TEST/bin/mktemp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
template=${@: -1}
printf '%s\n' "$template" >> "$TEST/candidates"
case "$template" in
    /var/tmp/*) template="$TEST/fallback/${template##*/}" ;;
    /opt/*) template="$TEST/opt/${template##*/}" ;;
esac
[ "${DENY_CREATE:-}" != all ] || exit 1
dir=$("$REAL_MKTEMP" -d -- "$template")
[ "$(stat -c %a "$dir")" = 700 ]
printf '%s\n' "$dir" >> "$TEST/created"
printf '%s\n' "$dir"
EOF
cat > "$TEST/bin/chmod" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
"$REAL_CHMOD" "$@"
file=${@: -1}
if [[ "$file" == */probe ]]; then
    case "${DENY_EXEC:-}:$file" in
        all:*|first:"$TEST/tmp/"*|two:"$TEST/tmp/"*|two:"$TEST/fallback/"*)
            "$REAL_CHMOD" 600 "$file" ;;
    esac
fi
EOF
cat > "$TEST/bin/uname" <<'EOF'
#!/usr/bin/env bash
case "$1" in -s) echo "${MOCK_OS:-Linux}" ;; -m) echo "${MOCK_ARCH:-x86_64}" ;; esac
EOF
cat > "$TEST/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ " $* " == *" --proto =https "* && " $* " == *" --proto-redir =https "* ]]
echo download >> "$TEST/downloads"
[[ "$1" == --disable ]]
OUT=''
while [ "$#" -gt 0 ]; do
    case "$1" in --output) OUT=$2; shift 2 ;; *) URL=$1; shift ;; esac
done
[[ "$URL" == "${EXPECT_PROXY:-}"https://github.com/M1saka10010/SwallowAgent/releases/download/"${EXPECT_VERSION:-v9.9.9-test}"/* ]]
ASSET=${URL##*/}
[ "${FAIL_ASSET:-}" != "$ASSET" ] || exit 22
if [ "${SIGNAL_STAGE:-}" = download ]; then source "$TEST/signal-child"; fi
cp "$FIXTURE/$ASSET" "$OUT"
if [ "${CORRUPT_ASSET:-}" = "$ASSET" ]; then echo corrupt >> "$OUT"; fi
EOF
cat > "$FIXTURE/install.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "$1" = --binary ] && [ -x "$2" ]
"$2"
[[ "$2" == *"swallow-agent-linux-${EXPECT_ARCH:-amd64}" ]]
TOKEN=$(cat)
[ "$TOKEN" = secret-fixture ]
printf '%s\n' "${0##*/}" > "$RESULT"
if [ "${SIGNAL_STAGE:-}" = script ]; then source "$TEST/signal-child"; fi
exit "${CHILD_EXIT:-0}"
EOF
cp "$FIXTURE/install.sh" "$FIXTURE/migrate-token-to-key.sh"
cat > "$FIXTURE/upgrade.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "$#" = 6 ] && [ "$1" = --binary ] && [ "$3" = --version ] && [ "$4" = v9.9.9-test ] && [ "$5" = --sha256 ]
HASH=$(sha256sum "$2")
[ "${HASH%% *}" = "$6" ]
echo upgrade.sh > "$RESULT"
EOF
printf '#!/bin/sh\nexit 0\n' > "$FIXTURE/swallow-agent-linux-amd64"
cp "$FIXTURE/swallow-agent-linux-amd64" "$FIXTURE/swallow-agent-linux-arm64"
(cd "$FIXTURE"; sha256sum install.sh migrate-token-to-key.sh upgrade.sh swallow-agent-* > checksums.txt)
cp "$FIXTURE/checksums.txt" "$TEST/checksums"
chmod +x "$TEST/bin/"*
run() {
    local want=$1 status=0
    shift
    rm -f "$RESULT"
    : > "$TEST/downloads"
    : > "$TEST/candidates"
    : > "$TEST/created"
    printf %s secret-fixture | bash "$ROOT/bootstrap.sh" "$@" >"$TEST/log" 2>&1 || status=$?
    [ "$status" -eq "$want" ] || { cat "$TEST/log"; echo "unexpected status $status != $want"; exit 1; }
    ! grep -q secret-fixture "$TEST/log"
    [ -z "$(ls -A "$TMPDIR")" ]
    [ -z "$(ls -A "$TEST/fallback")" ]
    [ -z "$(ls -A "$TEST/opt")" ]
    while IFS= read -r dir; do [ ! -e "$dir" ]; done < "$TEST/created"
}
ARGS=(--version v9.9.9-test --enrollment-token-stdin --report-url wss://example.com/report)
run 0 --version v9.9.9-test --mode upgrade
grep -qx upgrade.sh "$RESULT"
EXPECT_PROXY=https://proxy.example/prefix/ run 0 --version v9.9.9-test --mode upgrade --github-proxy https://proxy.example/prefix/
EXPECT_PROXY=https://proxy.example/ run 0 "${ARGS[@]}" --github-proxy https://proxy.example
for proxy in http://proxy.example https://user@proxy.example https://proxy.example/?q=x https://proxy.example/#x https://proxy.example/../x; do
    run 1 --version v9.9.9-test --mode upgrade --github-proxy "$proxy"
    [ ! -s "$TEST/downloads" ]
done
run 1 --version v9.9.9-test --mode upgrade --enrollment-token-stdin
run 1 --version v9.9.9-test --mode upgrade --report-url wss://example.com/report
CORRUPT_ASSET=upgrade.sh run 1 --version v9.9.9-test --mode upgrade
FAIL_ASSET=upgrade.sh run 1 --version v9.9.9-test --mode upgrade
run 0 "${ARGS[@]}"
grep -qx install.sh "$RESULT"
DENY_EXEC=first run 0 "${ARGS[@]}"
[ "$(wc -l < "$TEST/candidates")" -eq 2 ]
grep -q '^/var/tmp/' "$TEST/candidates"
DENY_EXEC=two run 0 "${ARGS[@]}"
[ "$(wc -l < "$TEST/candidates")" -eq 3 ]
grep -q '^/opt/' "$TEST/candidates"
for failure in exec create; do
    if [ "$failure" = exec ]; then DENY_EXEC=all run 1 "${ARGS[@]}"
    else DENY_CREATE=all run 1 "${ARGS[@]}"; fi
    [ ! -e "$RESULT" ]
    [ ! -s "$TEST/downloads" ]
    [ "$(wc -l < "$TEST/candidates")" -eq 3 ]
    grep -q 'No writable executable temporary directory' "$TEST/log"
done
DENY_EXEC=first CORRUPT_ASSET=install.sh run 1 "${ARGS[@]}"
DENY_EXEC=first CHILD_EXIT=37 run 37 "${ARGS[@]}"
MOCK_ARCH=aarch64 EXPECT_ARCH=arm64 run 0 "${ARGS[@]}" --mode migrate-token-to-key
grep -qx migrate-token-to-key.sh "$RESULT"
CHILD_EXIT=37 run 37 "${ARGS[@]}"
for asset in checksums.txt swallow-agent-linux-amd64 install.sh; do
    FAIL_ASSET=$asset run 1 "${ARGS[@]}"
    [ ! -e "$RESULT" ]
done
for asset in swallow-agent-linux-amd64 install.sh; do
    CORRUPT_ASSET=$asset run 1 "${ARGS[@]}"
    [ ! -e "$RESULT" ]
done
cat "$TEST/checksums" >> "$FIXTURE/checksums.txt"
run 1 "${ARGS[@]}"
[ ! -e "$RESULT" ]
echo '' > "$FIXTURE/checksums.txt"
run 1 "${ARGS[@]}"
[ ! -e "$RESULT" ]
cp "$TEST/checksums" "$FIXTURE/checksums.txt"
MOCK_ARCH=riscv64 run 1 "${ARGS[@]}"
MOCK_OS=Darwin run 1 "${ARGS[@]}"
run 1 --enrollment-token-stdin
run 1 --version latest --enrollment-token-stdin
for version in v01.2.3 v1.2.3-01 v1.2.3-a..b v1.2.3+; do
    run 1 --version "$version" --enrollment-token-stdin
done
EXPECT_VERSION=v1.2.3-rc.1+build.42 run 0 --version v1.2.3-rc.1+build.42 --enrollment-token-stdin
run 1 "${ARGS[@]}" --mode invalid
run 1 --version v9.9.9-test
cat > "$TEST/signal-child" <<'EOF'
trap 'kill -TERM "$PPID"; echo TERM >> "$RESULT"; exit 143' TERM
echo "$BASHPID" > "$TEST/child-pid"
exec 4<>"$TEST/idle"
echo ready > "$TEST/ready"
read -r -t 3 line <&4 || true
echo expired >> "$RESULT"
exit 99
EOF
python3 - "$ROOT/bootstrap.sh" <<'PY'
import os
import signal
import subprocess
import sys
from pathlib import Path

root = Path(os.environ['TEST'])
source = Path(sys.argv[1]).read_text()
for stage in ['download', 'script']:
    for window in ['before-launch', 'before-register', 'waiting']:
        for sig, expected in [('INT', 130), ('TERM', 143), ('HUP', 129)]:
            for name in ['ready', 'idle']:
                path = root / name
                path.unlink(missing_ok=True)
                os.mkfifo(path)
            Path(os.environ['RESULT']).unlink(missing_ok=True)
            (root / 'child-pid').unlink(missing_ok=True)
            lines = source.splitlines()
            launch = next(i for i, line in enumerate(lines) if ('curl --disable' if stage == 'download' else 'bash "$WORK/$SCRIPT"') in line)
            send = f'kill -{sig} "$$"; kill -TERM "$$"'
            barrier = 'read -r -t 3 ready < "$TEST/ready"'
            if window == 'before-launch':
                lines.insert(launch + 1, barrier)
                lines.insert(launch, send)
            elif window == 'before-register':
                lines.insert(launch + 1, barrier + '; ' + send)
            else:
                lines.insert(launch + 4, barrier + '; ' + send)
            script = root / 'instrumented.sh'
            script.write_text('\n'.join(lines) + '\n')
            child = subprocess.Popen(
                ['bash', str(script), '--version', 'v9.9.9-test', '--enrollment-token-stdin'],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                start_new_session=True, env={**os.environ, 'SIGNAL_STAGE': stage},
            )
            try:
                out, err = child.communicate(b'secret-fixture', timeout=5)
                diagnostic = (stage, window, sig, child.returncode, out, err)
                assert child.returncode == expected, diagnostic
                result = Path(os.environ['RESULT']).read_text()
                assert 'TERM' in result and 'expired' not in result, (diagnostic, result)
                pid = int((root / 'child-pid').read_text())
                try:
                    os.kill(pid, 0)
                except ProcessLookupError:
                    pass
                else:
                    raise AssertionError(('unreaped child', pid, diagnostic))
                assert not os.listdir(os.environ['TMPDIR']), diagnostic
            except subprocess.TimeoutExpired as error:
                raise AssertionError(('timeout', stage, window, sig, error.output, error.stderr)) from error
            finally:
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                child.communicate(timeout=5)
print('bootstrap mock tests and 18 signal-window cases passed')
PY
