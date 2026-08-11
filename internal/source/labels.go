package source

import (
	"strings"
)

// Label is one attribute of a line, or of the stream a line came from.
//
// A log database answers with far more than the message: which pod wrote it,
// which unit, which severity it was detected as. That belongs beside the line
// rather than lost with the rest of the response.
type Label struct {
	Key   string
	Value string
}

// SourceLabels describes where the lines of a stream come from: what every one
// of them shares, as opposed to [Line.Labels], which each line brings itself.
//
// For a merge it describes one merged source, named by from — the label its
// lines are tagged with — since a merge has no single origin of its own.
func (c Config) SourceLabels(from string) []Label {
	if c.Collector == CollectorMerge {
		children, labels := c.Children(), c.Labels()
		for i, l := range labels {
			if l == from {
				return children[i].SourceLabels("")
			}
		}
		return nil
	}

	var out []Label
	add := func(key, value string) {
		if v := strings.TrimSpace(value); v != "" {
			out = append(out, Label{Key: key, Value: v})
		}
	}
	add("collector", string(c.Collector))
	if c.Transport == TransportSSH {
		add("host", c.Host)
	}
	switch c.Collector {
	case CollectorVictoriaLogs, CollectorLoki:
		add("endpoint", c.Endpoint.Label())
		add("url", c.Endpoint.URL)
		add("tenant", c.Endpoint.Tenant)
		add("query", c.Target)
	case CollectorJournal:
		add("unit", c.Unit)
		if c.UserUnit {
			add("journal", "user")
		}
	case CollectorKubectl:
		add("namespace", c.Namespace)
		add("target", c.Target)
		add("container", c.Container)
		add("kubeconfig", c.KubeConfig)
		add("context", c.KubeContext)
	case CollectorDocker:
		add("container", c.Container)
	case CollectorCommand:
		add("command", c.Args)
	}
	return out
}
