package logs

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/go-faster/jx"

	"github.com/oteldb/telescope/internal/query"
)

func parseConsole(line []byte) (Record, bool) {
	stamp, rest := consoleWord(line)
	if len(stamp) < 20 || len(stamp) > 35 || stamp[4] != '-' || stamp[7] != '-' || stamp[10] != 'T' {
		return Record{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, string(stamp))
	if err != nil {
		at, err = time.Parse("2006-01-02T15:04:05.000Z0700", string(stamp))
		if err != nil {
			return Record{}, false
		}
	}
	level, rest := consoleWord(rest)
	levelName := ansi.Strip(string(level))
	// HTTP status codes after a timestamp are not log levels.
	if _, err := strconv.Atoi(levelName); err == nil {
		return Record{}, false
	}
	lvl, ok := query.ParseLevel(levelName)
	if !ok {
		return Record{}, false
	}
	message, prefix := consolePrefix(rest)
	message, context := consoleContext(message)
	r := Record{
		Structured: true, Time: at, Level: lvl, HasLevel: true, Body: string(message),
		TraceID: context.TraceID, SpanID: context.SpanID,
		Fields: []Field{
			{Key: "ts", Value: consoleString(string(stamp))},
			{Key: "level", Value: consoleString(lvl.String())},
			{Key: "msg", Value: consoleString(string(message))},
		},
	}
	reserved := map[string]bool{"ts": true, "level": true, "msg": true}
	for _, f := range prefix {
		r.Fields = append(r.Fields, f)
		reserved[f.Key] = true
	}
	for _, f := range context.Fields {
		// The console header owns these keys; the original context remains raw.
		if !reserved[f.Key] {
			r.Fields = append(r.Fields, f)
		}
	}
	return r, true
}

func consoleWord(data []byte) ([]byte, []byte) {
	data = bytes.TrimLeft(data, " \t")
	i := bytes.IndexAny(data, " \t")
	if i < 0 {
		return data, nil
	}
	return data[:i], bytes.TrimLeft(data[i:], " \t")
}

func consolePrefix(data []byte) ([]byte, []Field) {
	first, rest, ok := bytes.Cut(data, []byte("\t"))
	if !ok {
		return data, nil
	}
	first = bytes.TrimSpace(first)
	if consoleCaller(first) && len(bytes.TrimSpace(rest)) > 0 && bytes.TrimSpace(rest)[0] != '{' {
		return bytes.TrimLeft(rest, " \t"), []Field{{Key: "caller", Value: consoleString(string(first))}}
	}
	second, body, ok := bytes.Cut(rest, []byte("\t"))
	if ok && len(first) > 0 && !bytes.ContainsAny(first, " \t") && consoleCaller(bytes.TrimSpace(second)) {
		return bytes.TrimLeft(body, " \t"), []Field{
			{Key: "logger", Value: consoleString(string(first))},
			{Key: "caller", Value: consoleString(string(bytes.TrimSpace(second)))},
		}
	}
	return data, nil
}

func consoleCaller(data []byte) bool {
	i := bytes.LastIndexByte(data, ':')
	if i < 0 || !bytes.HasSuffix(data[:i], []byte(".go")) {
		return false
	}
	n, err := strconv.Atoi(string(data[i+1:]))
	return err == nil && n > 0
}

func consoleContext(data []byte) ([]byte, Record) {
	offset := 0
	// Bound speculative JSON decoding when a message itself contains braces.
	for range 16 {
		i := bytes.IndexByte(data[offset:], '{')
		if i < 0 {
			break
		}
		i += offset
		offset = i + 1
		if i > 0 && data[i-1] != ' ' && data[i-1] != '\t' {
			continue
		}
		if !json.Valid(data[i:]) {
			continue
		}
		r := Parse(data[i:])
		if r.Structured && len(r.Fields) > 0 {
			return bytes.TrimRight(data[:i], " \t"), r
		}
	}
	return data, Record{}
}

func consoleString(s string) jx.Raw {
	var e jx.Encoder
	e.Str(s)
	return jx.Raw(e.Bytes())
}

func recordJSON(r Record) []byte {
	var e jx.Encoder
	e.ObjStart()
	for _, f := range r.Fields {
		e.FieldStart(f.Key)
		e.Raw(f.Value)
	}
	e.ObjEnd()
	return e.Bytes()
}
