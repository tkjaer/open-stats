#!/usr/bin/env bash
# Measures what counting costs on a one-core server: the server stand-in from
# test/docker-compose.yml with one CPU and GoatCounter in a cgroup with its
# unit's memory limits, under a steady 20 counts a second from different
# public IP addresses, then a burst of 200, then 60 a second (over nginx's
# cap). Needs Go and Docker; takes about 4 minutes. Not part of test/run.sh.
#   test/load/run.sh             RATE=20 SECONDS_STEADY=150 BURST=200 OVER=60
set -euo pipefail
cd "$(dirname "$0")/../.."

rate=${RATE:-20} steady=${SECONDS_STEADY:-150} burst=${BURST:-200} over=${OVER:-60}
compose=(docker compose -f test/docker-compose.yml -f test/load/docker-compose.yml)
trap '[[ ${KEEP:-} == 1 ]] || "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true' EXIT

case $(docker version --format '{{.Server.Arch}}') in
    amd64|x86_64)  arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) echo "unsupported Docker architecture" >&2; exit 1 ;;
esac
mkdir -p test/.bin
CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -o "test/.bin/open-stats-linux-$arch" ./cmd/open-stats
CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -o "test/.bin/load-linux-$arch" ./test/load

"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
"${compose[@]}" build --quiet
"${compose[@]}" up -d --wait
vps() { "${compose[@]}" exec -T vps /repo/test/load/vps.sh "$@"; }
vps setup
"${compose[@]}" exec -d vps /repo/test/load/vps.sh sample
sleep 15 # idle baseline

field() { perl -ne 'print "$1\n" if /"'"$1"'":([0-9.]+)/' <<<"$2"; }
report() {
    local start end
    start=$(field start "$1") end=$(field end "$1")
    echo "$1" | perl -pe 's/^/  /'
    if (( end < start + 3 )); then
        # A burst: the second before it to three seconds after it.
        start=$((start - 1)) end=$((start + 4))
        sleep 4
    fi
    vps report "$start" "$end"
}

now=$(date +%s)
sleep 10
echo "== idle"
vps report "$now" "$(date +%s)"

# The load tool exits 1 if any request got something other than an empty 204;
# report the measurements anyway, then fail at the end.
failed=0
load() { "${compose[@]}" exec -T load "/repo/test/.bin/load-linux-$arch" "$@"; }
phase() { grep "\"phase\":\"$1\"" <<<"$2" || true; }

out=$(load -rate "$rate" -duration "${steady}s" -burst "$burst") || failed=1
steady_out=$(phase steady "$out") burst_out=$(phase burst "$out")
if [[ -z $steady_out ]]; then echo "no output from the load tool" >&2; exit 1; fi
if (( burst > 0 )); then
    echo "== steady $rate/s for ${steady}s, then a burst of $burst"
else
    echo "== steady $rate/s for ${steady}s"
fi
report "$steady_out"
sent=$(field sent "$steady_out")
if [[ -n $burst_out ]]; then
    echo "== burst"
    report "$burst_out"
    sent=$(( sent + $(field sent "$burst_out") ))
fi

sleep 12
over_out=$(load -rate "$over" -duration 30s -burst 0 -phase "over-the-cap") || failed=1
echo "== $over/s for 30 s (over nginx's cap)"
report "$over_out"
sent=$(( sent + $(field sent "$over_out") ))

echo "== accounting"
sleep 15 # GoatCounter stores counts every 10 s
rejected=$(vps rejected)
counted=$(vps counted)
echo "  sent $sent, rejected by nginx's cap $rejected, so let through $((sent - rejected)); GoatCounter counted $counted"
other=$(vps logged)
if [[ -n $other ]]; then
    echo "  other nginx messages (kind, count):"
    echo "$other"
fi
if (( counted != sent - rejected )); then
    echo "  MISMATCH" >&2
    exit 1
fi
if (( failed )); then
    echo "  not every request got an empty 204" >&2
    exit 1
fi
