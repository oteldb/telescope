package ui

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/go-faster/errors"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/source"
)

// TestMain stubs out completion so no test shells out to systemctl, kubectl or
// docker. Suggestions are injected explicitly with candidates.
func TestMain(m *testing.M) {
	fetcher = func(context.Context, complete.Request) ([]complete.Candidate, error) {
		return nil, nil
	}
	// Without a terminal lipgloss renders no color, which would make every
	// assertion about styling pass trivially.
	lipgloss.SetColorProfile(termenv.TrueColor)
	// Never read the developer's own config or history; withSaved supplies
	// whatever a test needs.
	loadConfig = func() (config.Config, error) { return config.Config{}, nil }
	loadHistory = func() config.History { return config.History{} }
	// Nor the developer's checkout or their browser: a test that opens things
	// says which ones itself.
	newLocator = func() locator { return locator{} }
	openBrowser = func(string) error { return errors.New("no browser here") }
	os.Exit(m.Run())
}

// screen renders m at a fixed size with colors stripped.
func screen(t *testing.T, m tea.Model) string {
	t.Helper()
	return ansi.Strip(m.View())
}

// send delivers msgs, following the navigation commands the views return.
// Commands with side effects (spawning a stream) are deliberately not run.
func send(t *testing.T, m tea.Model, msgs ...tea.Msg) tea.Model {
	t.Helper()
	for _, msg := range msgs {
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		if cmd == nil || !navigates(msg) {
			continue
		}
		switch out := cmd().(type) {
		case openEntryMsg:
			m, _ = m.Update(out)
		case backMsg:
			m, _ = m.Update(out)
		case connectMsg:
			// Safe to apply: the stream command it returns is never run.
			m, _ = m.Update(out)
		case requeryMsg:
			m, _ = m.Update(out)
		}
	}
	return m
}

// navigates reports whether a message may produce a view switch. Other
// commands are left unrun: connecting spawns a process, and a blinking cursor
// only resolves after a real delay.
func navigates(msg tea.Msg) bool {
	km, ok := msg.(tea.KeyMsg)
	return ok && (km.Type == tea.KeyEnter || km.Type == tea.KeyEsc)
}

func k(s string) tea.Msg {
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	return tea.KeyMsg{Type: map[string]tea.KeyType{
		"enter": tea.KeyEnter,
		"tab":   tea.KeyTab,
		"esc":   tea.KeyEsc,
		"down":  tea.KeyDown,
		"up":    tea.KeyUp,
	}[s]}
}

func size() tea.Msg { return tea.WindowSizeMsg{Width: 100, Height: 30} }

// withSaved makes New() see a declared config and a history, without touching
// the user's files.
func withSaved(t *testing.T, places []config.Place, h config.History) {
	t.Helper()
	withConfig(t, places, nil)
	prev := loadHistory
	loadHistory = func() config.History { return h }
	t.Cleanup(func() { loadHistory = prev })
}

// withConfig makes New() see what a config file would have said, resolved the
// same way reading one resolves it.
func withConfig(t *testing.T, places []config.Place, groups []config.Group) {
	t.Helper()
	cfg, err := config.New(places, groups)
	require.NoError(t, err)
	prev := loadConfig
	loadConfig = func() (config.Config, error) { return cfg, nil }
	t.Cleanup(func() { loadConfig = prev })
}

// TestGroupsComeFirst: a group is the reason for declaring places, so it heads
// the list, and it opens as one stream without asking anything.
func TestGroupsComeFirst(t *testing.T) {
	withConfig(t,
		[]config.Place{
			{Name: "api", Type: "docker", Container: "api"},
			{Name: "worker", Type: "docker", Container: "worker"},
		},
		[]config.Group{{Name: "prod", Places: []string{"api", "worker"}}},
	)

	m := send(t, New(), size())
	out := ansi.Strip(screen(t, m))
	require.Contains(t, out, "prod")
	require.Contains(t, out, "api + worker", "a group says which places it reads")

	m = send(t, m, k("enter"))
	cfg := m.(Model).logs.cfg
	require.Equal(t, source.CollectorMerge, cfg.Collector)
	require.Equal(t, []string{"api", "worker"}, cfg.Labels())
}

// TestGroupOfEndpointsOpensWithNothingNamed: several log databases read as one,
// with no query written anywhere — which is what a group is for.
func TestGroupOfEndpointsOpensWithNothingNamed(t *testing.T) {
	withConfig(t,
		[]config.Place{
			{Name: "vl-eu", Type: "victorialogs", URL: "https://eu.example.com"},
			{
				Name: "vl-us", Type: "victorialogs", URL: "https://us.example.com",
				Proxy: "socks5h://127.0.0.1:1080",
			},
		},
		[]config.Group{{Name: "prod", Places: []string{"vl-eu", "vl-us"}}},
	)

	m := send(t, New(), size(), k("enter"))
	cfg := m.(Model).logs.cfg
	require.Equal(t, source.CollectorMerge, cfg.Collector)
	require.NoError(t, cfg.Validate())

	children := cfg.Children()
	require.Len(t, children, 2)
	require.Empty(t, children[0].Target, "LogsQL has a match-all: the query is typed into the view")
	require.Equal(t, "socks5h://127.0.0.1:1080", children[1].Endpoint.Proxy,
		"each place is reached its own way")
}

// TestPlacesAreReachedTheirOwnWay: one group, one place read here and one over
// ssh, which is the shape of an environment that is not all in one system.
func TestPlacesAreReachedTheirOwnWay(t *testing.T) {
	withConfig(t,
		[]config.Place{
			{Name: "here", Type: "docker", Container: "api"},
			{Name: "there", Type: "journalctl", Via: "ssh://ops@node-1", Unit: "kubelet"},
		},
		[]config.Group{{Name: "both", Places: []string{"here", "there"}}},
	)

	m := send(t, New(), size(), k("enter"))
	children := m.(Model).logs.cfg.Children()
	require.Len(t, children, 2)
	require.Equal(t, source.TransportLocal, children[0].Transport)
	require.Equal(t, source.TransportSSH, children[1].Transport)
	require.Equal(t, "ops@node-1", children[1].Host)
}

// TestGroupAsksOnceForTheTarget: the same deployment on several clusters is
// one thing to type, not one per cluster, so a group whose places name no pod
// stops on the prompt and gives what is typed to all of them.
func TestGroupAsksOnceForTheTarget(t *testing.T) {
	withConfig(t,
		[]config.Place{
			{Name: "ops", Type: "kubectl", Via: "ssh://bastion", KubeConfig: "/root/ops.yml"},
			{Name: "obs", Type: "kubectl", Via: "ssh://bastion", KubeConfig: "/root/obs.yml"},
		},
		[]config.Group{{Name: "both", Places: []string{"ops", "obs"}}},
	)

	m := send(t, New(), size())
	require.Contains(t, ansi.Strip(screen(t, m)), "pick a pod", "the list says what it will ask for")

	m = send(t, m, k("enter"))
	require.Equal(t, stepGroup, m.(Model).start.step, "it stops rather than opening")
	require.Contains(t, ansi.Strip(screen(t, m)), "open 2 places")

	m = typed(t, m, "flux-system/deploy/kustomize-controller")
	m = send(t, m, k("enter"))

	cfg := m.(Model).logs.cfg
	require.Equal(t, source.CollectorMerge, cfg.Collector)
	children := cfg.Children()
	require.Len(t, children, 2)
	for _, child := range children {
		require.Equal(t, "flux-system", child.Namespace)
		require.Equal(t, "deploy/kustomize-controller", child.Target)
	}
	require.Equal(t, []string{"/root/ops.yml", "/root/obs.yml"},
		[]string{children[0].KubeConfig, children[1].KubeConfig},
		"each cluster is still its own")
}

// TestGroupListsEveryPlaceItAsks: a workload does not have to run on every
// cluster of a group, so what one of them knows and the others do not is still
// offered, and a value both of them have is offered once.
func TestGroupListsEveryPlaceItAsks(t *testing.T) {
	withConfig(t,
		[]config.Place{
			{Name: "ops", Type: "kubectl", KubeConfig: "/root/ops.yml"},
			{Name: "obs", Type: "kubectl", KubeConfig: "/root/obs.yml"},
		},
		[]config.Group{{Name: "both", Places: []string{"ops", "obs"}}},
	)
	prev := fetcher
	fetcher = func(_ context.Context, r complete.Request) ([]complete.Candidate, error) {
		if r.KubeConfig == "/root/ops.yml" {
			return []complete.Candidate{{Value: "octo/api"}, {Value: "kube-system/coredns"}}, nil
		}
		return []complete.Candidate{{Value: "kube-system/coredns"}, {Value: "octo/worker"}}, nil
	}
	t.Cleanup(func() { fetcher = prev })

	m, _ := New().Update(size())
	m, cmd := m.Update(k("enter"))
	m = runCmds(m, cmd)

	values := make([]string, 0, 3)
	for _, c := range m.(Model).start.filtered {
		values = append(values, c.Value)
	}
	require.Equal(t, stepGroup, m.(Model).start.step)
	require.ElementsMatch(t, []string{"octo/api", "kube-system/coredns", "octo/worker"}, values)
	require.False(t, m.(Model).start.loading, "both clusters answered")
}

// TestGroupAskingKeepsWhatAPlaceAlreadyNamed: a place that names its own target
// is not asked about, so a group may mix the two.
func TestGroupAskingKeepsWhatAPlaceAlreadyNamed(t *testing.T) {
	withConfig(t,
		[]config.Place{
			{Name: "ops", Type: "kubectl", KubeConfig: "/root/ops.yml"},
			{Name: "named", Type: "kubectl", KubeConfig: "/root/obs.yml", Target: "deploy/api"},
		},
		[]config.Group{{Name: "both", Places: []string{"ops", "named"}}},
	)

	m := send(t, New(), size(), k("enter"))
	m = typed(t, m, "deploy/worker")
	m = send(t, m, k("enter"))

	children := m.(Model).logs.cfg.Children()
	require.Equal(t, []string{"deploy/worker", "deploy/api"},
		[]string{children[0].Target, children[1].Target})
}

// TestGroupAskingUnwindsWithEsc: the prompt was reached from the list, so esc
// goes back to it rather than into the manual flow.
func TestGroupAskingUnwindsWithEsc(t *testing.T) {
	withConfig(t,
		[]config.Place{
			{Name: "ops", Type: "kubectl", KubeConfig: "/root/ops.yml"},
			{Name: "obs", Type: "kubectl", KubeConfig: "/root/obs.yml"},
		},
		[]config.Group{{Name: "both", Places: []string{"ops", "obs"}}},
	)

	m := send(t, New(), size(), k("enter"))
	require.Equal(t, stepGroup, m.(Model).start.step)
	m = send(t, m, k("esc"))
	require.Equal(t, stepSaved, m.(Model).start.step)
}

func TestSavedSourcesOpenFirst(t *testing.T) {
	withSaved(t, []config.Place{
		{Name: "node1 pods", Via: "ssh://node1", Type: "kubectl", Target: "ns/pod", Sudo: true, Query: "error"},
		{Name: "local docker", Type: "docker", Container: "navidrome"},
	}, config.History{})

	m := send(t, New(), size())
	out := screen(t, m)
	require.Equal(t, stepSaved, m.(Model).start.step)
	require.Contains(t, out, "node1 pods")
	require.Contains(t, out, "ssh://node1 · kubectl", "each source says where it reads from")
	require.Contains(t, out, "enter open")

	// Enter on a highlighted source goes straight to its logs.
	m = send(t, m, k("down"), k("down"), k("enter"))
	require.Equal(t, "navidrome", m.(Model).logs.cfg.Container)
	require.Contains(t, screen(t, m), "docker logs")
}

// TestPartialSavedSourceUnwinds covers the shape of a real config: an entry
// that pins a node, a kubeconfig and sudo, leaving the pod to be picked.
func TestPartialSavedSourceUnwinds(t *testing.T) {
	withSaved(t, []config.Place{{
		Name:       "k3s-ops",
		Via:        "ssh://node1",
		Type:       "kubectl",
		KubeConfig: "/root/.kube/ops.kubeconfig",
		Sudo:       true,
	}}, config.History{})

	m := send(t, New(), size())
	require.Contains(t, screen(t, m), "pick a pod", "the picker says it is not ready")

	m = send(t, m, k("enter"))
	start := m.(Model).start
	require.Equal(t, stepCollector, start.step, "it lands on the step still missing")
	require.Equal(t, source.TransportSSH, start.transport())
	require.Equal(t, source.CollectorKubectl, collectors[start.collector])
	require.Equal(t, "node1", start.host.Value())
	require.Equal(t, "/root/.kube/ops.kubeconfig", start.kubeconfig.Value())
	require.True(t, start.elevate)
	require.Contains(t, screen(t, m), "pod-or-kind/name[:container]", "the prompt asks for what is missing")

	// The pods are listed for that cluster, not the default one.
	require.Equal(t, complete.Request{
		Field:      complete.FieldTarget,
		Transport:  source.TransportSSH,
		Host:       "node1",
		Collector:  source.CollectorKubectl,
		Elevate:    true,
		KubeConfig: "/root/.kube/ops.kubeconfig",
	}.Key(), start.candKey())

	// Picking a pod completes it.
	m = send(t, m, candidates(m, "oteldb/oteldb-0"))
	// down highlights, enter accepts it, then enter advances and enter opens.
	m = send(t, m, k("down"), k("enter"), k("enter"), k("enter"))
	require.Equal(t,
		"sudo -n kubectl --kubeconfig=/root/.kube/ops.kubeconfig "+
			"logs -n oteldb oteldb-0 --tail 1000 -f",
		m.(Model).logs.cfg.Command())
}

// TestKubeContextEditor: ctrl+x picks a context, which is the only way to use
// a kubeconfig whose current-context is unset.
func TestKubeContextEditor(t *testing.T) {
	m := send(t, New(), size(), k("tab")) // kubectl
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	for _, r := range "/root/.kube/reader.kubeconfig" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = send(t, m, k("enter"))

	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	require.Equal(t, detailKubeContext, m.(Model).start.detail)
	require.Contains(t, screen(t, m), "context")
	require.Equal(t, complete.Request{
		Field:      complete.FieldKubeContext,
		Transport:  source.TransportLocal,
		Collector:  source.CollectorKubectl,
		KubeConfig: "/root/.kube/reader.kubeconfig",
	}.Key(), m.(Model).start.candKey(), "contexts are listed from the chosen file")

	m = send(t, m, candidates(m, "reader"))
	// enter accepts the highlighted context, a second one leaves the editor.
	m = send(t, m, k("down"), k("enter"))
	require.Equal(t, "reader", m.(Model).start.kubecontext.Value())
	m = send(t, m, k("enter"))
	require.Equal(t, detailNone, m.(Model).start.detail, "enter returns to the target")

	// Pods are now listed through that context.
	require.Contains(t, m.(Model).start.candKey(), "reader")

	for _, r := range "ns/pod" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = send(t, m, k("enter"), k("enter"))
	require.Equal(t,
		"kubectl --kubeconfig=/root/.kube/reader.kubeconfig --context=reader "+
			"logs -n ns pod --tail 1000 -f",
		m.(Model).logs.cfg.Command())
}

// TestEmptyListingExplainsItself: a kubeconfig with no contexts is an answer,
// not a missing one, and must not fall through to unrelated target examples.
func TestEmptyListingExplainsItself(t *testing.T) {
	m := send(t, New(), size(), k("tab")) // kubectl
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	m = send(t, m, candidates(m))

	out := screen(t, m)
	require.Contains(t, out, "no contexts")
	require.NotContains(t, out, "oteldb/oteldb-0", "target examples belong to the target step")

	// A filter that matches nothing reads differently from nothing to offer.
	m = send(t, m, candidates(m, "reader"))
	for _, r := range "zzz" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	require.Contains(t, screen(t, m), "no match")
}

// TestFilteredPickerActsOnTheHighlight: filtering reorders the list, so the
// entry opened is the one under the cursor and not the one at that index in the
// config file.
func TestFilteredPickerActsOnTheHighlight(t *testing.T) {
	withSaved(t, []config.Place{
		{Name: "alpha", Type: "docker", Container: "first"},
		{Name: "beta", Type: "docker", Container: "second"},
		{Name: "beta two", Type: "docker", Container: "third"},
	}, config.History{})

	m := send(t, New(), size())
	m = typeIn(t, m, "beta")
	m = send(t, m, k("down"), k("down"), k("enter"))
	require.Equal(t, "third", m.(Model).logs.cfg.Container)
}

func TestSavedSourceCarriesQuery(t *testing.T) {
	withSaved(t, []config.Place{
		{Name: "errors", Type: "docker", Container: "app", Query: "boom"},
	}, config.History{})

	m := send(t, New(), size(), k("enter"))
	require.Equal(t, "boom", m.(Model).logs.view.Filter().Query, "the declared filter is applied")
}

func TestSavedFilteringAndFallthrough(t *testing.T) {
	withSaved(t, []config.Place{
		{Name: "node1 pods", Type: "docker", Container: "a"},
		{Name: "prod journal", Type: "journalctl"},
	}, config.History{})

	m := send(t, New(), size())
	for _, r := range "prod" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	out := screen(t, m)
	require.Contains(t, out, "prod journal")
	require.NotContains(t, out, "node1 pods")

	// tab leaves the picker for the manual flow, esc comes back.
	m = send(t, m, k("tab"))
	require.Equal(t, stepCollector, m.(Model).start.step)
	m = send(t, m, k("esc"))
	require.Equal(t, stepSaved, m.(Model).start.step)
}

func TestNothingDeclaredSkipsThePicker(t *testing.T) {
	withSaved(t, nil, config.History{})
	m := send(t, New(), size())
	require.Equal(t, stepCollector, m.(Model).start.step)
	require.Contains(t, screen(t, m), "esc quit")
}

func TestBrokenConfigIsReported(t *testing.T) {
	prev := loadConfig
	loadConfig = func() (config.Config, error) {
		return config.Config{}, errors.New("parse config.yaml: bad indent")
	}
	t.Cleanup(func() { loadConfig = prev })

	require.Contains(t, screen(t, send(t, New(), size())), "bad indent")
}

func TestHistoryFloatsRecentToTheTop(t *testing.T) {
	withSaved(t, nil, config.History{
		Targets: map[string][]string{
			config.Scope(source.Config{
				Transport: source.TransportLocal,
				Collector: source.CollectorJournal,
			}): {"kubelet"},
		},
		Hosts: []string{"node9"},
	})

	m := send(t, New(), size())
	m = send(t, m, candidates(m, "alpha", "kubelet", "zeta"))

	got := m.(Model).start.filtered
	require.Equal(t, "kubelet", got[0].Value, "the last unit used comes first")

	// A remembered host the ssh config no longer lists is still offered.
	m = send(t, New(), size(), tea.KeyMsg{Type: tea.KeyCtrlO})
	m = send(t, m, candidates(m, "other"))
	values := make([]string, 0, 2)
	for _, c := range m.(Model).start.filtered {
		values = append(values, c.Value)
	}
	require.Equal(t, []string{"node9", "other"}, values)
}

func TestConnectingRemembersTheSource(t *testing.T) {
	withSaved(t, nil, config.History{})

	m := send(t, New(), size(), tea.KeyMsg{Type: tea.KeyCtrlO})
	m = typeIn(t, m, "node1")
	m = send(t, m, k("enter"), k("tab"), k("tab")) // docker
	for _, r := range "app" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = send(t, m, k("enter"), k("enter"))

	h := m.(Model).start.history
	require.Equal(t, []string{"node1"}, h.Hosts)
	require.Equal(t, []string{"app"}, h.Recent(m.(Model).logs.cfg))
}

func TestStartScreen(t *testing.T) {
	m := send(t, New(), size())
	out := screen(t, m)

	require.Contains(t, out, "telemetry viewer")
	require.Contains(t, out, "journalctl")
	require.Contains(t, out, "victorialogs", "every collector is on the first screen")
	require.Contains(t, out, "ctrl+o host local", "where it runs is a detail, not a step")
	require.Contains(t, out, "tab")
	require.Equal(t, 30, strings.Count(out, "\n")+1, "start screen fills the window")
}

func TestStartFlowReachesQuery(t *testing.T) {
	// local ▸ tab to kubectl ▸ target ▸ query.
	m := send(t, New(), size(), k("tab"))
	out := screen(t, m)
	require.Contains(t, out, "journalctl")
	require.Contains(t, out, "kubectl")

	m = send(t, m, k("o"), k("enter"))
	out = screen(t, m)
	require.Contains(t, out, "kubectl logs o", "breadcrumb shows the built command")
	require.Contains(t, out, "no filter", "query step")
}

// candidates fakes a completion reply for whatever the model last asked for.
func candidates(m tea.Model, values ...string) tea.Msg {
	items := make([]complete.Candidate, 0, len(values))
	for _, v := range values {
		items = append(items, complete.Candidate{Value: v, Detail: "running"})
	}
	return candidatesMsg{key: m.(Model).start.candKey(), items: items}
}

// offer puts suggestions in front of the user at a step that lists nothing of
// its own, and so has no reply for [candidates] to fake.
func offer(m tea.Model, values ...string) tea.Model {
	mm := m.(Model)
	for _, v := range values {
		mm.start.candidates = append(mm.start.candidates, complete.Candidate{Value: v})
	}
	mm.start.refilter()
	return mm
}

func TestCompletionListsAndFilters(t *testing.T) {
	m := send(t, New(), size(), k("tab"), k("tab")) // docker
	m = send(t, m, candidates(m, "oteldb", "clickhouse", "otel-collector"))

	out := screen(t, m)
	require.Contains(t, out, "oteldb")
	require.Contains(t, out, "clickhouse")
	require.Contains(t, out, "running", "detail column")
	require.Contains(t, out, "↑↓ suggestions")

	// Typing narrows the list without another fetch.
	m = send(t, m, k("o"), k("t"), k("e"), k("l"))
	out = screen(t, m)
	require.Contains(t, out, "oteldb")
	require.NotContains(t, out, "clickhouse")
}

// TestCompletionFilterTerms covers the GitHub-style query: "ns:oteldb" narrows
// the pods to a namespace, and only what is left of the query is a target.
func TestCompletionFilterTerms(t *testing.T) {
	m := send(t, New(), size(), k("tab")) // kubectl
	m = send(t, m, candidates(m,
		"oteldb/api-79c", "oteldb/deployment/api", "kube-system/coredns-7d7", "kube-system/kube-apiserver-1"))

	for _, r := range "ns:oteldb " {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	out := screen(t, m)
	require.Contains(t, out, "oteldb/api-79c")
	require.Contains(t, out, "oteldb/deployment/api")
	require.NotContains(t, out, "kube-apiserver", "another namespace is filtered out")

	// A kind narrows it further.
	for _, r := range "kind:deploy" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	out = screen(t, m)
	require.Contains(t, out, "oteldb/deployment/api")
	require.NotContains(t, out, "oteldb/api-79c")

	// Enter without picking a suggestion must not send "ns:oteldb" to kubectl,
	// but must still read from the namespace and kind the terms named.
	for _, r := range " api" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = send(t, m, k("enter"), k("enter"))
	require.Equal(t, "kubectl logs -n oteldb deploy/api --tail 1000 -f", m.(Model).logs.cfg.Command())
}

// TestUnknownFilterFieldIsSearchedFor: a colon in a value is the container
// syntax, so only known fields may act as filters.
func TestUnknownFilterFieldIsSearchedFor(t *testing.T) {
	m := send(t, New(), size(), k("tab")) // kubectl
	m = send(t, m, candidates(m, "oteldb/oteldb-ingest-0:ingest", "oteldb/api-79c"))

	for _, r := range "ingest-0:ing" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	out := screen(t, m)
	require.Contains(t, out, "oteldb/oteldb-ingest-0:ingest")
	require.NotContains(t, out, "api-79c")
}

func TestSuggestionMatchesAreHighlighted(t *testing.T) {
	m := send(t, New(), size(), k("tab"), k("tab")) // docker
	m = send(t, m, candidates(m, "oteldb-0"))
	for _, r := range "otdb" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	// Colored per rune, so the plain text is unchanged but the render is not.
	row := ""
	for line := range strings.SplitSeq(m.View(), "\n") {
		if strings.Contains(ansi.Strip(line), "oteldb-0") {
			row = line
			break
		}
	}
	require.NotEmpty(t, row)
	require.Contains(t, ansi.Strip(row), "oteldb-0", "the text itself is untouched")
	// "otdb" matches o,t then d,b, which are emitted as two runs.
	require.Contains(t, row, styleMatch.Render("ot"), "the matched runs are marked")
	require.Contains(t, row, styleMatch.Render("db"))
	require.NotContains(t, row, styleMatch.Render("el"), "the gap between them is not")
}

func TestCompletionAccept(t *testing.T) {
	m := send(t, New(), size(), k("tab"), k("tab"))
	m = send(t, m, candidates(m, "oteldb", "clickhouse"))

	// Down highlights the first suggestion, tab inserts it.
	m = send(t, m, k("down"), k("tab"))
	require.Equal(t, "oteldb", m.(Model).start.target.Value())

	// Accepting clears the highlight, so enter advances instead of re-accepting.
	m = send(t, m, k("enter"))
	require.Contains(t, screen(t, m), "docker logs")
}

func TestCompletionEnterAcceptsHighlighted(t *testing.T) {
	m := send(t, New(), size(), k("tab"), k("tab"))
	m = send(t, m, candidates(m, "oteldb", "clickhouse"))

	m = send(t, m, k("down"), k("down"), k("enter"))
	require.Equal(t, "clickhouse", m.(Model).start.target.Value())
	require.Equal(t, stepCollector, m.(Model).start.step, "enter accepted, it did not advance")
}

// TestCompletionIgnoresStaleReply guards against a slow listing from a
// previously selected collector overwriting the current one.
func TestCompletionIgnoresStaleReply(t *testing.T) {
	m := send(t, New(), size())
	stale := candidatesMsg{key: m.(Model).start.candKey(), items: []complete.Candidate{{Value: "stale-unit"}}}

	m = send(t, m, k("tab")) // move to kubectl, invalidating the request
	m = send(t, m, stale)
	require.NotContains(t, screen(t, m), "stale-unit")
}

func TestCompletionTabCyclesChipsWhenNothingHighlighted(t *testing.T) {
	m := send(t, New(), size())
	m = send(t, m, candidates(m, "kubelet"))

	// Nothing is highlighted yet, so tab still switches the collector.
	m = send(t, m, k("tab"))
	require.Equal(t, source.CollectorKubectl, collectors[m.(Model).start.collector])
}

func TestCompletionErrorIsShown(t *testing.T) {
	m := send(t, New(), size(), k("tab"), k("tab"))
	m = send(t, m, candidatesMsg{
		key: m.(Model).start.candKey(),
		err: errors.New("docker: command not found"),
	})
	require.Contains(t, screen(t, m), "docker: command not found")
}

// TestHostIsADetail: naming a host is what asks for ssh, so nobody has to
// choose "local" to say they meant this machine.
func TestHostIsADetail(t *testing.T) {
	m := send(t, New(), size(), k("tab")) // kubectl
	require.Equal(t, source.TransportLocal, m.(Model).start.transport())

	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlO})
	require.Equal(t, detailHost, m.(Model).start.detail)
	require.Equal(t, "host", m.(Model).start.candKey(), "ssh_config is where the hosts come from")

	m = typeIn(t, m, "node1")
	m = send(t, m, k("enter"))
	start := m.(Model).start
	require.Equal(t, detailNone, start.detail)
	require.Equal(t, source.TransportSSH, start.transport(), "a host is what makes it ssh")

	req, ok := start.request()
	require.True(t, ok)
	require.Equal(t, source.TransportSSH, req.Transport, "and the pods are listed over it")
	require.Equal(t, "node1", req.Host)
	require.Contains(t, screen(t, m), "ssh://node1")

	// Clearing it goes back to this machine, with nothing to choose.
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlO})
	for range 5 {
		m = send(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	require.Equal(t, source.TransportLocal, m.(Model).start.transport())
}

// countingFetcher records how many times each request key was looked up.
func countingFetcher(t *testing.T, items ...string) *map[string]int {
	t.Helper()
	calls := map[string]int{}
	prev := fetcher
	fetcher = func(_ context.Context, r complete.Request) ([]complete.Candidate, error) {
		calls[r.Key()]++
		out := make([]complete.Candidate, 0, len(items))
		for _, v := range items {
			out = append(out, complete.Candidate{Value: v})
		}
		return out, nil
	}
	t.Cleanup(func() { fetcher = prev })
	return &calls
}

// run executes the commands a model returns, so preloads actually happen.
func runCmds(m tea.Model, cmd tea.Cmd) tea.Model {
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			m = runCmds(m, c)
		}
	case candidatesMsg:
		m, _ = m.Update(msg)
	case initMsg:
		var next tea.Cmd
		m, next = m.Update(msg)
		m = runCmds(m, next)
	}
	return m
}

func TestCompletionPreloadsAndCaches(t *testing.T) {
	calls := countingFetcher(t, "alpha")

	m, cmd := New().Update(initMsg{}) // the collector step is the first one
	m = runCmds(m, cmd)
	m, _ = m.Update(size())

	// Every collector was warmed, not just the selected one, and so was the
	// host list the ssh prompt will want.
	require.Positive(t, (*calls)[targetKey(source.CollectorJournal)])
	require.Positive(t, (*calls)[targetKey(source.CollectorDocker)])
	require.Positive(t, (*calls)[targetKey(source.CollectorKubectl)])
	require.Positive(t, (*calls)["host"])

	// Walking the chips and stepping back and forth must hit the cache.
	for range 3 {
		m, cmd = m.Update(k("tab"))
		m = runCmds(m, cmd)
	}
	m, cmd = m.Update(k("enter"))
	m = runCmds(m, cmd)
	m, cmd = m.Update(k("esc"))
	m = runCmds(m, cmd)

	require.ElementsMatch(t, []string{
		"host",
		targetKey(source.CollectorJournal),
		targetKey(source.CollectorKubectl),
		targetKey(source.CollectorDocker),
		targetKey(source.CollectorCommand),
	}, keysOf(*calls))
	for key, n := range *calls {
		require.Equal(t, 1, n, "%s was looked up more than once", key)
	}
	require.Contains(t, screen(t, m), "alpha", "cached suggestions render immediately")
}

func TestCompletionRefreshDropsCache(t *testing.T) {
	calls := countingFetcher(t, "alpha")

	m, cmd := New().Update(initMsg{})
	m = runCmds(m, cmd)
	require.Equal(t, 1, (*calls)[targetKey(source.CollectorJournal)])

	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	runCmds(m, cmd)
	require.Equal(t, 2, (*calls)[targetKey(source.CollectorJournal)], "refresh re-runs the listing")
}

func TestHostsPreloadedAtInit(t *testing.T) {
	calls := countingFetcher(t, "node1")

	m := New()
	m2 := runCmds(m, m.Init())
	m2, cmd := m2.Update(size())
	m2 = runCmds(m2, cmd)

	require.Equal(t, 1, (*calls)["host"])

	// Opening the host prompt shows them without another lookup.
	m2, cmd = m2.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m2 = runCmds(m2, cmd)
	require.Equal(t, 1, (*calls)["host"])
	require.Contains(t, screen(t, m2), "node1")
}

// targetKey is the completion cache key for a local collector listing.
func targetKey(c source.Collector) string {
	return complete.Request{
		Field:     complete.FieldTarget,
		Transport: source.TransportLocal,
		Collector: c,
	}.Key()
}

func keysOf(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestUserUnitReachesCommand checks that the user/ prefix survives the whole
// way from a suggestion to the journalctl invocation.
func TestUserUnitReachesCommand(t *testing.T) {
	m := send(t, New(), size()) // journalctl
	m = send(t, m, candidates(m, "sshd", "user/syncthing"))

	m = send(t, m, k("down"), k("down"), k("tab"))
	require.Equal(t, "user/syncthing", m.(Model).start.target.Value())

	m = send(t, m, k("enter"))
	require.Contains(t, screen(t, m), "journalctl --user --no-pager -o cat -u syncthing")
}

// TestElevatedKubectlOverSSH walks the flow a root-only kubeconfig on a remote
// node needs: ssh host, kubectl, sudo on, a typed config path, then a pod.
func TestElevatedKubectlOverSSH(t *testing.T) {
	m := send(t, New(), size(), k("tab")) // kubectl
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlO})
	m = typeIn(t, m, "node1")
	m = send(t, m, k("enter"))

	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	require.True(t, m.(Model).start.elevate)

	// ctrl+k swaps the bar over to the kubeconfig path.
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	require.Equal(t, detailKubeConfig, m.(Model).start.detail)
	require.Contains(t, screen(t, m), "kubeconfig")
	for _, r := range "/etc/rancher/k3s/k3s.yaml" {
		m = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	// enter returns to the target rather than advancing the step.
	m = send(t, m, k("enter"))
	require.Equal(t, detailNone, m.(Model).start.detail)
	require.Equal(t, stepCollector, m.(Model).start.step)

	// The preview already shows what will run.
	require.Contains(t, screen(t, m),
		"sudo -n kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml")

	for _, r := range "oteldb/oteldb-0" {
		m = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = send(t, m, k("enter"), k("enter"))

	cfg := m.(Model).logs.cfg
	require.True(t, cfg.Elevate)
	require.Equal(t, "/etc/rancher/k3s/k3s.yaml", cfg.KubeConfig)
	require.Equal(t, "oteldb", cfg.Namespace)
	require.Equal(t, "oteldb-0", cfg.Target)
	require.Equal(t, []string{
		"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-tt", "node1",
		"sudo -n kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml logs " +
			"-n oteldb oteldb-0 --tail 1000 -f",
	}, cfg.Argv())
}

// TestKubeConfigChangeRefetchesPods guards the ordering trap: pods listed
// against the wrong cluster must not be reused once a config is chosen.
func TestKubeConfigChangeRefetchesPods(t *testing.T) {
	calls := countingFetcher(t, "ns/pod")

	m, cmd := New().Update(size())
	m = runCmds(m, cmd)
	m, cmd = m.Update(k("tab")) // kubectl
	m = runCmds(m, cmd)

	before := (*calls)[targetKey(source.CollectorKubectl)]
	require.Positive(t, before)

	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = runCmds(m, cmd)
	// Typing returns only a cursor blink; running it would sleep for real.
	for _, r := range "/tmp/kube.yaml" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd = m.Update(k("enter"))
	runCmds(m, cmd)

	withConfig := complete.Request{
		Field:      complete.FieldTarget,
		Transport:  source.TransportLocal,
		Collector:  source.CollectorKubectl,
		KubeConfig: "/tmp/kube.yaml",
	}.Key()
	require.Positive(t, (*calls)[withConfig], "pods are listed again for the new config")
}

// leftEdge is the column the prompt box starts at, per rendered line.
func leftEdge(out string) []int {
	var cols []int
	for line := range strings.SplitSeq(out, "\n") {
		if i := strings.IndexAny(line, "╭│╰"); i >= 0 {
			cols = append(cols, i)
		}
	}
	return cols
}

// TestPromptDoesNotShiftWhileTyping guards the jitter caused by centering on
// the widest line: the command preview grows with every keystroke, which used
// to move the prompt box under the cursor.
func TestPromptDoesNotShiftWhileTyping(t *testing.T) {
	m := send(t, New(), size(), k("tab")) // kubectl
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})

	want := leftEdge(screen(t, m))
	require.NotEmpty(t, want, "the prompt box is rendered")

	for _, r := range "/root/.kube/ops.kubeconfig" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		require.Equal(t, want, leftEdge(screen(t, m)),
			"prompt moved after typing %q", string(r))
	}
}

// TestStateColors checks that lifecycle words are colored by meaning, and that
// the resting states stay dim so a mostly-stopped list is not a wall of red.
func TestStateColors(t *testing.T) {
	green := lipgloss.NewStyle().Foreground(colorOK).Render("x")
	red := lipgloss.NewStyle().Foreground(colorErr).Render("x")
	amber := lipgloss.NewStyle().Foreground(colorWarn).Render("x")
	dim := styleDim.Render("x")

	color := func(s string) string {
		return strings.Replace(renderState(s), s, "x", 1)
	}
	for _, tt := range []struct {
		state string
		want  string
	}{
		{"running", green},
		{"active", green},
		{"Running", green},
		{"failed", red},
		{"CrashLoopBackOff", red},
		{"pending", amber},
		{"Pending", amber},
		// systemd calls an ordinary stopped unit dead; coloring it red would
		// make most of the journal list look broken.
		{"dead", dim},
		{"exited", dim},
		{"whatever", dim},
	} {
		t.Run(tt.state, func(t *testing.T) {
			require.Equal(t, tt.want, color(tt.state))
		})
	}
	require.Empty(t, renderState(""))
}

func TestRenderDetail(t *testing.T) {
	require.Equal(t, styleDim.Render("default"),
		renderDetail(complete.Candidate{Detail: "default"}))
	require.Equal(t, renderState("running"),
		renderDetail(complete.Candidate{State: "running"}))
	require.Equal(t, renderState("running")+styleDim.Render(" · app:latest"),
		renderDetail(complete.Candidate{State: "running", Detail: "app:latest"}))
}

func TestSuggestionPaging(t *testing.T) {
	values := make([]string, 60)
	for i := range values {
		values[i] = "ns/pod-" + strconv.Itoa(i)
	}
	m := send(t, New(), tea.WindowSizeMsg{Width: 100, Height: 30}, k("tab"))
	m = send(t, m, candidates(m, values...))
	page := m.(Model).start.listHeight()
	require.Positive(t, page)

	sel := func(m tea.Model) int { return m.(Model).start.sel }

	m = send(t, m, tea.KeyMsg{Type: tea.KeyEnd})
	require.Equal(t, len(values)-1, sel(m), "end jumps to the last suggestion")
	require.Contains(t, screen(t, m), values[len(values)-1])

	m = send(t, m, tea.KeyMsg{Type: tea.KeyHome})
	require.Equal(t, 0, sel(m), "home jumps back to the first")

	m = send(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	require.Equal(t, page, sel(m), "pgdown moves one visible page")
	m = send(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	require.Equal(t, 0, sel(m))
}

// TestMultilineEntryKeepsOneRow: a stacktrace used to be drawn into the list
// verbatim, pushing every row below it down and breaking the framed height.
func TestMultilineEntryKeepsOneRow(t *testing.T) {
	m := logsModel(t,
		`{"level":"info","msg":"before"}`,
		`{"level":"error","msg":"boom","error":"wrapped:\n    a.go:1\n  - inner"}`,
		`{"level":"info","msg":"after"}`,
	)
	out := screen(t, m)

	require.Equal(t, 30, strings.Count(out, "\n")+1, "the view still fills exactly the window")
	require.NotContains(t, out, "a.go:1", "the trace is folded away in the list")
	require.Contains(t, out, "⏎2", "and its size is shown instead")
	require.Contains(t, out, "before")
	require.Contains(t, out, "after")

	// Opening the entry shows the whole thing.
	m = send(t, m, tea.KeyMsg{Type: tea.KeyUp}, k("enter"))
	entry := screen(t, m)
	require.Contains(t, entry, "a.go:1")
	require.Contains(t, entry, "inner")
}

func TestLogViewJumpKeys(t *testing.T) {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = `{"level":"info","msg":"line-` + strconv.Itoa(i) + `"}`
	}
	m := logsModel(t, lines...)
	logs := func(m tea.Model) logModel { return m.(Model).logs }

	// Follow pins the cursor to the tail.
	require.Equal(t, len(lines)-1, logs(m).cursor)

	m = send(t, m, tea.KeyMsg{Type: tea.KeyHome})
	require.Equal(t, 0, logs(m).cursor, "home reaches the start of the whole list")
	require.False(t, logs(m).follow, "and stops following")

	m = send(t, m, tea.KeyMsg{Type: tea.KeyEnd})
	require.Equal(t, len(lines)-1, logs(m).cursor, "end reaches the tail")
	require.True(t, logs(m).follow)

	// H and L move within the visible window, not the whole list.
	top := logs(m).top
	m = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("H")})
	require.Equal(t, top, logs(m).cursor, "H goes to the top of the view")
	require.Greater(t, logs(m).cursor, 0, "which is not the top of the list")

	m = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})
	require.Equal(t, top+logs(m).bodyHeight()-1, logs(m).cursor, "L goes to the bottom of the view")

	m = send(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	require.Equal(t, top-1, logs(m).cursor, "pgup moves one screen back")
}

func TestEntryViewEndScrolls(t *testing.T) {
	fields := make([]string, 0, 60)
	for i := range 60 {
		fields = append(fields, `"f`+strconv.Itoa(i)+`":"v"`)
	}
	m := logsModel(t, `{"level":"info","msg":"big",`+strings.Join(fields, ",")+`}`)
	m = send(t, m, k("enter"))
	require.Equal(t, 0, m.(Model).entry.off)

	m = send(t, m, tea.KeyMsg{Type: tea.KeyEnd})
	require.Positive(t, m.(Model).entry.off, "end scrolls to the bottom of the entry")
	require.NotPanics(t, func() { _ = screen(t, m) })

	m = send(t, m, tea.KeyMsg{Type: tea.KeyHome})
	require.Equal(t, 0, m.(Model).entry.off)
}

// TestStatesLineUp: names of wildly different lengths must still put their
// state in one column. Capping the value column at half the row used to let
// long names push their state right, leaving a ragged edge.
func TestStatesLineUp(t *testing.T) {
	m := send(t, New(), size())
	m = send(t, m, candidates(m,
		"git/forgejo-runner-0",
		"cert-manager/cert-manager-cainjector-757c6bcb69-x4qgm",
		"kube-system/cilium-29x2j",
		"local-path-storage/local-path-provisioner-6d59db9dcc-kmlj7",
		"oteldb/otelgateway-76f445c9c6-j9vkg",
	))

	var columns []int
	for line := range strings.SplitSeq(screen(t, m), "\n") {
		if i := strings.Index(line, "running"); i >= 0 {
			columns = append(columns, i)
		}
	}
	require.Len(t, columns, 5, "every suggestion shows its state")
	for _, c := range columns[1:] {
		require.Equal(t, columns[0], c, "states are ragged: %v", columns)
	}
}

// TestRowsWidenWithTheTerminal checks that a long value keeps its detail
// column on a wide terminal instead of being truncated at a fixed width.
func TestRowsWidenWithTheTerminal(t *testing.T) {
	const long = "clickhouse/chi-altinity-clickhouse-operator-6974f69488-87p5x"

	rowFor := func(w int) string {
		m := send(t, New(), tea.WindowSizeMsg{Width: w, Height: 30})
		m = send(t, m, candidates(m, long))
		// The value is truncated on a narrow terminal, so match on its head.
		for line := range strings.SplitSeq(screen(t, m), "\n") {
			if strings.Contains(line, long[:30]) {
				return strings.TrimRight(line, " ")
			}
		}
		return ""
	}

	narrow, wide := rowFor(70), rowFor(160)
	require.NotEmpty(t, narrow)
	// Room for the state is always reserved: the value gives way, not the state.
	require.Contains(t, narrow, "…", "the value is truncated at 70 columns")
	require.Contains(t, narrow, "running")
	require.Contains(t, wide, long, "at 160 columns the whole value fits")
	require.Contains(t, wide, "running")
	require.Greater(t, len(wide), len(narrow))
}

// TestPromptFitsTheContentBlock guards the overflow that reintroduces jitter:
// the bar plus its border must never exceed the block it is centered in.
func TestPromptFitsTheContentBlock(t *testing.T) {
	for _, w := range []int{40, 80, 100, 160, 400} {
		m := send(t, New(), tea.WindowSizeMsg{Width: w, Height: 30})
		start := m.(Model).start
		require.LessOrEqual(t, start.promptWidth()+2, start.contentWidth(),
			"prompt overflows the content block at width %d", w)
	}
}

// TestStartScreenStartsNearTheTop: centering the block vertically left the
// suggestions floating in the middle of a tall terminal, with a dozen unused
// rows above them.
func TestStartScreenStartsNearTheTop(t *testing.T) {
	values := make([]string, 8)
	for i := range values {
		values[i] = "ns/pod-" + strconv.Itoa(i)
	}
	m := send(t, New(), size())
	m = send(t, m, candidates(m, values...))
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 60})

	lines := strings.Split(screen(t, m), "\n")
	require.Len(t, lines, 60, "the screen fills the window exactly")
	blank := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			break
		}
		blank++
	}
	require.LessOrEqual(t, blank, maxTopPad)
}

// TestSuggestionsOutgrowThePromptBar: a wide terminal has room for a pod name
// with a container, which used to be truncated to the width of the bar.
func TestSuggestionsOutgrowThePromptBar(t *testing.T) {
	const long = "storage/oteldb-clickhouse-57784dbf84-htwbk:clickhouse-server"

	m := send(t, New(), size())
	m = send(t, m, candidates(m, long))
	m = send(t, m, tea.WindowSizeMsg{Width: 280, Height: 40})

	start := m.(Model).start
	require.LessOrEqual(t, start.promptWidth(), maxPromptWidth, "the bar itself stays readable")
	require.Greater(t, start.listWidth(), start.promptWidth())

	var row, bar string
	for line := range strings.SplitSeq(screen(t, m), "\n") {
		switch {
		case strings.Contains(line, long):
			row = line
		case strings.Contains(line, "❯"):
			bar = line
		}
	}
	require.NotEmpty(t, row, "the whole value is rendered, not truncated")
	// The rows hang under the left edge of the bar rather than being centered
	// on their own, so the list still reads as belonging to the input.
	require.NotEmpty(t, bar)
	// The bar's leading spaces stop at the box border, so its text starts three
	// columns further right: the border and the "❯ " prompt. A row's leading
	// spaces already include its two-column marker.
	require.Equal(t, indentOf(bar)+3, indentOf(row),
		"suggestions are aligned with the prompt text")
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

func TestCompletionListGrowsWithTheWindow(t *testing.T) {
	values := make([]string, 40)
	for i := range values {
		values[i] = "ns/pod-" + strconv.Itoa(i)
	}

	count := func(h int, m tea.Model) int {
		m = send(t, m, tea.WindowSizeMsg{Width: 100, Height: h})
		n := 0
		for line := range strings.SplitSeq(screen(t, m), "\n") {
			if strings.Contains(line, "ns/pod-") {
				n++
			}
		}
		return n
	}

	m := send(t, New(), size())
	m = send(t, m, candidates(m, values...))

	short, tall := count(24, m), count(60, m)
	require.Positive(t, short)
	require.Greater(t, tall, short, "a taller window shows more suggestions")
	require.LessOrEqual(t, tall, len(values))
}

// TestCompletionWindowFollowsSelection: moving past the visible rows used to
// highlight a suggestion that was never drawn.
func TestCompletionWindowFollowsSelection(t *testing.T) {
	values := make([]string, 40)
	for i := range values {
		values[i] = "ns/pod-" + strconv.Itoa(i)
	}
	m := send(t, New(), size(), tea.WindowSizeMsg{Width: 100, Height: 24})
	m = send(t, m, candidates(m, values...))

	for range 20 {
		m = send(t, m, k("down"))
	}
	sel := m.(Model).start.sel
	require.Equal(t, 19, sel)
	require.Contains(t, screen(t, m), values[sel], "the highlighted row is on screen")
}

// TestEscUnwindsThenQuits: esc peels off one layer at a time and only quits
// once there is nothing left to back out of.
func TestEscQuitsFromTheFirstStep(t *testing.T) {
	quits := func(m tea.Model) bool {
		_, cmd := m.Update(k("esc"))
		if cmd == nil {
			return false
		}
		_, ok := cmd().(quitMsg)
		return ok
	}

	m := send(t, New(), size())
	require.Contains(t, screen(t, m), "esc quit", "quitting is discoverable")
	require.True(t, quits(m), "esc on the first step quits")

	// Deeper in, esc walks back instead.
	m = typeIn(t, m, "kubelet")
	m = send(t, m, k("enter"))
	require.False(t, quits(m))
	require.Contains(t, screen(t, m), "esc back")

	// A highlighted suggestion is dropped first.
	m = offer(m, "kubelet")
	m = send(t, m, k("down"))
	require.Equal(t, 0, m.(Model).start.sel)
	require.False(t, quits(m))

	m = send(t, m, k("esc"))
	require.Equal(t, -1, m.(Model).start.sel, "esc cleared the highlight")
	require.Equal(t, stepQuery, m.(Model).start.step, "and did not also step back")
}

// TestEmptyHostIsLocal: the step that asked which of the two it was is gone,
// and an unanswered host prompt is not an error.
func TestEmptyHostIsLocal(t *testing.T) {
	m := send(t, New(), size(), tea.KeyMsg{Type: tea.KeyCtrlO}, k("enter"))
	require.Equal(t, stepCollector, m.(Model).start.step, "enter left the detail, not the step")
	require.Equal(t, source.TransportLocal, m.(Model).start.transport())
	require.NotContains(t, screen(t, m), "requires a host")

	m = send(t, m, k("enter"), k("enter"))
	cfg := m.(Model).logs.cfg
	require.Equal(t, source.TransportLocal, cfg.Transport)
	require.Equal(t, []string{"sh", "-c", cfg.Command()}, cfg.Argv())
}

// TestFilteringADatabaseAsksItAgain: a place that can answer part of the query
// is asked it, and the view is rebuilt from what comes back rather than
// filtered out of everything the database has.
func TestFilteringADatabaseAsksItAgain(t *testing.T) {
	cfg := source.Config{
		Collector: source.CollectorVictoriaLogs,
		Endpoint:  source.Endpoint{Name: "prod", URL: "https://logs.example.com"},
		Follow:    true,
	}
	m := send(t, New(), size(), connectMsg{cfg: cfg})
	m = send(t, m, linesMsg{lines: []source.Line{{Data: []byte("connection reset")}}, closed: true})
	require.Equal(t, []string{"*"}, m.(Model).logs.cfg.Pushed())

	m = send(t, m, k("/"))
	m = typed(t, m, "reset")
	m = send(t, m, k("enter"))

	logs := m.(Model).logs
	require.Equal(t, []string{`*:~"(?i)reset"`}, logs.cfg.Pushed())
	require.Equal(t, "reset", logs.view.Filter().Query, "the filter is still applied here")
	require.Empty(t, logs.store.Entries(), "the view is rebuilt from the answer")
	require.Contains(t, ansi.Strip(screen(t, m)), "requerying")
}

// TestFilteringLokiIsWhatOpensIt: Loki reads nothing until the filter names a
// label, and the filter is where that is typed — the same prompt that narrows
// every other source is what starts this one.
func TestFilteringLokiIsWhatOpensIt(t *testing.T) {
	cfg := source.Config{
		Collector: source.CollectorLoki,
		Endpoint:  source.Endpoint{Name: "prod", URL: "https://logs.example.com"},
		Follow:    true,
	}
	m := send(t, New(), size(), connectMsg{cfg: cfg})
	require.Equal(t, []string{""}, m.(Model).logs.cfg.Pushed(), "nothing has been asked")

	m = send(t, m, k("/"))
	m = typed(t, m, "app=api error")
	m = send(t, m, k("enter"))

	logs := m.(Model).logs
	require.Equal(t, []string{`{app=~"(?i)api"}`}, logs.cfg.Pushed(),
		"the label selects the stream; the word is not a label and stays here")
	require.Equal(t, "app=api error", logs.view.Filter().Query)
	require.Contains(t, ansi.Strip(screen(t, m)), "requerying")
}

// TestFilteringACommandDoesNotAskItAgain: nothing can be pushed into docker
// logs, so filtering stays where it is and the lines already read stay too.
func TestFilteringACommandDoesNotAskItAgain(t *testing.T) {
	m := logsModel(t, "alpha", "beta")

	m = send(t, m, k("/"))
	m = typed(t, m, "beta")
	m = send(t, m, k("enter"))

	logs := m.(Model).logs
	require.Len(t, logs.store.Entries(), 2, "the stream is not restarted")
	require.Equal(t, []string{""}, logs.cfg.Pushed(), "there was nothing to ask")
}

func logsModel(t *testing.T, lines ...string) tea.Model {
	t.Helper()
	cfg := source.Config{Collector: source.CollectorDocker, Container: "app", Follow: true}
	m := send(t, New(), size(), connectMsg{cfg: cfg})

	batch := make([]source.Line, 0, len(lines))
	for _, l := range lines {
		batch = append(batch, source.Line{Data: []byte(l)})
	}
	return send(t, m, linesMsg{lines: batch, closed: true})
}

func TestLogView(t *testing.T) {
	m := logsModel(t,
		`{"level":"info","ts":"2026-08-10T10:00:00Z","msg":"started"}`,
		`{"level":"error","ts":"2026-08-10T10:00:05Z","msg":"exploded"}`,
		`unstructured tail`,
	)
	out := screen(t, m)

	require.Contains(t, out, "docker logs -f app")
	require.Contains(t, out, "3 shown")
	require.Contains(t, out, "started")
	require.Contains(t, out, "exploded")
	require.Contains(t, out, "unstructured tail")
	require.Contains(t, out, "follow on")
	require.Equal(t, 30, strings.Count(out, "\n")+1, "log view fills the window")
}

func TestLogViewFilter(t *testing.T) {
	m := logsModel(t,
		`{"level":"info","msg":"alpha"}`,
		`{"level":"info","msg":"beta"}`,
	)
	m = send(t, m, k("/"))
	m = typed(t, m, "beta")
	m = send(t, m, k("enter"))

	out := screen(t, m)
	require.Contains(t, out, "1 shown")
	require.Contains(t, out, "beta")
	require.NotContains(t, out, "alpha")
}

// TestLogViewFilterKeepsAQueryThatDoesNotParse: the view goes on showing what
// the last query selected, and the prompt says why this one selected nothing.
func TestLogViewFilterKeepsAQueryThatDoesNotParse(t *testing.T) {
	m := logsModel(t,
		`{"level":"info","msg":"alpha"}`,
		`{"level":"info","msg":"beta"}`,
	)
	m = send(t, m, k("/"))
	m = typed(t, m, "beta")
	m = send(t, m, k("enter"))
	m = send(t, m, k("/"))
	m = typed(t, m, " or (")
	m = send(t, m, k("enter"))

	out := ansi.Strip(screen(t, m))
	require.Contains(t, out, "term was expected")
	require.Contains(t, out, "1 shown")
	require.Contains(t, out, "beta")
	require.NotContains(t, out, "alpha")
}

// TestLogViewFilterIsAQuery checks that the filter is more than a grep: a field
// and a level reach lines the words alone do not.
func TestLogViewFilterIsAQuery(t *testing.T) {
	m := logsModel(t,
		`{"level":"info","msg":"alpha","pod":"api-7"}`,
		`{"level":"error","msg":"beta","pod":"worker-1"}`,
	)
	m = send(t, m, k("/"))
	m = typed(t, m, "level>=warn pod~worker")
	m = send(t, m, k("enter"))

	out := ansi.Strip(screen(t, m))
	require.Contains(t, out, "1 shown")
	require.Contains(t, out, "beta")
	require.NotContains(t, out, "alpha")
}

func typed(t *testing.T, m tea.Model, s string) tea.Model {
	t.Helper()
	for _, r := range s {
		m = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func TestEntryView(t *testing.T) {
	m := logsModel(t, `{"level":"warn","ts":"2026-08-10T10:00:00Z","msg":"slow","dur":"1s"}`)
	m = send(t, m, k("enter"))

	out := screen(t, m)
	require.Contains(t, out, "entry #0")
	require.Contains(t, out, "WARN")
	require.Contains(t, out, "slow")
	require.Contains(t, out, "fields")
	require.Contains(t, out, "dur")
	require.Contains(t, out, "raw")

	// esc returns to the logs.
	require.Contains(t, screen(t, send(t, m, k("esc"))), "docker logs")
}

func TestLogViewStreamError(t *testing.T) {
	cfg := source.Config{Collector: source.CollectorDocker, Container: "app"}
	m := send(t, New(), size(), connectMsg{cfg: cfg},
		streamErrMsg{err: errors.New("ssh: connect failed")})
	require.Contains(t, screen(t, m), "failed")
}

func TestLogViewEmpty(t *testing.T) {
	require.NotPanics(t, func() {
		m := logsModel(t)
		_ = screen(t, m)
		_ = screen(t, send(t, m, k("enter"), k("down"), k("up")))
	})
}

func TestTinyWindowDoesNotPanic(t *testing.T) {
	require.NotPanics(t, func() {
		m := send(t, New(), tea.WindowSizeMsg{Width: 4, Height: 2},
			connectMsg{cfg: source.Config{Collector: source.CollectorJournal}},
			linesMsg{lines: []source.Line{{Data: []byte("x")}}, closed: true})
		_ = m.View()
	})
}

// tabs advances the chip selection n times, so a test names the chip it wants
// rather than a count that shifts whenever a collector is added.
func tabs(n int) []tea.Msg {
	out := make([]tea.Msg, n)
	for i := range out {
		out[i] = k("tab")
	}
	return out
}

// firstDeclared is how many tabs reach the first endpoint from the config file:
// past the command collectors and the undeclared database chips.

// withEndpoints makes New() see declared endpoints, as a config file with an
// endpoints section would.
func withEndpoints(t *testing.T, places []config.Place) {
	t.Helper()
	withConfig(t, places, nil)
}

// TestEndpointOpensFromTheStartScreen: a log database is a place like any
// other, so it is offered in the same list, and an endpoint is the whole of
// what either kind names.
func TestEndpointOpensFromTheStartScreen(t *testing.T) {
	withEndpoints(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://grafana.example.com/api/datasources/proxy/uid/abc"},
		{Name: "staging", Type: "loki", URL: "https://staging.example.com"},
	})

	m := send(t, New(), size())
	require.Equal(t, stepSaved, m.(Model).start.step, "endpoints alone are enough to have a picker")
	out := screen(t, m)
	require.Contains(t, out, "prod")
	require.Contains(t, out, "grafana.example.com / abc", "the datasource uid tells two apart")
	require.Contains(t, out, "staging")

	m = send(t, m, k("down"), k("down"), k("enter"))
	require.Equal(t, stateLogs, m.(Model).state,
		"a place that names an endpoint says everything it has to")
	require.Contains(t, screen(t, m), "filter by a label to select a stream",
		"and Loki says what it still needs, above the lines it has none of")
}

// TestLokiIsFilteredRatherThanQueried: LogQL is compiled from the filter, so
// the step that would ask a Loki place for a target asks for the filter, and
// there is no second step behind it.
func TestLokiIsFilteredRatherThanQueried(t *testing.T) {
	withEndpoints(t, []config.Place{
		{Name: "staging", Type: "loki", URL: "https://staging.example.com"},
	})

	// Past the saved list, then along the chips to loki, which is last, and
	// which asks where to reach before anything else.
	m := send(t, New(), size(), k("tab"))
	for range collectors {
		if m.(Model).start.collectorAt() == source.CollectorLoki {
			break
		}
		m = send(t, m, k("tab"))
	}
	require.Equal(t, detailEndpoint, m.(Model).start.detail)
	m = typed(t, m, "staging")
	m = send(t, m, k("enter"))

	start := m.(Model).start
	require.Equal(t, source.CollectorLoki, start.collectorAt())
	require.Equal(t, detailNone, start.detail)
	require.Equal(t, &start.query, start.input(), "the prompt is the filter itself")
	require.Contains(t, screen(t, m), "a label selects the stream")
	require.Contains(t, screen(t, m), "open logs", "and enter opens rather than advancing")

	// A filter that names a label is the query, and it shows as one.
	m = typed(t, m, "app=api")
	require.Contains(t, screen(t, m), `logql '{app=~"(?i)api"}'`)

	m = send(t, m, k("enter"))
	require.Equal(t, stateLogs, m.(Model).state)
	require.Equal(t, []string{`{app=~"(?i)api"}`}, m.(Model).logs.cfg.Pushed(),
		"and the stream is opened on it rather than on everything")
}

// TestEndpointIsPickedFromAList: with more endpoints than fit a row of chips,
// the endpoint is chosen the way an ssh host is.
func TestEndpointIsPickedFromAList(t *testing.T) {
	withEndpoints(t, []config.Place{
		{Name: "humo-management", Type: "victorialogs", URL: "https://grafana.example.com", Datasource: "aaa"},
		{Name: "humo-observer", Type: "victorialogs", URL: "https://grafana.example.com", Datasource: "bbb"},
		{Name: "logs-loki", Type: "loki", URL: "https://loki.example.com"},
	})

	// tab leaves the picker for the manual flow; the chips are the collectors
	// themselves, one per kind and never one per endpoint.
	m := send(t, New(), size(), k("tab"))
	require.Len(t, collectors, 6)
	m = send(t, m, tabs(4)...) // victorialogs
	require.Equal(t, source.CollectorVictoriaLogs, m.(Model).start.collectorAt())
	require.Equal(t, detailEndpoint, m.(Model).start.detail,
		"a database with nowhere to reach asks for that first")

	out := screen(t, m)
	require.Contains(t, out, "humo-management")
	require.Contains(t, out, "humo-observer")
	require.NotContains(t, out, "logs-loki", "a Loki endpoint is not a choice for LogsQL")

	// Typing narrows them, and enter takes the highlighted one.
	m = typeIn(t, m, "obs")
	m = send(t, m, k("down"), k("enter"))
	require.Equal(t, "humo-observer", m.(Model).start.endpoint().Name)
	require.Equal(t,
		"https://grafana.example.com/api/datasources/proxy/uid/bbb",
		m.(Model).start.endpoint().URL)

	m = send(t, m, k("enter"))
	require.Equal(t, detailNone, m.(Model).start.detail, "enter returns to the query")
}

// TestEndpointQueryOpens: what is typed is the query, terms and all.
func TestEndpointQueryOpens(t *testing.T) {
	withEndpoints(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
	})

	// tab leaves the picker for the manual flow, which is where a query is
	// written against a declared endpoint.
	m := send(t, New(), size(), k("tab"))
	m = send(t, m, tabs(4)...)                        // victorialogs
	m = send(t, m, k("down"), k("enter"), k("enter")) // the only endpoint declared
	m = typeIn(t, m, "level:error app:api")
	m = send(t, m, k("enter"), k("enter"))

	cfg := m.(Model).logs.cfg
	require.Equal(t, source.CollectorVictoriaLogs, cfg.Collector)
	require.Equal(t, "level:error app:api", cfg.Target, "LogsQL is sent as written")
	require.Equal(t, "https://logs.example.com", cfg.Endpoint.URL)
}

// TestVictoriaLogsOpensWithoutAQuery: LogsQL has a match-all, so an endpoint is
// enough on its own — the way a journal with no unit named is.
func TestVictoriaLogsOpensWithoutAQuery(t *testing.T) {
	withEndpoints(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
	})

	m := send(t, New(), size(), k("enter"), k("enter"), k("enter"))
	cfg := m.(Model).logs.cfg
	require.Equal(t, source.CollectorVictoriaLogs, cfg.Collector)
	require.Empty(t, cfg.Target)
	require.NoError(t, cfg.Validate())
	require.Contains(t, cfg.Command(), "*")
}

// TestEndpointTokenFailureIsReported: an endpoint telescope cannot authenticate
// to says so where it is chosen.
func TestEndpointTokenFailureIsReported(t *testing.T) {
	withEndpoints(t, []config.Place{
		{
			Name: "prod", Type: "victorialogs", URL: "https://logs.example.com",
			Token: config.Token{Env: "TELESCOPE_TEST_UNSET"},
		},
	})

	m := send(t, New(), size())
	out := screen(t, m)
	require.Contains(t, out, "TELESCOPE_TEST_UNSET")
	require.Empty(t, m.(Model).start.endpoints, "an endpoint without its token is not offered")
}

// TestEndpointOffersRecentQueries: a query is not listable, so history is the
// only suggestion there is — and the step must still show it.
func TestEndpointOffersRecentQueries(t *testing.T) {
	withEndpoints(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
	})
	prev := loadHistory
	loadHistory = func() config.History {
		return config.History{Targets: map[string][]string{
			"victorialogs|prod":  {"level:error"},
			"victorialogs|other": {"app:elsewhere"},
		}}
	}
	t.Cleanup(func() { loadHistory = prev })

	m := send(t, New(), size(), k("tab"))
	m = send(t, m, tabs(4)...)                        // victorialogs
	m = send(t, m, k("down"), k("enter"), k("enter")) // the only endpoint declared
	out := screen(t, m)
	require.Contains(t, out, "level:error")
	require.NotContains(t, out, "app:elsewhere", "a query belongs to the endpoint it was written for")
}

// TestTypedEndpoint: a database with no declaration is reachable by typing its
// URL, the way an ssh host is, and no secret is involved.
func TestTypedEndpoint(t *testing.T) {
	m := send(t, New(), size())
	m = send(t, m, tabs(4)...) // victorialogs
	require.Equal(t, detailEndpoint, m.(Model).start.detail,
		"an endpoint with nowhere to connect asks for that first")
	require.Contains(t, screen(t, m), "endpoint")

	for _, r := range "127.0.0.1:9428" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = send(t, m, k("enter"))
	require.Equal(t, detailNone, m.(Model).start.detail, "enter returns to the query")

	for _, r := range "level:error" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = send(t, m, k("enter"), k("enter"))
	cfg := m.(Model).logs.cfg
	require.Equal(t, "http://127.0.0.1:9428", cfg.Endpoint.URL, "a loopback address is plain http")
	require.Equal(t, "level:error", cfg.Target)
	require.Empty(t, cfg.Endpoint.Token)
}

func TestNormalizeURL(t *testing.T) {
	for _, tt := range [][2]string{
		{"logs.example.com", "https://logs.example.com"},
		{"logs.example.com/api/datasources/proxy/uid/x", "https://logs.example.com/api/datasources/proxy/uid/x"},
		{"https://logs.example.com", "https://logs.example.com"},
		{"http://logs.example.com", "http://logs.example.com"},
		{"127.0.0.1:9428", "http://127.0.0.1:9428"},
		{"localhost:9428/x", "http://localhost:9428/x"},
		{"  ", ""},
	} {
		require.Equal(t, tt[1], normalizeURL(tt[0]))
	}
}

// TestTypedEndpointIsRemembered: a URL typed by hand is offered back, since no
// listing reports it.
func TestTypedEndpointIsRemembered(t *testing.T) {
	prev := loadHistory
	loadHistory = func() config.History {
		return config.History{Endpoints: []string{"https://logs.example.com"}}
	}
	t.Cleanup(func() { loadHistory = prev })

	m := send(t, New(), size())
	m = send(t, m, tabs(4)...) // victorialogs
	require.Contains(t, screen(t, m), "https://logs.example.com")
}

// typeIn sends s one rune at a time, the way it is typed.
func typeIn(t *testing.T, m tea.Model, s string) tea.Model {
	t.Helper()
	for _, r := range s {
		m = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// TestTimeRangePicker: ctrl+g opens the window the source is read over, offers
// the ones worth reaching for, and shows what the chosen one resolves to.
func TestTimeRangePicker(t *testing.T) {
	m := send(t, New(), size(), k("tab"), k("tab")) // docker
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlG})
	require.Equal(t, detailRange, m.(Model).start.detail)

	out := screen(t, m)
	require.Contains(t, out, "time range")
	require.Contains(t, out, "yesterday", "the presets are offered")

	// Picking one shows the window it means, not only the words for it.
	m = typeIn(t, m, "6h..1h")
	require.Contains(t, screen(t, m), " → ")

	m = send(t, m, k("enter"))
	require.Equal(t, detailNone, m.(Model).start.detail)
	require.Contains(t, screen(t, m), "--since", "the preview already shows the bounds")

	m = typeIn(t, m, "clickhouse")
	m = send(t, m, k("enter"), k("enter"))

	cfg := m.(Model).logs.cfg
	require.Equal(t, "6h..1h", cfg.Range.Spec)
	require.Equal(t, 5*time.Hour, cfg.Range.Until.Sub(cfg.Range.Since))
	require.NotContains(t, cfg.Command(), " -f", "a window that has closed is not followed")
}

// TestTimeRangeRejectsNonsense: a window that does not read as one is reported
// where it was typed, rather than quietly reading everything.
func TestTimeRangeRejectsNonsense(t *testing.T) {
	m := send(t, New(), size(), k("tab"), k("tab")) // docker
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlG})
	m = typeIn(t, m, "yesteryear")
	m = send(t, m, k("enter"))

	require.Equal(t, detailRange, m.(Model).start.detail, "the prompt stays open")
	require.Contains(t, screen(t, m), "cannot read")
}

// TestTimeRangeIsNotOfferedForCommand: a free-form command is whatever was
// typed, so its bounds belong in it.
func TestTimeRangeIsNotOfferedForCommand(t *testing.T) {
	m := send(t, New(), size())
	m = send(t, m, tabs(3)...) // command
	require.Equal(t, source.CollectorCommand, m.(Model).start.collectorAt())
	require.NotContains(t, screen(t, m), "ctrl+g")

	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlG})
	require.Equal(t, detailNone, m.(Model).start.detail)
}

// TestEntryViewAlignsOnItsOwnKeys: a Kubernetes attribute is forty characters
// of prose. Folding it into a fixed narrow column rendered the key one syllable
// per line, down the left edge, for every field an OTEL entry carries.
func TestEntryViewAlignsOnItsOwnKeys(t *testing.T) {
	const key = "kubernetes.pod_labels.app.kubernetes.io/instance"
	raw := `{"level":"info","msg":"started","kubernetes.container_name":"kafkaproxy","` +
		key + `":"kafka-proxy-producer"}`

	m := logsModel(t, raw)
	m = send(t, m, k("enter"))
	out := screen(t, m)

	require.Contains(t, out, "kafkaproxy")
	require.Contains(t, out, "kafka-proxy-producer")
	require.Contains(t, out, key, "the long key is whole, on a line of its own")

	// The short keys still line up with each other, and nothing is broken
	// mid-word down a ten-column gutter.
	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(line)
		require.NotEqual(t, "kubernetes", trimmed, "a key was wrapped: %q", line)
		require.NotEqual(t, ".pod_label", trimmed, "a key was wrapped: %q", line)
	}
	require.Contains(t, out, "kubernetes.container_name   kafkaproxy",
		"a key that fits keeps its value beside it, in the shared column")
	var alone bool
	for line := range strings.SplitSeq(out, "\n") {
		alone = alone || strings.Trim(line, "│ ") == key
	}
	require.True(t, alone, "and one that does not takes the line alone, its value under it")
}

// TestPickSourcesToMerge: several saved sources marked with ctrl+a open as one
// stream, tagged with where each line came from.
func TestPickSourcesToMerge(t *testing.T) {
	withSaved(t, []config.Place{
		{Name: "api", Type: "docker", Container: "api"},
		{Name: "worker", Type: "docker", Container: "worker"},
	}, config.History{})

	m := send(t, New(), size())
	// The first down enters the list, which is already on the first source.
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlA}, k("down"), k("down"), tea.KeyMsg{Type: tea.KeyCtrlA})
	out := screen(t, m)
	require.Contains(t, out, "✓api", "what is picked says so")
	require.Contains(t, out, "✓worker")
	require.Contains(t, out, "enter open 2 merged")

	m = send(t, m, k("enter"))
	cfg := m.(Model).logs.cfg
	require.Equal(t, source.CollectorMerge, cfg.Collector)
	require.Equal(t, []string{"api", "worker"}, cfg.Labels())
	require.Contains(t, screen(t, m), "merge api + worker")
}

// TestPickOneOpensItAlone: marking a single source is not a merge of one.
func TestPickOneOpensItAlone(t *testing.T) {
	withSaved(t, []config.Place{
		{Name: "api", Type: "docker", Container: "api"},
		{Name: "worker", Type: "docker", Container: "worker"},
	}, config.History{})

	m := send(t, New(), size())
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlA}, k("enter"))
	require.Equal(t, source.CollectorDocker, m.(Model).logs.cfg.Collector)
	require.Equal(t, "api", m.(Model).logs.cfg.Container)
}

// TestPickRefusesWhatWouldHaveToAsk: a merge reads everything at once, so a
// source that still needs a pod cannot be part of one.
func TestPickRefusesWhatWouldHaveToAsk(t *testing.T) {
	withSaved(t, []config.Place{
		{Name: "pods", Type: "kubectl"},
		{Name: "api", Type: "docker", Container: "api"},
	}, config.History{})

	m := send(t, New(), size())
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlA})
	require.Empty(t, m.(Model).start.picked)
	require.Contains(t, screen(t, m), "does not say enough to open on its own")
}

// TestMergedLinesSayWhereTheyCameFrom: a merged view is only readable if each
// line names its source, and the tag stays put when the view scrolls sideways.
func TestMergedLinesSayWhereTheyCameFrom(t *testing.T) {
	cfg := source.Config{Collector: source.CollectorMerge, Merge: []source.Config{
		{Name: "api", Collector: source.CollectorDocker, Container: "api"},
		{Name: "worker", Collector: source.CollectorDocker, Container: "worker"},
	}}
	lg := newLogs(cfg, logs.NewStore(10), "")
	lg.resize(100, 30)
	lg.store.Append(source.Line{Data: []byte("first"), Source: "api"})
	lg.store.Append(source.Line{Data: []byte("second"), Source: "worker"})

	out := ansi.Strip(lg.View())
	require.Contains(t, out, "api    first", "the tags are one column, whatever their length")
	require.Contains(t, out, "worker second")

	lg.hoff = 3
	require.Contains(t, ansi.Strip(lg.View()), "worker ond", "which source is not scrolled away")
}

// TestGutterCarriesTimeAndLevel: a line from a log database is often a bare
// sentence, and everything that makes it a log entry is beside it. Without the
// columns the list is a wall of prose in arrival order.
func TestGutterCarriesTimeAndLevel(t *testing.T) {
	cfg := source.Config{Collector: source.CollectorLoki, Target: `{app="api"}`}
	lg := newLogs(cfg, logs.NewStore(10), "")
	lg.resize(100, 30)
	at := time.Date(2026, 8, 11, 15, 16, 36, 357_000_000, time.Local)
	lg.append(source.Line{
		Data:   []byte("read_request_line: client closed socket"),
		At:     at,
		Labels: []source.Label{{Key: "detected_level", Value: "ERROR"}},
	})

	out := ansi.Strip(lg.View())
	require.Contains(t, out, "15:16:36.357 ERROR read_request_line: client closed socket")

	lg.hoff = 5
	require.Contains(t, ansi.Strip(lg.View()), "15:16:36.357 ERROR request_line",
		"when and how bad do not scroll away with the text")
}

// TestGutterKeepsStructuredLinesAligned: a structured line is rendered with its
// own time and level, so the columns are left empty rather than doubled — but
// they stay reserved, or the two kinds of line would not line up.
func TestGutterKeepsStructuredLinesAligned(t *testing.T) {
	lg := newLogs(source.Config{Collector: source.CollectorLoki, Target: "*"}, logs.NewStore(10), "")
	lg.resize(120, 30)
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.Local)
	lg.append(source.Line{Data: []byte("bare"), At: at, Labels: []source.Label{{Key: "level", Value: "WARN"}}})
	lg.append(source.Line{Data: []byte(`{"level":"info","msg":"structured"}`), At: at})

	rows := map[string]int{}
	for line := range strings.SplitSeq(ansi.Strip(lg.View()), "\n") {
		for _, word := range []string{"bare", "structured"} {
			if strings.Contains(line, word) {
				rows[word] = strings.Index(line, word)
			}
		}
	}
	require.NotZero(t, rows["bare"])
	require.Equal(t, rows["bare"], rows["structured"], "both kinds start in the same column")
}

// TestEntryViewShowsLabels: everything the list has no room for lands here,
// split into what the whole stream shares and what this line brought.
func TestEntryViewShowsLabels(t *testing.T) {
	cfg := source.Config{
		Collector: source.CollectorLoki,
		Endpoint:  source.Endpoint{Name: "homelab", URL: "https://logs.example.com"},
		Target:    `{k8s_namespace_name="flux-system"}`,
	}
	e := logs.NewStore(10).Append(source.Line{
		Data: []byte("artifact up-to-date with remote revision"),
		At:   time.Now(),
		Labels: []source.Label{
			{Key: "k8s_container_name", Value: "manager"},
			{Key: "k8s_pod_name", Value: "source-controller-7f56dddc9d"},
		},
	})

	m := newEntry(cfg, e)
	m.resize(100, 40)
	out := ansi.Strip(m.View())

	require.Contains(t, out, "source")
	require.Contains(t, out, "endpoint")
	require.Contains(t, out, "homelab")
	require.Contains(t, out, `{k8s_namespace_name="flux-system"}`)
	require.Contains(t, out, "labels")
	require.Contains(t, out, "k8s_pod_name")
	require.Contains(t, out, "source-controller-7f56dddc9d")
}

// TestEntryViewEscapesWhatItShows: a label value is bytes somebody else chose,
// and a view that obeys them is a view somebody else draws.
func TestEntryViewEscapesWhatItShows(t *testing.T) {
	e := logs.NewStore(10).Append(source.Line{
		Data: []byte("hello"),
		Labels: []source.Label{
			{Key: "note", Value: "one\ntwo\x1b[2J"},
		},
	})

	m := newEntry(source.Config{Collector: source.CollectorLoki, Target: "*"}, e)
	m.resize(100, 40)
	out := m.View()

	require.Contains(t, ansi.Strip(out), `one\ntwo\e[2J`)
	require.NotContains(t, out, "\x1b[2J", "the sequence is shown, not run")
	for line := range strings.SplitSeq(ansi.Strip(out), "\n") {
		require.NotEqual(t, "two", strings.Trim(line, "│ "), "and it stays on one row")
	}
}

// TestEntryViewRecognizesLabels: a trace id and a severity are worth spotting
// in a wall of twenty OTEL attributes.
func TestEntryViewRecognizesLabels(t *testing.T) {
	e := logs.NewStore(10).Append(source.Line{
		Data: []byte("Query Clickhouse"),
		At:   time.Now(),
		Labels: []source.Label{
			{Key: "detected_level", Value: "DEBUG"},
			{Key: "trace_id", Value: "e8b5497ee616f2116b0c46f13eb82c82"},
			{Key: "span_id", Value: "e3ac4c34da7b0d90"},
		},
	})
	require.Equal(t, "e8b5497ee616f2116b0c46f13eb82c82", e.Record.TraceID,
		"a trace id in a label is a trace id")
	require.Equal(t, "e3ac4c34da7b0d90", e.Record.SpanID)

	m := newEntry(source.Config{Collector: source.CollectorLoki, Target: "*"}, e)
	m.resize(100, 40)
	out := m.View()

	trace := styleTrace.Render("e8b5497ee616f2116b0c46f13eb82c82")
	require.Contains(t, out, trace, "the id is colored as one")
	require.Contains(t, out, renderLevelWord(zapcore.DebugLevel), "and the level as a level")
}

// TestLongKeyKeepsItsValue: a key one column short of the widest is not a long
// key, and pushing its value onto the next row breaks the alignment that makes
// a list of attributes readable in the first place.
func TestLongKeyKeepsItsValue(t *testing.T) {
	e := logs.NewStore(10).Append(source.Line{
		Data: []byte("Query Clickhouse"),
		Labels: []source.Label{
			// 27 columns, against a label column of 28.
			{Key: "process_runtime_description", Value: "go version go1.26.5 linux/amd64"},
			{Key: "process_runtime_name", Value: "go"},
		},
	})

	m := newEntry(source.Config{Collector: source.CollectorLoki, Target: "*"}, e)
	m.resize(150, 40)
	out := ansi.Strip(m.View())

	require.Contains(t, out, "process_runtime_description go version go1.26.5 linux/amd64")

	var long, short int
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.Contains(line, "process_runtime_description"):
			long = strings.Index(line, "go version")
		case strings.Contains(line, "process_runtime_name"):
			short = strings.LastIndex(line, "go")
		}
	}
	require.NotZero(t, long)
	require.Equal(t, short, long, "both values start in the shared column")
}

// bgSeqs returns the background sequences of a rendered row, in order.
var bgSeqs = regexp.MustCompile(`\x1b\[48;[0-9;]*m`).FindAllString

// TestCursorRowFades: the row under the cursor takes a violet-to-magenta fade,
// and the line's own coloring has to survive being laid on it.
func TestCursorRowFades(t *testing.T) {
	row := "\x1b[38;2;1;2;3mhello\x1b[0m world"
	out := cursorRow(row, 40)

	require.Equal(t, "hello world"+strings.Repeat(" ", 29), ansi.Strip(out),
		"the row reaches the frame")
	require.Contains(t, out, "\x1b[38;2;1;2;3m", "and keeps what it was wearing")

	seqs := bgSeqs(out, -1)
	require.Greater(t, len(seqs), 2)
	require.NotEqual(t, seqs[0], seqs[len(seqs)-1], "the background fades across the row")
}

// TestBandIsOneColorAllTheWay: a band is flat, so however many times the line
// resets its own colors, the wash under it is one color and one sequence per
// reset — not one per column.
func TestBandIsOneColorAllTheWay(t *testing.T) {
	row := "\x1b[31ma\x1b[0mb\x1b[0mc"
	out := bandRow(row, 30)

	require.Equal(t, "abc"+strings.Repeat(" ", 27), ansi.Strip(out))
	seqs := bgSeqs(out, -1)
	require.NotEmpty(t, seqs)
	for _, s := range seqs {
		require.Equal(t, seqs[0], s, "one wash, not a gradient")
	}
	require.LessOrEqual(t, len(seqs), 3, "one per reset, not one per column")
}

// TestBandsAndCursorReachTheList: what the store worked out about time shows up
// in the view, under the row it belongs to.
func TestBandsAndCursorReachTheList(t *testing.T) {
	lg := newLogs(source.Config{Collector: source.CollectorLoki, Target: "*"}, logs.NewStore(10), "")
	lg.resize(80, 20)
	at := time.Date(2026, 8, 11, 15, 16, 36, 0, time.Local)
	words := []string{"first", "second", "third", "fourth"}
	for i, w := range words {
		lg.append(source.Line{Data: []byte(w), At: at.Add(time.Duration(i) * time.Second)})
	}
	lg.cursor, lg.follow = 3, false

	rows := map[string]string{}
	for line := range strings.SplitSeq(lg.View(), "\n") {
		for _, w := range words {
			if strings.Contains(ansi.Strip(line), w) {
				rows[w] = line
			}
		}
	}
	require.Len(t, rows, 4)

	painted := func(w string) bool { return len(bgSeqs(rows[w], 1)) > 0 }
	require.True(t, painted("fourth"), "the cursor row is painted")
	require.Equal(t, painted("first"), painted("third"), "every other second is washed")
	require.NotEqual(t, painted("first"), painted("second"), "and the one between is not")
}

// TestBouncingOffValidationKeepsTheListing pins the listing to the step it
// belongs to. Walking on from a kubectl source that names no pod fails at the
// query, which lands back on the collector — where the pods have to be.
func TestBouncingOffValidationKeepsTheListing(t *testing.T) {
	m := send(t, New(), size(), k("tab")) // kubectl
	m = send(t, m, candidates(m, "api-0", "api-1"))
	require.Contains(t, screen(t, m), "api-0")

	// Nothing picked: enter walks on to the query, and enter there cannot
	// connect a source that names no pod.
	m = send(t, m, k("enter"), k("enter"))

	out := screen(t, m)
	require.Contains(t, out, "✗", "the source is still incomplete, and says so")
	require.Contains(t, out, "api-0", "the pods come back with the step")
	require.Contains(t, out, "api-1")
}
