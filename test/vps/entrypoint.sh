#!/usr/bin/env bash
# Install the repository (mounted read-only at /repo) the way
# collector/README.md describes, start GoatCounter and run nginx in the
# foreground. test/run.sh has cross-compiled open-stats into test/.bin/.
set -euo pipefail

case $(uname -m) in
    x86_64|amd64)  arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
install -m 0755 "/repo/test/.bin/open-stats-linux-$arch" /usr/local/bin/open-stats

rm -rf /opt/open-stats
install -d /opt/open-stats
cp -r /repo/collector /repo/projects /opt/open-stats/
install -m 0644 /opt/open-stats/collector/nginx/stats.irq.dk.conf /etc/nginx/sites-available/stats.irq.dk.conf
ln -sf /etc/nginx/sites-available/stats.irq.dk.conf /etc/nginx/sites-enabled/stats.irq.dk.conf
cp /opt/open-stats/collector/systemd/*.service /opt/open-stats/collector/systemd/*.timer /etc/systemd/system/

: >/var/log/goatcounter.log
chown goatcounter:goatcounter /var/log/goatcounter.log
gcctl start

nginx -t
exec nginx -g 'daemon off;'
