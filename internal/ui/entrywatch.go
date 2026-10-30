package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
	"github.com/oteldb/telescope/internal/query"
)

type watchDoc struct {
	width, indent int
	items         []item
}

func (d *watchDoc) text(lines ...string) {
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Wrap(line, d.width, ""), "\n")...)
	}
	d.items = append(d.items, item{lines: wrapped})
}

func (d *watchDoc) row(label, key, value, rendered string, filter query.Expr) {
	if value == "" {
		return
	}
	d.items = append(d.items, item{
		key: key, value: value, pick: true, filter: filter, noFilter: filter == nil,
		lines: strings.Split(wrapField(label, rendered, d.indent, d.width), "\n"),
	})
}

func (d *watchDoc) field(label, key, value string) {
	d.row(label, key, value, renderValue(key, value), query.Field{Key: key, Op: query.OpEq, Value: value})
}

func (d *watchDoc) stamp(label, key string, t time.Time) {
	if t.IsZero() {
		return
	}
	value := t.Local().Format(time.RFC3339Nano)
	d.row(label, key, value, value+styleDim.Render("  "+humanSince(t)), nil)
}

func (m entryModel) sectionOpen(key string) bool {
	if open, ok := m.expanded[key]; ok {
		return open
	}
	return key == "changes" && m.entry.Record.Watch != nil && m.entry.Record.Watch.Event == nil
}

func (d *watchDoc) section(m entryModel, key, label string, payload []byte) bool {
	open := m.sectionOpen(key)
	marker := "▸ "
	if open {
		marker = "▾ "
	}
	d.text("")
	d.items = append(d.items, item{
		key: key, payload: payload, section: key, pick: true,
		lines: strings.Split(ansi.Wrap(styleTitle.Render(marker+label), d.width, ""), "\n"),
	})
	return open
}

func (m entryModel) watchDocument(width int) []item {
	w := m.entry.Record.Watch
	d := watchDoc{width: width, indent: labelColumn([]string{"namespace", "occurrences", "first seen", "last seen", "reporter"}, width)}
	if w.Event != nil {
		heading := renderValue("reason", w.Reason)
		if w.Reason == "" {
			heading = styleTitle.Render("Kubernetes Event")
		}
		if m.entry.Record.HasLevel {
			heading = renderLevelWord(m.entry.Record.Level) + "  " + heading
		}
		if w.Reason == "" {
			d.text(heading)
		} else {
			d.items = append(d.items, item{key: "reason", value: w.Reason, pick: true, lines: strings.Split(ansi.Wrap(heading, width, ""), "\n")})
		}
	} else {
		style := styleWarn
		if w.Action == "ADDED" {
			style = styleOK
		}
		if w.Action == "DELETED" {
			style = styleErr
		}
		d.items = append(d.items, item{key: "watch_event", value: w.Action, pick: true, lines: []string{style.Bold(true).Render(w.Action)}})
	}
	d.text("")
	kind, name, kindKey, nameKey := w.Kind, w.Name, "kind", "name"
	if w.Event != nil {
		kind, name, kindKey, nameKey = w.ResourceKind, w.ResourceName, "resource_kind", "resource_name"
	}
	if name != "" {
		ref := name
		var filter query.And
		if kind != "" {
			ref = kind + "/" + name
			filter = append(filter, query.Field{Key: kindKey, Op: query.OpEq, Value: kind})
		}
		filter = append(filter, query.Field{Key: nameKey, Op: query.OpEq, Value: name})
		if w.Namespace != "" {
			filter = append(filter, query.Field{Key: "namespace", Op: query.OpEq, Value: w.Namespace})
		}
		d.row("resource", "resource", ref, styleTitle.Render(logs.Escape(ref)), filter)
	}
	d.field("namespace", "namespace", w.Namespace)
	if ev := w.Event; ev != nil {
		d.text("")
		lines := strings.Split(ev.Message, "\n")
		for i := range lines {
			lines[i] = logs.Escape(lines[i])
		}
		d.row("message", "body", ev.Message, strings.Join(lines, "\n"), nil)
		d.text("")
		d.stamp("last seen", "time", ev.LastSeen)
		d.stamp("first seen", "first_seen", ev.FirstSeen)
		if w.Count > 0 {
			value := strconv.FormatInt(w.Count, 10)
			rendered := value
			if ev.Increase > 0 {
				rendered += styleDim.Render("  increased by " + strconv.FormatInt(ev.Increase, 10))
			}
			d.row("occurrences", "count", value, rendered, query.Field{Key: "count", Op: query.OpEq, Value: value})
		}
		d.row("reporter", "reporter", ev.Reporter, logs.Escape(ev.Reporter), nil)
		if w.Action == "DELETED" {
			d.text("", styleDim.Render("Event object removed; this is its last recorded occurrence count."))
		}
	} else {
		d.stamp("observed", "time", m.entry.At)
	}
	place := m.entry.Source
	if place == "" {
		place = m.cfg.Name
	}
	d.row("place", "place", place, logs.Escape(place), nil)
	if w.BaselineMissing {
		d.text("", styleDim.Render("Previous state unavailable; no diff was recorded."))
	}
	noun := "fields"
	if len(w.Changes) == 1 {
		noun = "field"
	}
	if len(w.Changes) > 0 && d.section(m, "changes", fmt.Sprintf("Changes (%d %s)", len(w.Changes), noun), nil) {
		d.changes(w)
	}
	labels := append(m.cfg.SourceLabels(m.entry.Source), m.entry.Labels...)
	if len(labels) > 0 && d.section(m, "connection", "Connection", nil) {
		d.field("watch action", "watch_event", w.Action)
		for _, l := range labels {
			label := l.Key
			if label == "namespace" {
				label = "namespace scope"
			}
			d.row(logs.Escape(label), l.Key, l.Value, renderValue(l.Key, l.Value), nil)
		}
	}
	if len(w.Object) > 0 && d.section(m, "object", "Object", w.Object) {
		d.object(m, w.Object)
	}
	if d.section(m, "raw", "Raw entry", m.entry.Raw) {
		d.json("raw", m.entry.Raw)
	}
	return d.items
}
