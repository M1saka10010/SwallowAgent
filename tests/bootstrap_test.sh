#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TEST=$(mktemp -d)
trap 'rm -rf -- "$TEST"' EXIT
mkdir "$TEST/bin" "$TEST/assets" "$TEST/tmp"
export FIXTURE="$TEST/assets" RESULT="$TEST/result" TMPDIR="$TEST/tmp"
export PATH="$TEST/bin:$PATH"
cat > "$TEST/bin/uname" <<'EOF'
#!/usr/bin/env bash
case "$1" in -s) echo "${MOCK_OS:-Linux}" ;; -m) echo "${MOCK_ARCH:-x86_64}" ;; esac
EOF
cat > "$TEST/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ " $* " == *" --proto =https "* && " $* " == *" --proto-redir =https "* ]]
[[ "$1" == --disable ]]
OUT=''
while [ "$#" -gt 0 ]; do
    case "$1" in --output) OUT=$2; shift 2 ;; *) URL=$1; shift ;; esac
done
[[ "$URL" == https://github.com/M1saka10010/SwallowAgent/releases/download/"${EXPECT_VERSION:-v9.9.9-test}"/* ]]
ASSET=${URL##*/}
[ "${FAIL_ASSET:-}" != "$ASSET" ] || exit 22
cp "$FIXTURE/$ASSET" "$OUT"
if [ "${CORRUPT_ASSET:-}" = "$ASSET" ]; then echo corrupt >> "$OUT"; fi
EOF
cat > "$FIXTURE/install.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "$1" = --binary ] && [ -x "$2" ]
[[ "$2" == *"swallow-agent-linux-${EXPECT_ARCH:-amd64}" ]]
TOKEN=$(cat)
[ "$TOKEN" = secret-fixture ]
printf '%s\n' "${0##*/}" > "$RESULT"
if [ "${WAIT_SIGNAL:-}" = yes ]; then
    trap 'echo TERM >> "$RESULT"; exit 143' TERM
    echo ready >> "$RESULT"
    while :; do sleep 0.1; done
fi
exit "${CHILD_EXIT:-0}"
EOF
cp "$FIXTURE/install.sh" "$FIXTURE/migrate-token-to-key.sh"
printf 'mock binary\n' > "$FIXTURE/swallow-agent-linux-amd64"
cp "$FIXTURE/swallow-agent-linux-amd64" "$FIXTURE/swallow-agent-linux-arm64"
(cd "$FIXTURE"; sha256sum install.sh migrate-token-to-key.sh swallow-agent-* > checksums.txt)
cp "$FIXTURE/checksums.txt" "$TEST/checksums"
chmod +x "$TEST/bin/"*
run() {
    local want=$1 status=0
    shift
    rm -f "$RESULT"
    printf %s secret-fixture | bash "$ROOT/bootstrap.sh" "$@" >"$TEST/log" 2>&1 || status=$?
    [ "$status" -eq "$want" ] || { cat "$TEST/log"; echo "unexpected status $status != $want"; exit 1; }
    ! grep -q secret-fixture "$TEST/log"
    [ -z "$(ls -A "$TMPDIR")" ]
}
ARGS=(--version v9.9.9-test --enrollment-token-stdin --report-url wss://example.com/report)
run 0 "${ARGS[@]}"
grep -qx install.sh "$RESULT"
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
rm -f "$RESULT"
printf %s secret-fixture > "$TEST/input"
WAIT_SIGNAL=yes bash "$ROOT/bootstrap.sh" "${ARGS[@]}" <"$TEST/input" >"$TEST/log" 2>&1 &
PID=$!
for ((i=0; i<100; i++)); do
    if [ -f "$RESULT" ] && grep -q ready "$RESULT"; then break; fi
    sleep 0.05
done
grep -q ready "$RESULT"
kill -TERM "$PID"
STATUS=0
wait "$PID" || STATUS=$?
[ "$STATUS" -eq 143 ]
grep -q TERM "$RESULT"
[ -z "$(ls -A "$TMPDIR")" ]
echo 'bootstrap mock tests passed'
python3 - "$ROOT/bootstrap.sh" <<'PY'
import os
import signal
import subprocess
import sys
import time

for sig, expected in [(signal.SIGINT, 130), (signal.SIGHUP, 129)]:
    os.unlink(os.environ['RESULT'])
    child = subprocess.Popen(
        ['bash', sys.argv[1], '--version', 'v9.9.9-test', '--enrollment-token-stdin'],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        env={**os.environ, 'WAIT_SIGNAL': 'yes'},
    )
    try:
        child.stdin.write(b'secret-fixture')
        child.stdin.close()
        for _ in range(100):
            try:
                with open(os.environ['RESULT']) as result:
                    if 'ready' in result.read():
                        break
            except FileNotFoundError:
                pass
            time.sleep(0.05)
        else:
            raise AssertionError('child not ready')
        child.send_signal(sig)
        assert child.wait(timeout=5) == expected
        with open(os.environ['RESULT']) as result:
            assert 'TERM' in result.read()
        assert not os.listdir(os.environ['TMPDIR'])
    finally:
        if child.poll() is None:
            child.kill()
            child.wait()
print('bootstrap INT/HUP forwarding passed')
PY
