package logs

import (
	"encoding/json"
	"strings"

	"github.com/oteldb/telescope/internal/source"
)

func hasResourceEvent(rec Record) bool {
	for _, f := range rec.Fields {
		if f.Key == "watch_event" {
			switch f.String() {
			case "ADDED", "MODIFIED", "DELETED":
				return true
			}
		}
	}
	return false
}

func resourceMessage(rec Record) (string, bool) {
	if !hasResourceEvent(rec) {
		return "", false
	}
	var event string
	var changes []source.ResourceChange
	for _, f := range rec.Fields {
		switch f.Key {
		case "watch_event":
			event = f.String()
		case "changes":
			_ = json.Unmarshal(f.Value, &changes)
		}
	}
	color := ""
	switch event {
	case "ADDED":
		color = ansiOK
	case "MODIFIED":
		color = ansiWarn
	case "DELETED":
		color = ansiErr
	default:
		return "", false
	}
	var b strings.Builder
	b.WriteString(color + Escape(rec.Body) + ansiReset)
	for _, change := range changes {
		b.WriteString(" · " + Escape(change.Path) + ": ")
		switch {
		case len(change.Before) == 0:
			b.WriteString(ansiOK + "+ " + Escape(string(change.After)) + ansiReset)
		case len(change.After) == 0:
			b.WriteString(ansiErr + "- " + Escape(string(change.Before)) + ansiReset)
		default:
			b.WriteString(ansiErr + Escape(string(change.Before)) + ansiReset)
			b.WriteString(" → ")
			b.WriteString(ansiOK + Escape(string(change.After)) + ansiReset)
		}
	}
	return b.String(), true
}
