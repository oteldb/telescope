# telescope

A terminal log viewer. It opens one stream — a systemd unit, a Kubernetes
workload, a container, any command, or a query against VictoriaLogs or Loki,
locally or over ssh — and renders it as a filterable list. A `merge` reads
several of them as one timeline. `README.md` is written for users and is the
best description of what the thing does; read it before changing behavior, and
keep it that way — it says what a feature is and how to configure it, never how
it works inside. Why the code is the way it is belongs here.

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
| `internal/source` | building and running collectors. `Config` describes a stream; `Command`/`Argv` turn it into a process; `Stream` yields `Line`s. `loki.go`/`victorialogs.go` query over HTTP instead, `merge.go` interleaves several streams, `endpoint.go` holds the HTTP endpoint and its token, `fields.go` asks a database what its lines are labeled with, `page.go` asks it for what came before a line, `pushdown.go` is what to ask instead when it will not read the compiled filter. |
| `internal/logs` | what a line means and how it reads. `parse.go` turns JSON into a `Record`, `record.go`/`labels.go` say what a record and its labels are, `store.go` renders and retains entries and `fields.go` indexes what they were labeled with, `filter.go` pairs a parsed query with the level the view cycles, and keeps its incremental `View`, `highlight.go`/`escape.go` decide what reaches the screen. |
| `internal/ui` | the bubbletea models. `app.go` is the root, `start.go` picks a place or a group and `saved.go` is what the config offers it, `logview.go` is the list and `gutter.go` the time column and the gaps down its left, `entryview.go` one entry and `entrydoc.go` the rows its cursor walks, `clipboard.go` copies a row and `locate.go`/`open.go`/`stack.go` open what one points at, `page.go` asks for the lines before the first one when the reader reaches it, `complete.go` finishes what either filter prompt is typing — the list's and the start screen's — and `help.go` writes the filter language out, `theme.go` the palette, `rows.go` the row backgrounds, `logo.go` the banner. |
| `internal/config` | `config.yaml` and `history.yaml`. A `Place` is where logs are read from and how it is reached (`via:` for a command, `proxy:` for a database); a `Group` is several places read as one. `Load` resolves both; `New` does the same for declarations that did not come from a file. |
| `internal/query` | the filter language: `Parse` builds an `Expr`, `Match` evaluates it against one `Record`. It sits below `source` so a query can be compiled into one a database answers itself. |
| `internal/complete` | the suggestions the start screen offers, fetched by shelling out. |

The dependency order is `query` → `source` → `logs` → `ui`, and `config` feeds
`ui`. It does not run the other way: when `source` needed to date a line by
parsing it, the parser came in through `source.WithTimeFunc` rather than an
import. `query` is below `source` for the same reason in reverse — a query has
to be compilable into LogsQL or LogQL without `source` reaching up into `logs`.

## What the code assumes

- **A token never leaves memory.** It is resolved when the config is read, and
  never written to `history.yaml`, shown in a `Title()`, or passed to a child
  process. Check this whenever a config value gains a new path to the screen or
  the disk.
- **A line's time is `Line.At` if the source reported one, else what the line
  says about itself, else when it arrived.** `Entry.HasTime` is the difference
  between the first two and the last, and only the first two are worth showing.
- **What a place must name is the API's rule, not ours.** `kubectl` cannot
  stream without a pod or a selector, `docker` without a container;
  `journalctl`, VictoriaLogs and Loki need nothing but where to read. That is
  the whole of what a `Group` may name, and why one can be four regions and no
  query.
- **Pushing a query down is an optimization, never an answer.** `query.LogsQL`
  compiles what it can prove and drops the rest, `logs.Filter` still runs over
  everything that comes back, and only a conjunction may lose a term — an `or`
  branch or a `not` operand would narrow. `Config.Pushed()` is what the view
  compares to decide whether a filter is worth asking again. It follows that a
  server which will not read what was compiled for it must cost the
  optimization and not the stream: a 400 is asked again as the place alone and
  remembered per endpoint, since LogsQL is younger than a lot of the databases
  running it.
- **Loki is the exception, and only because LogQL has no match-all.**
  `query.LogQL` compiles the filter's label comparisons into a stream selector
  and nothing else — a bare word matches labels here and only the line there —
  and a filter that compiles to none is no query rather than a wide one, so
  `startAPI` refuses to open the stream. Everything above still holds: a term
  it cannot say is dropped, and the view filters what comes back.
- **A tail is a number only where nothing can be asked twice.** A command has
  written what it wrote, so how far back it reaches is chosen before it starts;
  a database can be asked again, so `Config.Page` reads what came before the
  oldest line held and `Store.Prepend` puts it in front. A page is bounded by
  `Store.Room` and never by eviction — dropping the newest lines to make room
  for older ones would undo the reading that asked for them. Loki has no notion
  of "the previous n lines" and only of a window, so an empty page there widens
  the window and asks again: a service quiet for a day is quiet, not finished.
  It follows that `tail: 0` — every line there is — opens a database on
  `backfillLimit` rather than on nothing, which is what it used to mean. A merge
  pages only where every child does, and by sorting rather than by the k-way
  merge the stream uses: a page is the whole window at once, and the newest
  `limit` lines of it are what keeps one page contiguous with the next.
- **A merge trusts its children to be ordered** and does a k-way merge over
  their heads. Each child may have exactly one line pending — the per-child ack
  channel is what enforces that, not the unbuffered item channel. A source that
  goes quiet past `mergeLag` is skipped for ordering; being quiet is not being
  finished, and treating it as finished loses lines.
- **Everything a source produces is somebody else's bytes.** Rendered lines go
  through `logs.Sanitize`, attribute values through `logs.Escape`. A background
  laid under a line has to be re-armed after every reset the line contains; see
  `ui.paint`.
- **What a row draws as and what it carries are two values.** An entry row keeps
  its rendered form — escaped, wrapped, colored — apart from the key and value
  as received, because the escaping is for the screen and nowhere else: a path
  with an escape drawn as `\e` is not a path any editor will open, and a wrapped
  trace id is not a trace id. Copy, open and narrow all take the raw side.
- **The gutter is drawn outside the horizontal offset.** The merge tag, the time
  and the level must not scroll away from the line they belong to.
- **How a time is written is the view's, so the view draws it.** `pl` is run
  with `NoTime` and `ui.gutter` stamps every line, structured or not: a
  rendering is worked out once, when the line arrives, and `timeMode` changes
  while somebody is looking at it. The age it can show is measured from
  `logModel.origin` — when the view opened, which is when what it holds was
  asked for — and never from `time.Now()`, since a list that renumbered itself
  on every redraw could not be read. A silence longer than `gapAfter` takes a
  row of its own between the two lines, which is why `logModel.clamp` counts the
  window in rows and not in entries.
- **A suggestion the database knows and the lines do not is worth saying so
  about.** `onRecordDetail` is worded as what it is, since "not seen yet" was
  read as "this value does not exist" — and the same distinction is what
  `Store.HasField` answers for an empty list: a value nothing has is a value to
  change, a field nothing has is a filter that was never going to match. It
  resolves a name the way `Entry.Field` does, off the index rather than by
  scanning, and the index never forgets.
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
