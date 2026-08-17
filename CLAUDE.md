# telescope

A terminal log viewer. It opens one stream — a systemd unit, a Kubernetes
workload, a container, any command, or a query against VictoriaLogs or Loki,
locally or over ssh — and renders it as a filterable list. A `merge` reads
several of them as one timeline. `README.md` is written for users and is the
best description of what the thing does; read it before changing behavior, and
keep it that way — it says how to install it, how to configure it, and points
out the features on top; never how it works inside, and never what the screen
already says. Every view writes its own keys along the bottom, so a key table in
the README is a second copy to keep right. Why the code is the way it is belongs
in the comments beside it, not here.

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
| `internal/source` | building and running collectors. `Config` describes a stream; `Command`/`Argv` turn it into a process; `Stream` yields `Line`s. `loki.go`/`victorialogs.go` query over HTTP instead, `merge.go` interleaves several streams, `prefix.go` reads which pod a kubectl line came from where one command is tailing several, `endpoint.go` holds the HTTP endpoint and its token, `fields.go` asks a database what its lines are labeled with, `page.go` asks it for what came before a line and `pagecmd.go` asks a collector the same by running it again over the window below the screen, `pushdown.go` is what to ask instead when it will not read the compiled filter. |
| `internal/logs` | what a line means and how it reads. `parse.go` turns JSON into a `Record`, `record.go`/`labels.go` say what a record and its labels are, `store.go` renders and retains entries and `fields.go` indexes what they were labeled with, `filter.go` pairs a parsed query with the level the view cycles, and keeps its incremental `View`, `order.go` puts a line that arrived out of order back where its time says it belongs and `origin.go` works out which label tells a view's streams apart, `semconv.go` says what a well-known key's value is, what color it reads in and which family it belongs to, `layout.go` picks and orders the fields a row shows and `vary.go` remembers which of them have ever said anything different, `highlight.go`/`escape.go` decide what reaches the screen. |
| `internal/ui` | the bubbletea models. `app.go` is the root, `start.go` picks a place or a group and `saved.go` is what the config offers it, `logview.go` is the list, and what of a row's fields the width will take is spent there rather than in `logs`, so a resize changes it; `gutter.go` is the time column and the gaps down its left, `status.go` the bar over it — the counts, the toggles and what is being held back — `histogram.go` the volume panel above it, `entryview.go` one entry and `entrydoc.go` the rows its cursor walks, `origin.go` the column naming which stream a line came from, `clipboard.go` copies a row and `locate.go`/`open.go`/`stack.go` open what one points at, `page.go` asks for the lines before the first one when the reader reaches it, `complete.go` finishes what either filter prompt is typing — the list's and the start screen's — and `help.go` writes the filter language out, `theme.go` the palette, `rows.go` the row backgrounds, `logo.go` the banner. |
| `internal/mcp` | what `telescope mcp` serves an agent: a second consumer of `config`, `source` and `logs` beside `ui`, drawing nothing on a screen. `mcp.go` builds the server and registers each tool read-only, `places.go` says what the config declares and what each place holds, `fields.go` asks a database what its lines are labeled with, `logs.go` reads a bounded window by walking `source.Page` back until it has enough matches, `summary.go` counts one instead of listing it, `render.go` writes both as the text a model reads, `resolve.go` turns the name a tool was given back into a `source.Config`. A tool names a declared place and never a command: an agent that could name one could run anything the user can, over ssh. An answer's text is the lines and its structured content is the facts about them — never both, since a client that showed both would be paying twice. What was cut is always reported: a reader that cannot scroll has no other way to tell a quiet window from an unfinished one. |
| `internal/config` | `config.yaml` and `history.yaml`. A `Place` is where a signal is read from and how it is reached (`via:` for a command, `proxy:` for a database); one of type `tempo` or `jaeger` reads traces rather than lines and is named by the places whose lines carry ids into it, so a store is declared once and reached through its own door rather than borrowing the door of whoever named it — `traces: <url>` still writes one out in place for the single-place case, and borrows as it always did. A `Group` is several places read as one. `Load` resolves both; `New` does the same for declarations that did not come from a file. Every key is declared once, as a `figureout` descriptor beside its type, and `schema.go` emits those declarations as the JSON Schema committed at `config.schema.json`. What one key means for another is a `figureout.Invariant` over the same functions `New` runs, so it is reported with the line it was read from and still holds for a config that never saw a file. |
| `internal/query` | the filter language: `Parse` builds an `Expr`, `Match` evaluates it against one `Record`. It sits below `source` so a query can be compiled into one a database answers itself. |
| `internal/complete` | the suggestions the start screen offers, fetched by shelling out. |
| `internal/setup` | what `telescope init` writes. `probe.go` asks the machine what it runs, through `complete`'s listings rather than a second prober; `grafana.go` reads datasources over the API or off the provisioning files; `ask.go` is the prompt, a line at a time rather than a form, since init runs where there may be no terminal; `render.go` writes the file and reads it back through `config.Parse` before it reaches disk. |
| `internal/trace` | a trace as it is read, and nothing that reads it: `span.go` is one operation, `tree.go` arranges them by their parent links, `skew.go` stops a child being drawn before the call that made it, `window.go` is the interval a view covers and the axis over it. `otlp.go` decodes what Tempo answers with, in either encoding, `jaeger.go` the Jaeger query API's response, `decode.go` reads whichever of the two arrived, and `search.go` is what a search answered with — a summary of a trace rather than its spans. Nothing here fetches — `source/tempo.go` does — and nothing here draws: `ui/gantt.go` does, `ui/traceview.go` is the model around it — the gantt, one span, the service filter and the span filter prompt are modes of it — with `ui/spandoc.go` drawing a span as the rows the entry view already reads, `ui/servicepick.go` the service filter and `ui/tracefilter.go` the prompt that narrows by what a span says — it is what teaches `query` to read one — `ui/spanpalette.go` the colors and `ui/tracecache.go` what was already fetched. `ui/tracesearch.go` is the search form and its results, drawn by `ui/searchview.go` with `ui/searchform.go` drawing the fields themselves — the form colors a tag filter and carries its own caret, which a text input cannot — asking through `source/search.go` and `source/tracequery.go`. |

The dependency order is `query` → `source` → `logs` → `ui`, and `config` feeds
`ui`. It does not run the other way: when `source` needed to date a line by
parsing it, the parser came in through `source.WithTimeFunc` rather than an
import. `query` is below `source` for the same reason in reverse — a query has
to be compilable into LogsQL or LogQL without `source` reaching up into `logs`.

`demo/` is what the README recording is made from, and what a screen can be
driven by hand against: `emit.sh` stands in for a service writing logs and
`tracestub/` for the store its traces went to. The stub serves both trace APIs
at once and the caller picks with `--api`, since the point of it is that the two
answer the same question differently. Its traces come out of a counter rather
than a seeded generator: a fixture whose traces move between runs cannot be
compared against yesterday's screenshot, and the ids `emit.sh` writes have to
land on the same traces every time.

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
