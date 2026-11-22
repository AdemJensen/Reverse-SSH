#!/bin/sh
set -eu

out="${1:-./release}"
version="${2:-}"
build_number="${3:-}"
mkdir -p "$out"
out=$(cd "$out" && pwd)
cd "$(dirname "$0")/.."

staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
app="$out/Reverse SSH.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"

for arch in arm64 amd64; do
  MACOSX_DEPLOYMENT_TARGET=13.0 GOOS=darwin GOARCH="$arch" CGO_ENABLED=1 \
    go build -trimpath -ldflags='-s -w' -o "$staging/ReverseSSH-$arch" .
done

lipo -create "$staging/ReverseSSH-arm64" "$staging/ReverseSSH-amd64" -output "$app/Contents/MacOS/ReverseSSH"
cp scripts/Info.plist "$app/Contents/Info.plist"
if [ -n "$version" ]; then
  plutil -replace CFBundleShortVersionString -string "$version" "$app/Contents/Info.plist"
fi
if [ -n "$build_number" ]; then
  plutil -replace CFBundleVersion -string "$build_number" "$app/Contents/Info.plist"
fi
swift -module-cache-path "$staging/swift-modules" scripts/icon.swift "$app/Contents/Resources/AppIcon.icns"
codesign --force --sign - "$app"
codesign --verify --strict "$app"
