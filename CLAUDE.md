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
| `internal/trace` | a trace as it is read, and nothing that reads it: `span.go` is one operation, `tree.go` arranges them by their parent links, `skew.go` stops a child being drawn before the call that made it, `window.go` is the interval a view covers and the axis over it. `otlp.go` decodes what Tempo answers with, in either encoding, and `jaeger.go` the Jaeger query API's response. Nothing here fetches — `source/tempo.go` does — and nothing here draws: `ui/gantt.go` does, `ui/traceview.go` is the model around it — the gantt, one span, and the service filter are modes of it — with `ui/spandoc.go` drawing a span as the rows the entry view already reads, `ui/servicepick.go` the filter, `ui/spanpalette.go` the colors and `ui/tracecache.go` what was already fetched. |

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
- **A trace that arrives is not a tree, and no span is dropped for it.** A
  parent sampled away, a span reported twice, a runtime that wrote its own id
  as its parent, a ring of parent links, a span stamped 1970: every one of them
  is ordinary in production, and `trace.Build` resolves each to something
  drawable rather than to an error, because there is nobody to report the
  missing tenth of a trace to. A span that cannot be placed becomes a root and
  says it was detached; a span with no clock takes its parent's, since the
  alternative is a window fifty-six years wide. `Tree.ClampSkew` is separate
  from that and optional: it moves a subtree by the least that stops the
  picture lying about causality, and is not Jaeger's per-service adjustment,
  which needs span kinds telescope does not have yet.
- **The jump between a line and its trace is one message each way.** `T` sends
  `openTraceMsg` with the merge tag, so a merge asks the trace store of the
  place the line came from; `f` sends the `filterMsg` the entry view already
  sends, so narrowing from a span and narrowing from a row of an entry are the
  same path and land in the same place. Neither invents a second way to do
  what the other does. A trace opened on its own refuses the second: there is
  no list under it, and dropping into an empty one says less than saying so.
- **A trace is held by where it was read from, not by its id.** The jump goes
  both ways and usually more than once, so `ui.traceCache` keeps what was
  fetched and `openTraceMsg` draws from it rather than asking again. The key is
  the endpoint's url and the id together: the same id names one request to one
  system and nothing at all to another, and a merge is several systems. It
  follows that the cache has to be droppable — a trace still being served grows
  after it was first read, and only the reader knows whether it has, which is
  what `r` and `reloadTraceMsg` are for. What is remembered is the tree and
  never the view over it: reopening a trace to find it folded the way it was
  left says the reading was kept when only the bytes were.
- **A filter over services may not rewrite the tree.** A span whose service is
  filtered out is still drawn when something under it is not: removing it would
  leave its children hanging under a span that never called them, and the tree
  is the whole of what says who called whom. `Node.Shown` is that rule and
  `Node.Scaffold` is what the drawing asks to know it is structure rather than
  content. It is also why nothing here has jaeger-ui's separate protection for
  the root's service — a root with anything visible below it is kept by the same
  rule as everything else. A filter that hides every span is dropped rather than
  obeyed: an empty screen says less than the trace does.
- **An OTLP id is hex or base64, and only its length says which.** The
  specification writes a trace id as hex; everything built on protojson writes
  base64, Tempo included, and that is the one the Grafana plugin is tested
  against. They cannot be told apart by trying one and catching the failure —
  every hex digit is in the base64 alphabet, so a hex id decodes as base64
  without complaint into sixteen bytes of nonsense, and every document parses
  with every id wrong. Length decides instead: hex spells the two ids in 32 and
  16 characters and base64 in 24 and 12, four lengths that do not collide. See
  `trace.hexID`, and note that the failure this avoids is silent — the tree
  still builds, since the nonsense is deterministic and parent links still
  match, and only the id somebody copies is wrong.
- **What a trace decodes as is decided by what came out, not by what failed.**
  A Jaeger response is JSON, so the OTLP decoder reads it as a valid payload
  describing no spans and reports no error — unknown fields are how OTLP stays
  forwards-compatible, and `DiscardUnknown` is what makes a newer Tempo
  readable. `cmd/telescope.decodeTrace` therefore falls through on an empty
  result rather than on an error, or a Jaeger file would open as an empty trace.
- **A service's color is counted out, not hashed.** `newServicePalette` hands
  out swatches in the order the trace first names a service, which is
  jaeger-ui's rule and theirs is arithmetic: a hash into twenty buckets collides
  for six services more than half the time, and two services sharing a color in
  the one trace on screen defeats the only thing the color is for. The counter
  is per trace rather than per session as theirs is, so the same trace draws the
  same way for everybody — their ADR records reconciling that as unfinished, and
  a gantt opened on one trace at a time gets it for free. It follows that
  failure cannot be carried by color: one of the twenty swatches is red, so a
  failed span is marked `✗` as well.
- **A bar's two edges do not get the same resolution.** The eighth blocks fill
  a cell from its left, so a bar *ending* mid-cell lands within an eighth with
  no idea what the terminal's background is; a bar *starting* mid-cell would
  need an eighth in reverse video, which means naming a background a
  transparent terminal does not have, so it gets a half. See `ui.barCells` —
  and the same rule is why a span too short to cover a cell is still drawn as
  one eighth rather than as nothing.
- **A merge trusts its children to be ordered** and does a k-way merge over
  their heads. Each child may have exactly one line pending — the per-child ack
  channel is what enforces that, not the unbuffered item channel. A source that
  goes quiet past `mergeLag` is skipped for ordering; being quiet is not being
  finished, and treating it as finished loses lines.
- **A place that does not have what a group named is not a failure of the
  group.** A collector pointed at a workload the cluster does not run writes its
  refusal on stderr and exits at once, so `forward` holds what a child says
  until it is known whether it opened: `absent` reads that complaint, and a
  child that never opened for that reason contributes nothing — no line, no
  note, no error out of `Done`. Anything else it said is kept, because a host
  that could not be reached is a place with something to say. The hold ends at
  the first line on stdout or at `openGrace`, since a container logging only to
  stderr is running and not refusing.
- **A note is telescope talking, not the source.** A collector's stderr is the
  source's own output — `docker` and `ssh` fold an application's stderr into it
  — so only what telescope writes itself is marked, and that is what `ui` washes
  with `noteWash`. `source.Kind` says which report it is and `Line.Reason`
  carries the failure alone: a place that never opened, a stream that broke
  halfway and a collector that exited are three different things to be told, and
  where the error arose is what separates them — an API child whose query
  returns 500 opened, so it stopped rather than failed to start. The sentence is
  written in `logs.noteText`, because it has to reach the screen and the filter
  as the same words: a note has no fields and no severity, so what it reads as
  is the only thing a query can match it by. It follows that `Line.Data` is
  empty for a note, and anything reading a stream's bytes directly — `absent`,
  `complete` — has to say what it means by a line that has none.
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
- **What repeats is a property of the screen, not of the log, so it is worked
  out at render time.** `ui.clampRuns` groups a line with the ones straight
  after it that said the same thing, and the cursor counts those runs rather
  than entries — a cursor that could land inside a fold would be somewhere the
  reader cannot see, which is why `takePage` carries it by the line it is on and
  looks the row up again with `runAt`. It is the mirror of the rule above and
  not an exception to it: two lines a filter has just brought together are a
  repetition and the same two with a third between them are not, so there is
  nothing worth keeping and keeping it would mean keeping something that goes
  stale on the next keystroke. `gapRow` is derived the same way and for the same
  reason. It follows that a run never spans a silence — folding a heartbeat
  would take the gap off the screen — and that the top bar counts what was
  folded: a row standing for four hundred lines without saying so is the same
  lie as a truncation nobody logged.
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
