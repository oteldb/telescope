package view

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestALinkSurvivesAShell: a link is pasted into a terminal, so what a shell
// would eat has to come out the other side as the one argument it went in as.
func TestALinkSurvivesAShell(t *testing.T) {
	for _, tt := range []struct {
		name string
		view View
		want string
	}{
		{
			name: "a plain place needs no quoting at all",
			view: View{Place: "prod"},
			want: "telescope prod",
		},
		{
			name: "a name with a space is one argument",
			view: View{Place: "humo observer"},
			want: "telescope 'humo observer'",
		},
		{
			name: "a filter carries quotes and globs",
			view: View{Place: "prod", Query: `"connection refused" pod=api-*`},
			want: `telescope prod --query '"connection refused" pod=api-*'`,
		},
		{
			name: "a single quote is spliced, since that is the one thing quoting cannot hold",
			view: View{Place: "prod", Query: `it's here`},
			want: `telescope prod --query 'it'\''s here'`,
		},
		{
			name: "a range is plain enough to go bare",
			view: View{Place: "prod", Range: "6h..1h"},
			want: "telescope prod --range 6h..1h",
		},
		{
			name: "a trace names its store and its id",
			view: View{Kind: KindTrace, Place: "prod traces", Trace: "4bf92f35"},
			want: `telescope trace --from 'prod traces' 4bf92f35`,
		},
		{
			name: "a search names the store and stops",
			view: View{Kind: KindSearch, Place: "prod traces"},
			want: `telescope trace --from 'prod traces'`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.view.Link())
		})
	}
}

// TestAShellSplitsALinkBackIntoWhatItWas: the quoting is only worth anything if
// what comes out is what went in, so it is checked by splitting rather than by
// eye.
func TestAShellSplitsALinkBackIntoWhatItWas(t *testing.T) {
	v := View{
		Place: `humo observer`,
		Query: `"connection refused" pod=api-* && it's $HOME`,
		Range: "2026-01-02 10:00..2026-01-02 12:00",
	}

	argv, err := splitShell(v.Link())
	require.NoError(t, err)
	require.Equal(t, []string{
		Program, v.Place, "--" + FlagQuery, v.Query, "--" + FlagRange, v.Range,
	}, argv)
}
