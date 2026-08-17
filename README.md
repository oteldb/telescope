# telescope [![Go Reference](https://img.shields.io/badge/go-pkg-00ADD8)](https://pkg.go.dev/github.com/oteldb/telescope#section-documentation) [![alpha](https://img.shields.io/badge/-alpha-orange)](https://go-faster.org/docs/projects/status#alpha) [![x](https://github.com/oteldb/telescope/actions/workflows/x.yml/badge.svg)](https://github.com/oteldb/telescope/actions/workflows/x.yml)

A terminal log viewer for the [oteldb](https://github.com/oteldb/oteldb) project.

It streams logs from `journalctl`, `kubectl`, `docker` or any command, locally
or through `ssh`, and from a VictoriaLogs or Loki database, directly or through
a Grafana datasource. A row is the message and then whatever tells the lines
apart — the method, the route, the status, how long it took — colored by what
each one means, with what did not fit counted at the end; what a whole stream
says the same way stays in the entry view. Text logs are pretty printed with
[go-faster/pl](https://github.com/go-faster/pl), with timestamps, levels,
numbers and paths highlighted.

![telescope](demo/demo.gif)

## Installation

A [release][releases] carries a binary for Linux, macOS and Windows on `amd64`
and `arm64`. It is one static file and depends on nothing — unpack it and put it
on `$PATH`:

```console
$ tar -xzf telescope_0.1.0_linux_amd64.tar.gz telescope
$ install -m755 telescope ~/.local/bin/
```

The same releases carry `.deb`, `.rpm`, `.apk` and Arch packages, and a
`checksums.txt` signed with [cosign][cosign]:

```console
$ cosign verify-blob --bundle checksums.txt.sigstore.json \
    --certificate-identity-regexp 'https://github.com/oteldb/telescope/.*' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    checksums.txt
```

With a Go toolchain, no release is needed:

```console
$ go install github.com/oteldb/telescope/cmd/telescope@latest
```

And from a checkout, which is also how it is developed:

```console
$ go run ./cmd/telescope
```

[releases]: https://github.com/oteldb/telescope/releases
[cosign]: https://docs.sigstore.dev/cosign/system_config/installation/

Nothing has to be configured: the start screen lists the units, pods and
containers it can find. Every screen writes its keys along the bottom, so this
file does not. Declaring the places you read daily saves picking them again —
see [Configuration](#configuration).

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
belong to the group. A place that fails to open, or stops reading, is reported
where its lines would have been; the rest keep streaming.

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

## Features

**The filter** (`/`) is a small query: `reset` or `"connection reset"` for a
substring, `/res[ei]t/` for a regular expression, `pod=api-7`, `pod~api`,
`level>=warn` for fields, and `and`, `or`, `not`, `-` and parentheses over the
lot. Terms next to each other are and-ed, so a query that is only words reads as
the grep it replaces. `?` writes the language out in full, and `tab` completes
field names and values — including the ones only the database has seen yet. Over
a log database as much of the filter as the server can answer is sent to it.

**Reading backwards.** The tail is where reading starts and not how far back it
goes: scroll to the first line and the ones before it are fetched. A database
answers directly, and journalctl, docker and kubectl are run again over the
window below the screen. A `command:` place is the exception — telescope did not
write that line and cannot bound it.

**An entry** (`enter`) is shown whole — the labels, the fields, the raw bytes.
`y` copies a value as it arrived, `f` narrows the list by it, and `o` opens what
it points at: a URL in the browser, a file in `$EDITOR` at the right line, or,
where the value is a stacktrace, the innermost frame that is in the checkout.
Go, zap, the JVM, CPython and V8 traces are understood.

**Traces.** `T` opens the trace a line was written inside and draws it as a
gantt; `f` goes back the other way, narrowing the list by the whole trace or by
the row the cursor is on in a span. `/` filters the chart itself, in the same
language and with the same completion as the log filter, over what the spans
say: their service, their name and whatever they were labeled with. What holds
up a match is kept, so the tree still says who called whom.

**Which stream wrote it.** A view reading more than one — the containers of a
pod, the pods of a deployment, the services of a namespace, the places of a
group — names every line down the left, in a color that stays with the stream
for as long as the view is open. The name is whichever label actually differs,
cut down to the part that does, and a view reading one thing has no such column.
`kubectl logs` on a deployment tails every pod at once and writes whichever
spoke first; their lines are put back in time order as they arrive.

**Repetition and silence.** A line repeated straight after itself is drawn once
with `×n`, and a gap in the log is drawn as the gap it is.

**Log volume** is drawn above the list as bars over time, stacked by severity,
with the bucket the cursor is reading marked underneath. It counts the lines
that have been read and follows the filter in force, so it never disagrees with
the list below it. `v` folds it away when the rows are wanted for the log.

### Traces from the command line

```console
$ telescope trace --from https://tempo.example.com 4bf92f3577b34da6a3ce929d0e0e4736
$ telescope trace --from prod 4bf92f3577b34da6a3ce929d0e0e4736
$ telescope trace --from prod
$ telescope trace ./saved.json
$ curl -s "$TEMPO/api/traces/$ID" | telescope trace -
```

`--from` names a trace store, either as a url or as the name of a place that
declares one, and the argument is then the trace id. With no argument it opens a
search of that store instead — a form over service, operation, tags and
duration, which `alt+t` on the start screen opens too. The store is asked what
it holds, so the services, the operations and the tag keys and values are
offered rather than remembered, as far as that store will say. Without `--from` the
argument is a file holding a response already, or `-` to read one on standard
input.

Two APIs are read, and a store says which it speaks: Tempo's — the one oteldb
and Grafana's Tempo datasource speak — and Jaeger's query API, which Jaeger and
VictoriaTraces serve. For a url, `--api tempo` or `--api jaeger` says so; a
place says it in the config. A file says nothing and needs to: which format it
holds is worked out from what comes out of it, and OTLP arrives as JSON or as
protobuf with both understood.

## For an agent

`telescope mcp` speaks the Model Context Protocol on stdin and stdout, so a
coding agent can read the same places the screen does:

```console
$ claude mcp add telescope -- telescope mcp
```

Any MCP client will do; the command takes no arguments and reads the same
config file.

Five tools. `places` lists what the config declares, what speaks at each and
whether it opens as it stands. `summary` counts a window rather than listing it
— lines by level, where the volume went, the messages that repeat most, the
values of a field — which is the cheap first question about an incident.
`logs` reads the lines, through the same filter language the `/` prompt takes.
`fields` and `field_values` say what can be filtered on, out of the database's
own index.

What comes back is written for something that reads rather than looks: labels
every line shares are hoisted out and said once, repetitions are folded with a
count, and an answer that was cut says so and says what to ask instead. The
counts a screen would show by scrolling are reported as numbers.

A tool names a place the config declares, and none of them takes a command
line. An agent reaches what the file already said telescope may run, over the
same ssh hosts and behind the same tokens, and nothing else — the guarantee the
start screen gives a person. Nothing here writes.

## Configuration

Places live in `$XDG_CONFIG_HOME/telescope/config.yaml`, by default
`~/.config/telescope/config.yaml`. When the file declares any, the start screen
opens on a picker; `tab` leaves it for the manual flow.

`telescope init` writes the first one. It offers what this machine already runs
— its containers, its units, the clusters its kubeconfigs name, the hosts its
ssh config does — and asks about each; `--yes` takes them all without asking and
`--print` writes the file to standard output instead of to disk. It will not
replace a config that is already there unless told to with `--force`.

A Grafana comes in the same way, as a place for each of its Loki, VictoriaLogs
and Tempo datasources:

```console
telescope init --grafana https://grafana.example.com --grafana-token env:GRAFANA_TOKEN
telescope init --grafana-provisioning /etc/grafana/provisioning/datasources
```

The token is named — `env:NAME`, `file:PATH` or `exec:COMMAND` — rather than
written out, and the config it leaves behind names it the same way.

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

A group takes `name`, `places`, and the same `range`, `tail`, `follow` and
`query`. Fields a place cannot use — a `command` with a `token`, a database
reached `via: ssh://…` — are reported as mistakes in the file rather than
ignored, as is a key that is not a key at all.

### What a key accepts

Every key is declared once, in the code that reads it, and telescope publishes
that declaration as a [JSON Schema][schema]. Point the file at it and an editor
completes the keys, says what each one means and marks what it does not accept:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/oteldb/telescope/main/config.schema.json
places:
  - name: navidrome
```

The `$schema` key works too, for an editor that reads it, and telescope accepts
it as the annotation it is. `telescope schema` writes the same document to
standard output, for a schema store or an editor that wants a local copy.

[schema]: https://github.com/oteldb/telescope/blob/main/config.schema.json

### Traces

A place can say where its traces are read from, which `telescope trace --from
<name>` then refers to by name:

```yaml
places:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    token:
      env: LOGS_TOKEN
    traces: https://tempo.example.com
```

A url on its own is a Tempo. A store that speaks Jaeger's query API — Jaeger
itself, or VictoriaTraces — says so:

```yaml
places:
  - name: prod
    type: victorialogs
    url: https://logs.example.com
    traces:
      url: https://victoria.example.com/select/jaeger
      type: jaeger
```

Either way the store borrows the place's token, tenant, proxy and TLS settings,
since a system's traces usually sit behind the same door as its logs.

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
`env:` an environment variable, `file:` a file with `~` accepted, or `exec:` a
command whose first line of output is the token.

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
passphrase can still ask.

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
