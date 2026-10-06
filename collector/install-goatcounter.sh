#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Download the pinned GoatCounter release, check its sha256 and install it.
#
#   bash install-goatcounter.sh [/usr/local/bin/goatcounter]
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source-path=SCRIPTDIR source=goatcounter.version
. "$here/goatcounter.version"
dest=${1:-/usr/local/bin/goatcounter}

case $(uname -m) in
    x86_64|amd64)  arch=amd64; gz_sum=$GOATCOUNTER_SHA256_LINUX_AMD64_GZ; bin_sum=$GOATCOUNTER_SHA256_LINUX_AMD64 ;;
    aarch64|arm64) arch=arm64; gz_sum=$GOATCOUNTER_SHA256_LINUX_ARM64_GZ; bin_sum=$GOATCOUNTER_SHA256_LINUX_ARM64 ;;
    *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

name=goatcounter-$GOATCOUNTER_VERSION-linux-$arch
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL --proto '=https' -o "$tmp/$name.gz" \
    "https://github.com/arp242/goatcounter/releases/download/$GOATCOUNTER_VERSION/$name.gz"
echo "$gz_sum  $tmp/$name.gz" | sha256sum -c -
gunzip "$tmp/$name.gz"
echo "$bin_sum  $tmp/$name" | sha256sum -c -
install -m 0755 "$tmp/$name" "$dest"
"$dest" version
