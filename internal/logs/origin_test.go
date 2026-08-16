package logs

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

// labeled is a line the source reported labels beside, which is how a database
// and a prefixing collector both report what wrote it.
func labeled(text string, kv ...string) source.Line {
	l := source.Line{Data: []byte(text)}
	for i := 0; i+1 < len(kv); i += 2 {
		l.Labels = append(l.Labels, source.Label{Key: kv[i], Value: kv[i+1]})
	}
	return l
}

// TestOneStreamHasNoOrigin: a view reading one thing has nothing to tell apart,
// and a column saying so on every row would be width taken from the log.
func TestOneStreamHasNoOrigin(t *testing.T) {
	s := indexed(t,
		labeled("started", "k8s.pod.name", "api-0", "k8s.container.name", "api"),
		labeled("serving", "k8s.pod.name", "api-0", "k8s.container.name", "api"),
	)
	require.False(t, s.Origins().Several())
}

// TestTheOriginIsWhateverDiffers: the containers of one pod are told apart by
// the container, and the pods of one deployment by the pod, without either
// being named anywhere.
func TestTheOriginIsWhateverDiffers(t *testing.T) {
	containers := indexed(t,
		labeled("a", "k8s.pod.name", "api-0", "k8s.container.name", "api"),
		labeled("b", "k8s.pod.name", "api-0", "k8s.container.name", "proxy"),
	)
	o := containers.Origins()
	require.True(t, o.Several())
	label, _ := o.Of(containers.Entries()[1])
	require.Equal(t, "proxy", label)

	pods := indexed(t,
		labeled("a", "k8s.pod.name", "api-6b8d4f-2xk9w", "k8s.container.name", "api"),
		labeled("b", "k8s.pod.name", "api-6b8d4f-lq7pv", "k8s.container.name", "api"),
	)
	o = pods.Origins()
	require.True(t, o.Several())
	label, _ = o.Of(pods.Entries()[0])
	require.Equal(t, "2xk9w", label, "what every pod shares says nothing about which pod")
}

// TestTheOriginIsTheServiceWhereThatIsWhatVaries: several services in one
// namespace are one query and several streams.
func TestTheOriginIsTheServiceWhereThatIsWhatVaries(t *testing.T) {
	s := indexed(t,
		labeled("a", "namespace", "apps", "service.name", "checkout"),
		labeled("b", "namespace", "apps", "service.name", "billing"),
	)
	o := s.Origins()
	require.True(t, o.Several())
	label, id := o.Of(s.Entries()[0])
	require.Equal(t, "checkout", label)
	require.Equal(t, "checkout", id)
}

// TestTheNarrowestNamesWin: where the streams differ in more than one way, the
// column is the two that say most about the line, not all of them.
func TestTheNarrowestNamesWin(t *testing.T) {
	s := indexed(t,
		labeled("a", "namespace", "one", "k8s.pod.name", "api-a", "k8s.container.name", "api"),
		labeled("b", "namespace", "two", "k8s.pod.name", "api-b", "k8s.container.name", "proxy"),
	)
	o := s.Origins()
	label, id := o.Of(s.Entries()[1])
	require.Equal(t, "b/proxy", label)
	require.Equal(t, "api-b/proxy", id, "the color is hung off the whole name, which does not shrink")
}

// TestALineCallingSomethingHostIsNotAStream: an access log names the caller,
// and what called in is not what wrote the line.
func TestALineCallingSomethingHostIsNotAStream(t *testing.T) {
	s := indexed(t,
		line(`{"msg":"GET /","host":"10.0.0.1","app":"api"}`),
		line(`{"msg":"GET /","host":"10.0.0.2","app":"api"}`),
	)
	require.False(t, s.Origins().Several())

	reported := indexed(t,
		labeled("GET /", "host", "node-a"),
		labeled("GET /", "host", "node-b"),
	)
	require.True(t, reported.Origins().Several(),
		"the same name is the stream when the source is the one saying it")
}

// TestTheServiceInTheLineIsAStream: a shipper that writes service.name into the
// record rather than beside it is describing what wrote the line either way.
func TestTheServiceInTheLineIsAStream(t *testing.T) {
	s := indexed(t,
		line(`{"msg":"a","service.name":"checkout"}`),
		line(`{"msg":"b","service.name":"billing"}`),
	)
	require.True(t, s.Origins().Several())
}

// TestTheOriginFallsBackToThePlace: a merge of two places whose lines carry no
// labels at all is still two streams, and telescope's own name for them is what
// is left to say so.
func TestTheOriginFallsBackToThePlace(t *testing.T) {
	s := indexed(t,
		source.Line{Data: []byte("a"), Source: "eu"},
		source.Line{Data: []byte("b"), Source: "us"},
	)
	o := s.Origins()
	require.True(t, o.Several())
	label, _ := o.Of(s.Entries()[1])
	require.Equal(t, "us", label)
}

// TestAnEntryWithNoOriginIsBlank: a note telescope wrote itself belongs to no
// stream, and inventing one for it would be a lie in a column.
func TestAnEntryWithNoOriginIsBlank(t *testing.T) {
	s := indexed(t,
		labeled("a", "k8s.pod.name", "api-a"),
		labeled("b", "k8s.pod.name", "api-b"),
	)
	require.NotNil(t, s.Append(source.Line{Kind: source.KindRestarted}))
	label, id := s.Origins().Of(s.Entries()[2])
	require.Empty(t, label)
	require.Empty(t, id)
}

func TestSharedPrefix(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values []string
		want   int
	}{
		{"nothing in common", []string{"api", "worker"}, 0},
		{"cut at the break", []string{"api-6b8d-2xk9", "api-6b8d-lq7p"}, 9},
		{"no break to cut at", []string{"apia", "apib"}, 0},
		{"one is the other's prefix", []string{"api-", "api-b"}, 0},
		{"a single value shares nothing", []string{"api-a"}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, sharedPrefix(tt.values))
		})
	}
}
