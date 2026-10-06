#!/usr/bin/env bash
# Runs all tests: Go unit tests on this machine, then the end-to-end checks
# against nginx and GoatCounter in a container (see test/README.md).
# Needs Go and Docker.
#   test/run.sh          build, test, tear down
#   KEEP=1 test/run.sh   leave the container running afterwards
set -euo pipefail
cd "$(dirname "$0")/.."

compose=(docker compose -f test/docker-compose.yml --profile systemd)
# Go with cgo, for building the end-to-end tests with the race detector.
golang=golang:1.26-bookworm@sha256:dc9ad6c05acc7a88e5b71bde60a5fe3bd4b9f0db209011711b464107438a8107
cleanup() {
    if [[ ${KEEP:-} != 1 ]]; then "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

echo "== unit tests"
unformatted=$(gofmt -l .)
if [[ -n $unformatted ]]; then
    echo "not gofmt'ed: $unformatted" >&2
    exit 1
fi
go vet ./...
go vet -tags e2e ./test/e2e/
go test -race ./...

case $(docker version --format '{{.Server.Arch}}') in
    amd64|x86_64)  arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) echo "unsupported Docker architecture" >&2; exit 1 ;;
esac
echo "== building for linux/$arch"
mkdir -p test/.bin
export CGO_ENABLED=0 GOOS=linux GOARCH=$arch
go build -trimpath -o "test/.bin/open-stats-linux-$arch" ./cmd/open-stats
unset CGO_ENABLED GOOS GOARCH
# The end-to-end tests run with the race detector, which needs cgo, so they
# are built in a Go container for the test image's Debian 12.
docker run --rm --platform "linux/$arch" -v "$PWD:/src:ro" -v open-stats-test-gomod:/go/pkg/mod \
    -v open-stats-test-gocache:/root/.cache/go-build -v "$PWD/test/.bin:/out" -w /src "$golang" \
    go test -c -race -tags e2e -o "/out/e2e-linux-$arch" ./test/e2e/

echo "== end-to-end checks in the container"
"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
"${compose[@]}" build --quiet
"${compose[@]}" up -d --wait
"${compose[@]}" exec -T vps "/repo/test/.bin/e2e-linux-$arch" -test.v -test.timeout 15m

echo "== the systemd units, under systemd"
"${compose[@]}" exec -T systemd "/repo/test/.bin/e2e-linux-$arch" -test.v -test.timeout 10m -test.run TestSystemd
