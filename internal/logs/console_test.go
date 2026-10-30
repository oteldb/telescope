package logs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	"github.com/oteldb/telescope/internal/source"
)

var consoleCases = []struct {
	name, line, body string
	fields           map[string]string
}{
	{
		name: "controller request",
		line: `2026-10-30T13:50:46Z    DEBUG   proxmox request {"controller": "proxmoxcluster", "controllerGroup": "imagebuilder.example.test", "controllerKind": "ProxmoxCluster", "ProxmoxCluster": {"name":"demo"}, "namespace": "", "name": "demo", "reconcileID": "00000000-0000-4000-8000-000000000001", "method": "GET", "path": "/version"}`,
		body: "proxmox request", fields: map[string]string{"method": "GET", "path": "/version", "name": "demo", "ProxmoxCluster": `{"name":"demo"}`},
	},
	{
		name: "probe",
		line: `2026-10-30T13:50:46Z    DEBUG   probed  {"controller": "proxmoxcluster", "cluster": "demo", "reason": "Connected"}`,
		body: "probed", fields: map[string]string{"reason": "Connected", "cluster": "demo"},
	},
	{
		name: "tab separated",
		line: "2026-10-30T13:50:46Z\tDEBUG\tproxmox request\t{\"method\":\"GET\",\"path\":\"/nodes\"}",
		body: "proxmox request", fields: map[string]string{"path": "/nodes"},
	},
	{
		name: "caller",
		line: "2026-10-30T13:50:46Z\tDEBUG\tcontrollers/proxmox.go:42\tproxmox request\t{\"method\":\"GET\"}",
		body: "proxmox request", fields: map[string]string{"caller": "controllers/proxmox.go:42", "method": "GET"},
	},
	{
		name: "logger and caller",
		line: "2026-10-30T13:50:46Z\tDEBUG\tcontroller.proxmox\tcontrollers/proxmox.go:42\tproxmox request\t{\"method\":\"GET\"}",
		body: "proxmox request", fields: map[string]string{"logger": "controller.proxmox", "caller": "controllers/proxmox.go:42"},
	},
	{
		name: "console without attributes",
		line: "2026-10-30T13:50:46Z\tDEBUG\tstarted listening",
		body: "started listening",
	},
	{
		name: "literal braces before attributes",
		line: `2026-10-30T13:50:46Z DEBUG checking {status} {"method":"GET"}`,
		body: "checking {status}", fields: map[string]string{"method": "GET"},
	},
	{
		name: "empty object in message",
		line: `2026-10-30T13:50:46Z DEBUG empty response {}`,
		body: "empty response {}",
	},
	{
		name: "malformed context stays visible",
		line: `2026-10-30T13:50:46Z DEBUG request {"method":`,
		body: `request {"method":`,
	},
}

func TestConsoleLogsExposeTheHeaderAndContext(t *testing.T) {
	for _, tt := range consoleCases {
		t.Run(tt.name, func(t *testing.T) {
			e := NewStore(10).Append(source.Line{Data: []byte(tt.line)})
			require.NotNil(t, e)
			require.True(t, e.Record.Structured)
			require.True(t, e.Record.HasLevel)
			require.Equal(t, zapcore.DebugLevel, e.Record.Level)
			require.Equal(t, time.Date(2026, 10, 30, 13, 50, 46, 0, time.UTC), e.Record.Time)
			require.Equal(t, tt.body, e.Record.Body)
			require.Equal(t, tt.body, ansi.Strip(e.Head))
			for key, want := range tt.fields {
				got, ok := e.Field(key)
				require.True(t, ok, key)
				require.Equal(t, want, got, key)
			}
			require.Equal(t, tt.line, string(e.Raw))
			require.NotContains(t, e.Text, "2026-10-30T13:50:46Z", "the time belongs in the gutter")
		})
	}
}

func TestConsoleLevelAndContextFilters(t *testing.T) {
	e := NewStore(10).Append(source.Line{Data: []byte(consoleCases[0].line)})
	filter := (Filter{Query: `level=debug method=GET path="/version"`}).Compile()
	require.NoError(t, filter.Err())
	require.True(t, filter.Match(e))
	require.False(t, (Filter{Query: "level>=info"}).Compile().Match(e))
	require.False(t, (Filter{Query: `path="/nodes"`}).Compile().Match(e))
	require.Equal(t, 1, strings.Count(ansi.Strip(e.Text), "proxmox request"))
}

func TestConsoleHeadersAreRecognizedConservatively(t *testing.T) {
	for _, tt := range []struct{ name, line string }{
		{"ordinary text", "proxmox request started"},
		{"JSON fragment", `proxmox request {"method":"GET"}`},
		{"invalid date", `2026-13-30T13:50:46Z DEBUG request {"method":"GET"}`},
		{"unknown level", `2026-10-30T13:50:46Z SOMETHING request {"method":"GET"}`},
		{"HTTP status", `2026-10-30T13:50:46Z 200 response {"method":"GET"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := Parse([]byte(tt.line))
			require.False(t, r.Structured)
			require.False(t, r.HasLevel)
			require.Equal(t, tt.line, r.Body)
		})
	}
}

func TestConsoleHeadersWinOverConflictingContext(t *testing.T) {
	line := `2026-10-30T13:50:46Z DEBUG outer message {"ts":1,"level":"fatal","msg":"inner message","trace_id":"abc"}`
	e := NewStore(1).Append(source.Line{Data: []byte(line)})
	require.Equal(t, "outer message", e.Record.Body)
	require.Equal(t, zapcore.DebugLevel, e.Record.Level)
	require.Equal(t, "abc", e.Record.TraceID)
	value, ok := e.Field("msg")
	require.True(t, ok)
	require.Equal(t, "outer message", value)
	require.NotContains(t, e.Text, "inner message")
	require.Equal(t, line, string(e.Raw), "the conflicting fields are retained in raw data")
}

func TestConsoleValuesOwnTheirBytes(t *testing.T) {
	input := []byte(consoleCases[0].line)
	r := Parse(input)
	before := string(recordJSON(r))
	clear(input)
	_ = Parse([]byte(consoleCases[1].line))
	require.Equal(t, before, string(recordJSON(r)))
	require.True(t, json.Valid(recordJSON(r)))
}

func TestZapConsoleEncoderOutput(t *testing.T) {
	for _, tt := range []struct {
		name        string
		encodeLevel zapcore.LevelEncoder
	}{
		{"plain", zapcore.CapitalLevelEncoder},
		{"colored", zapcore.CapitalColorLevelEncoder},
	} {
		t.Run(tt.name, func(t *testing.T) {
			enc := zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
				TimeKey: "ts", LevelKey: "level", NameKey: "logger", CallerKey: "caller", MessageKey: "msg",
				EncodeTime: zapcore.ISO8601TimeEncoder, EncodeLevel: tt.encodeLevel, EncodeCaller: zapcore.ShortCallerEncoder,
			})
			buffer, err := enc.EncodeEntry(zapcore.Entry{
				Time: time.Date(2026, 10, 30, 13, 50, 46, 0, time.UTC), Level: zapcore.DebugLevel,
				LoggerName: "controller.proxmox", Caller: zapcore.EntryCaller{Defined: true, File: "controllers/proxmox.go", Line: 42}, Message: "proxmox request",
			}, []zapcore.Field{{Key: "method", Type: zapcore.StringType, String: "GET"}})
			require.NoError(t, err)
			data := append([]byte(nil), buffer.Bytes()...)
			buffer.Free()
			e := NewStore(1).Append(source.Line{Data: data})
			require.True(t, e.Record.Structured)
			require.Equal(t, zapcore.DebugLevel, e.Record.Level)
			require.Equal(t, "proxmox request", e.Record.Body)
			for key, want := range map[string]string{"caller": "controllers/proxmox.go:42", "logger": "controller.proxmox", "method": "GET"} {
				got, ok := e.Field(key)
				require.True(t, ok, key)
				require.Equal(t, want, got, key)
			}
		})
	}
}

func TestConsoleTimezone(t *testing.T) {
	for _, stamp := range []string{"2026-10-30T16:50:46+03:00", "2026-10-30T16:50:46.000+0300"} {
		t.Run(stamp, func(t *testing.T) {
			r := Parse([]byte(stamp + " DEBUG request"))
			require.True(t, r.Structured)
			require.Equal(t, "2026-10-30T13:50:46Z", r.Time.UTC().Format(time.RFC3339Nano))
		})
	}
}

func FuzzConsole(f *testing.F) {
	for _, tt := range consoleCases {
		f.Add(tt.line)
	}
	f.Add("2026-10-30T13:50:46Z\t\x1b[35mDEBUG\x1b[0m\trequest\t{\"method\":\"GET\"}")
	f.Add("2026-10-30T13:50:46Z DEBUG " + strings.Repeat("{ ", 32) + `{"method":"GET"}`)
	f.Fuzz(func(t *testing.T, line string) {
		input := []byte(line)
		r, ok := parseConsole(input)
		if !ok {
			return
		}
		require.True(t, r.Structured)
		require.True(t, r.HasLevel)
		require.False(t, r.Time.IsZero())
		encoded := string(recordJSON(r))
		require.True(t, json.Valid([]byte(encoded)))
		clear(input)
		require.Equal(t, encoded, string(recordJSON(r)))
	})
}
