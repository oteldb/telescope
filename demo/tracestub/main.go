// Command tracestub serves made-up traces over both APIs telescope reads, so
// the trace search can be driven without a Tempo or a VictoriaTraces to hand.
//
//	go run ./demo/tracestub &
//	telescope trace --from http://127.0.0.1:8765 --api jaeger
//	telescope trace --from http://127.0.0.1:8765 --api tempo
//
// It is the sibling of demo/emit.sh: that one stands in for a service writing
// logs, this one for the store its traces went to. Adding
//
//	traces:
//	  url: http://127.0.0.1:8765
//	  type: jaeger
//
// to a place in demo/home/telescope/config.yaml is what makes `T` open a trace
// there, since the first traces served carry the ids emit.sh writes into its
// lines.
//
// Both APIs are served at once and the caller picks with --api, which is the
// point of the thing: the two answer differently to the same question, and the
// screen has to be right about both.
package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// listen is where the stub serves. It is a flag rather than a constant only so
// two of them can run at once.
const listen = "127.0.0.1:8765"

var services = []string{"gateway", "api", "checkout", "inventory", "postgres"}

var operations = map[string][]string{
	"gateway":   {"POST /checkout", "GET /v1/orders", "GET /healthz"},
	"api":       {"GET /v1/orders", "POST /v1/orders", "GET /v1/cart"},
	"checkout":  {"Checkout.Submit", "Checkout.Validate"},
	"inventory": {"Inventory.Reserve", "Inventory.Release"},
	"postgres":  {"SELECT orders", "INSERT orders"},
}

// demoIDs are the trace ids demo/emit.sh writes into its log lines. The first
// traces served take them, so that pressing `T` on one of those lines opens
// something rather than a trace the store has never heard of.
var demoIDs = []string{
	"4bf92f3577b34da6a3ce929d0e0e4736",
	"8a3c60f7d188f8fa79d48a391a778fa6",
	"1b4a2f9c0e5d8877a1c2b3d4e5f60718",
}

type span struct {
	TraceID       string `json:"traceID"`
	SpanID        string `json:"spanID"`
	OperationName string `json:"operationName"`
	References    []ref  `json:"references"`
	StartTime     int64  `json:"startTime"`
	Duration      int64  `json:"duration"`
	Tags          []tag  `json:"tags"`
	Process       proc   `json:"process"`
}

type ref struct {
	RefType string `json:"refType"`
	TraceID string `json:"traceID"`
	SpanID  string `json:"spanID"`
}

type tag struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type proc struct {
	ServiceName string `json:"serviceName"`
}

type trace struct {
	TraceID string `json:"traceID"`
	Spans   []span `json:"spans"`

	// What a Tempo search answers with is a summary rather than the spans, so
	// it is worked out when the trace is made. None of it goes onto the wire
	// under these names.
	root    span
	start   time.Time
	dur     time.Duration
	errored int
}

// rng is a counter run through splitmix64.
//
// The numbers have to be the same on every run — a fixture whose traces move
// about cannot be compared against a screenshot from yesterday, and the demo
// ids have to land on the same traces every time. That rules out a seeded
// generator whose sequence is not part of the source, so the sequence is here.
type rng uint64

func (r *rng) next() uint64 {
	*r += 0x9e3779b97f4a7c15
	z := uint64(*r)
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *rng) intn(n int) int { return int(r.next() % uint64(n)) }

// build makes one trace: a call chain a few services deep, each span inside the
// one that called it, and one trace in four failing at the bottom.
func build(r *rng, id string, at time.Time) trace {
	depth := 3 + r.intn(4)
	total := time.Duration(20+r.intn(900)) * time.Millisecond
	fails := r.intn(4) == 0

	t := trace{TraceID: id, start: at, dur: total}
	var parent string
	offset, left := at, total
	for i := range depth {
		service := services[min(i, len(services)-1)]
		ops := operations[service]
		dur := left
		if i < depth-1 {
			dur = left * time.Duration(60+r.intn(30)) / 100
		}
		s := span{
			TraceID:       id,
			SpanID:        fmt.Sprintf("%016x", r.next()),
			OperationName: ops[r.intn(len(ops))],
			StartTime:     offset.UnixMicro(),
			Duration:      dur.Microseconds(),
			Process:       proc{ServiceName: service},
			Tags: []tag{
				{Key: "http.method", Type: "string", Value: "POST"},
				{Key: "http.route", Type: "string", Value: "/v1/orders"},
				{Key: "span.kind", Type: "string", Value: "server"},
			},
		}
		if parent != "" {
			s.References = []ref{{RefType: "CHILD_OF", TraceID: id, SpanID: parent}}
		}
		// The failure is at the bottom of the chain and nowhere else: a request
		// that failed deep inside is what somebody opens a trace to find.
		if fails && i == depth-1 {
			s.Tags = append(s.Tags,
				tag{Key: "error", Type: "bool", Value: true},
				tag{Key: "http.status_code", Type: "int64", Value: 500},
				tag{Key: "otel.status_description", Type: "string", Value: "upstream timed out"},
			)
			t.errored++
		} else {
			s.Tags = append(s.Tags, tag{Key: "http.status_code", Type: "int64", Value: 200})
		}
		if i == 0 {
			t.root = s
		}
		t.Spans = append(t.Spans, s)
		parent, offset, left = s.SpanID, offset.Add(dur/8), dur
	}
	return t
}

type store struct {
	held []trace
	byID map[string]trace
}

func newStore(count int, now time.Time) *store {
	s := &store{byID: make(map[string]trace, count)}
	var r rng
	for i := range count {
		id := fmt.Sprintf("%016x%016x", r.next(), r.next())
		if i < len(demoIDs) {
			id = demoIDs[i]
		}
		// Newest first, spread back over the last couple of hours.
		t := build(&r, id, now.Add(-time.Duration(i)*3*time.Minute))
		s.held = append(s.held, t)
		s.byID[t.TraceID] = t
	}
	return s
}

func main() {
	addr := flag.String("addr", listen, "where to listen")
	count := flag.Int("traces", 40, "how many traces to hold")
	flag.Parse()

	s := newStore(*count, time.Now())
	mux := http.NewServeMux()
	s.routeJaeger(mux)
	s.routeTempo(mux)

	log.Printf("serving %d traces on http://%s", len(s.held), *addr)
	log.Printf("  jaeger: telescope trace --from http://%s --api jaeger", *addr)
	log.Printf("  tempo:  telescope trace --from http://%s --api tempo", *addr)

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// routeJaeger serves the query API, which is what Jaeger and VictoriaTraces
// answer on.
func (s *store) routeJaeger(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/services", func(w http.ResponseWriter, r *http.Request) {
		logAsked(r)
		writeJSON(w, map[string]any{"data": services})
	})
	mux.HandleFunc("GET /api/services/{service}/operations", func(w http.ResponseWriter, r *http.Request) {
		logAsked(r)
		out := []map[string]string{}
		for _, op := range operations[r.PathValue("service")] {
			out = append(out, map[string]string{"name": op, "spanKind": "server"})
		}
		writeJSON(w, map[string]any{"data": out})
	})
	mux.HandleFunc("GET /api/traces", func(w http.ResponseWriter, r *http.Request) {
		logAsked(r)
		q := r.URL.Query()
		// Jaeger indexes per service and refuses without one, which is a rule
		// the search screen has to be right about.
		if q.Get("service") == "" {
			http.Error(w, `{"errors":[{"msg":"parameter 'service' is required"}]}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"data": s.matching(q)})
	})
	mux.HandleFunc("GET /api/traces/{id}", func(w http.ResponseWriter, r *http.Request) {
		logAsked(r)
		t, ok := s.byID[r.PathValue("id")]
		if !ok {
			http.Error(w, `{"errors":[{"msg":"trace not found"}]}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"data": []trace{t}})
	})
}

// routeTempo serves Tempo's API. Only enough TraceQL is read to see the form
// arrive as a query: it is matched by substring rather than parsed, which is
// the one thing here that a real store does properly.
func (s *store) routeTempo(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		logAsked(r)
		q := r.URL.Query()
		out := []map[string]any{}
		for _, t := range s.held {
			if len(out) >= limitOf(q) {
				break
			}
			if !traceQLMatches(q.Get("q"), t) {
				continue
			}
			out = append(out, map[string]any{
				"traceID":         t.TraceID,
				"rootServiceName": t.root.Process.ServiceName,
				"rootTraceName":   t.root.OperationName,
				// A 64-bit field, so protojson writes it as a string.
				"startTimeUnixNano": strconv.FormatInt(t.start.UnixNano(), 10),
				"durationMs":        t.dur.Milliseconds(),
				"spanSets":          []map[string]any{{"matched": len(t.Spans)}},
			})
		}
		writeJSON(w, map[string]any{
			"traces":  out,
			"metrics": map[string]any{"inspectedTraces": len(s.held)},
		})
	})
	mux.HandleFunc("GET /api/v2/traces/{id}", func(w http.ResponseWriter, r *http.Request) {
		logAsked(r)
		t, ok := s.byID[r.PathValue("id")]
		if !ok {
			http.Error(w, `{"error":"trace not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, otlpOf(t))
	})
	mux.HandleFunc("GET /api/v2/search/tag/{tag}/values", func(w http.ResponseWriter, r *http.Request) {
		logAsked(r)
		values := []map[string]string{}
		add := func(v string) { values = append(values, map[string]string{"type": "string", "value": v}) }
		if strings.Contains(r.PathValue("tag"), "service.name") {
			for _, name := range services {
				add(name)
			}
		} else {
			// Tempo has no index of what one service was called to do, so it
			// answers with every span name it holds.
			for _, ops := range operations {
				for _, op := range ops {
					add(op)
				}
			}
		}
		writeJSON(w, map[string]any{"tagValues": values})
	})
}

// matching filters by the parameters Jaeger reads, which is how a form that
// reached the server is told from one that did not.
func (s *store) matching(q map[string][]string) []trace {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	out := []trace{}
	for _, t := range s.held {
		if len(out) >= limitOf(q) {
			break
		}
		if name := get("service"); name != "" && !hasService(t, name) {
			continue
		}
		if op := get("operation"); op != "" && !hasOperation(t, op) {
			continue
		}
		if d, err := time.ParseDuration(get("minDuration")); err == nil && t.dur < d {
			continue
		}
		if d, err := time.ParseDuration(get("maxDuration")); err == nil && t.dur > d {
			continue
		}
		if tags := get("tags"); tags != "" {
			var want map[string]string
			if json.Unmarshal([]byte(tags), &want) == nil && !hasTags(t, want) {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

func limitOf(q map[string][]string) int {
	if v := q["limit"]; len(v) > 0 {
		if n, err := strconv.Atoi(v[0]); err == nil && n > 0 {
			return n
		}
	}
	return 20
}

func hasService(t trace, name string) bool {
	for _, s := range t.Spans {
		if s.Process.ServiceName == name {
			return true
		}
	}
	return false
}

func hasOperation(t trace, name string) bool {
	for _, s := range t.Spans {
		if s.OperationName == name {
			return true
		}
	}
	return false
}

func hasTags(t trace, want map[string]string) bool {
	for k, v := range want {
		var found bool
		for _, s := range t.Spans {
			for _, tg := range s.Tags {
				if tg.Key == k && fmt.Sprint(tg.Value) == v {
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// traceQLMatches reads the compiled query by looking for the values in it. It
// is not a parser and does not pretend to be one: what it is for is seeing that
// the service, the operation and the duration typed into the form arrived.
func traceQLMatches(q string, t trace) bool {
	if q == "" || q == "{}" {
		return true
	}
	for _, name := range services {
		if strings.Contains(q, `"`+name+`"`) && !hasService(t, name) {
			return false
		}
	}
	for _, ops := range operations {
		for _, op := range ops {
			if strings.Contains(q, `"`+op+`"`) && !hasOperation(t, op) {
				return false
			}
		}
	}
	if strings.Contains(q, "traceDuration>") && t.dur < 300*time.Millisecond {
		return false
	}
	if strings.Contains(q, "500") && t.errored == 0 {
		return false
	}
	return true
}

// otlpOf writes a trace the way Tempo's v2 endpoint does: OTLP through
// protojson, which means the ids arrive base64 and not as the hex the
// specification writes them in. Serving them that way is the point — it is the
// encoding the decoder has to tell apart by length.
func otlpOf(t trace) map[string]any {
	byService := map[string][]map[string]any{}
	for _, s := range t.Spans {
		out := map[string]any{
			"traceId":           b64(s.TraceID),
			"spanId":            b64(s.SpanID),
			"name":              s.OperationName,
			"kind":              "SPAN_KIND_SERVER",
			"startTimeUnixNano": strconv.FormatInt(s.StartTime*1000, 10),
			"endTimeUnixNano":   strconv.FormatInt((s.StartTime+s.Duration)*1000, 10),
			"attributes":        otlpAttrs(s.Tags),
		}
		if len(s.References) > 0 {
			out["parentSpanId"] = b64(s.References[0].SpanID)
		}
		for _, tg := range s.Tags {
			if tg.Key == "error" {
				out["status"] = map[string]any{
					"code":    "STATUS_CODE_ERROR",
					"message": "upstream timed out",
				}
			}
		}
		byService[s.Process.ServiceName] = append(byService[s.Process.ServiceName], out)
	}

	resourceSpans := []map[string]any{}
	// One resource per service, which is where a span's service.name lives in
	// OTLP: it is the resource's and not the span's.
	for _, name := range services {
		spans := byService[name]
		if len(spans) == 0 {
			continue
		}
		resourceSpans = append(resourceSpans, map[string]any{
			"resource": map[string]any{"attributes": []map[string]any{
				{"key": "service.name", "value": map[string]any{"stringValue": name}},
			}},
			"scopeSpans": []map[string]any{{"spans": spans}},
		})
	}
	return map[string]any{"resourceSpans": resourceSpans}
}

func otlpAttrs(tags []tag) []map[string]any {
	out := make([]map[string]any, 0, len(tags))
	for _, tg := range tags {
		var value map[string]any
		switch v := tg.Value.(type) {
		case bool:
			value = map[string]any{"boolValue": v}
		case int:
			value = map[string]any{"intValue": strconv.Itoa(v)}
		default:
			value = map[string]any{"stringValue": fmt.Sprint(v)}
		}
		out = append(out, map[string]any{"key": tg.Key, "value": value})
	}
	return out
}

// b64 rewrites a hex id the way protojson writes one.
func b64(id string) string {
	raw, err := hex.DecodeString(id)
	if err != nil {
		return id
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// logAsked records what was asked for, which is most of what this is for:
// watching the form arrive as a query. The URL is somebody else's bytes even
// here, so the control characters that would forge a second log line are
// dropped rather than written.
func logAsked(r *http.Request) {
	log.Println(r.Method, strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return -1
		}
		return c
	}, r.URL.String()))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
