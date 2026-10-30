package logs

import (
	"strings"
)

func hasResourceEvent(rec Record) bool {
	return rec.Watch != nil
}

func resourceMessage(rec Record) (string, bool) {
	if !hasResourceEvent(rec) {
		return "", false
	}
	w := rec.Watch
	if w.Event != nil {
		var b strings.Builder
		b.WriteString(levelColors[rec.Level.String()] + Escape(w.Reason) + ansiReset)
		if w.ResourceName != "" {
			b.WriteString(" " + Escape(w.ResourceKind+"/"+w.ResourceName))
		}
		b.WriteString(" " + Escape(w.Event.Message))
		if w.Count > 0 {
			b.WriteString(" · " + w.OccurrenceText())
		}
		return b.String(), true
	}
	color := ""
	switch w.Action {
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
	for _, change := range w.Changes {
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
