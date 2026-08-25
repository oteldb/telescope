package mcp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

// vlogs answers the two listings a VictoriaLogs place is asked for, recording
// what it was asked.
func vlogs(t *testing.T, values []string) (endpoint string, asked *url.Values) {
	t.Helper()
	var got url.Values
	entries := make([]string, 0, len(values))
	for _, v := range values {
		entries = append(entries, `{"value":`+strconv.Quote(v)+`}`)
	}
	body := `{"values":[` + strings.Join(entries, ",") + `]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &got
}

// TestFieldsAsksTheDatabase: a place that indexes its lines is the only one
// that can say what a filter may be written against before anything is read.
func TestFieldsAsksTheDatabase(t *testing.T) {
	srv, asked := vlogs(t, []string{"_msg", "pod"})
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv, Target: "app:api"},
	}, nil)

	_, out, err := fieldsHandler(cfg)(t.Context(), nil, fieldsInput{Place: "prod"})
	require.NoError(t, err)
	require.Equal(t, []string{"msg", "pod"}, out.Fields)
	require.Empty(t, out.Note)
	require.Equal(t, "app:api", asked.Get("query"))
}

// TestFieldsOverAWindow: the window is the caller's, since an agent asking what
// a field said during an incident is not asking what it says now.
func TestFieldsOverAWindow(t *testing.T) {
	srv, asked := vlogs(t, nil)
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
	}, nil)

	_, _, err := fieldsHandler(cfg)(t.Context(), nil, fieldsInput{Place: "prod", Range: "6h..1h"})
	require.NoError(t, err)
	require.NotEmpty(t, asked.Get("start"))
	require.NotEmpty(t, asked.Get("end"))
}

// TestFieldsSaysWhenNobodyCanAnswer: an empty list reads as a place with no
// fields, which is not what a journal is.
func TestFieldsSaysWhenNobodyCanAnswer(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "node", Type: "journalctl", Unit: "nginx"},
	}, nil)

	_, out, err := fieldsHandler(cfg)(t.Context(), nil, fieldsInput{Place: "node"})
	require.NoError(t, err)
	require.Empty(t, out.Fields)
	require.Contains(t, out.Note, "node writes lines rather than indexing them")
}

// TestFieldsSaysWhichHalfOfAGroupAnswered: a merge of a database and a
// container answers for half of itself, and the missing half is the half whose
// fields are not in the reply.
func TestFieldsSaysWhichHalfOfAGroupAnswered(t *testing.T) {
	srv, _ := vlogs(t, []string{"pod"})
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
		{Name: "local", Type: "docker", Container: "api"},
	}, []config.Group{
		{Name: "both", Places: []string{"prod", "local"}},
	})

	_, out, err := fieldsHandler(cfg)(t.Context(), nil, fieldsInput{Place: "both"})
	require.NoError(t, err)
	require.Equal(t, []string{"pod"}, out.Fields)
	require.Equal(t, "only prod was asked: local writes lines rather than indexing them, "+
		"so what one is labeled with is only known once it has been read", out.Note)
}

// TestFieldValuesReportsTheCut: a store asked for a limit and answering with
// exactly that many has almost certainly more, and a reader that cannot scroll
// has no other way to tell.
func TestFieldValuesReportsTheCut(t *testing.T) {
	values := make([]string, source.FieldValuesLimit)
	for i := range values {
		values[i] = "pod-" + strconv.Itoa(i)
	}
	srv, asked := vlogs(t, values)
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
	}, nil)

	_, out, err := fieldValuesOf(t, cfg, "prod", "pod")
	require.NoError(t, err)
	require.Len(t, out.Values, source.FieldValuesLimit)
	require.Contains(t, out.Note, "so there are more")
	require.Equal(t, "pod", asked.Get("field"))
}

func TestFieldValuesNeedsAField(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
	}, nil)

	_, _, err := fieldValuesOf(t, cfg, "prod", " ")
	require.ErrorIs(t, err, errNoField)
}

func fieldValuesOf(t *testing.T, cfg config.Config, place, field string) (any, valuesOutput, error) {
	t.Helper()
	return valuesHandler(cfg)(t.Context(), nil, valuesInput{Place: place, Field: field})
}

// TestUnknownPlaceNamesTheOnesThatExist: a wrong name is most often a near
// miss, and the list is short enough to write out.
func TestUnknownPlaceNamesTheOnesThatExist(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
	}, []config.Group{})

	_, _, err := fieldsHandler(cfg)(t.Context(), nil, fieldsInput{Place: "prd"})
	require.Error(t, err)
	require.Contains(t, err.Error(), `no place named "prd"`)
	require.Contains(t, err.Error(), "the ones declared are prod")
}

func TestUnknownRangeIsReported(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: "https://logs.example.com"},
	}, nil)

	_, _, err := fieldsHandler(cfg)(t.Context(), nil, fieldsInput{Place: "prod", Range: "yesterdayish"})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "yesterdayish"), err.Error())
}

// TestFieldsOverAnAbsoluteWindow: an agent looking at an incident has two
// instants and not a duration, and the tools say they take them.
func TestFieldsOverAnAbsoluteWindow(t *testing.T) {
	srv, asked := vlogs(t, nil)
	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv},
	}, nil)

	_, _, err := fieldsHandler(cfg)(t.Context(), nil,
		fieldsInput{Place: "prod", Range: "2026-08-17T12:00:00Z..2026-08-17T12:05:00Z"})
	require.NoError(t, err)
	require.Equal(t, "2026-08-17T12:00:00Z", asked.Get("start"))
	require.Equal(t, "2026-08-17T12:05:00Z", asked.Get("end"))
}

// TestFieldListingsAreNotTheirOwnStructuredContentTwice: the same bargain
// places makes — the text is a reading of the answer, not the answer's JSON.
func TestFieldListingsAreNotTheirOwnStructuredContentTwice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "field_names"):
			_, _ = w.Write([]byte(`{"values":[{"value":"pod"},{"value":"namespace"}]}`))
		default:
			_, _ = w.Write([]byte(`{"values":[{"value":"api-1"},{"value":"api-2"}]}`))
		}
	}))
	t.Cleanup(srv.Close)

	cfg := testConfig(t, []config.Place{
		{Name: "prod", Type: "victorialogs", URL: srv.URL},
	}, nil)

	res, out, err := fieldsHandler(cfg)(t.Context(), nil, fieldsInput{Place: "prod"})
	require.NoError(t, err)
	require.NotEmpty(t, out.Fields)
	text := res.Content[0].(*sdk.TextContent).Text
	require.NotContains(t, text, `{"fields"`)
	require.Contains(t, text, "fields of prod (2):")
	require.Contains(t, text, "pod")

	res, values, err := valuesHandler(cfg)(t.Context(), nil, valuesInput{Place: "prod", Field: "pod"})
	require.NoError(t, err)
	require.NotEmpty(t, values.Values)
	text = res.Content[0].(*sdk.TextContent).Text
	require.NotContains(t, text, `{"values"`)
	require.Contains(t, text, "pod at prod (2):")
	require.Contains(t, text, "api-1  api-2")
}

// TestAPlaceThatIndexesNothingStillSaysWhy: a collector answers no listing, and
// an empty list on its own reads as a database with no fields in it.
func TestAPlaceThatIndexesNothingStillSaysWhy(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "node", Type: "journalctl", Unit: "nginx"},
	}, nil)

	res, out, err := fieldsHandler(cfg)(t.Context(), nil, fieldsInput{Place: "node"})
	require.NoError(t, err)
	require.Empty(t, out.Fields)

	text := res.Content[0].(*sdk.TextContent).Text
	require.Contains(t, text, "fields of node (0): none")
	require.Contains(t, text, "note: node writes lines rather than indexing them")
}
