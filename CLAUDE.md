# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`modernc.org/quickjs` is a **pure-Go, CGo-free embeddable JavaScript engine** (ES2023). It is an
idiomatic Go wrapper around `modernc.org/libquickjs` — the Bellard QuickJS C library mechanically
transpiled to Go with `ccgo`. There is no C compiler, no CGo, and no shared library involved; the
whole engine cross-compiles as ordinary Go. Confirm this before reaching for CGo: `grep -rn 'import "C"'`
returns nothing.

## Build, lint, test

The workflow is driven by the `Makefile`, not by bare `go` commands.

```sh
make editor        # primary build+lint gate: gofmt -s -w, go test -c (compile only),
                   #   go build generator.go, go install ./..., staticcheck
make test          # go test -failfast -v -timeout 24h -count=1 ./...
make shorttest     # same, adds -short (skips long-running cases)
make leak          # go test -v -run TestMemgrind2 -tags=libc.memgrind  (leak detection)
make benchmark     # delegates to ./compare (see below)
make build_all_targets   # cross-build + compile test binary for all 15 GOOS/GOARCH, ±libc.memgrind
```

Run a single test / subtest:

```sh
go test -run TestEval -v            # one test
go test -run 'TestEval/eval1' -v    # one subtest (t.Run subtests are common here)
```

Lint tools used: `gofmt -s`, `staticcheck`, `golint`. Always run `gofmt -s -w .` (or `make editor`)
before considering a change done.

### Local dev environment

`go.work` (untracked; regenerate with `make work`) wires in **sibling checkouts** of the transpiled
dependencies — development assumes `../libc` (`modernc.org/libc`) and `../libquickjs`
(`modernc.org/libquickjs`) exist next to this repo. Edits to the value representation or new binding
surface usually require the matching `libquickjs` version to be present locally.

### Memgrind (leak detection)

`memgrind_test.go` is guarded by `//go:build libc.memgrind`. The `libc.memgrind` tag swaps in libc's
allocation-tracking allocator. Because JS `Value`s are reference-counted C objects, leak tests are the
canonical way to validate `Dup`/`Free` pairing — see the `TestMemgrind` doc.

## Architecture

### The three-layer stack

1. **`modernc.org/libquickjs`** (external) — transpiled QuickJS. Symbol conventions to know when
   editing the wrapper: `lib.X*` = exported C functions (e.g. `XJS_NewRuntime`, `XJS_Eval`),
   `lib.T*` = C types (`TJSValue`, `TJSRuntime`, `TJSContext`, `TJSAtom`), `lib.E*` = enum constants
   (`EJS_TAG_NULL`), `lib.M*` = macro constants (`MJS_EVAL_TYPE_GLOBAL`, `MCONFIG_VERSION`). Every
   transpiled C function takes a `*libc.TLS` as its first argument.
2. **`modernc.org/libc`** (external) — C-runtime emulation. `libc.TLS` is the emulated thread-local
   storage / C stack; memory the engine touches is allocated via `libc.Xcalloc`/`libc.Xfree` and
   strings via `libc.CString` — **not** Go's GC heap.
3. **This package** (`quickjs.go` + friends) — the public Go API.

### Core types (quickjs.go)

- `runtime` (unexported): wraps `lib.TJSRuntime` plus its own `*libc.TLS`. One TLS per runtime.
- `VM` (the main handle): wraps a `lib.TJSContext` (a JS realm). Created by `NewVM()`, released by
  `Close()`. **Not safe for concurrent use by multiple goroutines** — same for `runtime`.
- `Value`: wraps a reference-counted `lib.TJSValue`. `Dup()`/`Free()` must be **strictly paired and
  properly nested** (the type doc spells out how this breaks across goroutines/channels). Getting this
  wrong causes leaks or use-after-free, caught by the memgrind tests.
- `Object`, `Atom` (= `lib.TJSAtom`), `HostFunc` (`func(args []any) (any, error)`).

### 32-bit vs 64-bit value representation — keep both in sync

`JSValue` uses NaN-boxing whose layout depends on pointer size. This is split across two files with
opposite build tags, and **any change to value encoding must be mirrored in both**:

- `quickjs64.go` (`//go:build !(386 || arm)`): `TJSValue` is a struct with an `.Ftag` field.
- `quickjs32.go` (`//go:build 386 || arm`): `TJSValue` is a `uint64` with explicit NaN-boxing bit math.

Both define the same set: `sizeofJsValue`, `null`, `undefined`, `newFloat`, `newBool`, `tag`,
`isException`, `jsvToInt32`, `jsvSetInt32`, `jsvToFloat64`, `jsvToPtr`.

linux/s390x is the only **big-endian** target and it makes the 64-bit union layout observable: `ccgo`
represents `JSValueUnion` as its widest member, an `uint64`, and accesses the narrow members in the
*value* domain (`JS_MKVAL` zero-extends into the low 32 bits, `JS_VALUE_GET_INT` truncates). So the
`int32`/bool payload lives in the low 32 bits, which are the *last* four bytes of the union on s390x.
Read and write it only through `jsvToInt32`/`jsvSetInt32` — a raw `*(*int32)(unsafe.Pointer(&v))`
silently decodes every JS integer and boolean as 0/false there. The float64 and pointer payloads are
as wide as the union itself, so those stay plain casts.

### C → Go callbacks

`fp(f)` extracts the raw code pointer from a Go func value (second word of the interface) so it can be
handed to C as a function pointer (used for the interrupt handler, host functions, module loaders).
Go functions exposed to JS via `RegisterFunc`/`RegisterHostFunc` are kept alive in package-global
registries keyed by an int32 "magic": `goFuncs` (+ `freeMagic` for reuse) under `goFuncsMu`, magics
handed out by the `staticMagic` atomic counter. Custom module loaders live in `customLoaders` under
`customLoadersMu`. These globals are process-wide, not per-VM.

### Interrupt / eval timeout

Each `VM` owns an `interruptData` struct (allocated in libc memory) holding `interrupt int32` and
`timeLimit int64`. Every eval-family entry point (`Eval`, `EvalValue`, `Call`, `CallValue`,
`ExecutePendingJobs`, …) calls `configureInterrupt()` **first** to re-arm the deadline from
`m.timeout`; the C-side `interruptHandler` polls both fields. `SetEvalTimeout` sets the duration;
`Interrupt()` sets the flag atomically for async cancellation from another goroutine. When adding a new
entry point that runs JS, remember to call `configureInterrupt()` or the timeout won't apply.

## Sub-packages and generated assets

- `fmt/` — module `modernc.org/quickjs/fmt`; exposes Go's `fmt.Printf/Println/Sprintf/Sprintln` to a VM
  via `Register(obj Value)` (JS names are lower-cased, e.g. `fmt.println`).
- `compare/` — **separate module** (`modernc.org/quickjs/compare`, own `go.mod`) benchmarking this
  engine against `github.com/dop251/goja` and `github.com/fastschema/qjs`, plus the arewefastyet V8
  suite in `internal/arewefastyet/`. `make benchmark` runs here.
- `internal/autogen/*.mod` — 15 per-`GOOS_GOARCH` snapshots of `go.mod`, maintained by the
  modernc.org builder's "autogen" feature (see `builder.json`); the recent "<arch> auto generate"
  commits are these. Don't hand-edit.
- `testdata/benchmarks/<qjs-version>/<os>_<arch>.txt` — recorded benchmark geomeans.
- `generator.go` (`//go:build ignore`, standalone `package main`, run via `//go:generate go run
  generator.go`): runs `make benchmark`, reads the geomean rows from
  `testdata/benchmarks/<version>/*.txt` (version taken from `compare/go.mod`), and **rewrites the
  `# Performance` table inside the `quickjs.go` package doc comment**. If you edit that table by hand it
  will be overwritten.

## Supported platforms

15 combos (see the `quickjs.go` package doc and `builder.json`): darwin/{amd64,arm64},
freebsd/{amd64,arm64}, linux/{386,amd64,arm,arm64,loong64,ppc64le,riscv64,s390x}, windows/{amd64,arm64}.
Changes must keep `make build_all_targets` green across all of them.
