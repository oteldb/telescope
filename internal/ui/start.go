package ui

import (
	"context"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/source"
)

// logo is the banner shown on the start screen.
const logo = "" +
	"╺┳╸┏━╸╻  ┏━╸┏━┓┏━╸┏━┓┏━┓┏━╸\n" +
	" ┃ ┣╸ ┃  ┣╸ ┗━┓┃  ┃ ┃┣━┛┣╸ \n" +
	" ╹ ┗━╸┗━╸┗━╸┗━┛┗━╸┗━┛╹  ┗━╸"

// The prompt bar, and the suggestion list under it, grow with the terminal
// between these bounds.
const (
	minPromptWidth = 64
	maxPromptWidth = 100
)

type startStep int

const (
	stepTransport startStep = iota
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
)

// maxCompletions is how many suggestions are listed under the prompt, and
// detailColumn caps how far the detail column is pushed right.
const (
	// maxContentWidth keeps the centered screen readable on a wide terminal.
	maxContentWidth = 120
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

	host       textinput.Model
	target     textinput.Model
	query      textinput.Model
	kubeconfig textinput.Model

	// editKube swaps the prompt bar over to the kubeconfig path while the
	// collector step is open, so the pod listing can be re-run against it
	// before a target is typed.
	editKube bool
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
		host:       mk("user@host"),
		target:     mk(""),
		query:      mk("grep term or regexp, empty for everything"),
		kubeconfig: mk("path to kubeconfig, e.g. /etc/rancher/k3s/k3s.yaml"),
		tail:       1000,
		follow:     true,
		sel:        -1,
		cache:      map[string]cacheEntry{},
		inflight:   map[string]bool{},
	}
	m.syncPlaceholder()
	m.focus()
	return m
}

// request describes what the current step can complete.
func (m startModel) request() (complete.Request, bool) {
	switch m.step {
	case stepTransport:
		if transports[m.transport] != source.TransportSSH {
			return complete.Request{}, false
		}
		return complete.Request{Field: complete.FieldHost}, true
	case stepCollector:
		req := complete.Request{
			Field:      complete.FieldTarget,
			Transport:  transports[m.transport],
			Host:       strings.TrimSpace(m.host.Value()),
			Collector:  collectors[m.collector],
			Elevate:    m.elevate,
			KubeConfig: strings.TrimSpace(m.kubeconfig.Value()),
		}
		if m.editKube {
			req.Field, req.Collector = complete.FieldKubeConfig, source.CollectorKubectl
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

	req, ok := m.request()
	if !ok {
		m.candKey, m.loading = "", false
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
				Field:      complete.FieldTarget,
				Transport:  transports[m.transport],
				Host:       strings.TrimSpace(m.host.Value()),
				Collector:  c,
				Elevate:    m.elevate,
				KubeConfig: strings.TrimSpace(m.kubeconfig.Value()),
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

// refilter narrows the suggestions to what has been typed.
func (m *startModel) refilter() {
	m.filtered = complete.Rank(m.candidates, m.input().Value())
	if m.sel >= len(m.filtered) {
		m.sel = len(m.filtered) - 1
	}
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
	case m.step == stepTransport:
		return &m.host
	case m.step == stepCollector && m.editKube:
		return &m.kubeconfig
	case m.step == stepCollector:
		return &m.target
	default:
		return &m.query
	}
}

// kubectlSelected reports whether the kubeconfig applies to the current choice.
func (m startModel) kubectlSelected() bool {
	return collectors[m.collector] == source.CollectorKubectl
}

// active reports whether the current step accepts text.
func (m startModel) active() bool {
	return m.step != stepTransport || transports[m.transport] == source.TransportSSH
}

func (m *startModel) focus() {
	m.host.Blur()
	m.target.Blur()
	m.query.Blur()
	m.kubeconfig.Blur()
	if m.active() {
		m.input().Focus()
	}
}

func (m *startModel) syncPlaceholder() {
	switch collectors[m.collector] {
	case source.CollectorJournal:
		m.target.Placeholder = "[user/]unit, e.g. kubelet — empty for the whole journal"
	case source.CollectorKubectl:
		m.target.Placeholder = "[namespace/]pod[:container] or [ns/]app=name"
	case source.CollectorDocker:
		m.target.Placeholder = "container name or id"
	case source.CollectorCommand:
		m.target.Placeholder = "any command writing logs to stdout"
	}
}

func (m startModel) config() source.Config {
	cfg := source.Config{
		Transport: transports[m.transport],
		Host:      strings.TrimSpace(m.host.Value()),
		Collector: collectors[m.collector],
		Tail:      m.tail,
		Follow:    m.follow,
	}
	cfg.Elevate = m.elevate
	cfg.KubeConfig = strings.TrimSpace(m.kubeconfig.Value())

	target := strings.TrimSpace(m.target.Value())
	switch cfg.Collector {
	case source.CollectorJournal:
		cfg.Unit, cfg.UserUnit = source.ParseJournalTarget(target)
	case source.CollectorKubectl:
		cfg.Namespace, cfg.Target, cfg.Container = source.ParseKubeTarget(target)
	case source.CollectorDocker:
		cfg.Container = target
	case source.CollectorCommand:
		cfg.Args = target
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
		case "tab":
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
				m.editKube = !m.editKube
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
			if m.sel >= 0 {
				m.accept()
				return m, nil
			}
			if m.editKube {
				m.editKube = false
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
			case m.editKube:
				m.editKube = false
				m.focus()
				return m, m.fetch()
			case m.step > stepTransport:
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
		return len(collectors)
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
		m.editKube = false
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

func (m startModel) View() string {
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

	if m.editKube {
		b.WriteString(styleChipActive.Render("kubeconfig"))
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

	// The suggestion list takes whatever height is left, so a tall terminal
	// shows more of it instead of a fixed handful.
	head := b.String()
	body := m.completions(max(m.h-lipgloss.Height(head)-1, 3))
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
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, content)
}

// contentWidth is the stable width every line of the start screen is centered
// within.
func (m startModel) contentWidth() int {
	return min(max(m.w-2*screenPad, minPromptWidth), maxContentWidth)
}

// promptWidth is the width shared by the prompt bar and the suggestion list,
// so the two stay aligned as the terminal grows. It leaves room for the box
// border: a bar as wide as the content block would overflow it, and the screen
// would start shifting again as lines grow.
func (m startModel) promptWidth() int {
	return min(m.contentWidth()-2, maxPromptWidth)
}

// detailColumn is where suggestion details line up. Half the row keeps long
// values from pushing details off the end while still leaving room to read
// them.
func (m startModel) detailColumn() int {
	return m.promptWidth() / 2
}

// resizeInputs keeps the text inputs matched to the prompt bar.
func (m *startModel) resizeInputs() {
	w := max(m.promptWidth()-6, 10)
	for _, in := range []*textinput.Model{&m.host, &m.target, &m.query, &m.kubeconfig} {
		in.Width = w
	}
}

func (m startModel) breadcrumb() string {
	var parts []string
	if m.step > stepTransport {
		if transports[m.transport] == source.TransportSSH {
			parts = append(parts, "ssh://"+strings.TrimSpace(m.host.Value()))
		} else {
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
	case stepTransport:
		for _, t := range transports {
			items = append(items, string(t))
		}
		sel = m.transport
	case stepCollector:
		for _, c := range collectors {
			items = append(items, string(c))
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
	if m.editKube {
		parts = []string{key("enter", "use it"), key("esc", "cancel")}
		if len(m.filtered) > 0 {
			parts = append([]string{key("↑↓", "suggestions")}, parts...)
		}
		return strings.Join(parts, styleHint.Render(" · "))
	}
	parts = append(parts, key("enter", "next"), key("esc", m.escLabel()))
	if m.step >= stepCollector {
		parts = append(parts, key("ctrl+s", "sudo "+onOff(m.elevate)))
	}
	if m.step == stepCollector && m.kubectlSelected() {
		parts = append(parts, key("ctrl+k", "kubeconfig"))
	}
	if _, ok := m.request(); ok {
		parts = append(parts, key("ctrl+r", "refresh"))
	}
	return strings.Join(parts, styleHint.Render(" · "))
}

// escLabel names what esc will do from here, so quitting is discoverable on
// the first step rather than only through ctrl+c.
func (m startModel) escLabel() string {
	if m.step == stepTransport {
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
	width := m.promptWidth()
	block := lipgloss.NewStyle().Width(width).Align(lipgloss.Left)
	switch {
	case m.step == stepQuery:
		return ""
	case m.loading:
		return block.Render(styleHint.Render("  looking for sources…"))
	case m.candErr != nil:
		msg, _, _ := strings.Cut(m.candErr.Error(), "\n")
		return block.Render(styleErr.Render("  " + msg))
	case len(m.filtered) == 0 || limit <= 0:
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

	// Align the detail column against the widest value actually shown.
	valueWidth := 0
	for _, c := range window {
		valueWidth = max(valueWidth, lipgloss.Width(c.Value))
	}
	valueWidth = min(valueWidth, m.detailColumn())

	rows := make([]string, 0, shown+1)
	for i, c := range window {
		marker, value := "  ", c.Value
		if start+i == m.sel {
			marker, value = styleSelected.Render("▎ "), styleSelected.Render(c.Value)
		}
		row := marker + value
		if c.Detail != "" {
			pad := max(valueWidth-lipgloss.Width(c.Value), 0)
			row += strings.Repeat(" ", pad) + styleDim.Render("  "+c.Detail)
		}
		// Truncate rather than let the block wrap: a long image name would
		// otherwise push the rest of the list down.
		rows = append(rows, block.Render(ansi.Truncate(row, width, styleDim.Render("…"))))
	}
	if rest := len(m.filtered) - start - shown; rest > 0 {
		rows = append(rows, block.Render(styleHint.Render("  … "+strconv.Itoa(rest)+" more")))
	}
	return strings.Join(rows, "\n")
}

// hints shows step-appropriate examples, in the spirit of a search landing page.
func (m startModel) hints() string {
	var lines []string
	switch m.step {
	case stepTransport:
		lines = []string{
			"local    read from this machine",
			"ssh      proxy every command through a node",
		}
	case stepCollector:
		switch collectors[m.collector] {
		case source.CollectorJournal:
			lines = []string{
				"kubelet",
				"user/syncthing   a unit of the user manager",
				"(empty)          everything in the journal",
			}
		case source.CollectorKubectl:
			lines = []string{"oteldb/oteldb-0", "oteldb/app=oteldb", "oteldb/oteldb-0:clickhouse"}
		case source.CollectorDocker:
			lines = []string{"clickhouse", "3f1a0c2b9d44"}
		case source.CollectorCommand:
			lines = []string{"tail -F /var/log/app.log", "dmesg -w"}
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

// tailSteps are the values cycled by ctrl+n.
var tailSteps = []int{100, 1000, 10000, 0}

func nextTail(cur int) int {
	for i, v := range tailSteps {
		if v == cur {
			return tailSteps[(i+1)%len(tailSteps)]
		}
	}
	return tailSteps[0]
}
