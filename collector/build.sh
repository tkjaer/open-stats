#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Build the open-stats binary for the server: static, for linux/amd64 and
# linux/arm64, into dist/ with a SHA256SUMS file. The build is reproducible:
# the same commit and Go version give the same bytes, so anyone can rebuild
# and compare the checksums.
#
#   collector/build.sh
set -euo pipefail
cd "$(dirname "$0")/.."

version=$(git describe --tags --always --dirty 2>/dev/null || echo unknown)
rm -rf dist
mkdir -p dist
for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -buildvcs=false \
        -ldflags "-s -w -buildid= -X main.buildVersion=$version" \
        -o "dist/open-stats-linux-$arch" ./cmd/open-stats
done
sum=(sha256sum)
if ! command -v sha256sum >/dev/null; then sum=(shasum -a 256); fi
(cd dist && "${sum[@]}" open-stats-linux-*) >dist/SHA256SUMS
echo "open-stats $version, $(go env GOVERSION):"
cat dist/SHA256SUMS
