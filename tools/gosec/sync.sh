#!/bin/sh
# sync.sh — regenerate the vendored gosec cmd files from the pinned release.
#
# Usage: tools/gosec/sync.sh   (or: make gosec-sync)
#
# The .go files in this directory are verbatim copies of the cmd/gosec
# package of the gosec release pinned in go.mod (excluding _test.go files).
# After changing the pinned version, run this script, then rebuild with
# `make tools` and verify with `make security`.
set -eu

cd "$(dirname "$0")"

PKG=github.com/securego/gosec/v2

# Read the pinned version from go.mod (e.g. v2.29.0).
VERSION="$(awk -v pkg="$PKG" '$1 == "require" && $2 == pkg { print $3; exit }' go.mod)"
if [ -z "$VERSION" ]; then
    echo "error: could not find the gosec requirement in go.mod" >&2
    exit 1
fi

# Ensure the release source is in the module cache.
GOBIN_TMP="$(mktemp -d)"
trap 'rm -rf "$GOBIN_TMP"' EXIT
GOBIN="$GOBIN_TMP" go install "$PKG/cmd/gosec@$VERSION"

UPSTREAM="$(go env GOMODCACHE)/$(echo "$PKG" | sed 's/\([A-Z]\)/!\l\1/g')@$VERSION/cmd/gosec"
if [ ! -d "$UPSTREAM" ]; then
    echo "error: upstream source not found at $UPSTREAM" >&2
    exit 1
fi

# Re-copy the non-test source files (the wrapper's doc.go/go.mod stay).
rm -f main.go profiling_debug.go profiling_release.go sort_issues.go version.go
for f in main.go profiling_debug.go profiling_release.go sort_issues.go version.go; do
    cp "$UPSTREAM/$f" "$f"
    chmod +w "$f"
done

echo "synced $(ls main.go profiling_debug.go profiling_release.go sort_issues.go version.go | wc -l | tr -d ' ') files from $PKG@$VERSION"
echo "run: make tools && make security"
