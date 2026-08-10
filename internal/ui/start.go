package ui

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

// logo is the banner shown on the start screen.
const logo = "" +
	"╺┳╸┏━╸╻  ┏━╸┏━┓┏━╸┏━┓┏━┓┏━╸\n" +
	" ┃ ┣╸ ┃  ┣╸ ┗━┓┃  ┃ ┃┣━┛┣╸ \n" +
	" ╹ ┗━╸┗━╸┗━╸┗━┛┗━╸┗━┛╹  ┗━╸"

// The prompt bar grows with the terminal between these bounds.
const (
	minPromptWidth = 64
	maxPromptWidth = 100
)

// detail names which detail of a source the prompt bar is editing, apart from
// the target itself.
type detail int

const (
	detailNone detail = iota
	detailKubeConfig
	detailKubeContext
	detailEndpoint
)

// label names the detail for the chip above the prompt.
func (d detail) label() string {
	switch d {
	case detailKubeContext:
		return "context"
	case detailEndpoint:
		return "endpoint"
	default:
		return "kubeconfig"
	}
}

type startStep int

const (
	// stepSaved is only reached when the config file declares sources.
	stepSaved startStep = iota
	stepTransport
	stepCollector
	stepQuery
)

var (
	transports = []source.Transport{source.TransportLocal, source.TransportSSH}
	collectors = []source.Collector{
		source.CollectorJournal,
		source.CollectorKubectl,
		source.CollectorDocker,
		source.CollectorCommand,
	}
	// databases read over HTTP rather than by running a command. Each is
	// offered with nothing declared, for an endpoint typed at the prompt.
	databases = []source.Collector{
		source.CollectorVictoriaLogs,
		source.CollectorLoki,
	}
)

// choice is one chip of the collector step. A collector that reads from a log
// database is offered once per declared endpoint, since the endpoint is as much
// a part of "where the logs are" as the host is for the others.
type choice struct {
	collector source.Collector
	endpoint  source.Endpoint
}

// label names the chip. A declared endpoint is named by itself: which database
// it speaks to is already visible in the command preview. One with nothing
// declared is named by its collector, since that is all it is yet.
func (c choice) label() string {
	if c.collector.IsRemoteAPI() {
		if l := c.endpoint.Label(); l != "" {
			return l
		}
	}
	return string(c.collector)
}

// optionsFor lists the collectors offered, followed by one entry per endpoint.
//
// A collector that reads from a database is offered even with nothing declared:
// its endpoint is then typed at the prompt, the way an ssh host is.
func optionsFor(endpoints []source.Endpoint) []choice {
	out := make([]choice, 0, len(collectors)+len(databases)+len(endpoints))
	for _, c := range collectors {
		out = append(out, choice{collector: c})
	}
	for _, c := range databases {
		out = append(out, choice{collector: c})
	}
	for _, e := range endpoints {
		out = append(out, choice{collector: e.Collector, endpoint: e})
	}
	return out
}

const (
	// maxContentWidth keeps the centered screen readable on a wide terminal,
	// and minDetailWidth is the room always left for a state word next to a
	// value, wide enough for the longest one worth reading.
	maxContentWidth = 160
	minDetailWidth  = 20
	// maxTopPad keeps the block near the top of a tall terminal. Centering it
	// vertically leaves the suggestions floating in the middle with a dozen
	// empty rows above them, and moves the logo whenever the list grows.
	maxTopPad = 2
)

// connectMsg asks the app to open a stream.
type connectMsg struct {
	cfg   source.Config
	query string
}

// candidatesMsg carries a completion result. Key identifies the request, so a
// reply that arrives after the user moved on is discarded.
type candidatesMsg struct {
	key   string
	items []complete.Candidate
	err   error
}

type startModel struct {
	w, h int

	step      startStep
	transport int
	collector int

	savedFilter textinput.Model
	host        textinput.Model
	target      textinput.Model
	query       textinput.Model
	kubeconfig  textinput.Model
	endpointURL textinput.Model

	kubecontext textinput.Model
	// detail swaps the prompt bar over to a detail of the source while the
	// collector step is open, so the pod listing can be re-run against a
	// kubeconfig before a target is typed.
	detail detail
	// elevate runs the collector, and its listings, under sudo.
	elevate bool

	tail   int
	follow bool
	err    error

	// Completion state for the current step.
	candKey    string
	candidates []complete.Candidate
	filtered   []complete.Candidate
	// sel is the highlighted suggestion, or -1 when the input has focus.
	sel     int
	loading bool
	candErr error

	// saved are the sources declared in the config file, and history is what
	// previous runs reached for.
	saved []config.Source
	// savedIdx maps each filtered suggestion back to its source, since two
	// sources may share a name and filtering reorders them.
	savedIdx []int
	history  config.History

	// options are the collector chips, one of them per declared endpoint.
	options []choice

	// cache holds every completion result seen so far, keyed by request, so
	// stepping back and forth never waits on a listing twice.
	cache map[string]cacheEntry
	// inflight tracks requests already asked for, including preloads.
	inflight map[string]bool
}

// cacheEntry is one completed lookup, successful or not.
type cacheEntry struct {
	items []complete.Candidate
	err   error
}

func newStart() startModel {
	mk := func(placeholder string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = placeholder
		ti.Prompt = "❯ "
		ti.PromptStyle = lipgloss.NewStyle().Foreground(colorAccent)
		ti.Width = minPromptWidth - 6
		return ti
	}
	m := startModel{
		savedFilter: mk("filter saved sources"),
		host:        mk("user@host"),
		target:      mk(""),
		query:       mk("grep term or regexp, empty for everything"),
		kubeconfig:  mk("path to kubeconfig, e.g. /etc/rancher/k3s/k3s.yaml"),
		endpointURL: mk("https://logs.example.com or https://grafana/api/datasources/proxy/uid/<uid>"),
		kubecontext: mk("context inside that kubeconfig"),
		tail:        1000,
		follow:      true,
		sel:         -1,
		cache:       map[string]cacheEntry{},
		inflight:    map[string]bool{},
	}
	cfg, err := loadConfig()
	m.saved, m.err = cfg.Sources, err
	endpoints, endpointErr := cfg.Resolved()
	if m.err == nil {
		m.err = endpointErr
	}
	m.options = optionsFor(endpoints)
	m.history = loadHistory()
	if len(m.saved) == 0 {
		m.step = stepTransport
	} else {
		m.candidates = m.savedCandidates()
		m.refilter()
	}

	m.syncPlaceholder()
	m.focus()
	return m
}

// loadConfig and loadHistory are variables so tests can supply their own
// without touching the user's files.
var (
	loadConfig  = config.Load
	loadHistory = config.LoadHistory
)

// savedCandidates renders the declared sources as suggestions. A source that
// does not name a target says so, since picking it opens the prompt rather
// than the logs.
func (m startModel) savedCandidates() []complete.Candidate {
	out := make([]complete.Candidate, 0, len(m.saved))
	for _, s := range m.saved {
		cfg, ready, err := s.Stream()
		if err != nil {
			out = append(out, complete.Candidate{Value: s.Name, State: "invalid", Detail: err.Error()})
			continue
		}
		where := "local"
		switch {
		case cfg.Collector.IsRemoteAPI():
			where = cfg.Endpoint.Label()
		case cfg.Transport == source.TransportSSH:
			where = "ssh://" + cfg.Host
		}
		detail := where + " · " + string(cfg.Collector)
		if !ready {
			detail += " · " + missingLabel(cfg.Collector)
		}
		out = append(out, complete.Candidate{Value: s.Name, Detail: detail})
	}
	return out
}

// choice is the collector chip in hand.
func (m startModel) choice() choice {
	if m.collector < 0 || m.collector >= len(m.options) {
		return choice{collector: collectors[0]}
	}
	return m.options[m.collector]
}

// endpoint is the endpoint in hand: the one the chip carries, or, for the chip
// that carries none, whatever was typed at the prompt.
func (m startModel) endpoint() source.Endpoint {
	e := m.choice().endpoint
	if e.URL == "" {
		e.URL = normalizeURL(m.endpointURL.Value())
	}
	return e
}

// normalizeURL fills in the scheme an address typed by hand tends to omit. A
// loopback address is plain http: nothing serves TLS on localhost.
func normalizeURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "://") {
		return s
	}
	host, _, _ := strings.Cut(s, "/")
	host, _, _ = strings.Cut(host, ":")
	switch host {
	case "localhost", "127.0.0.1", "[::1]", "::1":
		return "http://" + s
	}
	return "https://" + s
}

// toggleDetail switches the prompt to a detail, or back out of it when that
// detail is already being edited.
func toggleDetail(cur, want detail) detail {
	if cur == want {
		return detailNone
	}
	return want
}

// missingLabel names what an incomplete source will ask for.
func missingLabel(c source.Collector) string {
	switch c {
	case source.CollectorKubectl:
		return "pick a pod"
	case source.CollectorDocker:
		return "pick a container"
	case source.CollectorCommand:
		return "type a command"
	case source.CollectorVictoriaLogs, source.CollectorLoki:
		return "type a query"
	default:
		return "pick a unit"
	}
}

// openSaved acts on the source at index i: it streams one that names a target,
// and otherwise unwinds into the prompt with everything it did name already
// filled in, so a source can pin a cluster and leave the pod open.
func (m startModel) openSaved(i int) (startModel, tea.Cmd) {
	if i < 0 || i >= len(m.saved) {
		return m, nil
	}
	s := m.saved[i]
	cfg, ready, err := s.Stream()
	if err != nil {
		m.err = err
		return m, nil
	}
	if ready {
		return m, func() tea.Msg { return connectMsg{cfg: cfg, query: s.Query} }
	}
	return m.prefill(cfg, s.Query)
}

// prefill seeds the manual flow from a config and stops at the step that still
// needs an answer.
func (m startModel) prefill(cfg source.Config, query string) (startModel, tea.Cmd) {
	m.transport = max(slices.Index(transports, cfg.Transport), 0)
	m.collector = max(slices.IndexFunc(m.options, func(c choice) bool {
		return c.collector == cfg.Collector && c.endpoint.URL == cfg.Endpoint.URL
	}), 0)
	m.host.SetValue(cfg.Host)
	m.kubeconfig.SetValue(cfg.KubeConfig)
	m.kubecontext.SetValue(cfg.KubeContext)
	m.target.SetValue(config.Target(cfg))
	m.query.SetValue(query)
	m.elevate = cfg.Elevate
	m.tail, m.follow = cfg.Tail, cfg.Follow

	m.step = stepCollector
	m.detail = detailNone
	m.err = nil
	m.syncPlaceholder()
	m.focus()
	m.input().CursorEnd()
	return m, m.fetch()
}

// request describes what the current step can complete.
func (m startModel) request() (complete.Request, bool) {
	switch m.step {
	case stepSaved:
		// Declared sources are already in memory; nothing to look up.
		return complete.Request{}, false
	case stepTransport:
		if transports[m.transport] != source.TransportSSH {
			return complete.Request{}, false
		}
		return complete.Request{Field: complete.FieldHost}, true
	case stepCollector:
		if m.choice().collector.IsRemoteAPI() {
			// A query is written, not picked from a listing. What was written
			// before is still offered, from history.
			return complete.Request{}, false
		}
		req := complete.Request{
			Field:       complete.FieldTarget,
			Transport:   transports[m.transport],
			Host:        strings.TrimSpace(m.host.Value()),
			Collector:   m.choice().collector,
			Elevate:     m.elevate,
			KubeConfig:  strings.TrimSpace(m.kubeconfig.Value()),
			KubeContext: strings.TrimSpace(m.kubecontext.Value()),
		}
		switch m.detail {
		case detailKubeConfig:
			req.Field, req.Collector = complete.FieldKubeConfig, source.CollectorKubectl
		case detailKubeContext:
			req.Field, req.Collector = complete.FieldKubeContext, source.CollectorKubectl
		}
		return req, true
	default:
		return complete.Request{}, false
	}
}

// fetch shows the suggestions for the current step, from cache when possible,
// and preloads the ones the next keystroke is likely to need.
func (m *startModel) fetch() tea.Cmd {
	m.candidates, m.filtered, m.candErr, m.sel = nil, nil, nil, -1

	if m.step == stepSaved {
		m.candKey, m.loading = "", false
		m.candidates = m.savedCandidates()
		m.refilter()
		return nil
	}

	req, ok := m.request()
	if !ok {
		// Nothing to list, which is not the same as nothing to show: a step
		// with no listing still offers what was used here before.
		m.candKey, m.loading = "", false
		m.refilter()
		return m.preload()
	}
	m.candKey = req.Key()

	if e, cached := m.cache[m.candKey]; cached {
		m.loading = false
		m.candidates, m.candErr = e.items, e.err
		m.refilter()
		return m.preload()
	}
	m.loading = true
	return tea.Batch(m.requestCmd(req), m.preload())
}

// requestCmd asks for one result set, unless it is already on its way.
func (m *startModel) requestCmd(req complete.Request) tea.Cmd {
	key := req.Key()
	if m.inflight[key] {
		return nil
	}
	m.inflight[key] = true
	return func() tea.Msg {
		items, err := fetcher(context.Background(), req)
		return candidatesMsg{key: key, items: items, err: err}
	}
}

// preload warms the result sets the user has not asked for yet: the ssh hosts
// while the transport is still being chosen, and every collector once the
// transport is known. Listings are slow enough that switching chips would
// otherwise stall on each one in turn.
func (m *startModel) preload() tea.Cmd {
	var cmds []tea.Cmd
	queue := func(req complete.Request) {
		if _, cached := m.cache[req.Key()]; cached {
			return
		}
		if cmd := m.requestCmd(req); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	switch m.step {
	case stepTransport:
		// Reading ssh_config is cheap; do it even while local is selected.
		queue(complete.Request{Field: complete.FieldHost})
	case stepCollector:
		for _, c := range collectors {
			queue(complete.Request{
				Field:       complete.FieldTarget,
				Transport:   transports[m.transport],
				Host:        strings.TrimSpace(m.host.Value()),
				Collector:   c,
				Elevate:     m.elevate,
				KubeConfig:  strings.TrimSpace(m.kubeconfig.Value()),
				KubeContext: strings.TrimSpace(m.kubecontext.Value()),
			})
		}
	}
	return tea.Batch(cmds...)
}

// refresh drops the cached result for the current step and asks again.
func (m *startModel) refresh() tea.Cmd {
	req, ok := m.request()
	if !ok {
		return nil
	}
	delete(m.cache, req.Key())
	delete(m.inflight, req.Key())
	m.candidates, m.filtered, m.candErr, m.sel = nil, nil, nil, -1
	m.loading = true
	return m.requestCmd(req)
}

// fetcher resolves completions. It is a variable so tests can avoid running
// real listing commands.
var fetcher = complete.Fetch

// refilter narrows the suggestions to what has been typed, with the values
// this machine reached for before floated to the top.
func (m *startModel) refilter() {
	if m.step == stepSaved {
		m.filtered, m.savedIdx = m.filterSaved(m.input().Value())
	} else {
		m.filtered = complete.Rank(withRecent(m.candidates, m.recent()), m.input().Value(), m.attr())
	}
	if m.sel >= len(m.filtered) {
		m.sel = len(m.filtered) - 1
	}
}

// attr resolves the "field:value" terms of a query against the candidates of
// the step being completed. Only the target has fields worth naming: a host or
// a kubeconfig path has nothing to filter by.
func (m startModel) attr() complete.Attr {
	if m.step != stepCollector || m.detail != detailNone {
		return nil
	}
	return complete.AttrFor(m.choice().collector)
}

// filterSaved narrows the declared sources and records where each survivor came
// from, consuming each origin once so identical names stay distinct.
func (m startModel) filterSaved(query string) ([]complete.Candidate, []int) {
	all := m.savedCandidates()
	ranked := complete.Rank(all, query, nil)

	idx := make([]int, len(ranked))
	taken := make([]bool, len(all))
	for i, c := range ranked {
		for j, a := range all {
			// Compared field by field: a candidate carries match offsets, and
			// two sources may otherwise look identical.
			if !taken[j] && a.Value == c.Value && a.Detail == c.Detail && a.State == c.State {
				idx[i], taken[j] = j, true
				break
			}
		}
	}
	return ranked, idx
}

// recent returns the remembered values for the current step, newest first.
func (m startModel) recent() []string {
	switch {
	case m.step == stepTransport:
		if transports[m.transport] != source.TransportSSH {
			return nil
		}
		return m.history.Hosts
	case m.step == stepCollector && m.detail == detailKubeConfig:
		return m.history.KubeConfigs
	case m.step == stepCollector && m.detail == detailKubeContext:
		return nil
	case m.step == stepCollector && m.detail == detailEndpoint:
		return m.history.Endpoints
	case m.step == stepCollector:
		// Scoped to the cluster or host in the prompt: a pod remembered from
		// another kubeconfig does not exist here.
		return m.history.Recent(m.config())
	default:
		return nil
	}
}

// withRecent moves remembered values to the front, adding any that the host no
// longer lists: a kubeconfig typed by hand is not something a probe can find.
func withRecent(items []complete.Candidate, recent []string) []complete.Candidate {
	if len(recent) == 0 {
		return items
	}
	known := make(map[string]complete.Candidate, len(items))
	for _, c := range items {
		known[c.Value] = c
	}

	out := make([]complete.Candidate, 0, len(items)+len(recent))
	seen := make(map[string]bool, len(recent))
	for _, v := range recent {
		if seen[v] {
			continue
		}
		seen[v] = true
		if c, ok := known[v]; ok {
			out = append(out, c)
			continue
		}
		out = append(out, complete.Candidate{Value: v, Detail: "recent"})
	}
	for _, c := range items {
		if !seen[c.Value] {
			out = append(out, c)
		}
	}
	return out
}

// accept inserts the highlighted suggestion.
func (m *startModel) accept() {
	if m.sel < 0 || m.sel >= len(m.filtered) {
		return
	}
	in := m.input()
	in.SetValue(m.filtered[m.sel].Value)
	in.CursorEnd()
	m.sel = -1
	m.refilter()
}

// input returns the text input backing the current step.
func (m *startModel) input() *textinput.Model {
	switch {
	case m.step == stepSaved:
		return &m.savedFilter
	case m.step == stepTransport:
		return &m.host
	case m.step == stepCollector && m.detail == detailKubeConfig:
		return &m.kubeconfig
	case m.step == stepCollector && m.detail == detailKubeContext:
		return &m.kubecontext
	case m.step == stepCollector && m.detail == detailEndpoint:
		return &m.endpointURL
	case m.step == stepCollector:
		return &m.target
	default:
		return &m.query
	}
}

// kubectlSelected reports whether the kubeconfig applies to the current choice.
func (m startModel) kubectlSelected() bool {
	return m.choice().collector == source.CollectorKubectl
}

// active reports whether the current step accepts text.
func (m startModel) active() bool {
	return m.step != stepTransport || transports[m.transport] == source.TransportSSH
}

func (m *startModel) focus() {
	m.savedFilter.Blur()
	m.host.Blur()
	m.target.Blur()
	m.query.Blur()
	m.kubeconfig.Blur()
	m.endpointURL.Blur()
	m.kubecontext.Blur()
	if m.active() {
		m.input().Focus()
	}
}

func (m *startModel) syncPlaceholder() {
	switch m.choice().collector {
	case source.CollectorJournal:
		m.target.Placeholder = "[user/]unit, e.g. kubelet — empty for the whole journal"
	case source.CollectorKubectl:
		m.target.Placeholder = "[namespace/]pod-or-kind/name[:container] or [ns/]app=name"
	case source.CollectorDocker:
		m.target.Placeholder = "container name or id"
	case source.CollectorCommand:
		m.target.Placeholder = "any command writing logs to stdout"
	case source.CollectorVictoriaLogs:
		m.target.Placeholder = "LogsQL, e.g. level:error _time:5m"
	case source.CollectorLoki:
		m.target.Placeholder = `LogQL, e.g. {app="api"} |= "error"`
	}
}

func (m startModel) config() source.Config {
	cfg := source.Config{
		Transport: transports[m.transport],
		Host:      strings.TrimSpace(m.host.Value()),
		Collector: m.choice().collector,
		Endpoint:  m.endpoint(),
		Tail:      m.tail,
		Follow:    m.follow,
	}
	cfg.Elevate = m.elevate
	cfg.KubeConfig = strings.TrimSpace(m.kubeconfig.Value())
	cfg.KubeContext = strings.TrimSpace(m.kubecontext.Value())

	// The filter terms narrowed the list; what is left, plus whatever the terms
	// named, is the target itself.
	target := complete.Target(m.target.Value(), cfg.Collector)
	switch cfg.Collector {
	case source.CollectorJournal:
		cfg.Unit, cfg.UserUnit = source.ParseJournalTarget(target)
	case source.CollectorKubectl:
		cfg.Namespace, cfg.Target, cfg.Container = source.ParseKubeTarget(target)
	case source.CollectorDocker:
		cfg.Container = target
	case source.CollectorCommand:
		cfg.Args = target
	case source.CollectorVictoriaLogs, source.CollectorLoki:
		cfg.Target = target
	}
	return cfg
}

func (m startModel) Update(msg tea.Msg) (startModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.resizeInputs()
		return m, nil
	case candidatesMsg:
		// Every reply is cached, including preloads for steps not yet visited.
		m.cache[msg.key] = cacheEntry{items: msg.items, err: msg.err}
		delete(m.inflight, msg.key)
		if msg.key != m.candKey {
			return m, nil
		}
		m.loading = false
		m.candidates, m.candErr = msg.items, msg.err
		m.refilter()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "down", "ctrl+n":
			if len(m.filtered) > 0 {
				m.sel = min(m.sel+1, len(m.filtered)-1)
				return m, nil
			}
		case "up", "ctrl+p":
			if m.sel >= 0 {
				m.sel--
				return m, nil
			}
		case "pgdown":
			if len(m.filtered) > 0 {
				m.sel = min(m.sel+m.listHeight(), len(m.filtered)-1)
				return m, nil
			}
		case "pgup":
			if m.sel >= 0 {
				m.sel = max(m.sel-m.listHeight(), 0)
				return m, nil
			}
		case "home":
			if len(m.filtered) > 0 {
				m.sel = 0
				return m, nil
			}
		case "end":
			if len(m.filtered) > 0 {
				m.sel = len(m.filtered) - 1
				return m, nil
			}
		case "tab":
			if m.step == stepSaved {
				m.step = stepTransport
				m.err = nil
				m.focus()
				return m, m.fetch()
			}
			if m.sel >= 0 {
				m.accept()
				return m, nil
			}
			if m.choices() > 0 {
				return m, m.cycle(1)
			}
		case "right":
			if n := m.choices(); n > 0 && !m.active() {
				return m, m.cycle(1)
			}
		case "shift+tab", "left":
			if n := m.choices(); n > 0 && (msg.String() == "shift+tab" || !m.active()) {
				return m, m.cycle(-1)
			}
		case "ctrl+r":
			return m, m.refresh()
		case "ctrl+s":
			if m.step >= stepCollector {
				m.elevate = !m.elevate
				return m, m.fetch()
			}
		case "ctrl+k":
			if m.step == stepCollector && m.kubectlSelected() {
				m.detail = toggleDetail(m.detail, detailKubeConfig)
				m.focus()
				return m, m.fetch()
			}
		case "ctrl+x":
			if m.step == stepCollector && m.kubectlSelected() {
				m.detail = toggleDetail(m.detail, detailKubeContext)
				m.focus()
				return m, m.fetch()
			}
		case "ctrl+e":
			if m.step == stepCollector && m.choice().collector.IsRemoteAPI() &&
				m.choice().endpoint.Name == "" {
				m.detail = toggleDetail(m.detail, detailEndpoint)
				m.focus()
				return m, m.fetch()
			}
		case "ctrl+f":
			m.follow = !m.follow
			return m, nil
		case "ctrl+t":
			m.tail = nextTail(m.tail)
			return m, nil
		case "enter":
			if m.step == stepSaved {
				if len(m.filtered) == 0 {
					return m, nil
				}
				return m.openSaved(m.savedIdx[max(m.sel, 0)])
			}
			if m.sel >= 0 {
				m.accept()
				return m, nil
			}
			if m.detail != detailNone {
				m.detail = detailNone
				m.focus()
				return m, m.fetch()
			}
			return m.advance()
		case "esc":
			// Unwind one layer at a time, and quit once there is nothing left
			// to back out of.
			switch {
			case m.sel >= 0:
				m.sel = -1
			case m.detail != detailNone:
				m.detail = detailNone
				m.focus()
				return m, m.fetch()
			case m.step > stepTransport, m.step == stepTransport && len(m.saved) > 0:
				m.step--
				m.err = nil
				m.focus()
				return m, m.fetch()
			default:
				return m, func() tea.Msg { return quitMsg{} }
			}
			return m, nil
		}
	}
	if !m.active() {
		return m, nil
	}
	before := m.input().Value()
	var cmd tea.Cmd
	*m.input(), cmd = m.input().Update(msg)
	if m.input().Value() != before {
		m.sel = -1
		m.refilter()
	}
	return m, cmd
}

// choices returns how many chips the current step offers.
func (m startModel) choices() int {
	switch m.step {
	case stepTransport:
		return len(transports)
	case stepCollector:
		return len(m.options)
	default:
		return 0
	}
}

// cycle moves the chip selection and refreshes the suggestions it invalidates.
func (m *startModel) cycle(d int) tea.Cmd {
	n := m.choices()
	switch m.step {
	case stepTransport:
		m.transport = (m.transport + d + n) % n
	case stepCollector:
		m.collector = (m.collector + d + n) % n
		m.syncPlaceholder()
		// An endpoint with nowhere to connect asks for that before a query,
		// the way the ssh transport asks for a host.
		m.detail = detailNone
		if m.endpoint().URL == "" && m.choice().collector.IsRemoteAPI() {
			m.detail = detailEndpoint
		}
	}
	m.err = nil
	m.focus()
	return m.fetch()
}

func (m startModel) advance() (startModel, tea.Cmd) {
	if m.step < stepQuery {
		if m.step == stepTransport && transports[m.transport] == source.TransportSSH &&
			strings.TrimSpace(m.host.Value()) == "" {
			m.err = errEmptyHost
			return m, nil
		}
		m.step++
		m.detail = detailNone
		m.err = nil
		m.focus()
		return m, m.fetch()
	}
	cfg := m.config()
	if err := cfg.Validate(); err != nil {
		m.err = err
		m.step = stepCollector
		m.focus()
		return m, nil
	}
	query := strings.TrimSpace(m.query.Value())
	return m, func() tea.Msg { return connectMsg{cfg: cfg, query: query} }
}

// head renders everything above the suggestion list.
func (m startModel) head() string {
	var b strings.Builder
	b.WriteString(styleLogo.Render(logo))
	b.WriteString("\n")
	b.WriteString(styleHint.Render("telemetry viewer · logs"))
	b.WriteString("\n\n")

	if crumb := m.breadcrumb(); crumb != "" {
		b.WriteString(crumb)
		b.WriteString("\n\n")
	}
	if chips := m.chips(); chips != "" {
		b.WriteString(chips)
		b.WriteString("\n\n")
	}

	if m.detail != detailNone {
		b.WriteString(styleChipActive.Render(m.detail.label()))
		b.WriteString("\n\n")
	}
	box := styleBox
	if m.active() {
		box = styleBoxFocus
	}
	bar := m.input().View()
	if !m.active() {
		bar = styleDim.Render("  local machine")
	}
	b.WriteString(box.Width(m.promptWidth()).Render(bar))
	b.WriteString("\n\n")

	if m.err != nil {
		b.WriteString(styleErr.Render("✗ " + m.err.Error()))
	} else {
		b.WriteString(styleHint.Render(m.help()))
	}
	b.WriteString("\n\n")
	return b.String()
}

// topPad is how many rows are left above the logo. It is derived from the head
// alone, never from the list, so the screen does not move as suggestions come
// and go.
func (m startModel) topPad() int {
	return min(max((m.h-lipgloss.Height(m.head()))/2, 0), maxTopPad)
}

// listHeight is how many suggestion rows fit under the head. Paging keys use
// the same number the view renders.
func (m startModel) listHeight() int {
	// The head ends in a blank line that the list is joined onto, so it costs
	// one row less than it measures.
	return max(m.h-m.topPad()-lipgloss.Height(m.head())+1, 3)
}

func (m startModel) View() string {
	head := m.head()
	body := m.completions(m.listHeight())
	if body == "" {
		body = m.hints()
	}

	// A fixed content width is what keeps the screen still: centering on the
	// widest line would shift every row as the command preview grows with each
	// keystroke.
	content := lipgloss.NewStyle().
		Width(m.contentWidth()).
		Align(lipgloss.Center).
		Render(head + body)
	content = strings.Repeat("\n", m.topPad()) + content
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Top, content)
}

// contentWidth is the stable width every line of the start screen is centered
// within.
func (m startModel) contentWidth() int {
	return min(max(m.w-2*screenPad, minPromptWidth), maxContentWidth)
}

// promptWidth is the width of the prompt bar. It leaves room for the box
// border: a bar as wide as the content block would overflow it, and the screen
// would start shifting again as lines grow. It is capped well below the content
// block because a bar spanning a wide terminal is hard to read, while the list
// under it is not.
func (m startModel) promptWidth() int {
	return min(m.contentWidth()-2, maxPromptWidth)
}

// listIndent puts the suggestion rows under the left edge of the prompt bar,
// which is narrower than the block the list may use.
func (m startModel) listIndent() int {
	return max((m.contentWidth()-m.promptWidth())/2, 0)
}

// listWidth is how wide a suggestion row may be. The list is allowed to outgrow
// the prompt bar to the right: a pod name with a container is long, and
// truncating it while the terminal has columns to spare hides exactly what
// tells two rows apart.
func (m startModel) listWidth() int {
	return m.contentWidth() - m.listIndent()
}

// valueColumn is how wide the value column is: the widest value on screen, so
// states line up, bounded so a single very long name cannot push every state
// off the row.
func (m startModel) valueColumn(window []complete.Candidate) int {
	widest := 0
	for _, c := range window {
		widest = max(widest, lipgloss.Width(c.Value))
	}
	return min(widest, max(m.listWidth()-minDetailWidth, 10))
}

// resizeInputs keeps the text inputs matched to the prompt bar.
func (m *startModel) resizeInputs() {
	w := max(m.promptWidth()-6, 10)
	for _, in := range []*textinput.Model{&m.savedFilter, &m.host, &m.target, &m.query, &m.kubeconfig, &m.kubecontext} {
		in.Width = w
	}
}

func (m startModel) breadcrumb() string {
	if m.step == stepSaved {
		return ""
	}
	var parts []string
	if m.step > stepTransport {
		switch {
		case m.choice().collector.IsRemoteAPI():
			// The transport is nothing to a database read over HTTP; where it
			// is reached is the endpoint itself, once there is one.
			where := m.endpoint().Label()
			if where == "" {
				where = string(m.choice().collector)
			}
			parts = append(parts, where)
		case transports[m.transport] == source.TransportSSH:
			parts = append(parts, "ssh://"+strings.TrimSpace(m.host.Value()))
		default:
			parts = append(parts, "local")
		}
	}
	if m.step >= stepCollector {
		parts = append(parts, m.config().Command())
	}
	if len(parts) == 0 {
		return ""
	}
	// The command preview is the only place sudo and the kubeconfig are
	// visible, so it gets the full screen width rather than the prompt width.
	crumb := styleDim.Render(strings.Join(parts, "  ▸  "))
	return ansi.Truncate(crumb, m.contentWidth(), styleDim.Render("…"))
}

func (m startModel) chips() string {
	var (
		items []string
		sel   int
	)
	switch m.step {
	case stepSaved:
		return ""
	case stepTransport:
		for _, t := range transports {
			items = append(items, string(t))
		}
		sel = m.transport
	case stepCollector:
		for _, c := range m.options {
			items = append(items, c.label())
		}
		sel = m.collector
	default:
		return ""
	}
	out := make([]string, len(items))
	for i, it := range items {
		if i == sel {
			out[i] = styleChipActive.Render(it)
		} else {
			out[i] = styleChip.Render(it)
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Center, out...)
}

func (m startModel) help() string {
	if m.step == stepSaved {
		return strings.Join([]string{
			key("↑↓", "pick"),
			key("enter", "open"),
			key("tab", "new source"),
			key("esc", "quit"),
		}, styleHint.Render(" · "))
	}
	if m.step == stepQuery {
		return strings.Join([]string{
			key("enter", "open logs"),
			key("esc", m.escLabel()),
			key("ctrl+f", "follow "+onOff(m.follow)),
			key("ctrl+t", "tail "+tailLabel(m.tail)),
		}, styleHint.Render(" · "))
	}
	parts := []string{key("tab", "switch")}
	if len(m.filtered) > 0 {
		parts = []string{key("↑↓", "suggestions"), key("tab", "complete")}
	}
	if m.detail != detailNone {
		parts = []string{key("enter", "use it"), key("esc", "cancel")}
		if len(m.filtered) > 0 {
			parts = append([]string{key("↑↓", "suggestions")}, parts...)
		}
		return strings.Join(parts, styleHint.Render(" · "))
	}
	parts = append(parts, key("enter", "next"), key("esc", m.escLabel()))
	if m.step >= stepCollector && !m.choice().collector.IsRemoteAPI() {
		parts = append(parts, key("ctrl+s", "sudo "+onOff(m.elevate)))
	}
	if m.step == stepCollector && m.kubectlSelected() {
		parts = append(parts, key("ctrl+k", "kubeconfig"), key("ctrl+x", "context"))
	}
	if m.step == stepCollector && m.choice().collector.IsRemoteAPI() && m.choice().endpoint.Name == "" {
		parts = append(parts, key("ctrl+e", "endpoint"))
	}
	if _, ok := m.request(); ok {
		parts = append(parts, key("ctrl+r", "refresh"))
	}
	return strings.Join(parts, styleHint.Render(" · "))
}

// escLabel names what esc will do from here, so quitting is discoverable on
// the first step rather than only through ctrl+c.
func (m startModel) escLabel() string {
	if m.step == stepTransport && len(m.saved) == 0 {
		return "quit"
	}
	return "back"
}

func tailLabel(n int) string {
	if n == 0 {
		return "all"
	}
	return strconv.Itoa(n)
}

// completions renders the suggestion list, or the state that replaces it while
// there is nothing to suggest yet. An empty result means the caller should fall
// back to the static hints.
func (m startModel) completions(limit int) string {
	width := m.listWidth()
	indent := strings.Repeat(" ", m.listIndent())
	block := lipgloss.NewStyle().Width(m.contentWidth()).Align(lipgloss.Left)
	switch {
	case m.step == stepQuery:
		return ""
	case m.loading:
		return block.Render(indent + styleHint.Render("  looking for sources…"))
	case m.candErr != nil:
		msg, _, _ := strings.Cut(m.candErr.Error(), "\n")
		return block.Render(indent + styleErr.Render("  "+msg))
	case limit <= 0:
		return ""
	case len(m.filtered) == 0:
		// Saying nothing was found beats falling through to unrelated hints:
		// a kubeconfig with no contexts is a real answer, not a missing one.
		if msg := m.emptyLabel(); msg != "" {
			return block.Render(indent + styleHint.Render("  "+msg))
		}
		return ""
	}

	// Reserve a row for the overflow count when the list does not fit.
	shown := len(m.filtered)
	if shown > limit {
		shown = max(limit-1, 1)
	}
	// Scroll the window so the highlighted suggestion is always rendered.
	start := 0
	if m.sel >= shown {
		start = m.sel - shown + 1
	}
	window := m.filtered[start : start+shown]

	valueWidth := m.valueColumn(window)

	rows := make([]string, 0, shown+1)
	for i, c := range window {
		// Truncate rather than overflow the column, so every state starts at
		// the same offset no matter how long a name is.
		label := c.Value
		if lipgloss.Width(label) > valueWidth {
			label = ansi.Truncate(label, valueWidth, "…")
		}
		pad := strings.Repeat(" ", max(valueWidth-lipgloss.Width(label), 0))

		marker := "  "
		selected := start+i == m.sel
		if selected {
			marker = styleSelected.Render("▎ ")
		}
		row := marker + highlightMatch(label, c.Matched, selected) + pad
		if c.State != "" || c.Detail != "" {
			row += "  " + renderDetail(c)
		}
		// Truncate rather than let the block wrap: a long image name would
		// otherwise push the rest of the list down.
		rows = append(rows, block.Render(indent+ansi.Truncate(row, width, styleDim.Render("…"))))
	}
	if rest := len(m.filtered) - start - shown; rest > 0 {
		rows = append(rows, block.Render(indent+styleHint.Render("  … "+strconv.Itoa(rest)+" more")))
	}
	return strings.Join(rows, "\n")
}

// highlightMatch colors the runes the query matched, so a fuzzy hit shows why
// it is in the list. Offsets past the end are ignored, which is what happens
// when a long value was truncated to fit the column.
func highlightMatch(value string, matched []int, selected bool) string {
	plain := lipgloss.NewStyle()
	if selected {
		plain = styleSelected
	}
	if len(matched) == 0 {
		return plain.Render(value)
	}

	hit := make(map[int]bool, len(matched))
	for _, i := range matched {
		hit[i] = true
	}

	// Emitted in runs rather than per rune: a styled rune costs an escape pair,
	// and a pod name would otherwise carry sixty of them.
	var (
		b     strings.Builder
		runes = []rune(value)
	)
	for i := 0; i < len(runes); {
		j := i
		for j < len(runes) && hit[j] == hit[i] {
			j++
		}
		run := string(runes[i:j])
		if hit[i] {
			b.WriteString(styleMatch.Render(run))
		} else {
			b.WriteString(plain.Render(run))
		}
		i = j
	}
	return b.String()
}

// renderDetail joins a candidate's state and detail, coloring the state by
// what it means.
func renderDetail(c complete.Candidate) string {
	switch {
	case c.State == "":
		return styleDim.Render(c.Detail)
	case c.Detail == "":
		return renderState(c.State)
	default:
		return renderState(c.State) + styleDim.Render(" · "+c.Detail)
	}
}

// emptyLabel explains an empty suggestion list, or is blank when the step has
// nothing to look up and the static hints should show instead.
func (m startModel) emptyLabel() string {
	req, ok := m.request()
	if !ok {
		return ""
	}
	if len(m.candidates) > 0 {
		// A query that filtered everything away is usually a field typed at a
		// venture, so name the ones that exist.
		if terms, _ := complete.ParseQuery(m.input().Value()); len(terms) > 0 {
			if fields := complete.Fields(m.choice().collector); len(fields) > 0 {
				return "no match — filters: " + strings.Join(fields, ": ") + ":"
			}
		}
		return "no match"
	}
	switch req.Field {
	case complete.FieldHost:
		return "no hosts in ssh config — type one"
	case complete.FieldKubeConfig:
		return "no kubeconfig found — type a path"
	case complete.FieldKubeContext:
		// kubectl reports nothing either way, so name both possibilities.
		return "no contexts — is the kubeconfig path right?"
	}
	switch m.choice().collector {
	case source.CollectorKubectl:
		return "no pods"
	case source.CollectorDocker:
		return "no containers"
	case source.CollectorJournal:
		return "no units"
	default:
		return ""
	}
}

// hints shows step-appropriate examples, in the spirit of a search landing page.
func (m startModel) hints() string {
	if m.detail != detailNone {
		// The examples below are about targets, not about the detail being
		// edited, so they would only mislead here.
		return ""
	}

	var lines []string
	switch m.step {
	case stepSaved:
		lines = []string{"no saved sources yet — press tab to configure one by hand"}
	case stepTransport:
		lines = []string{
			"local    read from this machine",
			"ssh      proxy every command through a node",
		}
	case stepCollector:
		switch m.choice().collector {
		case source.CollectorJournal:
			lines = []string{
				"kubelet",
				"user/syncthing   a unit of the user manager",
				"(empty)          everything in the journal",
			}
		case source.CollectorKubectl:
			lines = []string{
				"oteldb/oteldb-0",
				"oteldb/deploy/api          a workload, which outlives its pods",
				"oteldb/app=oteldb",
				"oteldb/oteldb-0:clickhouse",
				"ns:oteldb api              narrow by field, as on GitHub",
			}
		case source.CollectorDocker:
			lines = []string{"clickhouse", "3f1a0c2b9d44"}
		case source.CollectorCommand:
			lines = []string{"tail -F /var/log/app.log", "dmesg -w"}
		case source.CollectorVictoriaLogs:
			lines = []string{
				"level:error",
				"kubernetes.namespace:oteldb        a field of the log itself",
				`_msg:"connection refused"`,
				"level:error | drop _stream_id      LogsQL, sent as written",
			}
		case source.CollectorLoki:
			lines = []string{
				`{app="api"}                        a stream selector is required`,
				`{namespace="oteldb"} |= "error"`,
				`{app="api"} | json | duration > 1s LogQL, sent as written`,
			}
		}
	case stepQuery:
		lines = []string{"error", "trace_id=abc", "level=(warn|error)", "(empty)  no filter"}
	}
	// Pad every line to the same width so the surrounding centering moves the
	// block as a whole instead of centering each line on its own.
	block := lipgloss.NewStyle().Width(m.promptWidth()).Align(lipgloss.Left)
	for i, l := range lines {
		lines[i] = block.Render(styleHint.Render("  " + l))
	}
	return strings.Join(lines, "\n")
}

func onOff(v bool) string {
	if v {
		return styleOK.Render("on")
	}
	return styleDim.Render("off")
}

// tailSteps are the values cycled by ctrl+t.
var tailSteps = []int{100, 1000, 10000, 0}

func nextTail(cur int) int {
	for i, v := range tailSteps {
		if v == cur {
			return tailSteps[(i+1)%len(tailSteps)]
		}
	}
	return tailSteps[0]
}
