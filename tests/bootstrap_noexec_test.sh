#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TEST=$(mktemp -d /var/tmp/swallow-noexec-test.XXXXXXXXXX)
PROBE=$(mktemp -d /tmp/swallow-noexec-test.XXXXXXXXXX)
trap 'rm -rf -- "$TEST" "$PROBE"' EXIT
printf '#!/bin/sh\nexit 0\n' > "$PROBE/probe"
chmod 700 "$PROBE/probe"
if "$PROBE/probe" </dev/null 2>/dev/null; then
    echo 'This test requires /tmp mounted noexec' >&2
    exit 1
fi
mkdir "$TEST/bin" "$TEST/assets"
export FIXTURE="$TEST/assets" RESULT="$TEST/result" CREATED="$TEST/created"
export REAL_MKTEMP=$(command -v mktemp)
cat > "$TEST/bin/mktemp" <<'EOF'
#!/bin/bash
set -euo pipefail
dir=$("$REAL_MKTEMP" "$@")
[ "$(stat -c %a "$dir")" = 700 ]
printf '%s\n' "$dir" >> "$CREATED"
printf '%s\n' "$dir"
EOF
cat > "$TEST/bin/curl" <<'EOF'
#!/bin/bash
set -euo pipefail
while [ "$#" -gt 0 ]; do
    case "$1" in --output) OUT=$2; shift 2 ;; *) URL=$1; shift ;; esac
done
cp "$FIXTURE/${URL##*/}" "$OUT"
EOF
cat > "$FIXTURE/install.sh" <<'EOF'
#!/bin/bash
set -euo pipefail
[ "$1" = --binary ]
case "$2" in /var/tmp/swallow-bootstrap.*/*|/opt/swallow-bootstrap.*/*) ;; *) exit 1 ;; esac
[ "$(stat -c %a "${2%/*}")" = 700 ]
"$2"
[ "$(cat)" = noexec-fixture-token ]
printf '%s\n' "$2" > "$RESULT"
EOF
printf '#!/bin/sh\nexit 0\n' > "$FIXTURE/swallow-agent-linux-amd64"
cp "$FIXTURE/swallow-agent-linux-amd64" "$FIXTURE/swallow-agent-linux-arm64"
(cd "$FIXTURE"; sha256sum install.sh swallow-agent-* > checksums.txt)
chmod 700 "$TEST/bin/"*
export PATH="$TEST/bin:$PATH"
for preference in default explicit; do
    : > "$CREATED"
    if [ "$preference" = default ]; then unset TMPDIR; else export TMPDIR=/tmp; fi
    printf %s noexec-fixture-token | bash "$ROOT/bootstrap.sh" --version v9.9.9-test --enrollment-token-stdin
    grep -q '^/tmp/swallow-bootstrap.' "$CREATED"
    [ "$(wc -l < "$CREATED")" -ge 2 ]
    while IFS= read -r dir; do [ ! -e "$dir" ]; done < "$CREATED"
    printf 'real noexec fallback (%s), binary executed, token preserved, cleanup passed: %s\n' "$preference" "$(cat "$RESULT")"
done
