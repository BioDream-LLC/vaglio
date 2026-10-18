#!/bin/sh
# run.sh - runs the Inferno Da Vinci PAS Server test kit (davinci-pas-test-kit v0.15.2, PAS Server v2.2.1) against
# Perfuse with Vaglio as its decision service (-pas-decider-url), and says how many of the kit's items Vaglio decided.
#
# Vaglio is not a FHIR server: PAS conformance is the PAS server's. What this run shows is that putting Vaglio behind a
# conforming PAS server keeps it conforming, with Vaglio deciding what its policies cover (here surgery.vaglio, the
# kit's X12 service type 2, which its rules leave to a reviewer) and Perfuse's rules the rest.
#
# Uses Perfuse's own Inferno harness (its conformance/inferno, Apache-2.0), copied to a work directory and run unchanged
# but for the extra flags. Needs Docker, Python 3, openssl, git, a Perfuse binary and its source tree:
#
#   PERFUSE=/path/to/perfuse PERFUSE_SRC=/path/to/perfuse-source conformance/inferno/run.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
: "${PERFUSE:?set PERFUSE to a Perfuse binary}" "${PERFUSE_SRC:?set PERFUSE_SRC to the Perfuse source tree}"
work=${WORK:-"$here/.work"}
port=18740
mkdir -p "$work"
rm -rf "$work/harness" "$work/vaglio"
cp -R "$PERFUSE_SRC/conformance/inferno" "$work/harness"
rm -rf "$work/harness/.work"

# Vaglio, on loopback: only the test policy, a token Perfuse sends, a fresh data directory.
(cd "$root" && go build -o "$work/vaglio-bin" ./cmd/vaglio)
mkdir -m 700 "$work/vaglio" "$work/vaglio/policies"
cp "$here/surgery.vaglio" "$work/vaglio/policies/"
VAGLIO_INFERNO_TOKEN=inferno-run-token
export VAGLIO_INFERNO_TOKEN
pkill -f "vaglio-bin serve .*127.0.0.1:$port" 2>/dev/null || true
"$work/vaglio-bin" serve -data "$work/vaglio/data" -policies "$work/vaglio/policies" -library=false \
  -addr "127.0.0.1:$port" -token-env VAGLIO_INFERNO_TOKEN > "$work/vaglio/serve.log" 2>&1 &
vpid=$!
trap 'kill $vpid 2>/dev/null || true' EXIT
i=0
until [ -f "$work/vaglio/data/tls/ca.crt" ] && curl -s --cacert "$work/vaglio/data/tls/ca.crt" "https://127.0.0.1:$port/api/me" >/dev/null 2>&1; do
  i=$((i + 1)); [ $i -lt 30 ] || { echo "run.sh: Vaglio did not start" >&2; tail -5 "$work/vaglio/serve.log" >&2; exit 1; }
  sleep 1
done

"$work/harness/kit.sh" pas
PERFUSE="$PERFUSE" "$work/harness/serve.sh" pas \
  -pas-decider-url "https://127.0.0.1:$port/api/pas/decide" \
  -pas-decider-ca "$work/vaglio/data/tls/ca.crt" -pas-decider-token-env VAGLIO_INFERNO_TOKEN
PERFUSE_TOKEN=$(cat "$work/harness/.work/pas/token") python3 "$work/harness/pas/run.py" | tee "$work/results.txt"

decided=$(grep -c '"action":"case.opened"' "$work/vaglio/data/audit.jsonl" || true)
echo "Vaglio decided $decided item(s) of the kit's requests; Perfuse's rules decided the rest." | tee -a "$work/results.txt"
