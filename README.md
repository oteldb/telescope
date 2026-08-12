# telescope

A terminal log viewer for the [oteldb](https://github.com/oteldb/oteldb) project.

It streams logs from `journalctl`, `kubectl`, `docker` or any command, locally
or through `ssh`, and from a VictoriaLogs or Loki database, directly or through
a Grafana datasource. It pretty prints them with [go-faster/pl](https://github.com/go-faster/pl).
Structured lines are rendered by level and field; anything unstructured passes
through with timestamps, levels, numbers and paths highlighted.

```
go run ./cmd/telescope
```

## Places

A **place** is somewhere logs can be read from: what speaks there, how to get to
it, and what it takes to be let in.

| type | reads |
| --- | --- |
| `journalctl` | a systemd unit, or the whole journal |
| `kubectl` | a pod, a container, or a label selector |
| `docker` | a container |
| `command` | anything writing to stdout |
| `victorialogs` | a [VictoriaLogs](https://docs.victoriametrics.com/victorialogs/) query, over HTTP |
| `loki` | a [Loki](https://grafana.com/oss/loki/) query, over HTTP |

The first four run a command, on this machine unless the place is reached over
ssh (`ctrl+o`, or `via: ssh://host` in the config file) — then every one of them
runs through `ssh(1)` instead. Naming a host is what asks for ssh: there is no
step that makes you say "local" first.

`victorialogs` and `loki` run no command at all. They query a log database over
HTTP, so `via:`, `sudo` and the kubeconfig mean nothing to them; what they need
instead is a URL and, usually, a token. Such a place is dialed rather than
entered, so it says `proxy:` where the others say `via:`.

A **group** is several places read as one stream. See [Merging](#merging).

Targets use a compact syntax, the same in the prompt and in the config file:

| syntax | means |
| --- | --- |
| `kubelet` | a system unit |
| `user/syncthing` | a unit of the user manager (`journalctl --user`) |
| `pod` | a pod in the default namespace |
| `oteldb/oteldb-0` | a pod in a namespace |
| `oteldb/oteldb-0:clickhouse` | one container of that pod |
| `oteldb/deploy/api` | a workload: `deployment`, `statefulset`, `daemonset`, `job`, … |
| `oteldb/app=oteldb` | a label selector (`kubectl logs -l`) |

A leading segment naming a resource kind is read as a kind, not a namespace,
so `deploy/api` and `oteldb/deploy/api` both mean what they look like. Naming a
workload rather than a pod survives a restart, at the cost of `kubectl` picking
one of its pods.

### Merging

An incident rarely stays inside one place. A **group** reads several at once and
shows them as a single timeline, each line tagged in the gutter with where it
came from:

```
  api     10:29:09.660  POST /orders 201
  worker  10:29:09.662  job started id=91
  api     10:29:09.671  GET /health 200
```

Pick places on the start screen with `ctrl+a` and open them together, or declare
the combination you reach for daily:

```yaml
places:
  - name: api
    type: docker
    container: api
  - name: worker
    type: docker
    container: worker
groups:
  - name: prod
    places: [api, worker]
    range: 1h
```

The window, tail and follow belong to the group — it is one view, and a view has
one timeline — so a `range:` on a place it names is not used.

A group is an environment more often than it is two containers, and the places
in one need not be alike: `kubectl` on a cluster, `journalctl` over ssh on a
node, and a VictoriaLogs instance in another region read as one timeline, each
reached its own way.

Each place is already in order by itself, so ordering the whole is a merge over
their heads: the oldest pending line is the next one out. Places that report a
time out of band are ordered by it, and merged `docker` and `kubectl` are asked
for the timestamps they otherwise leave out; for the rest it is the time inside
the line, and for a line with no time at all, the one before it from the same
source. `journalctl` is read with `-o cat`, which is the message and nothing
else, so a merged journal is ordered by when its lines arrive.

A place that goes quiet is not waited on beyond 250 ms — one idle service must
not hold the view back — so a line arriving after that lands where it arrives
rather than where its timestamp belongs. A place that fails to open is reported
in place of its lines: a group of four environments is not as available as its
worst one.

What a place must name is the API's rule rather than telescope's: `kubectl`
cannot stream without a pod or a selector, `docker` without a container, and
Loki without a stream selector. `journalctl` with no unit is the whole journal,
and LogsQL has a match-all, so a VictoriaLogs place needs nothing at all — which
is what lets a group of four regions be four lines with no query written
anywhere. The query is then typed once, into the view.

A group cannot stop to ask once per place, but it can ask once. When the places
it names leave the same thing open — four clusters and no pod on any of them —
picking the group stops on the prompt, and what is typed there is given to every
one of them:

```yaml
places:
  - {name: ops, type: kubectl, kubeconfig: ~/.kube/ops.yml}
  - {name: obs, type: kubectl, kubeconfig: ~/.kube/obs.yml}
groups:
  - name: both
    places: [ops, obs]      # neither names a pod
```

```
both  ▸  merge ops + obs
❯ flux-system/deploy/kustomize-controller
```

The same deployment usually has the same name on every cluster, which is the
whole reason this is one question rather than four. A place that already names
its own target keeps it and is not asked about, so a group may mix the two. What
a group cannot do is ask for two different things at once: places that leave
different kinds of thing open — a pod on one and a LogQL selector on another —
are a mistake in the file, since one answer cannot be both.

### Labels

A `docker logs` line is a line. A line out of a log database is a line and
everything the database knows about it, and that is often where the interesting
part is: a Loki entry can be nothing but `artifact up-to-date`, with the pod,
the container, the namespace and the severity in its label set.

Telescope keeps both, and tells them apart:

- **source labels** describe the stream — the endpoint and the query, the
  container, the unit, the namespace, the ssh host. They are the same for every
  line in it.
- **labels** are what the source reported beside the line itself: for Loki, the
  labels of the stream it was found in plus its structured metadata.

The log list stays a list. What labels buy it is two columns to the left of the
message, drawn outside the horizontal scroll so `←`/`→` never takes them away:

```
  15:16:36.357 ERROR read_request_line: Client (fd: 4) closed socket
  15:16:38.402 INFO  artifact up-to-date with remote revision: '0.40.0'
```

The time is the one the source reported, and the level comes from `level`,
`severity`, `detected_level` or their spellings. Both columns appear only once a
line needs them, and stay for the rest of the view so the text does not shift as
lines arrive. A structured line is left to the formatter, which renders its own
time and level; the columns stay reserved but empty, so both kinds line up.

Everything else is in the entry view, under `source` and `labels`, and reachable
from the filter: `/` matches the labels along with the line, so
`k8s_pod_name~source-controller` finds lines that never mention it.

There, a value is drawn by what its key says it is: a severity as a severity, a
`trace_id` or `span_id` marked so it can be picked out of thirty OTEL
attributes, and everything else through the same highlighting the log list uses.
A trace or span id found in a label is the entry's, so it shows in the header
beside the time as well.

Values are somebody else's bytes, and are shown rather than obeyed: control
characters are escaped visibly — `\n`, `\t`, `\e[2J`, `\xff` for a byte that is
not a character at all — so nothing in a log line can break a row in two or
repaint the screen. Rendered lines keep the colors a collector wrote, because
that is what makes `docker logs` look like `docker logs`, and lose every other
escape sequence.

### Time range

A line count is not a time range. `ctrl+g` bounds the window instead, on the
prompt and at the query step, and shows what the window resolves to:

| written | means |
| --- | --- |
| `1h`, `30m`, `7d` | a window ending now |
| `6h..1h` | one that has already closed |
| `today`, `yesterday` | since local midnight, and the day before |
| `10:00..12:00` | clock times today |
| `2026-01-02 10:00..12:00` | a date and time, or RFC 3339 |
| `all` | no bounds — the tail alone |

The spec is kept as written and resolved when the place opens, so `1h` means
the last hour on every run rather than the hour that had passed when it was
typed. A range with an end is a window that has already happened, so nothing is
followed however the toggle is set.

Each collector spells it in its own terms: `journalctl --since/--until`,
`docker logs --since/--until`, `kubectl logs --since-time`, and `start`/`end`
on a database query. `kubectl` has no end bound, so asking for one is an error
rather than a wider window than was asked for, and a free-form `command` is
whatever was typed — bound it in the command itself.

## Keys

### Start screen

| key | |
| --- | --- |
| `↑` `↓`, `ctrl+p` `ctrl+n` | move through suggestions |
| `pgup` `pgdown` | page through them |
| `home` `end` | first, last suggestion |
| `tab` | accept the highlighted suggestion, else switch collector |
| `shift+tab` | previous collector |
| `enter` | accept the highlighted suggestion, else go to the next step |
| `esc` | drop the highlight, leave the editor, go back a step, then quit |
| `ctrl+a` | pick a saved place to group; opening more than one reads them together |
| `ctrl+r` | re-run the current listing, ignoring the cache |
| `ctrl+s` | toggle `sudo -n` |
| `ctrl+k` | edit the kubeconfig path (kubectl) |
| `ctrl+x` | edit the context (kubectl) |
| `ctrl+e` | pick the endpoint (victorialogs, loki) |
| `ctrl+o` | set the ssh host, empty for this machine |
| `ctrl+g` | edit the time range |
| `ctrl+f` | toggle follow |
| `ctrl+t` | cycle tail: 100, 1000, 10000, all |
| `ctrl+c` | quit |

### Log view

| key | |
| --- | --- |
| `↑` `k`, `↓` `j` | move |
| `pgup` `pgdown` | page |
| `home` `g`, `end` `G` | ends of the whole list |
| `H`, `L` | top, bottom of the visible window |
| `←` `→` | scroll sideways, `0` resets |
| `enter` | open the entry |
| `/` | filter (`enter` applies, `esc` cancels) |
| `f` | toggle follow |
| `l` | cycle minimum level: all, info, warn, error |
| `esc` | back to the picker |
| `q`, `ctrl+c` | quit |

The filter is a small query:

| written | means |
| --- | --- |
| `reset`, `"connection reset"` | a case-insensitive substring |
| `/res[ei]t/` | a regular expression, always case-insensitive |
| `pod=api-7`, `pod!=api-7` | a field, compared exactly |
| `pod~api` | a field, matched as a regular expression |
| `level>=warn` | severity, which is the one thing that is ordered |
| `a b`, `a and b`, `a or b`, `not a`, `-a`, `(a b) or c` | the rest |

Terms sitting next to each other are and-ed, which is why a query that is only
words reads as the grep it replaces.

Words and regular expressions match what a line **says**: for a structured line
its values, not the JSON around them, and for anything else the line itself.
Searching for `level` does not match every line that has one. The labels the
source reported are searched too, since the list has no room to show them.

A field is looked up where the line named it first, then among those labels,
then under the names a record is read as, so `msg` and `trace_id` work whatever
the shipper called them; `source` is the merge tag and `stream` is `stdout` or
`stderr`. A key is matched exactly — a field name is what it is — while values
are compared without case, because a pod name typed in a hurry is still that
pod. A line that never reported a level passes no `level` comparison at all: an
unlevelled line is not quietly an info one.

Every term asks about one line and nothing else, which is what lets one query
mean the same thing across a group of several places — and what lets a place
that can answer part of it be asked. A query that does not parse is not applied:
the prompt stays open on what was typed and says why, rather than filtering by
something else.

### Pushed down

Applying a filter over a log database asks the database again, with as much of
the query as it can be asked, and rebuilds the view from the answer. Filtering a
group of four VictoriaLogs instances is four queries, not four tails of
everything they hold.

What survives translation is a narrowing and never a change of meaning. A term
that cannot be translated with certainty is dropped rather than approximated —
`level>=warn` is read here from a dozen spellings and from severity numbers, and
no one field holds it — and the filter still runs over everything that comes
back. Only a conjunction may lose a term that way: dropping a branch of an `or`,
or the operand of a `not`, would exclude lines the filter keeps, so those are
not pushed at all. The result is the same either way; the difference is how much
came over the wire.

| written | sent to VictoriaLogs |
| --- | --- |
| `reset` | `*:~"(?i)reset"` |
| `pod=api-7` | `pod:~"(?i)^api-7$"` |
| `pod!=api-7` | `-pod:~"(?i)^api-7$"` |
| `level>=warn reset` | `*:~"(?i)reset"`, the level applied here |
| `level>=warn or reset` | nothing: the `or` cannot lose a branch |

A query the place itself names bounds all of this: it is sent as written, and
what the filter adds narrows it further. Loki is sent its selector and nothing
more — a LogQL line filter reads the line and not the labels beside it, so
pushing one there would drop lines this filter keeps.

`l` cycles the minimum level over what a line says about itself and what its
source said for it, so a Loki view filters by `detected_level` even though no
line mentions a level.

A line whose rendering spans several lines, such as a stacktrace, occupies one
row marked `⏎N`; `enter` shows the whole thing.

Rows are shaded by the second they happened in, so a burst of lines reads as one
block and a gap between them shows as a seam. It is not striping for its own
sake: on a busy stream a band is a group, and on a quiet one, where every line
falls in a second of its own, it comes out as plain alternating rows. Which band
a line belongs to is settled when it arrives, so scrolling and filtering never
repaint what is already on screen. The row under the cursor takes a violet to
magenta fade instead — a line brings colors of its own, and the background is
laid under them without disturbing them.

### Entry view

| key | |
| --- | --- |
| `↑` `k`, `↓` `j`, `pgup` `pgdown`, `home` `g`, `end` `G` | select |
| `y` | copy the selected value |
| `Y` | copy the whole entry as it arrived |
| `o` | open the selected value: a URL in a browser, a file in `$EDITOR` |
| `f` | narrow the list by the selected value |
| `esc`, `enter`, `backspace` | back |
| `q`, `ctrl+c` | quit |

The entry is shown whole: its time, level, trace and body, then `source` and
`labels` (see [Labels](#labels)), the full rendering with its stacktrace, the
structured fields of the line, and the raw bytes it arrived as.

The cursor moves between those rows rather than between lines, so a value that
wraps across the frame, and a stacktrace that fills it, are each one thing to
land on. `y` copies the selected value as it was received: what the screen shows
is escaped, wrapped and colored, and none of that is wanted anywhere the value
is going next — a path drawn with `\e` in it opens in no editor. `Y` copies the
whole entry from wherever the cursor is standing, which is the same value as `y`
on the `raw` row.

`o` opens what the selected value points at. An `http` or `https` URL goes to
the desktop browser; a file goes to `$VISUAL` or `$EDITOR`, at the right line,
with telescope standing aside until the editor exits. Only those two schemes
are opened — a log line is somebody else's bytes, and the desktop opener runs
whatever program a scheme happens to be registered to.

The path a logger writes is rarely the path the file is at. zap writes the
package-relative `ui/start.go`, a binary built on a CI runner writes that
machine's absolute path, and a container writes the path it had inside the
image. So `o` tries the path as written, then relative to the repository, and
failing both looks for a tracked file that path is the tail of — which is why
`caller` opens the right file in a checkout that has never seen the build
machine. The line comes from the value when a logger wrote `file.go:42`, and
from `code.line.number` beside `code.file.path` when it followed OTEL and kept
them apart; either way `o` on either row opens the same place.

`f` takes the selected value back to the list as a filter term, anded onto
whatever is already there — so reading one entry, spotting the pod it came from
and pressing `f` leaves the list showing that pod and nothing else. The term is
written as you would have typed it, quoting where the prompt needs it, and a
query already in force is parenthesized if it is an `or`: a jump narrows, and
`timeout or refused` picking up a term in one branch would widen. Where the
place is a log database, the term is pushed down and the lines are fetched
again rather than filtered out of what had already arrived.

Only rows worth narrowing by offer it. A timestamp belongs to one line and a
body usually carries an id, so `f` on those says there is nothing to do rather
than filtering the list down to the entry you are already reading.

The clipboard is the one on the machine telescope runs on, reached through
`wl-copy`, `xclip` or `pbcopy` as the session's display calls for. Where there
is no display — telescope run over ssh on the far side — the value goes to the
terminal itself with OSC 52, which under tmux needs `set -g set-clipboard on`.

## Completion

Suggestions are fetched once per step and filtered as you type. Matching is
fuzzy: the characters have to appear in order but need not be adjacent, so
`ksdns` finds `kube-system/coredns-7d764666f9-5gq2n`, and the characters that
matched are highlighted. What you typed literally sorts above the same letters
in another case, which sorts above a scattered match.

A query may also carry `field:value` terms, in the shape GitHub and
Sourcegraph use. They narrow the list before anything is matched; the rest of
the query still matches fuzzily.

```
ns:oteldb api            pods and workloads named api, in namespaces matching oteldb
kind:deployment        only workloads
-ns:kube-system        everything outside kube-system
container:             only the rows that name a container
scope:user error       user units, matching error
```

| collector | fields |
| --- | --- |
| `kubectl` | `ns` (`namespace`), `kind` (`type`), `name` (`pod`), `container` (`c`), `state` |
| `journalctl` | `name` (`unit`), `scope`, `state` |
| `docker` | `name`, `image`, `state` |

A term matches its field as a case-insensitive substring. A bare `field:` keeps
the candidates that have that field at all. Only these names are terms, so a
value that merely contains a colon, such as `oteldb/api-79c:migrate`, is still
searched for literally.

The terms are never sent as part of the target, but the ones that name a single
thing — `ns`, `kind`, `container`, `scope` — do fill in what the target leaves
unsaid, so pressing enter on `ns:oteldb kind:deploy api` reads `deploy/api` from
`oteldb` rather than from `default`. Whatever the target spells out itself wins.

Everything but the ssh hosts is listed **wherever the logs will be read**, with
the same privileges, kubeconfig and context the logs will use, so picking a
remote node lists that node's units and containers.

| field | from |
| --- | --- |
| ssh host | `~/.ssh/config`, `/etc/ssh/ssh_config` and their `Include`s, then `known_hosts` |
| unit | `systemctl list-units`, plus `systemctl --user` tagged `user/` |
| pod | `kubectl get pods -A`, with a row per container when a pod has several, and for every init container |
| workload | `kubectl get deployments,statefulsets,daemonsets -A`, likewise per container |
| container | `docker ps -a` |
| kubeconfig | the usual paths, plus whatever you type |
| context | `kubectl config get-contexts` of the chosen kubeconfig |

Results are cached for the session and preloaded ahead of use; `ctrl+r` forces
a refresh. Hosts, kubeconfigs and targets you have opened before are offered
first.

## Configuration

Declared places live in `$XDG_CONFIG_HOME/telescope/config.yaml`, by default
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

  # A log database. See Endpoints for the token and the proxy.
  - name: vl-eu
    type: victorialogs
    url: https://logs.eu.example.com
    token:
      env: VL_TOKEN

groups:
  - name: prod
    places: [k3s-ops, vl-eu]
```

A place does not have to be complete. One that pins a host, a kubeconfig and
`sudo` but no pod is valid: choosing it fills in what it knew and stops at the
step still missing, with the pods already listing for that cluster. The picker
marks those with what they will ask for. A group can only name the ones that ask
for nothing; see [Merging](#merging).

| field | default | |
| --- | --- | --- |
| `name` | required | shown in the picker, and how a group names it |
| `type` | required | `journalctl`, `kubectl`, `docker`, `command`, `victorialogs`, `loki` |
| `via` | `local` | `local`, or `ssh://[user@]host` to run the collector over ssh |
| `unit` | | systemd unit, `user/` prefix accepted |
| `user_unit` | `false` | read the user journal |
| `namespace` | | Kubernetes namespace |
| `target` | | pod name or label selector, `ns/pod:container` accepted; the query for a database |
| `container` | | container, for kubectl or docker |
| `kubeconfig` | | passed as `--kubeconfig` |
| `context` | | passed as `--context` |
| `args` | | command line, for `type: command` |
| `sudo` | `false` | run the collector under `sudo -n` |
| `url` | required for a database | the base the API paths hang off |
| `datasource` | | Grafana datasource uid, appended to `url` as a proxy path |
| `token` | | where the bearer token is read from; see [Endpoints](#endpoints) |
| `tenant` | | `AccountID:ProjectID` for VictoriaLogs, the org id for Loki |
| `headers` | | anything else the database or its proxy needs |
| `proxy` | | reach this database through `http://…` or `socks5h://…` |
| `insecure` | `false` | skip TLS verification |
| `range` | | the window read: `1h`, `today`, `6h..1h`; see [Time range](#time-range) |
| `tail` | `1000` | lines of history, `0` for all |
| `follow` | `true` | keep streaming |
| `query` | | pre-fills the filter |

A group takes `name`, `places`, and the same `range`, `tail`, `follow` and
`query` — which belong to the view it opens rather than to any place in it.

The fields a place cannot use are an error rather than a shrug: a `command` with
a `token`, or a `victorialogs` reached `via: ssh://…` instead of through a
`proxy`, is a mistake in the file and is reported as one. So is a key that is not
a key at all, since a config half understood opens half of what it names and
says nothing about the rest.

A malformed file, or a place naming an unknown type, is reported on the start
screen rather than ignored.

### Endpoints

A place of type `victorialogs` or `loki` is a log database: a URL, whatever it
takes to be let in, and optionally a query worth naming. What is the same for
every query — the URL, the datasource, the tenant and the credential — is
declared once, so a second query against it costs three lines and no secret.

A VictoriaLogs place needs no query at all, since LogsQL has a match-all; give
one only when it is worth a name of its own.

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

  # The database itself, with no Grafana in front of it, and a query worth
  # keeping.
  - name: prod api
    type: loki
    url: http://127.0.0.1:3100
    target: '{app="api"}'
```

Queries are remembered per database and offered back there.

`ctrl+e` picks the endpoint from all the declared ones that speak the chosen
database — a list, filtered as you type, not a chip per endpoint, since a
handful of environments fills a row of chips immediately.

An endpoint needs no declaration at all when it needs no credentials: the same
prompt takes a URL, and the ones typed there are remembered like ssh hosts. A
missing scheme is filled in — `https://`, or `http://` for a loopback address.
Anything needing a token belongs in the config file, since the prompt writes
what it is given to the history in plain text.

`proxy` is per place on purpose: one database behind a corporate proxy should
not push every other request through it. Unset, the proxy comes from the
environment (`HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY`), so a SOCKS setup that
already works for everything else needs nothing here. It is also how a database
reachable only from a bastion is reached — `ssh -D 1080 bastion` and
`proxy: socks5h://127.0.0.1:1080` — since `via:` runs a command and there is no
command to run.

**The token is named, never written.** The config file stays shareable, and the
secret keeps the permissions it already has. One of three, at most:

| `token:` | |
| --- | --- |
| `env: NAME` | an environment variable |
| `file: PATH` | a file, `~` accepted |
| `exec: …` | a command, whose first line of output is the token |

`exec` takes either a command line, run through `sh -c` so a pipe works, or a
list of arguments, which needs no quoting:

```yaml
    token:
      exec: pass show grafana/prod | head -1
    token:
      exec: ["bw", "get", "password", "grafana-prod"]
```

That covers a keyring (`secret-tool lookup …`), `pass`, Bitwarden, 1Password,
Proton Pass — anything with a CLI. The command inherits the terminal, and
telescope reads its config before taking over the screen, so a manager that
needs to ask for a passphrase can still ask. It runs once per run, per place,
and only for places that are declared; what it writes to stderr is kept for the
error, and it is given a minute to answer.

A place whose token cannot be read says so where it is chosen, and takes down
neither the config nor any group that does not name it.

The proxy comes from the environment, so an endpoint reachable only through
`HTTPS_PROXY` or `ALL_PROXY=socks5h://…` needs nothing further.

The target is the query, in that database's own language, **sent as written**.
LogsQL has a match-all, so an empty one tails everything the endpoint has — the
way a journal with no unit named does. LogQL has none: Loki selects streams by
label, and a query without a selector is a parse error from the server.
`field:value` there belongs to [LogsQL][logsql], not to telescope's own filter,
and no compact syntax is compiled into [LogQL][logql]: label names are whatever
the shipper wrote them as — `k8s_namespace_name` as readily as `namespace` — so
translating them would be a guess. `tail` becomes the query's limit, the time
range its bounds, and `follow` keeps it open.

| | VictoriaLogs | Loki |
| --- | --- | --- |
| history | `/select/logsql/query` | `/loki/api/v1/query_range` |
| window | `start` / `end` | `start` / `end`, in unix nanoseconds; the last 6h with no range |
| following | `/select/logsql/tail`, a live stream | the same query repeated, every 2s |
| tenant header | `AccountID` / `ProjectID` | `X-Scope-OrgID` |

VictoriaLogs holds new entries back briefly before serving them, so a followed
line appears a few seconds after it was written. Loki's own tail endpoint is a
websocket, which a Grafana datasource proxy will not upgrade, so following it is
a query repeated against a moving start instead.

The `_time` and `_msg` of a VictoriaLogs entry are rendered as the line's
timestamp and message rather than as fields named after its envelope; everything
else, `_stream` included, is left as it came, and a query can drop what it does
not want with `| drop _stream_id`. A Loki line is the application's own output
with nothing added, and its timestamp comes from the response rather than from
the line.

[logsql]: https://docs.victoriametrics.com/victorialogs/logsql/
[logql]: https://grafana.com/docs/loki/latest/query/

## History

Hosts, kubeconfigs and targets you open are written to
`$XDG_STATE_HOME/telescope/history.yaml`, by default
`~/.local/state/telescope/history.yaml`, twenty of each. They are offered first
in the matching suggestions, including values no listing reports, such as a
root-only kubeconfig path typed by hand.

Targets are remembered per cluster and per host, not per collector: a pod name
means nothing on another kubeconfig, and a container name nothing on another
node, so they are not offered there.

History is kept apart from the config so the file you write is never rewritten.

## Notes

**ssh** runs through `ssh(1)` rather than a Go client, so `~/.ssh/config`,
`ProxyJump`, the agent and `known_hosts` all apply. It runs with
`BatchMode=yes`: an unknown host key or a passphrase without an agent fails
with a message instead of hanging. Following forces a pty (`-tt`) so the remote
command is hung up when the view closes, at the cost of stderr folding into
stdout.

**sudo** is `sudo -n`, so it needs `NOPASSWD`. It prefixes the collector
directly, which means a sudoers rule may name the tool itself:

```
you ALL=(ALL) NOPASSWD: /usr/bin/kubectl
```

For that to keep working, the kubeconfig is passed as `--kubeconfig=` rather
than through the environment: `sudo env ...` and `sudo sh -c ...` would not be
covered by such a rule. A free-form `command` source is the exception and still
needs a shell.

**Naming a context** is the only way to use a kubeconfig whose
`current-context` is unset, which is why the context listing does not require
one. Note that `kubectl config` cannot tell a missing kubeconfig from one
without a context — it reports "current-context is not set" for both — so the
pod listing runs unguarded and lets `kubectl` name the real problem.

**Listing user units** over ssh needs a session bus, so the command sets
`XDG_RUNTIME_DIR`; it works when the account has an active session or lingering
enabled. Reading user logs needs nothing special. When the listing fails, the
system units still complete and `user/name` can be typed by hand.

**journalctl** is run with `-o cat`, which drops the journal's own timestamps.
That suits services logging structured lines, and loses time information for
plain ones.

A listing is given five seconds. An unreachable cluster cannot be detected up
front — it has a perfectly valid context and only reveals itself by hanging —
so that is the bound. The failure is cached, so it costs once per session.

Up to 200 000 lines are kept in memory per stream; older ones are dropped and
counted in the top bar.
