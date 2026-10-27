#!/bin/sh
# LUMO release packaging: cross-compile lumos/lumoc for the supported
# platform matrix and produce standard archives plus a checksum file.
set -e
cd "$(dirname "$0")"

VERSION=$(sed -n 's/.*Version = "\(.*\)"/\1/p' internal/version/version.go | head -1)
if [ -z "$VERSION" ]; then
    echo "cannot determine version" >&2
    exit 1
fi
echo "packaging LUMO $VERSION"

OUT=release/packages
rm -rf "$OUT"
mkdir -p "$OUT"

TARGETS="linux/amd64 linux/arm64 linux/386 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64"

for t in $TARGETS; do
    os=${t%/*}
    arch=${t#*/}
    suffix="${os}_${arch}"
    dir="$OUT/lumo_${VERSION}_${suffix}"
    mkdir -p "$dir"

    ext=""
    [ "$os" = "windows" ] && ext=".exe"

    for cmd in lumos lumoc; do
        env CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
            go build -trimpath -ldflags "-s -w" \
            -o "$dir/$cmd$ext" "./cmd/$cmd"
    done

    cp conf/lumos.toml conf/lumoc.toml LICENSE "$dir/"

    if [ "$os" = "windows" ]; then
        /c/Windows/System32/tar.exe -a -c -f "$OUT/lumo_${VERSION}_${suffix}.zip" -C "$OUT" "lumo_${VERSION}_${suffix}"
    else
        tar -zcf "$OUT/lumo_${VERSION}_${suffix}.tar.gz" -C "$OUT" "lumo_${VERSION}_${suffix}"
    fi
    rm -rf "$dir"
    echo "built lumo_${VERSION}_${suffix}"
done

rm -rf "$OUT"/lumo_${VERSION}_*/
cd "$OUT"
sha256sum lumo_* > "lumo_${VERSION}_sha256_checksums.txt"
echo "---- artifacts ----"
ls -lh
