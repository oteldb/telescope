package mcp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/oteldb/telescope/internal/config"
)

// listingServer answers each listing path with what it is given, and 404s the
// ones it is not, so a test can take one away.
func listingServer(t *testing.T, by map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := by[r.URL.Path]
		if !ok {
			http.Error(w, "no such listing", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func callTraceFields(t *testing.T, cfg config.Config, in traceFieldsInput) (string, traceFieldsOutput, error) {
	t.Helper()
	res, out, err := traceFieldsHandler(cfg)(t.Context(), nil, in)
	if err != nil {
		return "", traceFieldsOutput{}, err
	}
	return res.Content[0].(*sdk.TextContent).Text, out, nil
}

// TestATraceStoreSaysWhatItCanBeSearchedBy: a jaeger store refuses a search
// that names no service, so this is the listing that stands between an agent
// and any answer at all.
func TestATraceStoreSaysWhatItCanBeSearchedBy(t *testing.T) {
	srv := listingServer(t, map[string]string{
		"/api/services":                     `{"data":["checkout","payments","fx"]}`,
		"/api/services/checkout/operations": `{"data":["POST /api/orders","GET /api/orders"]}`,
		"/api/v3/traces/attributes":         `{"attributes":["error","http.status_code"]}`,
	})
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "jaeger", URL: srv.URL},
	}, nil)

	text, out, err := callTraceFields(t, cfg, traceFieldsInput{Service: "checkout"})
	require.NoError(t, err)
	require.Equal(t, []string{"checkout", "fx", "payments"}, out.Services,
		"sorted, since a listing is read rather than ranked")
	require.Equal(t, []string{"GET /api/orders", "POST /api/orders"}, out.Operations)
	require.Contains(t, text, "services (3)")
	require.Contains(t, text, "checkout")
	require.Contains(t, text, "operations of checkout (2)")
}

// TestOperationsAreOnlyListedForAServiceThatWasNamed: there is one list per
// service and no useful union of them.
func TestOperationsAreOnlyListedForAServiceThatWasNamed(t *testing.T) {
	srv := listingServer(t, map[string]string{
		"/api/services": `{"data":["checkout"]}`,
	})
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "jaeger", URL: srv.URL},
	}, nil)

	text, out, err := callTraceFields(t, cfg, traceFieldsInput{})
	require.NoError(t, err)
	require.Empty(t, out.Operations)
	require.NotContains(t, text, "operations")
}

// TestAStoreThatAnswersOneListingIsWorthWhatItAnswered: an agent holding the
// services can search a Jaeger, whatever the tag listing did.
func TestAStoreThatAnswersOneListingIsWorthWhatItAnswered(t *testing.T) {
	srv := listingServer(t, map[string]string{
		"/api/services": `{"data":["checkout"]}`,
	})
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "jaeger", URL: srv.URL},
	}, nil)

	text, out, err := callTraceFields(t, cfg, traceFieldsInput{})
	require.NoError(t, err, "one listing failing is not the tool failing")
	require.Equal(t, []string{"checkout"}, out.Services)
	require.Contains(t, text, "services (1)")
}

// TestAJaegerStoreDoesNotClaimToHaveNoTags: its API has no tag-name endpoint,
// so nothing was asked and nothing came back. An empty list would be the wrong
// answer to a question nobody put — the spans carry tags and a search narrows
// by them.
func TestAJaegerStoreDoesNotClaimToHaveNoTags(t *testing.T) {
	srv := listingServer(t, map[string]string{
		"/api/services": `{"data":["checkout"]}`,
	})
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "jaeger", URL: srv.URL},
	}, nil)

	text, out, err := callTraceFields(t, cfg, traceFieldsInput{})
	require.NoError(t, err)
	require.Contains(t, out.Note, "no listing of tag names")
	require.NotContains(t, text, "tags (0)", "which would read as a store with no tags in it")

	_, values, err := traceValuesHandler(cfg)(t.Context(), nil, traceValuesInput{Tag: "error"})
	require.NoError(t, err)
	require.Empty(t, values.Values)
	require.Contains(t, values.Note, "no listing of tag names")
}

// TestAStoreThatAnswersNothingIsAFailure: a tool that returned three empty
// lists would read as a store with no services in it.
func TestAStoreThatAnswersNothingIsAFailure(t *testing.T) {
	srv := listingServer(t, nil)
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "tempo", URL: srv.URL},
	}, nil)

	_, _, err := callTraceFields(t, cfg, traceFieldsInput{Service: "checkout"})
	require.ErrorContains(t, err, "answered nothing it was asked")
}

// TestTagValuesNeedATag: the store is optional and the tag never is.
func TestTagValuesNeedATag(t *testing.T) {
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "tempo", URL: "https://tempo.example.com"},
	}, nil)

	_, _, err := traceValuesHandler(cfg)(t.Context(), nil, traceValuesInput{})
	require.ErrorContains(t, err, "name a tag")
}

// TestALongListingWrapsRatherThanRunningDown: a hundred names down the left is
// a hundred lines of mostly margin.
func TestALongListingWrapsRatherThanRunningDown(t *testing.T) {
	var names []string
	for i := range 40 {
		names = append(names, fmt.Sprintf(`"service-number-%02d"`, i))
	}
	srv := listingServer(t, map[string]string{
		"/api/services": `{"data":[` + strings.Join(names, ",") + `]}`,
	})
	cfg := testConfig(t, []config.Place{
		{Name: "prod traces", Type: "jaeger", URL: srv.URL},
	}, nil)

	text, out, err := callTraceFields(t, cfg, traceFieldsInput{})
	require.NoError(t, err)
	require.Len(t, out.Services, 40)

	var listed int
	for line := range strings.SplitSeq(text, "\n") {
		if !strings.HasPrefix(line, "  ") {
			continue
		}
		listed++
		require.LessOrEqual(t, len(line), listWrap+len("service-number-00"), line)
	}
	require.Greater(t, listed, 1, "wrapped over several lines rather than one long one")
	require.Less(t, listed, 40, "and not one line each")
}
