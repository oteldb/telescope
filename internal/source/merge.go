package source

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
)

// mergeLag bounds how long the merge waits on a source that has gone quiet.
//
// A k-way merge is only in order while every source has a line to compare. One
// that says nothing would hold the whole view back, so past this it is dropped
// from the comparison until it speaks again — at the price of its next line
// landing where it arrives rather than where its timestamp belongs.
const mergeLag = 250 * time.Millisecond

// Children returns the merged configs as they are actually opened: the window,
// tail and follow of the merge, and the out-of-band timestamps ordering them
// needs.
func (c Config) Children() []Config {
	out := make([]Config, 0, len(c.Merge))
	for _, sub := range c.Merge {
		sub.Range, sub.Tail, sub.Follow = c.Range, c.Tail, c.Follow
		// The filter is the group's: it is one view, read through one query,
		// and each place answers as much of it as it can.
		sub.Filter = c.Filter
		sub.Stamp = true
		out = append(out, sub)
	}
	return out
}

// Label is the short name a stream is known by, which is what a merge tags its
// lines with.
func (c Config) Label() string {
	if n := strings.TrimSpace(c.Name); n != "" {
		return n
	}
	var what string
	switch c.Collector {
	case CollectorMerge:
		return "merge"
	case CollectorJournal:
		what = strings.TrimSpace(c.Unit)
		if what == "" {
			what = "journal"
		}
	case CollectorKubectl:
		what = strings.TrimSpace(c.Target)
		if ns := strings.TrimSpace(c.Namespace); ns != "" {
			what = ns + "/" + what
		}
	case CollectorDocker:
		what = strings.TrimSpace(c.Container)
	case CollectorCommand:
		what, _, _ = strings.Cut(strings.TrimSpace(c.Args), " ")
	default:
		// A query is prose; where it was asked is what identifies the stream.
		return c.Endpoint.Label()
	}
	if h := strings.TrimSpace(c.Host); h != "" && c.Transport == TransportSSH {
		// The same container name on two hosts is two different streams.
		return h + ":" + what
	}
	return what
}

// mergeLabels names each merged stream, keeping the names distinct: a tag that
// two sources share tells the reader nothing.
func mergeLabels(children []Config) []string {
	seen := make(map[string]int, len(children))
	out := make([]string, len(children))
	for i, c := range children {
		label := c.Label()
		seen[label]++
		if n := seen[label]; n > 1 {
			label += "#" + strconv.Itoa(n)
		}
		out[i] = label
	}
	return out
}

// Labels names each merged source, in the order they were given, which is what
// this stream's lines are tagged with.
func (c Config) Labels() []string { return mergeLabels(c.Children()) }

// mergeItem carries one line, or the end of one source, to the merge loop.
type mergeItem struct {
	idx  int
	line Line
	err  error
	end  bool
}

// startMerge opens every merged source and interleaves them by time.
//
// Each source is ordered by itself, so ordering the whole is a k-way merge over
// their heads: the oldest pending line is the next one out.
func startMerge(ctx context.Context, cfg Config, opt options) (*Stream, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &Stream{
		cfg:    cfg,
		lines:  make(chan Line, 4096),
		done:   make(chan error, 1),
		cancel: cancel,
	}

	children := cfg.Children()
	labels := cfg.Labels()

	items := make(chan mergeItem)

	var (
		open   = make([]bool, len(children))
		acks   = make([]chan struct{}, len(children))
		failed []error
		notes  []Line
	)
	for i, child := range children {
		acks[i] = make(chan struct{}, 1)
		sub, err := Start(ctx, child)
		if err != nil {
			// One unreachable source is not the end of the other three: it is
			// reported where its lines would have been.
			failed = append(failed, errors.Wrap(err, labels[i]))
			notes = append(notes, Line{
				Data:   []byte("telescope: " + labels[i] + ": " + err.Error()),
				Stderr: true,
				Note:   true,
				Source: labels[i],
			})
			continue
		}
		open[i] = true
		go forward(ctx, i, sub, labels[i], items, acks[i])
	}
	if !slices.Contains(open, true) {
		cancel()
		return nil, errors.Join(failed...)
	}

	go s.merge(ctx, items, acks, open, notes, failed, opt)
	return s, nil
}

// openGrace bounds how long a source's opening stderr is held back. A collector
// that cannot read where it was pointed says so and exits at once, so what it
// wrote in that moment is a complaint and not output; past this it is still
// running, and what it writes is the log.
const openGrace = 2 * time.Second

// maxHeld bounds what is held over [openGrace]. A source this talkative on
// stderr is a source writing its log there.
const maxHeld = 64

// forward carries one source's lines to the merge, one at a time: the merge
// compares the head of every source, so a source may only ever have one line
// pending. It waits for the merge to take that line before reading the next,
// which is what keeps a source that is far ahead from buffering into the merge.
//
// What a source says while it is opening is held until it is known whether it
// opened: a place a group names and this one does not have writes its refusal
// on stderr and exits, and that is not a line in the timeline.
func forward(ctx context.Context, idx int, sub *Stream, label string, items chan<- mergeItem, ack <-chan struct{}) {
	send := func(l Line) bool {
		l.Source = label
		select {
		case items <- mergeItem{idx: idx, line: l}:
		case <-ctx.Done():
			return false
		}
		select {
		case <-ack:
			return true
		case <-ctx.Done():
			return false
		}
	}

	var (
		held    []Line
		holding = true
	)
	release := func() bool {
		holding = false
		for _, l := range held {
			if !send(l) {
				return false
			}
		}
		held = nil
		return true
	}

	grace := time.NewTimer(openGrace)
	defer grace.Stop()

	lines := sub.Lines()
	for lines != nil {
		select {
		case l, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			switch {
			case !holding:
			case l.Stderr && len(held) < maxHeld:
				held = append(held, l)
				continue
			default:
				// A source that is reading wrote what it wrote on the way there.
				if !release() {
					return
				}
			}
			if !send(l) {
				return
			}
		case <-grace.C:
			if !release() {
				return
			}
		case <-ctx.Done():
			return
		}
	}

	err := <-sub.Done()
	if holding && absent(held) {
		// The place does not have what the group named. It contributes nothing,
		// which is the whole of what there is to say about it.
		held, err = nil, nil
	}
	for _, l := range held {
		if !send(l) {
			return
		}
	}
	if err != nil {
		// Said where its lines would have been, since the exit error is only
		// reported once every source has ended and a source can outlive the
		// reader's interest in why another one stopped.
		if !send(Line{
			Data:   []byte("telescope: " + label + ": " + err.Error()),
			Stderr: true,
			Note:   true,
		}) {
			return
		}
	}
	select {
	case items <- mergeItem{idx: idx, err: err, end: true}:
	case <-ctx.Done():
	}
}

func (s *Stream) merge(
	ctx context.Context,
	items <-chan mergeItem,
	acks []chan struct{},
	open []bool,
	notes []Line,
	failed []error,
	opt options,
) {
	defer close(s.lines)
	defer func() {
		s.done <- errors.Join(failed...)
		close(s.done)
	}()

	send := func(l Line) bool {
		select {
		case s.lines <- l:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for _, l := range notes {
		if !send(l) {
			return
		}
	}

	n := len(open)
	var (
		heads = make([]*Line, n)
		at    = make([]time.Time, n)
		// last is the newest time each source reported, carried onto the lines
		// that follow it: a stacktrace continues the line it belongs to, and a
		// source ordered by itself never goes back in time.
		last = make([]time.Time, n)
		// lagging marks a source silent past [mergeLag], which the merge stops
		// waiting for until it speaks again.
		lagging = make([]bool, n)
		// A source that never opened has been reported already, and nothing
		// more is coming from it.
		ended = make([]bool, n)
	)
	for i, ok := range open {
		ended[i] = !ok
	}

	when := func(i int, l Line) time.Time {
		t := l.At
		if t.IsZero() && opt.timeFunc != nil {
			t = opt.timeFunc(l)
		}
		if !t.IsZero() {
			last[i] = t
			return t
		}
		if !last[i].IsZero() {
			return last[i]
		}
		// Nothing dates this line, so it happened when it arrived.
		return time.Now()
	}
	oldest := func() int {
		best := -1
		for i, h := range heads {
			if h != nil && (best < 0 || at[i].Before(at[best])) {
				best = i
			}
		}
		return best
	}
	waiting := func() bool {
		for i := range n {
			if !ended[i] && !lagging[i] && heads[i] == nil {
				return true
			}
		}
		return false
	}

	timer := time.NewTimer(mergeLag)
	defer timer.Stop()
	timer.Stop()

	for {
		// The next line out is the oldest pending one, once nothing that might
		// still have an older one is left to hear from.
		next := -1
		if !waiting() {
			next = oldest()
			// A source that went quiet has not said it is finished, so it is
			// still read; only when every source has ended is there nothing
			// left to wait for.
			if next < 0 && !slices.Contains(ended, false) {
				return
			}
		}
		if next >= 0 {
			l := *heads[next]
			l.At = at[next]
			heads[next] = nil
			// Buffered, so the source is free to read its next line whether or
			// not it is waiting for this yet.
			acks[next] <- struct{}{}
			if !send(l) {
				return
			}
			continue
		}

		var lag <-chan time.Time
		if oldest() >= 0 {
			// Only worth timing when there is a line to release.
			timer.Reset(mergeLag)
			lag = timer.C
		}
		select {
		case it := <-items:
			timer.Stop()
			switch {
			case it.end:
				ended[it.idx] = true
				if it.err != nil {
					failed = append(failed, it.err)
				}
			default:
				lagging[it.idx] = false
				at[it.idx] = when(it.idx, it.line)
				heads[it.idx] = &it.line
			}
		case <-lag:
			for i := range n {
				if !ended[i] && heads[i] == nil {
					lagging[i] = true
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
