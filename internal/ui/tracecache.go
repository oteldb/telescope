package ui

import (
	"github.com/oteldb/telescope/internal/source"
	"github.com/oteldb/telescope/internal/trace"
)

// traceAsk is a fetch that was made: what was asked for and who was asked. It
// is what a reload repeats, and it holds an endpoint rather than a URL because
// asking again means asking with the same token.
type traceAsk struct {
	endpoint source.Endpoint
	id       string
}

// traceCacheSize is how many traces are held at once.
//
// Small on purpose: a trace is spans and their attributes all the way down, and
// the reason to keep any of them is the walk between a list and a request and
// back, which is a handful of traces and not a session's worth. What falls out
// is refetched, which costs one request and no correctness.
const traceCacheSize = 8

// traceCache remembers traces that were fetched, so that walking out of a trace
// into the lines it explains and back again does not ask for the same bytes
// twice.
//
// A trace is keyed by where it was read from as well as by its id: the same id
// means one request to one system and nothing at all to another, and a merge is
// several systems.
type traceCache struct {
	trees map[string]*trace.Tree
	// order is oldest first, which is enough of an eviction policy for eight
	// entries: what a reader is walking between is what was fetched last.
	order []string
}

func traceKey(url, id string) string { return url + "\x00" + id }

func (c *traceCache) get(url, id string) (*trace.Tree, bool) {
	t, ok := c.trees[traceKey(url, id)]
	return t, ok
}

func (c *traceCache) put(url, id string, t *trace.Tree) {
	if t == nil || id == "" {
		return
	}
	key := traceKey(url, id)
	if _, seen := c.trees[key]; seen {
		c.trees[key] = t
		return
	}
	if c.trees == nil {
		c.trees = make(map[string]*trace.Tree)
	}
	c.trees[key] = t
	c.order = append(c.order, key)
	for len(c.order) > traceCacheSize {
		delete(c.trees, c.order[0])
		c.order = c.order[1:]
	}
}

// drop forgets one trace, which is what a reload is: a trace still being
// written gains spans after it was first read, and the only way to see them is
// to ask again.
func (c *traceCache) drop(url, id string) {
	key := traceKey(url, id)
	if _, ok := c.trees[key]; !ok {
		return
	}
	delete(c.trees, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}
