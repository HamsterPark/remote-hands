#!/bin/sh
# Builds Remote Hands for every supported system into dist/, plus SHA256SUMS.
# Usage: VERSION=v1.2.3 ./scripts/build.sh
set -eu

version="${VERSION:-dev}"
cd "$(dirname "$0")/.."
rm -rf dist
mkdir dist

for target in windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 linux/arm; do
	os="${target%/*}"
	arch="${target#*/}"
	out="dist/remote-hands-$os-$arch"
	[ "$os" = windows ] && out="$out.exe"
	echo "building $out"
	# GOARM=6 runs on every Raspberry Pi, including the Zero and the 1.
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOARM=6 \
		go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$out" .
done

cd dist
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum remote-hands-* >SHA256SUMS
else
	shasum -a 256 remote-hands-* >SHA256SUMS
fi
