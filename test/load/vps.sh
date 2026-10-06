#!/usr/bin/env bash
# Runs inside the server stand-in for test/load/run.sh. Test use only.
#   vps.sh setup                 cgroups with the unit's limits, one nginx worker, rejections logged
#   vps.sh sample                print CPU and memory counters once a second
#   vps.sh report START END      CPU and memory between two Unix times
#   vps.sh counted               page loads GoatCounter has counted today
set -euo pipefail
cg=/sys/fs/cgroup
samples=/tmp/load-samples

case ${1:-} in
setup)
    OPEN_STATS_ADMIN_PASSWORD=load-$RANDOM$RANDOM open-stats configure \
        -admin-email admin@example.invalid how-the-internet-works >/dev/null

    # As on a one-core server (worker_processes auto counts all host CPUs).
    sed -i 's/^worker_processes .*/worker_processes 1;/' /etc/nginx/nginx.conf
    # Measurement only: log rate-limit rejections to count what got through.
    sed -i 's#error_log /dev/null crit;#error_log /var/log/nginx/openstats-load.log info;#' \
        /etc/nginx/sites-available/stats.irq.dk.conf
    nginx -t 2>/dev/null
    nginx -s reload
    sleep 2

    mkdir -p $cg/rest $cg/nginx $cg/goatcounter
    for _ in 1 2 3 4 5; do # processes started meanwhile (this script) move too
        while read -r p; do echo "$p" >$cg/rest/cgroup.procs 2>/dev/null || true; done <$cg/cgroup.procs
    done
    echo "+cpu +memory" >$cg/cgroup.subtree_control
    # Only the worker: docker exec (the health check, this script) joins the
    # cgroup of PID 1, the nginx master, which does no request work.
    for p in $(pgrep -f "nginx: worker"); do echo "$p" >$cg/nginx/cgroup.procs; done
    # GoatCounter's unit: CPUWeight=20, MemoryHigh=120M, MemoryMax=150M.
    echo 20 >$cg/goatcounter/cpu.weight
    echo 120M >$cg/goatcounter/memory.high
    echo 150M >$cg/goatcounter/memory.max
    gcctl stop
    sh -c "echo \$\$ >$cg/goatcounter/cgroup.procs; exec gcctl start"
    echo "nginx: $(pgrep -c -f "nginx: worker") worker; GoatCounter in $(cat /proc/"$(cat /run/goatcounter.pid)"/cgroup)"
    ;;
sample)
    : >$samples
    while true; do
        echo "$(date +%s.%N) $(awk '/^usage_usec/{print $2}' $cg/goatcounter/cpu.stat)" \
            "$(awk '/^usage_usec/{print $2}' $cg/nginx/cpu.stat)" \
            "$(cat $cg/goatcounter/memory.current)" \
            "$(awk '/^VmRSS/{print $2}' /proc/"$(cat /run/goatcounter.pid)"/status)" >>$samples
        sleep 1
    done
    ;;
report)
    start=$2 end=$3
    awk -v s="$start" -v e="$end" '
        $1 >= s && $1 <= e {
            if (n++) {
                dt = $1 - t
                g = ($2 - gu) / 1e4 / dt; x = ($3 - nu) / 1e4 / dt
                if (g > gmax) gmax = g
                if (x > nmax) nmax = x
            } else { t0 = $1; g0 = $2; n0 = $3 }
            t = $1; gu = $2; nu = $3
            if ($4 > mem) mem = $4
            if ($5 > rss) rss = $5
        }
        END {
            d = t - t0
            printf "  GoatCounter CPU: %.1f%% of one core on average, %.1f%% at most in one second\n", (gu - g0) / 1e4 / d, gmax
            printf "  nginx CPU:       %.1f%% of one core on average, %.1f%% at most in one second\n", (nu - n0) / 1e4 / d, nmax
            printf "  GoatCounter memory: RSS at most %.1f MB, cgroup (RSS + page cache) at most %.1f MB\n", rss / 1024, mem / 1048576
        }' $samples
    ;;
counted)
    tok=$(sed -n 's/^GOATCOUNTER_TOKEN=//p' /etc/open-stats/env)
    day=$(date -u +%F)
    curl -fsS -H "Authorization: Bearer $tok" -H "Host: how-the-internet-works.localhost" \
        "http://127.0.0.1:8081/api/v0/stats/total?start=${day}T00:00:00Z&end=${day}T23:59:59Z" |
        tr -d ' \n' | sed 's/.*"total":\([0-9]*\).*/\1/'
    ;;
rejected)
    grep -c 'limiting requests' /var/log/nginx/openstats-load.log || true
    ;;
logged)
    # Kinds of message only, counted; info-level ones come from clients closing early.
    grep -v -e 'limiting requests' -e 'closed keepalive connection' /var/log/nginx/openstats-load.log |
        sed -E 's/^[^[]*\[([a-z]+)\] [0-9#]+: (\*[0-9]+ )?/\1: /; s/ \(.*//; s/,.*//' | sort | uniq -c || true
    ;;
*)
    echo "usage: vps.sh setup|sample|report START END|counted|rejected|logged" >&2
    exit 2
    ;;
esac
