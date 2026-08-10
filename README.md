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

## Sources

A source is a **transport** and a **collector**. ssh is a transport, not a
source, so every collector works locally and on a remote node alike.

| transport | |
| --- | --- |
| `local` | run on this machine |
| `ssh` | run on `[user@]host`, through `ssh(1)` |

| collector | reads |
| --- | --- |
| `journalctl` | a systemd unit, or the whole journal |
| `kubectl` | a pod, a container, or a label selector |
| `docker` | a container |
| `command` | anything writing to stdout |
| `victorialogs` | a [VictoriaLogs](https://docs.victoriametrics.com/victorialogs/) query, over HTTP |
| `loki` | a [Loki](https://grafana.com/oss/loki/) query, over HTTP |

The last two read from a log database rather than a host, so they run no command
and the transport, `sudo` and the kubeconfig mean nothing to them. See
[Endpoints](#endpoints).

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

The spec is kept as written and resolved when the source opens, so `1h` means
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
| `tab` | accept the highlighted suggestion, else switch source kind |
| `shift+tab` | previous source kind |
| `enter` | accept the highlighted suggestion, else go to the next step |
| `esc` | drop the highlight, leave the editor, go back a step, then quit |
| `ctrl+r` | re-run the current listing, ignoring the cache |
| `ctrl+s` | toggle `sudo -n` |
| `ctrl+k` | edit the kubeconfig path (kubectl) |
| `ctrl+x` | edit the context (kubectl) |
| `ctrl+e` | edit the endpoint URL (victorialogs, loki) |
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
| `esc` | back to sources |
| `q`, `ctrl+c` | quit |

The filter is a regular expression, falling back to a case-insensitive
substring when it does not compile. It matches the raw line, not the rendering.

A line whose rendering spans several lines, such as a stacktrace, occupies one
row marked `⏎N`; `enter` shows the whole thing.

### Entry view

| key | |
| --- | --- |
| `↑` `k`, `↓` `j`, `pgup` `pgdown`, `home` `g`, `end` `G` | scroll |
| `esc`, `enter`, `backspace` | back |
| `q`, `ctrl+c` | quit |

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

Everything but the ssh hosts is listed **through the chosen transport**, with
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

Declared sources live in `$XDG_CONFIG_HOME/telescope/config.yaml`, by default
`~/.config/telescope/config.yaml`. When the file declares any, the start screen
opens on a picker; `tab` leaves it for the manual flow.

```yaml
sources:
  # Named in full: opens straight into the logs.
  - name: navidrome
    collector: docker
    container: navidrome
    tail: 50

  # A cluster reachable only as root on a node that refuses root logins.
  # No pod named, so picking it opens the prompt with the rest filled in.
  - name: k3s-ops
    transport: ssh
    host: node1
    collector: kubectl
    kubeconfig: /root/.kube/ops.kubeconfig
    context: admin@ops
    sudo: true

  - name: syncthing
    collector: journalctl
    unit: user/syncthing
    query: error
```

A source does not have to be complete. One that pins a host, a kubeconfig and
`sudo` but no pod is valid: choosing it fills in what it knew and stops at the
step still missing, with the pods already listing for that cluster. The picker
marks those with what they will ask for.

| field | default | |
| --- | --- | --- |
| `name` | required | shown in the picker |
| `collector` | required | `journalctl`, `kubectl`, `docker`, `command`, `victorialogs`, `loki`; taken from the endpoint when one is named |
| `endpoint` | | a declared endpoint, required by `victorialogs` |
| `transport` | `local` | `local` or `ssh` |
| `host` | | ssh destination, required when `transport: ssh` |
| `unit` | | systemd unit, `user/` prefix accepted |
| `user_unit` | `false` | read the user journal |
| `namespace` | | Kubernetes namespace |
| `target` | | pod name or label selector, `ns/pod:container` accepted; the query for a database |
| `container` | | container, for kubectl or docker |
| `kubeconfig` | | passed as `--kubeconfig` |
| `context` | | passed as `--context` |
| `args` | | command line, for `collector: command` |
| `sudo` | `false` | run the collector under `sudo -n` |
| `range` | | the window read: `1h`, `today`, `6h..1h`; see [Time range](#time-range) |
| `tail` | `1000` | lines of history, `0` for all |
| `follow` | `true` | keep streaming |
| `query` | | pre-fills the filter |

A malformed file, or a source naming an unknown collector, is reported on the
start screen rather than ignored.

### Endpoints

A collector that reads from a log database needs an endpoint. Endpoints are
declared once and referred to by name:

```yaml
endpoints:
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

  # The database itself, with no Grafana in front of it.
  - name: local
    type: loki
    url: http://127.0.0.1:3100

sources:
  - name: prod api
    endpoint: prod
    target: 'kubernetes.namespace:oteldb level:error'
```

| field | | |
| --- | --- | --- |
| `name` | required | referred to by a source's `endpoint` |
| `type` | required | `victorialogs` or `loki` |
| `url` | required | the base the API paths hang off |
| `datasource` | | Grafana datasource uid, appended to `url` as a proxy path |
| `token` | | where the bearer token is read from; see below |
| `tenant` | | `AccountID:ProjectID` for VictoriaLogs, the org id for Loki |
| `headers` | | anything else the endpoint or its proxy needs |
| `insecure` | `false` | skip TLS verification |

Every declared endpoint is offered on the start screen next to the collectors,
so a query can be written without declaring a source for it. Queries are
remembered per endpoint, and offered back there.

A source naming an endpoint does not need a `collector`: the endpoint already
says which API it speaks, and saying otherwise is an error rather than a silent
mistranslation.

An endpoint needs no declaration at all when it needs no credentials: choosing
`victorialogs` or `loki` asks for a URL, `ctrl+e` returns to it, and the URLs typed there
are remembered like ssh hosts. A missing scheme is filled in — `https://`, or
`http://` for a loopback address. Anything needing a token belongs in the config
file, since the prompt writes what it is given to the history in plain text.

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
needs to ask for a passphrase can still ask. It runs once per run, per endpoint,
and only for endpoints that are declared; what it writes to stderr is kept for
the error, and it is given a minute to answer.

An endpoint whose token cannot be read marks its own sources invalid in the
picker and leaves the rest working.

The proxy comes from the environment, so an endpoint reachable only through
`HTTPS_PROXY` or `ALL_PROXY=socks5h://…` needs nothing further.

The target is the query, in that database's own language, **sent as written**.
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
