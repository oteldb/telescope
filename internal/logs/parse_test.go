package logs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

func TestParse(t *testing.T) {
	for _, tt := range []struct {
		name       string
		line       string
		structured bool
		body       string
		level      zapcore.Level
		time       time.Time
		trace      string
	}{
		{
			name:       "zap",
			line:       `{"level":"warn","ts":1700000000.5,"msg":"slow query","dur":"1s"}`,
			structured: true,
			body:       "slow query",
			level:      zapcore.WarnLevel,
			time:       time.Unix(1700000000, 500000000),
		},
		{
			name:       "rfc3339",
			line:       `{"time":"2026-08-10T10:00:00.5Z","level":"ERROR","message":"boom"}`,
			structured: true,
			body:       "boom",
			level:      zapcore.ErrorLevel,
			time:       time.Date(2026, 8, 10, 10, 0, 0, 500000000, time.UTC),
		},
		{
			name:       "otel severity number",
			line:       `{"Timestamp":"2026-08-10T10:00:00Z","SeverityText":"17","Body":"oops","TraceID":"abc"}`,
			structured: true,
			body:       "oops",
			level:      zapcore.ErrorLevel,
			time:       time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC),
			trace:      "abc",
		},
		{
			name:       "epoch millis",
			line:       `{"timestamp":1700000000123,"level":"info","msg":"hi"}`,
			structured: true,
			body:       "hi",
			level:      zapcore.InfoLevel,
			time:       time.Unix(1700000000, 123000000),
		},
		{
			name:       "victorialogs",
			line:       `{"_time":"2026-08-10T10:00:00Z","_stream":"{app=\"api\"}","_msg":"boom","level":"error"}`,
			structured: true,
			body:       "boom",
			level:      zapcore.ErrorLevel,
			time:       time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC),
		},
		{
			name:  "plain text",
			line:  "just a line",
			body:  "just a line",
			level: zapcore.DebugLevel,
		},
		{
			name:  "broken json",
			line:  `{"level":`,
			body:  `{"level":`,
			level: zapcore.DebugLevel,
		},
		{
			name:  "empty",
			line:  "   ",
			body:  "",
			level: zapcore.DebugLevel,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse([]byte(tt.line))
			require.Equal(t, tt.structured, got.Structured)
			require.Equal(t, tt.body, got.Body)
			require.Equal(t, tt.trace, got.TraceID)
			if tt.structured {
				require.Equal(t, tt.level, got.Level)
			}
			if !tt.time.IsZero() {
				require.True(t, tt.time.Equal(got.Time), "want %s, got %s", tt.time, got.Time)
			}
		})
	}
}

func TestParseFieldOrder(t *testing.T) {
	got := Parse([]byte(`{"level":"info","msg":"a","z":1,"b":2}`))
	require.Equal(t, []string{"level", "msg", "z", "b"}, keys(got.Fields))
}

func keys(fields []Field) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.Key
	}
	return out
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`{"level":"warn","ts":1700000000.5,"msg":"slow query"}`,
		`{"time":"2026-08-10T10:00:00Z","severity":3,"body":"x"}`,
		`{`,
		"plain",
		"",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		r := Parse([]byte(line))
		if !r.Structured {
			require.Empty(t, r.Fields)
		}
	})
}
