#!/bin/sh
set -eu

platform=$1
version=$2
build_number=$3
out=$4
mkdir -p "$out"
out=$(cd "$out" && pwd)
cd "$(dirname "$0")/.."

staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
contents="$staging/portable"
licenses="$contents/ThirdPartyLicenses"
mkdir -p "$licenses"

case "$platform" in
  macos) asset=ReverseSSH-macOS-Universal.zip; target=darwin; arch=$(go env GOARCH); sys_package=unix ;;
  windows-x64) asset=ReverseSSH-Windows-x64.zip; target=windows; arch=amd64; sys_package=windows ;;
  windows-arm64) asset=ReverseSSH-Windows-ARM64.zip; target=windows; arch=arm64; sys_package=windows ;;
  *) printf 'Unknown release platform %s\n' "$platform" >&2; exit 1 ;;
esac

cp README.md "$contents/使用说明.md"
cp "$(go env GOROOT)/LICENSE" "$licenses/Go.txt"

copy_license() {
  module=$(GOOS="$target" GOARCH="$arch" go list -f '{{.Module.Dir}}' "$1")
  test -n "$module"
  cp "$module/$2" "$licenses/$3"
}

copy_license golang.org/x/crypto/ssh LICENSE Go-x-crypto.txt
copy_license "golang.org/x/sys/$sys_package" LICENSE Go-x-sys.txt

if [ "$platform" = macos ]; then
  sh scripts/build-macos.sh "$contents" "$version" "$build_number"
  ditto -c -k --sequesterRsrc "$contents" "$staging/$asset"
else
  GOOS=windows GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -ldflags='-s -w -H windowsgui' -o "$contents/Reverse SSH.exe" .
  copy_license github.com/Microsoft/go-winio LICENSE Go-winio.txt
  copy_license github.com/jchv/go-winloader LICENSE.md Go-winloader.txt
  copy_license github.com/wailsapp/go-webview2/webviewloader LICENSE Go-webview2.txt
  copy_license github.com/wailsapp/go-webview2/webviewloader webviewloader/LICENSE WebView2Loader.txt
  (cd "$contents" && zip -q -9 -r "$staging/$asset" 'Reverse SSH.exe' 使用说明.md ThirdPartyLicenses)
fi

unzip -tq "$staging/$asset"
mv "$staging/$asset" "$out/$asset"
