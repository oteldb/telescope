# telescope

A terminal log viewer. It opens one stream — a systemd unit, a Kubernetes
workload, a container, any command, or a query against VictoriaLogs or Loki,
locally or over ssh — and renders it as a filterable list. A `merge` reads
several of them as one timeline. `README.md` is written for users and is the
best description of what the thing does; read it before changing behavior.

## Commands

```
go build ./...
go test ./...
golangci-lint fmt ./...   # formatting, not gofmt by hand
golangci-lint run ./...
```

A parent `go.work` sometimes shadows this module; if a go command complains
about a workspace, re-run it with `GOWORK=off`.

## Layout

| package | what lives there |
| --- | --- |
| `internal/source` | building and running collectors. `Config` describes a stream; `Command`/`Argv` turn it into a process; `Stream` yields `Line`s. `loki.go`/`victorialogs.go` query over HTTP instead, `merge.go` interleaves several streams, `endpoint.go` holds the HTTP endpoint and its token. |
| `internal/logs` | what a line means and how it reads. `parse.go` turns JSON into a `Record`, `store.go` renders and retains entries, `filter.go` is the grep and level filter with its incremental `View`, `highlight.go`/`escape.go` decide what reaches the screen. |
| `internal/ui` | the bubbletea models. `app.go` is the root, `start.go` picks a source, `logview.go` is the list, `entryview.go` one entry, `theme.go` the palette, `rows.go` the row backgrounds. |
| `internal/config` | `config.yaml` and `history.yaml`: endpoints, sources, merges, tokens. |
| `internal/complete` | the suggestions the start screen offers, fetched by shelling out. |

The dependency order is `source` → `logs` → `ui`, and `config` feeds `ui`. It
does not run the other way: when `source` needed to date a line by parsing it,
the parser came in through `source.WithTimeFunc` rather than an import.

## What the code assumes

- **A token never leaves memory.** It is resolved when the config is read, and
  never written to `history.yaml`, shown in a `Title()`, or passed to a child
  process. Check this whenever a config value gains a new path to the screen or
  the disk.
- **A line's time is `Line.At` if the source reported one, else what the line
  says about itself, else when it arrived.** `Entry.HasTime` is the difference
  between the first two and the last, and only the first two are worth showing.
- **A merge trusts its children to be ordered** and does a k-way merge over
  their heads. Each child may have exactly one line pending — the per-child ack
  channel is what enforces that, not the unbuffered item channel. A source that
  goes quiet past `mergeLag` is skipped for ordering; being quiet is not being
  finished, and treating it as finished loses lines.
- **Everything a source produces is somebody else's bytes.** Rendered lines go
  through `logs.Sanitize`, attribute values through `logs.Escape`. A background
  laid under a line has to be re-armed after every reset the line contains; see
  `ui.paint`.
- **The gutter is drawn outside the horizontal offset.** The merge tag, the time
  and the level must not scroll away from the line they belong to.
- **Ordering-sensitive state is computed once, on arrival.** `Entry.Band` is
  worked out in `Store.Append` because a view that scrolls or filters cannot
  work it out again without disagreeing with itself.
- `Config.Labels()` names the children of a merge; `Config.SourceLabels(from)`
  describes where a stream comes from. They are different things with similar
  names.

## Tests

- `internal/ui` renders models and asserts on the output. `TestMain` forces a
  truecolor profile, stubs the completion fetcher, and blocks the real config
  and history — no test may read the developer's own files or shell out.
- Compare against `ansi.Strip(...)` for text and against a rendered style
  (`styleTrace.Render(...)`) for color.
- Collectors are tested against `httptest` servers; the timing paths of the
  merge run under `testing/synctest`, never a real sleep.
- Anything that decodes bytes from outside gets a fuzzer with the table tests as
  its corpus, and the inputs it finds are committed.

## Voice

The comments in this repo say why, not what: what the code does is in the code.
They are written as prose, and a comment that only names the next five lines is
noise. Files are split by subject rather than divided by banner comments, and
tests are named as the sentence they are checking.
