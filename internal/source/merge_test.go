package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMergeInterleaves: two sources, each in order by itself, read as one
// timeline rather than one after the other.
func TestMergeInterleaves(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			vlogsEntry("2026-08-11T10:00:04Z", "api second") +
				vlogsEntry("2026-08-11T10:00:01Z", "api first"),
		))
	}))
	defer api.Close()
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			vlogsEntry("2026-08-11T10:00:05Z", "worker second") +
				vlogsEntry("2026-08-11T10:00:02Z", "worker first"),
		))
	}))
	defer worker.Close()

	first := vlogsConfig(api.URL, false)
	first.Name = "api"
	second := vlogsConfig(worker.URL, false)
	second.Name = "worker"

	cfg := Config{Collector: CollectorMerge, Merge: []Config{first, second}, Tail: 100}
	require.NoError(t, cfg.Validate())

	s, err := Start(t.Context(), cfg)
	require.NoError(t, err)

	var (
		bodies []string
		tags   []string
	)
	for l := range s.Lines() {
		bodies = append(bodies, body(string(l.Data)))
		tags = append(tags, l.Source)
	}
	require.NoError(t, <-s.Done())
	require.Equal(t, []string{"api first", "worker first", "api second", "worker second"}, bodies)
	require.Equal(t, []string{"api", "worker", "api", "worker"}, tags,
		"every line says which source it came from")
}

// body is the message of a rendered VictoriaLogs line, which the collector has
// already renamed off the wire's _msg.
func body(line string) string {
	_, rest, _ := strings.Cut(line, `"msg":"`)
	msg, _, _ := strings.Cut(rest, `"`)
	return msg
}

// TestMergeSurvivesOneSource: a merge of four environments is not as available
// as its worst one.
func TestMergeSurvivesOneSource(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(vlogsEntry("2026-08-11T10:00:01Z", "still here")))
	}))
	defer ok.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer broken.Close()

	good := vlogsConfig(ok.URL, false)
	good.Name = "good"
	bad := vlogsConfig(broken.URL, false)
	bad.Name = "bad"

	s, err := Start(t.Context(), Config{
		Collector: CollectorMerge,
		Merge:     []Config{good, bad},
		Tail:      100,
	})
	require.NoError(t, err)

	lines, err := drain(t, s)
	require.Len(t, lines, 2)
	require.Contains(t, lines[0], "still here")
	require.Contains(t, lines[1], "telescope: bad:", "and the one that failed says so where its lines would have been")
	require.Contains(t, lines[1], "500")
	require.ErrorContains(t, err, "500", "and the reader is told again at the end")
}

// TestMergeLabels: what each line is tagged with, when nothing was named.
func TestMergeLabels(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"named", Config{Name: "prod api", Collector: CollectorDocker, Container: "api"}, "prod api"},
		{"container", Config{Collector: CollectorDocker, Container: "api"}, "api"},
		{"unit", Config{Collector: CollectorJournal, Unit: "kubelet"}, "kubelet"},
		{"whole journal", Config{Collector: CollectorJournal}, "journal"},
		{"pod", Config{Collector: CollectorKubectl, Namespace: "ns", Target: "api"}, "ns/api"},
		{"command", Config{Collector: CollectorCommand, Args: "tail -F /var/log/x"}, "tail"},
		{"endpoint", Config{Collector: CollectorLoki, Endpoint: Endpoint{Name: "prod"}}, "prod"},
		{
			"over ssh",
			Config{Transport: TransportSSH, Host: "node1", Collector: CollectorDocker, Container: "api"},
			"node1:api",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.cfg.Label())
		})
	}

	// The same container on two hosts is two streams, and one tag for both
	// would say nothing.
	same := Config{Collector: CollectorDocker, Container: "api"}
	require.Equal(t, []string{"api", "api#2"}, mergeLabels([]Config{same, same}))
}

// TestMergeValidate: what a merge refuses, and how it says which source is at
// fault.
func TestMergeValidate(t *testing.T) {
	docker := Config{Collector: CollectorDocker, Container: "api"}

	require.ErrorContains(t,
		Config{Collector: CollectorMerge, Merge: []Config{docker}}.Validate(),
		"two or more sources")
	require.ErrorContains(t,
		Config{Collector: CollectorMerge, Merge: []Config{
			docker,
			{Name: "inner", Collector: CollectorMerge, Merge: []Config{docker, docker}},
		}}.Validate(),
		"merged inner: a merge cannot contain a merge")
	require.ErrorContains(t,
		Config{Collector: CollectorMerge, Merge: []Config{
			docker,
			{Name: "nowhere", Collector: CollectorLoki},
		}}.Validate(),
		"merged nowhere: loki requires an endpoint")

	// The window belongs to the merge, so a source that cannot honor it says so
	// even though it was declared without one.
	closed, err := ParseRange("6h..1h", time.Now())
	require.NoError(t, err)
	require.ErrorContains(t,
		Config{
			Collector: CollectorMerge,
			Range:     closed,
			Merge:     []Config{docker, {Name: "pods", Collector: CollectorKubectl, Target: "api"}},
		}.Validate(),
		"merged pods: kubectl has no end bound")
}

// TestMergeTitle: a merge has no single host, so its sources are its title.
func TestMergeTitle(t *testing.T) {
	cfg := Config{Collector: CollectorMerge, Merge: []Config{
		{Name: "api", Collector: CollectorDocker, Container: "api"},
		{Name: "worker", Collector: CollectorDocker, Container: "worker"},
	}}
	require.Equal(t, "merge api + worker", cfg.Title())
	require.Nil(t, cfg.Argv(), "a merge runs no command of its own")

	cfg.Range, _ = ParseRange("1h", time.Now())
	require.Equal(t, "merge api + worker · last 1h", cfg.Title())
}

// TestMergeStamps: a merged docker or kubectl is asked for the timestamps it
// otherwise leaves out, and they are carried beside the line rather than
// rendered in front of it.
func TestMergeStamps(t *testing.T) {
	cfg := Config{Collector: CollectorMerge, Tail: 10, Merge: []Config{
		{Collector: CollectorDocker, Container: "api"},
		{Collector: CollectorKubectl, Target: "api"},
		{Collector: CollectorJournal, Unit: "kubelet"},
	}}
	children := cfg.Children()
	require.Contains(t, children[0].Command(), "--timestamps")
	require.Contains(t, children[1].Command(), "--timestamps")
	require.NotContains(t, children[2].Command(), "--timestamps",
		"journalctl -o cat has no timestamp to ask for")

	data, at := unstamp([]byte("2026-08-11T10:00:01.5Z hello"))
	require.Equal(t, "hello", string(data))
	require.Equal(t, time.Date(2026, 8, 11, 10, 0, 1, 500_000_000, time.UTC), at.UTC())

	// A line that carries no stamp is left exactly as it came.
	data, at = unstamp([]byte("hello world"))
	require.Equal(t, "hello world", string(data))
	require.True(t, at.IsZero())
}

// TestMergeUndatedLineFollowsItsSource: a stacktrace, or any line the parser
// cannot date, belongs where the line before it did.
func TestMergeUndatedLineFollowsItsSource(t *testing.T) {
	at := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return v
	}

	s, kids := fakeMerge(t.Context(), 2)
	kids[1].feed(Line{Data: []byte("b1"), At: at("2026-08-11T10:00:02Z")})
	kids[0].feed(Line{Data: []byte("a1"), At: at("2026-08-11T10:00:01Z")})
	kids[0].feed(Line{Data: []byte("a1 stacktrace")})
	kids[0].end(nil)
	kids[1].end(nil)

	var got []string
	for l := range s.Lines() {
		got = append(got, string(l.Data))
	}
	require.NoError(t, <-s.Done())
	require.Equal(t, []string{"a1", "a1 stacktrace", "b1"}, got)
}

// TestMergeDoesNotWaitOnASilentSource: one source with nothing to say must not
// hold the view back.
func TestMergeDoesNotWaitOnASilentSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, kids := fakeMerge(t.Context(), 2)

		kids[0].feed(Line{Data: []byte("a1"), At: time.Now()})
		synctest.Wait()
		require.Empty(t, s.Lines(), "the other source may still have something older")

		time.Sleep(mergeLag)
		synctest.Wait()
		require.Len(t, s.Lines(), 1, "past the lag it is read without them")

		// And once it speaks it is waited on again.
		kids[1].feed(Line{Data: []byte("b1"), At: time.Now()})
		kids[0].end(nil)
		kids[1].end(nil)
		for range s.Lines() {
		}
		require.NoError(t, <-s.Done())
	})
}

// TestMergeWaitsOutASilentSource: going quiet is not the same as being
// finished, and a source still open is read however long it took.
func TestMergeWaitsOutASilentSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, kids := fakeMerge(t.Context(), 2)

		kids[0].feed(Line{Data: []byte("a1"), At: time.Now()})
		time.Sleep(2 * mergeLag)
		kids[0].end(nil)
		synctest.Wait()

		// Everything else has ended and this one has been quiet throughout,
		// which is not a reason to stop reading it.
		kids[1].feed(Line{Data: []byte("b1"), At: time.Now()})
		kids[1].end(nil)

		var got []string
		for l := range s.Lines() {
			got = append(got, string(l.Data))
		}
		require.Equal(t, []string{"a1", "b1"}, got)
		require.NoError(t, <-s.Done())
	})
}

// TestMergeSaysNothingOfAPlaceThatDoesNotHaveIt: a group may name a workload
// that only one of its clusters runs, and the rest refusing is not news.
func TestMergeSaysNothingOfAPlaceThatDoesNotHaveIt(t *testing.T) {
	s, kids := fakeMerge(t.Context(), 2)

	kids[0].feed(Line{Data: []byte("serving")})
	kids[1].feed(Line{
		Data:   []byte(`error: error from server (NotFound): deployments.apps "api" not found in namespace "octo"`),
		Stderr: true,
	})
	kids[1].end(errors.New("exit status 1"))
	kids[0].end(nil)

	var got []string
	for l := range s.Lines() {
		got = append(got, string(l.Data))
	}
	require.Equal(t, []string{"serving"}, got, "the place that has it is the whole timeline")
	require.NoError(t, <-s.Done(), "and a place with nothing to give is not a failure")
}

// TestMergeSaysWhyASourceStopped: a place that could not be read is a place
// with something to say, and telescope says it where its lines would have been.
func TestMergeSaysWhyASourceStopped(t *testing.T) {
	s, kids := fakeMerge(t.Context(), 1)

	kids[0].feed(Line{
		Data:   []byte("ssh: connect to host 10.0.0.1 port 22: Connection refused"),
		Stderr: true,
	})
	kids[0].end(errors.New("exit status 255"))

	var got []Line
	for l := range s.Lines() {
		got = append(got, l)
	}
	require.Len(t, got, 2)
	require.Contains(t, string(got[0].Data), "Connection refused")
	require.False(t, got[0].Note, "what the collector wrote is the collector talking")
	require.Contains(t, string(got[1].Data), "telescope: s0: exit status 255")
	require.True(t, got[1].Note, "and what telescope wrote is marked as its own")
	require.ErrorContains(t, <-s.Done(), "exit status 255")
}

// TestMergeHoldsOnlyWhatComesBeforeTheLog: a source that is reading writes its
// log to whichever stream it likes, and holding that back would be holding the
// log back.
func TestMergeHoldsOnlyWhatComesBeforeTheLog(t *testing.T) {
	s, kids := fakeMerge(t.Context(), 1)

	kids[0].feed(Line{Data: []byte("W deprecated flag"), Stderr: true})
	kids[0].feed(Line{Data: []byte("listening")})
	kids[0].end(nil)

	var got []string
	for l := range s.Lines() {
		got = append(got, string(l.Data))
	}
	require.Equal(t, []string{"W deprecated flag", "listening"}, got)
	require.NoError(t, <-s.Done())
}

// TestMergeReleasesASourceThatOnlyWritesStderr: a container logging to stderr
// and nowhere else is not refusing to open, it is running, and past the grace
// it is read like anything else.
func TestMergeReleasesASourceThatOnlyWritesStderr(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, kids := fakeMerge(t.Context(), 1)

		kids[0].feed(Line{Data: []byte("E boom"), Stderr: true, At: time.Now()})
		synctest.Wait()
		require.Empty(t, s.Lines(), "held while it may still be a refusal")

		time.Sleep(openGrace)
		synctest.Wait()
		require.Len(t, s.Lines(), 1, "past the grace it is the log")

		kids[0].end(nil)
		for range s.Lines() {
		}
		require.NoError(t, <-s.Done())
	})
}

// fakeMerge runs the merge over sources fed by hand, which is the only way to
// say when a source goes quiet.
func fakeMerge(ctx context.Context, n int) (*Stream, []*Stream) {
	var (
		items = make(chan mergeItem)
		kids  = make([]*Stream, n)
		acks  = make([]chan struct{}, n)
		open  = make([]bool, n)
	)
	for i := range n {
		kids[i] = &Stream{lines: make(chan Line), done: make(chan error, 1)}
		acks[i] = make(chan struct{}, 1)
		open[i] = true
		go forward(ctx, i, kids[i], "s"+strconv.Itoa(i), items, acks[i])
	}
	s := &Stream{lines: make(chan Line, 64), done: make(chan error, 1)}
	go s.merge(ctx, items, acks, open, nil, nil, options{})
	return s, kids
}

// feed and end drive a fake source: what it says, and that it has finished.
func (s *Stream) feed(l Line) { s.lines <- l }

func (s *Stream) end(err error) {
	s.done <- err
	close(s.lines)
}
