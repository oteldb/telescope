package logs

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/oteldb/telescope/internal/source"
)

// WatchRecord is the watch envelope beside a log record, decoded once on arrival.
type WatchRecord struct {
	Action, APIVersion, Kind, Namespace, Name string
	Reason, ResourceKind, ResourceName        string
	Count                                     int64
	Object                                    json.RawMessage
	Changes                                   []source.ResourceChange
	Event                                     *EventSummary
	BaselineMissing                           bool
}

// EventSummary holds the Event's own message and occurrence history.
type EventSummary struct {
	Message, Reporter   string
	FirstSeen, LastSeen time.Time
	Increase            int64
}

// OccurrenceText formats the Event's total.
func (w WatchRecord) OccurrenceText() string {
	if w.Count == 1 {
		return "1 occurrence"
	}
	return strconv.FormatInt(w.Count, 10) + " occurrences"
}

func parseWatch(rec Record) *WatchRecord {
	var w WatchRecord
	for _, f := range rec.Fields {
		switch f.Key {
		case "watch_event":
			w.Action = f.String()
		case "api_version":
			w.APIVersion = f.String()
		case "kind":
			w.Kind = f.String()
		case "namespace":
			w.Namespace = f.String()
		case "name":
			w.Name = f.String()
		case "reason":
			w.Reason = f.String()
		case "resource_kind":
			w.ResourceKind = f.String()
		case "resource_name":
			w.ResourceName = f.String()
		case "count":
			w.Count, _ = strconv.ParseInt(f.String(), 10, 64)
		case "object":
			w.Object = json.RawMessage(f.Value)
		}
	}
	switch w.Action {
	case "ADDED", "MODIFIED", "DELETED":
	default:
		return nil
	}
	for _, f := range rec.Fields {
		if f.Key == "changes" {
			_ = json.Unmarshal(f.Value, &w.Changes)
		}
	}
	w.BaselineMissing = strings.HasSuffix(rec.Body, " (previous state unavailable)")
	if w.Kind == "Event" && (w.APIVersion == "v1" || strings.HasPrefix(w.APIVersion, "events.k8s.io/")) {
		w.Event = eventSummary(w, rec)
	}
	return &w
}

func eventSummary(w WatchRecord, rec Record) *EventSummary {
	var obj struct {
		Message                  string `json:"message"`
		Note                     string `json:"note"`
		FirstTimestamp           string `json:"firstTimestamp"`
		DeprecatedFirstTimestamp string `json:"deprecatedFirstTimestamp"`
		LastTimestamp            string `json:"lastTimestamp"`
		DeprecatedLastTimestamp  string `json:"deprecatedLastTimestamp"`
		EventTime                string `json:"eventTime"`
		ReportingController      string `json:"reportingController"`
		ReportingComponent       string `json:"reportingComponent"`
		Source                   struct {
			Component string `json:"component"`
		} `json:"source"`
		Series struct {
			LastObservedTime string `json:"lastObservedTime"`
		} `json:"series"`
		Metadata struct {
			CreationTimestamp string `json:"creationTimestamp"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(w.Object, &obj); err != nil {
		return nil
	}
	e := &EventSummary{
		Message:   obj.Message,
		Reporter:  obj.ReportingController,
		FirstSeen: firstWatchTime(obj.FirstTimestamp, obj.DeprecatedFirstTimestamp, obj.EventTime, obj.Metadata.CreationTimestamp),
		LastSeen:  firstWatchTime(obj.Series.LastObservedTime, obj.LastTimestamp, obj.DeprecatedLastTimestamp, obj.EventTime),
	}
	if obj.Note != "" {
		e.Message = obj.Note
	}
	if e.Message == "" {
		e.Message = rec.Body
	}
	if e.Reporter == "" {
		e.Reporter = obj.ReportingComponent
	}
	if e.Reporter == "" {
		e.Reporter = obj.Source.Component
	}
	if e.LastSeen.IsZero() {
		e.LastSeen = rec.Time
	}
	for _, change := range w.Changes {
		if change.Path != "/count" && change.Path != "/series/count" {
			continue
		}
		var before, after int64
		if json.Unmarshal(change.Before, &before) == nil && json.Unmarshal(change.After, &after) == nil && before >= 0 && after > before {
			e.Increase = after - before
		}
	}
	return e
}

func firstWatchTime(values ...string) time.Time {
	for _, v := range values {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t
		}
	}
	return time.Time{}
}
