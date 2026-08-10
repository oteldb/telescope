package logs

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/jx"
	"go.uber.org/zap/zapcore"
)

// timeKeys, levelKeys and bodyKeys are the field aliases understood on input,
// covering zap, OTEL log records, logrus, bunyan and friends.
var (
	timeKeys  = []string{"ts", "time", "timestamp", "@timestamp", "Timestamp", "ObservedTimestamp"}
	levelKeys = []string{"level", "lvl", "severity", "SeverityText", "severity_text"}
	bodyKeys  = []string{"msg", "message", "Body", "body"}
	traceKeys = []string{"trace_id", "traceID", "TraceID", "traceid"}
	spanKeys  = []string{"span_id", "spanID", "SpanID", "spanid"}
)

// Parse decodes a JSON log line. Non-JSON lines yield an unstructured record.
func Parse(line []byte) Record {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Record{Body: string(trimmed)}
	}

	var (
		r      Record
		fields []Field
	)
	d := jx.DecodeBytes(trimmed)
	if err := d.ObjBytes(func(d *jx.Decoder, key []byte) error {
		// RawAppend copies the value out of the decoder buffer so it stays
		// valid after the callback returns.
		raw, err := d.RawAppend(nil)
		if err != nil {
			return err
		}
		fields = append(fields, Field{Key: string(key), Value: raw})
		return nil
	}); err != nil {
		return Record{Body: string(trimmed)}
	}

	r.Structured = true
	r.Fields = fields
	for _, f := range fields {
		switch {
		case r.Time.IsZero() && matches(f.Key, timeKeys):
			if t, ok := parseTime(f.Value); ok {
				r.Time = t
			}
		case r.Body == "" && matches(f.Key, bodyKeys):
			r.Body = asString(f.Value)
		case matches(f.Key, levelKeys):
			if l, ok := parseLevel(f.Value); ok {
				r.Level = l
			}
		case r.TraceID == "" && matches(f.Key, traceKeys):
			r.TraceID = asString(f.Value)
		case r.SpanID == "" && matches(f.Key, spanKeys):
			r.SpanID = asString(f.Value)
		}
	}
	return r
}

func matches(key string, aliases []string) bool {
	for _, a := range aliases {
		if strings.EqualFold(key, a) {
			return true
		}
	}
	return false
}

func parseLevel(raw jx.Raw) (zapcore.Level, bool) {
	s := strings.ToLower(strings.TrimSpace(asString(raw)))
	switch s {
	case "trace", "trace1", "trace2", "trace3", "trace4":
		return zapcore.DebugLevel, true
	case "warning":
		return zapcore.WarnLevel, true
	case "err", "critical", "crit", "alert", "emerg", "emergency":
		return zapcore.ErrorLevel, true
	case "notice":
		return zapcore.InfoLevel, true
	}
	// Numeric severity, either an OTEL severity number or a syslog priority.
	if n, err := strconv.Atoi(s); err == nil {
		return severityNumber(n), true
	}
	var l zapcore.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return 0, false
	}
	return l, true
}

// severityNumber maps an OTEL severity number onto a zap level.
func severityNumber(n int) zapcore.Level {
	switch {
	case n <= 4:
		return zapcore.DebugLevel
	case n <= 12:
		return zapcore.InfoLevel
	case n <= 16:
		return zapcore.WarnLevel
	case n <= 20:
		return zapcore.ErrorLevel
	default:
		return zapcore.FatalLevel
	}
}

// timeLayouts are tried in order for string timestamps.
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.000Z0700",
	"2006-01-02 15:04:05.000000000 -0700 MST",
	"2006-01-02 15:04:05.000",
	"2006-01-02 15:04:05",
	"15:04:05.000",
}

func parseTime(raw jx.Raw) (time.Time, bool) {
	if raw.Type() == jx.Number {
		num := string(raw)
		// Integer epochs are converted exactly; float64 cannot hold nanosecond
		// precision for millisecond-and-finer epochs.
		if !strings.ContainsAny(num, ".eE") {
			n, err := strconv.ParseInt(num, 10, 64)
			if err != nil || n <= 0 {
				return time.Time{}, false
			}
			return time.Unix(0, n*int64(epochUnit(float64(n)))), true
		}
		f, err := strconv.ParseFloat(num, 64)
		if err != nil || f <= 0 {
			return time.Time{}, false
		}
		sec, frac := math.Modf(f * float64(epochUnit(f)) / float64(time.Second))
		return time.Unix(int64(sec), int64(frac*float64(time.Second))), true
	}
	s := asString(raw)
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// epochUnit guesses the unit of a numeric epoch timestamp by magnitude.
func epochUnit(n float64) time.Duration {
	switch {
	case n >= 1e18:
		return time.Nanosecond
	case n >= 1e15:
		return time.Microsecond
	case n >= 1e12:
		return time.Millisecond
	default:
		return time.Second
	}
}

func asString(raw jx.Raw) string {
	if raw.Type() == jx.String {
		if s, err := jx.DecodeBytes(raw).Str(); err == nil {
			return s
		}
	}
	return string(raw)
}
