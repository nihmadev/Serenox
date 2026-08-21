# Contributing

## Boundaries

The Go implementation occupies the repository root. The `hikka/` Python source is a
read-only behavioral reference. A contribution must not edit, delete, rename,
or format any legacy file.

## Required workflow

```sh
make fmt
make check
```

`make check` runs formatting checks, Go and Python static analysis, syntax
compilation, unit/integration tests, the Go race detector, builds, and the
legacy-tree integrity check. CI invokes the same target.

Go compilation and tests default to two concurrent package workers so the same
commands remain usable on small hosts. Set `GO_PARALLEL=1` for the lowest-memory
validation path, or raise it explicitly on a larger CI runner.

The pinned Go tools are installed under `.tools/bin`; Python tools come from the
sidecar lockfile. Install the repository-local pre-commit hooks with
`uv tool run pre-commit install` if desired.

## Resource ownership review

Every goroutine, child process, listener, socket, file, ticker, and queue needs:

- one owning component;
- a hard capacity or cardinality limit;
- a parent context and cancellation path;
- a bounded shutdown wait;
- overload and cleanup tests.

Filters must be evaluated before plugin admission. Do not start a goroutine or
timer per update. Public protocol DTOs may not import a Telegram client library.

## Compatibility evidence

Add a recorded fixture or golden test for each compatibility claim. Preserve an
observable legacy result when practical, but document and fix detached work,
unbounded retention, non-atomic persistence, and other unsafe implementation
details. Unsupported Python APIs require `UnsupportedAPIError` with a named
replacement or migration guidance.
