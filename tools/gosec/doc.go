// Package main rebuilds the gosec CLI against a newer golang.org/x/tools.
//
// Why this wrapper exists
// -----------------------
// gosec v2.29.0 is pinned to golang.org/x/tools v0.49.0, whose export-data
// decoder (gcexportdata) supports at most version 4. The Go 1.27 compiler
// emits export data version 5, so an unpatched gosec binary cannot load the
// build cache of a Go 1.27 toolchain: every package reports "has type
// errors", the SSA-based analyzers silently skip, and gosec exits non-zero
// without having analyzed anything.
//
// This module vendors gosec v2.29.0's cmd/gosec files unchanged (see
// LICENSE-UPSTREAM.txt, MIT) and forces a newer golang.org/x/tools via a
// replace directive, which restores the decoder's ability to read the Go
// 1.27 build cache. The Makefile builds $(TOOLCHAIN)/gosec from here, so
// `make security` runs the real gosec v2.29.0 release with a working loader.
//
// Keeping this wrapper in sync
// ----------------------------
// The vendored .go files must match the gosec version in go.mod. After
// changing the version, regenerate them with:
//
//	tools/gosec/sync.sh
//
// (the Makefile `gosec-sync` target runs it).
package main
