package ui

import (
	"encoding/json"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/oteldb/telescope/internal/logs"
)

func (d *watchDoc) changes(w *logs.WatchRecord) {
	for _, change := range w.Changes {
		label := change.Path
		if w.Event != nil {
			switch label {
			case "/count", "/series/count":
				label = "occurrences"
			case "/lastTimestamp", "/series/lastObservedTime", "/deprecatedLastTimestamp":
				label = "last seen"
			case "/message", "/note":
				label = "message"
			}
		}
		d.text("", styleTitle.Render(logs.Escape(label)))
		if len(change.Before) > 0 {
			d.changeValue("before", change.Before, styleErr)
		}
		if len(change.After) > 0 {
			d.changeValue("after", change.After, styleOK)
		}
	}
}

func (d *watchDoc) changeValue(key string, raw json.RawMessage, style lipgloss.Style) {
	value := string(raw)
	var decoded string
	if json.Unmarshal(raw, &decoded) == nil && len(raw) > 0 && raw[0] == '"' {
		value = decoded
	}
	var lines []string
	for line := range strings.SplitSeq(prettyJSON(raw), "\n") {
		lines = append(lines, style.Render(logs.Escape(line)))
	}
	d.items = append(d.items, item{
		key: key, value: value, pick: true, noFilter: true,
		lines: strings.Split(wrapField(key, strings.Join(lines, "\n"), d.indent, d.width), "\n"),
	})
}

func (d *watchDoc) json(key string, raw []byte) {
	d.jsonDisplay(key, raw, raw)
}

func (d *watchDoc) jsonDisplay(key string, raw, original []byte) {
	for line := range strings.SplitSeq(prettyJSON(raw), "\n") {
		wrapped := ansi.Wrap(logs.Highlight(logs.Escape(line)), max(d.width-2, 1), "")
		for part := range strings.SplitSeq(wrapped, "\n") {
			d.items = append(d.items, item{key: key, payload: original, pick: true, noFilter: true, lines: []string{"  " + part}})
		}
	}
}

func (d *watchDoc) object(m entryModel, raw json.RawMessage) {
	var object, metadata map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || json.Unmarshal(object["metadata"], &metadata) != nil || len(metadata["managedFields"]) == 0 {
		d.json("object", raw)
		return
	}
	managed := metadata["managedFields"]
	delete(metadata, "managedFields")
	object["metadata"], _ = json.Marshal(metadata)
	withoutManaged, _ := json.Marshal(object)
	d.jsonDisplay("object", withoutManaged, raw)
	if d.section(m, "managedFields", "Managed fields", managed) {
		d.json("managedFields", managed)
	}
}
