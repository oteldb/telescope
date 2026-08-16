package source

import (
	"context"
	"time"

	"github.com/go-faster/errors"
)

// allLines is the tail meaning every line there is. Only kubectl needs it spelt
// out: "kubectl logs -l" quietly defaults its tail to ten, and ten lines off the
// wrong end of the window is not a page.
const allLines = -1

// pageConfig is this stream asked again over the window that ends where the
// reader's oldest line begins.
//
// It is the config the stream was opened with and not a second argv assembled
// by hand, so a page is read through the transport, the sudo and the kubeconfig
// the stream was read through: a place reached over ssh pages over ssh.
func (c Config) pageConfig(before time.Time, limit int) Config {
	c.Follow = false
	// The spec resolved to the window the stream opened with, and this is a
	// different one; what it says would be a lie on any screen that showed it.
	c.Range.Spec = ""
	// Every line of a page has to be dated: it is what tells the lines below
	// the reader's oldest from the ones they are already holding, and what a
	// merge orders the whole page by.
	c.Stamp = true
	c.Tail = limit

	switch c.Collector {
	case CollectorJournal:
		// journalctl reads --until to the second, inclusively, and in the local
		// time of the host: the bound is rounded up rather than down, and moved
		// into the zone the stamp will be read in. Rounding up costs a duplicate
		// second at the seam, which the list folds into the run above it;
		// rounding down would cost a hole, which the reader cannot find later.
		// What comes back past `before` is dropped as it is read.
		c.Range.Until = ceilSecond(before).In(time.Local)
	case CollectorKubectl:
		// There is no end bound to set — see [Config.kubePage].
		c.Range.Until = time.Time{}
		c.Tail = allLines
	default:
		c.Range.Until = pageEnd(before)
	}
	return c
}

func ceilSecond(t time.Time) time.Time {
	if s := t.Truncate(time.Second); s.Before(t) {
		return s.Add(time.Second)
	}
	return t
}

// readPage runs one collector over one window and keeps the newest limit lines
// older than before, oldest first.
//
// The collector is run the way the stream runs it, so what it writes on stderr
// and how it exits mean here what they mean there: a place that does not have
// what it was pointed at contributes nothing, and everything else it complains
// about is the reader's to see. Its stderr is not part of the page either way —
// a complaint belongs beside the reading that provoked it, not prepended to the
// timeline an hour before anything went wrong.
func readPage(ctx context.Context, cfg Config, before time.Time, limit int, opt options) ([]Line, error) {
	s, err := Start(ctx, cfg)
	if err != nil {
		return nil, err
	}

	var (
		kept []Line
		said []Line
		past int
	)
	for l := range s.Lines() {
		if l.Stderr || l.Kind.IsNote() {
			said = append(said, l)
			continue
		}
		at := l.At
		if at.IsZero() && opt.timeFunc != nil {
			at = opt.timeFunc(l)
		}
		if !at.IsZero() {
			l.At = at
			if !at.Before(before) {
				past++
				if past > pageOverrun {
					break
				}
				continue
			}
		}
		if kept = append(kept, l); len(kept) > limit {
			// Dropped from the far end, so what is kept still runs up to the
			// line the reader is holding and the next page asks for the rest.
			kept = kept[1:]
		}
	}
	s.Close()
	for range s.Lines() {
	}
	err = <-s.Done()

	if cerr := ctx.Err(); cerr != nil {
		// The reading stopped short of the boundary, so what it gathered does
		// not join what the reader holds. Half a page with a hole in the middle
		// of it is worse than none, and this is what there is to say instead.
		return nil, errors.Wrap(cerr, "reading older")
	}
	if err != nil {
		if absent(said) {
			return nil, errAbsent
		}
		return nil, err
	}
	return kept, nil
}

// errAbsent is a collector saying the place has nothing to give rather than
// that it could not be read. It never leaves this file: to a page it is an empty
// answer, and the only reason it is an error on the way there is that widening
// the window would put the same question to the same missing place again.
var errAbsent = errors.New("nothing here to read")

// commandPage is a page read by running the collector again.
func (c Config) commandPage(ctx context.Context, before time.Time, limit int, opt options) ([]Line, error) {
	if c.Collector == CollectorKubectl {
		return c.kubePage(ctx, before, limit, opt)
	}
	lines, err := readPage(ctx, c.pageConfig(before, limit), before, limit, opt)
	if errors.Is(err, errAbsent) {
		return nil, nil
	}
	return lines, err
}

// pageOverrun is how far past the boundary a page keeps reading before it stops.
//
// A tool whose end bound is coarser than the boundary writes a little of what is
// already on the screen, and "kubectl logs" over a selector or a workload merges
// several pods without ordering what it merges — so the first line newer than
// the reader's oldest is not the end of what is older than it. Everything past
// the boundary is dropped either way; this is only how long the dropping goes on
// before the collector is told there is nothing more worth sending.
const pageOverrun = 256

// The windows a kubectl page walks back through.
const (
	// kubePageStep is the first window asked over, and the whole of what a busy
	// pod costs: every line written inside it is read to keep a page's worth off
	// its far end, so the minutes here are the bound on reading a pod that never
	// stops writing.
	kubePageStep = 5 * time.Minute
	// kubePageGrow is how fast the window widens when it came back empty. Each
	// attempt re-reads the one before it, so growing faster than doubling is
	// what keeps a quiet pod from costing a round trip per five minutes of
	// silence.
	kubePageGrow = 4
	// kubePageReach is how wide the window gets before the last attempt drops
	// its start bound and reads the log from the beginning.
	kubePageReach = 12 * time.Hour
)

// kubePage reads back through a window "kubectl logs" cannot be asked for.
//
// kubectl has --since, --since-time and --tail, and no --until: the only window
// it takes starts at an instant and runs to now. A page is the other way round,
// so it is asked for from earlier and read only as far as the line the reader is
// already holding, then stopped. That is what bounds the cost — asked with
// --since-time alone, a long-running pod would stream its whole log past us to
// keep a thousand lines off the far end of it — and it is why kubectl pages at
// all rather than saying it cannot: the window it will not name is one the
// reading can close by hand.
//
// A window with nothing in it is widened rather than answered, the way Loki's
// is: a pod quiet for an hour is quiet, not finished. The widest one has no
// start bound at all, so the emptiness that ends the walk is the emptiness of
// the log — "at the start" is the one thing a page must never say on a log's
// behalf while there is more below.
func (c Config) kubePage(ctx context.Context, before time.Time, limit int, opt options) ([]Line, error) {
	for span, back := kubePageStep, time.Duration(0); ; span *= kubePageGrow {
		cfg := c.pageConfig(before, limit)
		start := before.Add(-back - span)
		last := back+span >= kubePageReach
		switch t := c.Range.Since; {
		case !t.IsZero() && !start.After(t):
			// The place named where its window begins, so that is as far back as
			// there is to read and no attempt after this one would be inside it.
			cfg.Range.Since, last = t, true
		case last:
			cfg.Range.Since = time.Time{}
		default:
			cfg.Range.Since = start
		}

		lines, err := readPage(ctx, cfg, before, limit, opt)
		switch {
		case errors.Is(err, errAbsent):
			return nil, nil
		case err != nil:
			return nil, err
		case len(lines) > 0, last:
			return lines, nil
		}
		back += span
	}
}
