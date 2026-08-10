# telescope

A terminal log viewer for the [oteldb](https://github.com/oteldb/oteldb) project.

It streams logs from `journalctl`, `kubectl`, `docker` or any command, locally
or through `ssh`, and pretty prints them with [go-faster/pl](https://github.com/go-faster/pl).
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
    context: 1-admin@1
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
| `collector` | required | `journalctl`, `kubectl`, `docker`, `command` |
| `transport` | `local` | `local` or `ssh` |
| `host` | | ssh destination, required when `transport: ssh` |
| `unit` | | systemd unit, `user/` prefix accepted |
| `user_unit` | `false` | read the user journal |
| `namespace` | | Kubernetes namespace |
| `target` | | pod name or label selector, `ns/pod:container` accepted |
| `container` | | container, for kubectl or docker |
| `kubeconfig` | | passed as `--kubeconfig` |
| `context` | | passed as `--context` |
| `args` | | command line, for `collector: command` |
| `sudo` | `false` | run the collector under `sudo -n` |
| `tail` | `1000` | lines of history, `0` for all |
| `follow` | `true` | keep streaming |
| `query` | | pre-fills the filter |

A malformed file, or a source naming an unknown collector, is reported on the
start screen rather than ignored.

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
