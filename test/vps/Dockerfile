# A stand-in for the VPS: Debian 12 with its own nginx, GoatCounter from the
# pinned release and git; open-stats is installed from test/.bin by the
# entrypoint. Test use only.
FROM debian:bookworm-slim

RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      nginx git openssh-client curl ca-certificates openssl procps \
      systemd \
 && rm -rf /var/lib/apt/lists/*

COPY collector/goatcounter.version collector/install-goatcounter.sh /tmp/gc/
RUN bash /tmp/gc/install-goatcounter.sh /usr/local/bin/goatcounter && rm -rf /tmp/gc

RUN adduser --system --group --home /var/lib/goatcounter --no-create-home goatcounter \
 && adduser --system --group --home /var/lib/open-stats --no-create-home open-stats \
 && install -d -o goatcounter -g goatcounter -m 0700 /var/lib/goatcounter \
 && install -d -o open-stats -g open-stats -m 0700 /var/lib/open-stats \
 && rm -f /etc/nginx/sites-enabled/default \
 && install -d -m 0755 /etc/ssl/stats.irq.dk \
 && openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 3650 \
      -subj /CN=stats.irq.dk -addext subjectAltName=DNS:stats.irq.dk \
      -keyout /etc/ssl/stats.irq.dk/privkey.pem -out /etc/ssl/stats.irq.dk/fullchain.pem 2>/dev/null

COPY test/vps/test-realip.conf /etc/nginx/conf.d/zz-open-stats-test-realip.conf
COPY test/vps/other-site.conf /etc/nginx/sites-enabled/zz-other-site.conf
# Log everything down to info level, so a request-related line from
# stats.irq.dk would show up in the test (limit_req rejections are at info).
RUN sed -i "s|^error_log /var/log/nginx/error.log;$|error_log /var/log/nginx/error.log info;|" /etc/nginx/nginx.conf \
 && grep -q "^error_log /var/log/nginx/error.log info;" /etc/nginx/nginx.conf
COPY test/vps/entrypoint.sh test/vps/gcctl /usr/local/sbin/
ENTRYPOINT ["/usr/local/sbin/entrypoint.sh"]
