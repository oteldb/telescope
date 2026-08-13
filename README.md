# telescope [![Go Reference](https://img.shields.io/badge/go-pkg-00ADD8)](https://pkg.go.dev/github.com/oteldb/telescope#section-documentation) [![alpha](https://img.shields.io/badge/-alpha-orange)](https://go-faster.org/docs/projects/status#alpha)

A terminal log viewer for the [oteldb](https://github.com/oteldb/oteldb) project.

It streams logs from `journalctl`, `kubectl`, `docker` or any command, locally
or through `ssh`, and from a VictoriaLogs or Loki database, directly or through
a Grafana datasource. Structured lines are pretty printed with
[go-faster/pl](https://github.com/go-faster/pl); anything else passes through
with timestamps, levels, numbers and paths highlighted.

![telescope](demo/demo.gif)

```
go run ./cmd/telescope
```

Nothing has to be configured: the start screen lists the units, pods and
containers it can find. Declaring the places you read daily saves picking them
again — see [Configuration](#configuration).

## Sources

| type | reads |
| --- | --- |
| `journalctl` | a systemd unit, or the whole journal |
| `kubectl` | a pod, a container, or a label selector |
| `docker` | a container |
| `command` | anything writing to stdout |
| `victorialogs` | a [LogsQL][logsql] query, over HTTP |
| `loki` | a Loki endpoint, over HTTP; the filter selects the stream |

The first four run a command, on this machine unless you name an ssh host with
`ctrl+o` (or `via: ssh://host`). The last two query a database over HTTP and
need a URL and usually a token; see [Endpoints](#endpoints).

Targets are written the same way in the prompt and in the config file:

| syntax | means |
| --- | --- |
| `kubelet` | a system unit |
| `user/syncthing` | a unit of the user manager (`journalctl --user`) |
| `pod` | a pod in the default namespace |
| `oteldb/oteldb-0` | a pod in a namespace |
| `oteldb/oteldb-0:clickhouse` | one container of that pod |
| `oteldb/deploy/api` | a workload: `deployment`, `statefulset`, `daemonset`, `job`, … |
| `oteldb/app=oteldb` | a label selector (`kubectl logs -l`) |

Naming a workload rather than a pod survives a restart.

### Merging

Several places can be read as one timeline, each line tagged with where it came
from. Pick them on the start screen with `ctrl+a`, or declare a group:

```yaml
places:
  - {name: api, type: docker, container: api}
  - {name: worker, type: docker, container: worker}
groups:
  - name: prod
    places: [api, worker]
    range: 1h
```

```
  api     10:29:09.660  POST /orders 201
  worker  10:29:09.662  job started id=91
  api     10:29:09.671  GET /health 200
```

The places need not be alike — `kubectl` on a cluster, `journalctl` over ssh,
and a database in another region read as one stream. The window, tail and follow
belong to the group. A place that fails to open is reported where its lines
would have been; the rest keep streaming.

When every place in a group leaves the same thing open — four clusters and no
pod on any of them — picking the group asks once and gives the answer to all of
them.

### Time range

`ctrl+g` bounds the window, and shows what it resolves to:

| written | means |
| --- | --- |
| `1h`, `30m`, `7d` | a window ending now |
| `6h..1h` | one that has already closed |
| `today`, `yesterday` | since local midnight, and the day before |
| `10:00..12:00` | clock times today |
| `2026-01-02 10:00..12:00` | a date and time, or RFC 3339 |
| `all` | no bounds — the tail alone |

A range with an end is not followed. `kubectl` has no end bound, and a
free-form `command` has no range at all — bound it in the command itself.

## Keys

### Start screen

| key | |
| --- | --- |
| `↑` `↓`, `ctrl+p` `ctrl+n` | move through suggestions |
| `pgup` `pgdown`, `home` `end` | page, first, last |
| `tab` | accept the highlighted suggestion, else switch source type |
| `enter` | accept the highlighted suggestion, else go to the next step |
| `esc` | drop the highlight, leave the editor, go back a step, then quit |
| `ctrl+a` | pick a saved place to group; opening more than one merges them |
| `ctrl+r` | re-run the current listing, ignoring the cache |
| `ctrl+s` | toggle `sudo -n` |
| `ctrl+k`, `ctrl+x` | kubeconfig path, context (kubectl) |
| `ctrl+e` | pick the endpoint (victorialogs, loki) |
| `ctrl+o` | ssh host, empty for this machine |
| `ctrl+g` | time range |
| `ctrl+f` | toggle follow |
| `ctrl+t` | cycle tail: 100, 1000, 10000, all |
| `ctrl+c` | quit |

Suggestions are matched fuzzily, so `ksdns` finds
`kube-system/coredns-7d764666f9-5gq2n`. A query may also carry `field:value`
terms, in the shape GitHub and Sourcegraph use — `ns:oteldb -kind:pod api` —
over `ns` (`namespace`), `kind` (`type`), `name` (`pod`, `unit`), `container`,
`image`, `scope`, `state`, depending on what is being listed.

Everything is listed wherever the logs will be read, with the same privileges
and kubeconfig, so picking a remote node lists that node's units and containers.
Results are cached for the session; `ctrl+r` refreshes. Hosts, kubeconfigs and
targets you have opened before are offered first.

### Log view

| key | |
| --- | --- |
| `↑` `k`, `↓` `j` | move |
| `pgup` `pgdown` | page |
| `home` `g`, `end` `G` | ends of the list |
| `H`, `L` | top, bottom of the window |
| `←` `→` | scroll sideways, `0` resets |
| `enter` | open the entry |
| `/` | filter (`enter` applies, `esc` cancels) |
| `tab` | complete the field or value being typed, `↑` `↓` pick |
| `?` | the filter language, written out |
| `f` | toggle follow |
| `l` | cycle minimum level: all, info, warn, error |
| `t` | cycle the time column: clock, full date, age |
| `esc` | back to the picker |
| `q`, `ctrl+c` | quit |

The filter is a small query:

| written | means |
| --- | --- |
| `reset`, `"connection reset"` | a case-insensitive substring |
| `/res[ei]t/` | a regular expression, always case-insensitive |
| `pod=api-7`, `pod!=api-7` | a field, compared exactly |
| `pod~api` | a field, matched as a regular expression |
| `level>=warn` | severity |
| `a b`, `a and b`, `a or b`, `not a`, `-a`, `(a b) or c` | the rest |

Terms next to each other are and-ed, so a query that is only words reads as the
grep it replaces. Words match what a line **says** — for a structured line its
values, not the JSON around them — and the labels its source reported.

A field is looked up where the line named it, then among those labels, then
under the names a record is read as, so `msg` and `trace_id` work whatever the
shipper called them; `source` is the merge tag and `stream` is `stdout` or
`stderr`. `service.name` also finds a label that arrived as `service_name`.
Values are compared without case. A line that reported no level passes no
`level` comparison at all.

`tab` completes: field names where a bare word is, and the values under one once
it is named. What has already been read is offered first, and a log database is
also asked what else it holds, so `pod=` completes to a pod no line has
mentioned yet. Those are marked *on record, not in these lines* — the database
has it, the lines here do not, and filtering by one will select nothing until
such a line arrives.

That is also what a list with nothing in it says: `no lines match` where the
value is the reason, and `no lines match · no line carries service_name` where
the field is. A field none of the lines have is a filter that was never going
to match, however the value is spelled.

Over a log database the filter is also sent to the server, as much of it as the
server can answer, and the view is rebuilt from the result rather than filtered
out of everything the database holds.

Over a database the tail is where reading starts and not how far back it goes:
scroll to the first line and the ones before it are fetched, as many at a time
as the tail asks for. The header says so — `reading older` while a page is on
its way, `at the start` once there is nothing before it, `holding all it can`
when the view is full. A command has already written what it wrote, so there
nothing can be fetched and `tail` is the whole of the history.

Every line's time is drawn in a column to the left, which `←`/`→` never scrolls
away, and `t` cycles how it is written: the clock time, the whole instant with
its date and offset, or how long before the view opened the line was written.
That last one is measured from when the lines were fetched and not from now, so
a list being read does not renumber itself. A line a database reported the
severity of rather than saying it itself gets a level column beside the time.

A line written inside a trace is marked `◆` beside its time, colored by the
trace it belongs to, so the lines of one request stand out among everything
else that was happening; `enter` has the id, and `f` there narrows to it.

A line whose rendering spans several lines, such as a stacktrace, takes one row
marked `⏎N`; `enter` shows the whole thing. Rows are shaded by the second they
happened in, so a burst reads as one block; where a log went quiet for a minute
or more the two lines are separated by how long the silence lasted and the
instant it ended.

### Entry view

| key | |
| --- | --- |
| `↑` `k`, `↓` `j`, `pgup` `pgdown`, `home` `g`, `end` `G` | select |
| `y` | copy the selected value, as it arrived |
| `Y` | copy the whole entry |
| `o` | open the selected value: a URL in a browser, a file in `$EDITOR` |
| `f` | narrow the list by the selected value |
| `?` | the filter language, written out |
| `esc`, `enter`, `backspace` | back |
| `q`, `ctrl+c` | quit |

The entry is shown whole: time, level, trace and body, then the labels of the
stream and of the line, the full rendering, the structured fields and the raw
bytes it arrived as.

`o` opens what a value points at. A file goes to `$VISUAL` or `$EDITOR` at the
right line, with telescope standing aside until it exits; only `http` and
`https` URLs are handed to the browser. The path a logger writes is rarely the
path the file is at, so `o` tries it as written, then relative to the
repository, then looks for a tracked file it is the tail of — which is why
`caller` opens the right file in a fresh checkout. Where the value names no
file, `o` reads it as a stacktrace and opens the innermost frame that is in the
checkout; Go, zap, the JVM, CPython and V8 traces are all understood, and an
entry carrying one under `stacktrace`, `stack_trace`, `stack`,
`exception.stacktrace` or `error.stack` opens it from any of its rows.

`f` takes the value back to the list as a filter term, anded onto whatever is
already there — read one entry, spot the pod, press `f`.

The clipboard is the one on the machine telescope runs on (`wl-copy`, `xclip`,
`pbcopy`). Over ssh it falls back to OSC 52, which under tmux needs
`set -g set-clipboard on`.

## Configuration

Places live in `$XDG_CONFIG_HOME/telescope/config.yaml`, by default
`~/.config/telescope/config.yaml`. When the file declares any, the start screen
opens on a picker; `tab` leaves it for the manual flow.

```yaml
places:
  # Named in full: opens straight into the logs.
  - name: navidrome
    type: docker
    container: navidrome
    tail: 50

  # A cluster reachable only as root on a node that refuses root logins.
  # No pod named, so picking it opens the prompt with the rest filled in.
  - name: k3s-ops
    type: kubectl
    via: ssh://node1
    kubeconfig: /root/.kube/ops.kubeconfig
    context: admin@ops
    sudo: true

  - name: syncthing
    type: journalctl
    unit: user/syncthing
    query: error

groups:
  - name: prod
    places: [k3s-ops, navidrome]
```

A place does not have to be complete: one that pins a host, a kubeconfig and
`sudo` but no pod fills in what it knew and stops at the step still missing.

| field | default | |
| --- | --- | --- |
| `name` | required | shown in the picker, and how a group names it |
| `type` | required | `journalctl`, `kubectl`, `docker`, `command`, `victorialogs`, `loki` |
| `via` | `local` | `local`, or `ssh://[user@]host` |
| `unit` | | systemd unit, `user/` prefix accepted |
| `user_unit` | `false` | read the user journal |
| `namespace` | | Kubernetes namespace |
| `target` | | pod name or label selector, `ns/pod:container` accepted; the LogsQL query for VictoriaLogs |
| `container` | | container, for kubectl or docker |
| `kubeconfig` | | passed as `--kubeconfig` |
| `context` | | passed as `--context` |
| `args` | | command line, for `type: command` |
| `sudo` | `false` | run the collector under `sudo -n` |
| `url` | required for a database | the base the API paths hang off |
| `datasource` | | Grafana datasource uid, appended to `url` as a proxy path |
| `token` | | where the bearer token is read from |
| `tenant` | | `AccountID:ProjectID` for VictoriaLogs, the org id for Loki |
| `headers` | | anything else the database or its proxy needs |
| `proxy` | | reach this database through `http://…` or `socks5h://…` |
| `insecure` | `false` | skip TLS verification |
| `range` | | the window read: `1h`, `today`, `6h..1h` |
| `tail` | `1000` | lines of history to open with, `0` for all; over a database it is also the size of a page |
| `follow` | `true` | keep streaming |
| `query` | | pre-fills the filter, and is what selects a Loki stream |

A group takes `name`, `places`, and the same `range`, `tail`, `follow` and
`query`. Fields a place cannot use — a `command` with a `token`, a database
reached `via: ssh://…` — are reported as mistakes in the file rather than
ignored, as is a key that is not a key at all.

### Endpoints

```yaml
places:
  # A Grafana datasource: the URL is the Grafana, and telescope resolves the
  # datasource proxy path against it.
  - name: prod
    type: victorialogs
    url: https://grafana.example.com
    datasource: adm5h5433d8hsa
    token:
      env: GRAFANA_TOKEN
    tenant: "1:1"

  # The token from a keyring, a password manager, anything with a CLI.
  - name: staging
    type: victorialogs
    url: https://logs.staging.example.com
    token:
      exec: secret-tool lookup service telescope account staging

  # Loki: no query of its own, the filter selects the stream.
  - name: prod api
    type: loki
    url: http://127.0.0.1:3100
    query: app=api
```

An endpoint that needs no credentials needs no declaration either: `ctrl+e`
takes a URL, and the ones typed there are remembered. Anything needing a token
belongs in the config file, since the prompt writes what it is given to the
history in plain text.

`tail` becomes the query's limit, the time range its bounds, and `follow` keeps
it open. VictoriaLogs takes [LogsQL][logsql] in `target`, sent as written; it
has a match-all, so an empty one tails the whole database.

[LogQL][logql] has no match-all — every query selects streams by label — so a
Loki place names no query at all and reads nothing until the filter names a
label. `app=api pod!=api-7 error` is sent as
`{app=~"(?i)api", pod!~"(?i)api-7"}`, and `error` is applied here. Label names
that are not Prometheus identifiers are sent quoted, so `service.name=api`
works where the server understands it.

`proxy` is per place, so one database behind a corporate proxy does not push
every other request through it. Unset, the environment applies (`HTTPS_PROXY`,
`ALL_PROXY`, `NO_PROXY`). It is also how a database reachable only from a
bastion is reached — `ssh -D 1080 bastion` and `proxy: socks5h://127.0.0.1:1080`.

**The token is named, never written**, so the config file stays shareable:

| `token:` | |
| --- | --- |
| `env: NAME` | an environment variable |
| `file: PATH` | a file, `~` accepted |
| `exec: …` | a command, whose first line of output is the token |

`exec` takes a command line, run through `sh -c` so a pipe works, or a list of
arguments, which needs no quoting:

```yaml
    token:
      exec: pass show grafana/prod | head -1
    token:
      exec: ["bw", "get", "password", "grafana-prod"]
```

That covers a keyring, `pass`, Bitwarden, 1Password — anything with a CLI. It
runs once per run, before the screen is taken over, so a manager that needs a
passphrase can still ask. A place whose token cannot be read says so where it is
chosen, and takes down nothing else.

### History

Hosts, kubeconfigs and targets you open are written to
`$XDG_STATE_HOME/telescope/history.yaml`, by default
`~/.local/state/telescope/history.yaml`, twenty of each, and offered first next
time. Targets are remembered per cluster and per host: a pod name means nothing
on another kubeconfig. The config file you write is never rewritten.

## Notes

**ssh** runs through `ssh(1)`, so `~/.ssh/config`, `ProxyJump`, the agent and
`known_hosts` all apply. It runs with `BatchMode=yes`, so an unknown host key or
a passphrase without an agent fails with a message instead of hanging.

**sudo** is `sudo -n` and needs `NOPASSWD`. It prefixes the collector directly,
so a sudoers rule may name the tool itself:

```
you ALL=(ALL) NOPASSWD: /usr/bin/kubectl
```

The kubeconfig is passed as `--kubeconfig=` rather than through the environment
so that such a rule keeps working. A free-form `command` still needs a shell.

**journalctl** is run with `-o cat`, which drops the journal's own timestamps.
That suits services logging structured lines and loses time information for
plain ones.

**Listing user units** over ssh needs a session bus, so it works when the
account has an active session or lingering enabled. When it fails, system units
still complete and `user/name` can be typed by hand.

A listing is given five seconds, and up to 200 000 lines are kept per stream;
older ones are dropped and counted in the top bar.

[logsql]: https://docs.victoriametrics.com/victorialogs/logsql/
[logql]: https://grafana.com/docs/loki/latest/query/
