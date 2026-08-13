package source

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/go-faster/errors"
)

// pageTimeout bounds one page. It is a scroll and not the stream: a database
// that has not answered by now has cost more than reading further back is
// worth, and the lines already held are still there.
const pageTimeout = 30 * time.Second

// backfillLimit is how many lines the first query over a database asks for when
// the place bounded it by nothing.
//
// A tail of zero is every line there is, which is what a command means by it and
// what no database can be asked. It used to mean no backfill at all, which is
// the opposite of what it says; now that reading further back is what a page
// does, an unbounded tail starts at a page and walks back from there.
const backfillLimit = 1000

// backfill is how many lines to open with.
func (c Config) backfill() int {
	if c.Tail > 0 {
		return c.Tail
	}
	return backfillLimit
}

// CanPage reports whether the source can be asked for what came before a line.
//
// Only a log database can. A collector is a process writing to a pipe: what it
// has already written is gone, and what it will write next is the only thing
// left to read. That is why the tail of a command is a number chosen up front
// and the tail of a database need not be.
//
// A merge pages only where every child does. One that cannot would contribute
// nothing to the page, and a stream missing from a stretch of the timeline
// reads as a stream that was quiet then — which is the one thing a merge must
// never say on a source's behalf.
func (c Config) CanPage() bool {
	if c.Collector == CollectorMerge {
		return len(c.Merge) > 0 && !slices.ContainsFunc(c.Children(),
			func(sub Config) bool { return !sub.CanPage() })
	}
	return c.Collector.IsRemoteAPI()
}

// Page reads at most limit entries older than before, oldest first, so a view
// that has scrolled to its first line can ask what came before it.
//
// It is the same query the stream opened with, against a different end: the
// filter is still pushed down where it compiles, and what does not compile is
// still the view's to apply. A source that cannot page answers with nothing,
// which is not a failure — see [Config.CanPage].
//
// The window is bounded below by [Range.Since] where the place has one. An
// empty page inside that window means the window is empty, not that the
// database is: what the bound is when nothing was named is each collector's
// own, since it is their rule and not ours.
func (c Config) Page(ctx context.Context, before time.Time, limit int) ([]Line, error) {
	if limit <= 0 || before.IsZero() || !c.CanPage() {
		return nil, nil
	}
	if t := c.Range.Since; !t.IsZero() && !before.After(t) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()

	if c.Collector == CollectorMerge {
		return c.mergePage(ctx, before, limit)
	}
	client := httpClient(c.Endpoint)
	switch c.Collector {
	case CollectorLoki:
		return c.lokiPage(ctx, client, before, limit)
	case CollectorVictoriaLogs:
		return c.vlogsPage(ctx, client, before, limit)
	default:
		return nil, nil
	}
}

// mergePage asks every child for the page before the same instant and
// interleaves the answers.
//
// The stream does a k-way merge over the heads of its sources because it reads
// them forwards and one line at a time. A page is the whole window at once, so
// it is a sort — and then a truncation to the newest limit lines of the lot,
// which is what makes the page contiguous: whatever is dropped is older than
// everything kept, so the next page asks for it and nothing falls between them.
//
// A child that fails costs its own lines and not the page, the way an
// unreachable source costs its own lines and not the merge.
func (c Config) mergePage(ctx context.Context, before time.Time, limit int) ([]Line, error) {
	var (
		children = c.Children()
		labels   = c.Labels()
		pages    = make([][]Line, len(children))
		errs     = make([]error, len(children))
		wg       sync.WaitGroup
	)
	for i, child := range children {
		wg.Go(func() {
			lines, err := child.Page(ctx, before, limit)
			for j := range lines {
				lines[j].Source = labels[i]
			}
			pages[i], errs[i] = lines, err
		})
	}
	wg.Wait()

	out := slices.Concat(pages...)
	if len(out) == 0 {
		return nil, errors.Join(errs...)
	}
	slices.SortStableFunc(out, func(a, b Line) int { return a.At.Compare(b.At) })
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// pageEnd is the newest instant a page may hold: everything already shown is
// held by the caller, and both databases read their end bound inclusively.
func pageEnd(before time.Time) time.Time { return before.Add(-time.Nanosecond) }

// lokiPage reads the entries before an instant.
//
// Loki answers within a range and has no notion of "the previous n lines", so a
// page is a window that ends where the last one began. A window with nothing in
// it is not the end of the stream — a service quiet for a day is quiet, not
// finished — so an empty answer widens the window and asks again, out to
// [lokiPageReach]. The doubling is what keeps a long silence from costing a
// request per hour of it.
func (c Config) lokiPage(ctx context.Context, client *http.Client, before time.Time, limit int) ([]Line, error) {
	q, ok := c.lokiQuery()
	if !ok {
		return nil, errNoSelector
	}
	end := pageEnd(before)
	for span, back := lokiSince, time.Duration(0); back < lokiPageReach; span *= 2 {
		start := end.Add(-back - span)
		if t := c.Range.Since; !t.IsZero() && !start.After(t) {
			start = t
		}
		entries, err := c.lokiRequest(ctx, client, url.Values{
			"query":     {q},
			"limit":     {strconv.Itoa(limit)},
			"direction": {"backward"},
			"start":     {lokiNanos(start)},
			"end":       {lokiNanos(end)},
		})
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			lines := make([]Line, 0, len(entries))
			for _, e := range entries {
				lines = append(lines, e.line())
			}
			return lines, nil
		}
		if t := c.Range.Since; !t.IsZero() && !start.After(t) {
			return nil, nil
		}
		back += span
	}
	return nil, nil
}

// lokiPageReach is how far back one page looks for something to show before
// answering that there is nothing. Loki's own limit on the length of a query is
// what bounds it: a month is inside every default there is.
const lokiPageReach = 30 * 24 * time.Hour

// vlogsPage reads the entries before an instant.
//
// Unlike Loki there is no window to widen: a LogsQL query with a limit and no
// start reads back as far as the data goes, so an empty page here is an empty
// database rather than a quiet window.
func (c Config) vlogsPage(ctx context.Context, client *http.Client, before time.Time, limit int) ([]Line, error) {
	q, plain := c.vlogsQuery(), c.vlogsSelector()
	lines, err := c.vlogsPageQuery(ctx, client, q, before, limit)
	if err == nil {
		return lines, nil
	}
	q, err = c.retryUnpushed(q, plain, err)
	if err != nil {
		return nil, err
	}
	return c.vlogsPageQuery(ctx, client, q, before, limit)
}

func (c Config) vlogsPageQuery(ctx context.Context, client *http.Client, q string, before time.Time, limit int) ([]Line, error) {
	params := url.Values{
		"query": {q},
		"limit": {strconv.Itoa(limit)},
		"end":   {pageEnd(before).Format(time.RFC3339Nano)},
	}
	if t := c.Range.Since; !t.IsZero() {
		params.Set("start", t.Format(time.RFC3339Nano))
	}
	var lines []Line
	if err := c.vlogsRequest(ctx, client, vlogsQueryPath, params, func(entry []byte) bool {
		line := Line{Data: vlogsNormalize(entry)}
		if t, ok := vlogsTime(entry); ok {
			line.At = t
		}
		lines = append(lines, line)
		return true
	}); err != nil {
		return nil, err
	}
	// A limited query answers newest first, and a page is read the way a stream
	// is.
	slices.Reverse(lines)
	return lines, nil
}
