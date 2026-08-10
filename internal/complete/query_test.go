package complete

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/source"
)

func TestParseQuery(t *testing.T) {
	for _, tt := range []struct {
		name  string
		query string
		terms []Term
		text  string
	}{
		{"plain text is untouched", "api oteldb", nil, "api oteldb"},
		{"one term", "ns:oteldb", []Term{{Field: "ns", Value: "oteldb"}}, ""},
		{"term and text", "ns:oteldb api", []Term{{Field: "ns", Value: "oteldb"}}, "api"},
		{"text before term", "api ns:oteldb", []Term{{Field: "ns", Value: "oteldb"}}, "api"},
		{"aliases", "namespace:oteldb status:running", []Term{
			{Field: "ns", Value: "oteldb"}, {Field: "state", Value: "running"},
		}, ""},
		{"negated", "-ns:kube-system", []Term{{Field: "ns", Value: "kube-system", Negate: true}}, ""},
		{"bare term asks for the field", "container:", []Term{{Field: "container"}}, ""},
		// A colon in a value is the container syntax, not a term.
		{"unknown field stays text", "oteldb/api:migrate", nil, "oteldb/api:migrate"},
		{"leading dash is not a term", "-oteldb", nil, "-oteldb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			terms, text := ParseQuery(tt.query)
			require.Equal(t, tt.terms, terms)
			require.Equal(t, tt.text, text)
		})
	}
}

// TestRankTerms: a term narrows the set before anything is ranked, and what is
// left of the query still matches fuzzily.
func TestRankTerms(t *testing.T) {
	pods := []Candidate{
		{Value: "oteldb/api-79c", State: "Running"},
		{Value: "oteldb/deployment/api", Detail: "deployment · 1/1 ready"},
		{Value: "oteldb/oteldb-ingest-0:ingest", State: "Running"},
		{Value: "kube-system/coredns-7d7", State: "Running"},
		{Value: "kube-system/kube-apiserver-1", State: "Pending"},
	}
	attr := AttrFor(source.CollectorKubectl)

	for _, tt := range []struct {
		name  string
		query string
		want  []string
	}{
		{"namespace", "ns:oteldb", []string{
			"oteldb/api-79c", "oteldb/deployment/api", "oteldb/oteldb-ingest-0:ingest",
		}},
		{"namespace and text", "ns:oteldb api", []string{"oteldb/api-79c", "oteldb/deployment/api"}},
		{"negated namespace", "-ns:kube-system api", []string{"oteldb/api-79c", "oteldb/deployment/api"}},
		{"kind", "kind:deployment", []string{"oteldb/deployment/api"}},
		// Everything the listing offers that is not a workload is a pod.
		{"pods are a kind too", "kind:pod api", []string{"oteldb/api-79c", "kube-system/kube-apiserver-1"}},
		{"state", "state:pending", []string{"kube-system/kube-apiserver-1"}},
		{"a bare term asks for the field", "container:", []string{"oteldb/oteldb-ingest-0:ingest"}},
		{"terms combine", "ns:oteldb -kind:deployment", []string{"oteldb/api-79c", "oteldb/oteldb-ingest-0:ingest"}},
		{"no candidate matches", "ns:nope", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, values(Rank(pods, tt.query, attr)))
		})
	}
}

// TestLogsQLIsNotFiltered: a prompt holding a query language of its own has no
// attributes to filter by, so "level:error" narrows nothing locally and is sent
// as written.
func TestLogsQLIsNotFiltered(t *testing.T) {
	require.Nil(t, AttrFor(source.CollectorVictoriaLogs))

	recent := []Candidate{{Value: "level:error"}, {Value: "app:api"}}
	require.Equal(t, []string{"level:error"},
		values(Rank(recent, "level:error", AttrFor(source.CollectorVictoriaLogs))))
}

func TestAttrFor(t *testing.T) {
	for _, tt := range []struct {
		collector source.Collector
		candidate Candidate
		field     string
		want      string
	}{
		{source.CollectorKubectl, Candidate{Value: "oteldb/api-79c"}, "ns", "oteldb"},
		{source.CollectorKubectl, Candidate{Value: "oteldb/api-79c"}, "kind", "pod"},
		{source.CollectorKubectl, Candidate{Value: "oteldb/api-79c"}, "name", "api-79c"},
		{source.CollectorKubectl, Candidate{Value: "api-79c"}, "ns", ""},
		{source.CollectorKubectl, Candidate{Value: "oteldb/deploy/api"}, "kind", "deploy"},
		{source.CollectorKubectl, Candidate{Value: "oteldb/deploy/api"}, "name", "api"},
		{source.CollectorKubectl, Candidate{Value: "oteldb/pod-0:sidecar"}, "container", "sidecar"},
		{source.CollectorKubectl, Candidate{Value: "p", State: "Running"}, "state", "Running"},
		{source.CollectorJournal, Candidate{Value: "user/syncthing"}, "scope", "user"},
		{source.CollectorJournal, Candidate{Value: "sshd"}, "scope", "system"},
		{source.CollectorJournal, Candidate{Value: "user/syncthing"}, "name", "syncthing"},
		{source.CollectorDocker, Candidate{Value: "app", Detail: "app:latest"}, "image", "app:latest"},
		{source.CollectorDocker, Candidate{Value: "app"}, "ns", ""},
	} {
		t.Run(string(tt.collector)+" "+tt.field+" of "+tt.candidate.Value, func(t *testing.T) {
			require.Equal(t, tt.want, AttrFor(tt.collector)(tt.candidate, tt.field))
		})
	}
}

// TestQueryTextIsWhatGetsSubmitted: pressing enter without picking a suggestion
// must not send "ns:oteldb api" to kubectl.
func TestQueryTextIsWhatGetsSubmitted(t *testing.T) {
	require.Equal(t, "oteldb/api", QueryText("ns:oteldb oteldb/api"))
	require.Equal(t, "oteldb/api", QueryText("  oteldb/api  "))
	require.Empty(t, QueryText("ns:oteldb"))
}

// TestTarget: a namespace typed as a filter is still the namespace to read
// from, so "ns:oteldb deploy/api" must not run against "default".
func TestTarget(t *testing.T) {
	for _, tt := range []struct {
		name      string
		collector source.Collector
		query     string
		want      string
	}{
		{"no terms", source.CollectorKubectl, "oteldb/api", "oteldb/api"},
		{"namespace", source.CollectorKubectl, "ns:oteldb deploy/api", "oteldb/deploy/api"},
		{"namespace of a pod", source.CollectorKubectl, "ns:oteldb api-79c", "oteldb/api-79c"},
		{"text wins", source.CollectorKubectl, "ns:kube-system oteldb/api-79c", "oteldb/api-79c"},
		{"kind", source.CollectorKubectl, "ns:oteldb kind:deploy api", "oteldb/deploy/api"},
		{"kind already spelled out", source.CollectorKubectl, "kind:deploy sts/api", "sts/api"},
		{"container", source.CollectorKubectl, "ns:oteldb c:ingest oteldb-0", "oteldb/oteldb-0:ingest"},
		{"negated terms name nothing", source.CollectorKubectl, "-ns:kube-system api", "api"},
		{"bare terms name nothing", source.CollectorKubectl, "ns: api", "api"},
		{"nothing typed", source.CollectorKubectl, "ns:oteldb", ""},
		{"user scope", source.CollectorJournal, "scope:user syncthing", "user/syncthing"},
		{"scope already spelled out", source.CollectorJournal, "scope:user user/syncthing", "user/syncthing"},
		{"system scope is the default", source.CollectorJournal, "scope:system sshd", "sshd"},
		{"docker has nothing to fold in", source.CollectorDocker, "image:app api", "api"},
		// LogsQL is itself written as "field:value", so none of it is ours.
		{"logsql is untouched", source.CollectorVictoriaLogs, "ns:oteldb error", "ns:oteldb error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Target(tt.query, tt.collector))
		})
	}
}
