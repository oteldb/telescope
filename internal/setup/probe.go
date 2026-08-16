package setup

import (
	"context"
	"slices"
	"strings"

	"github.com/oteldb/telescope/internal/complete"
	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

// maxPerKind caps how many of one kind of thing are offered. A desktop runs
// eighty services and a busy host as many containers, and a hundred questions
// is not a setup flow. What the cap leaves out is what the prompt completes
// anyway, so nothing becomes unreachable by being left out of the file.
const maxPerKind = 20

// probe asks the machine what it is running. Every listing goes through the
// same commands the prompt completes with, so a tool that is not installed
// answers with an error here exactly as it does there, and is passed over.
func (o Options) probe(ctx context.Context) []Offer {
	var out []Offer
	out = append(out, o.containers(ctx)...)
	out = append(out, o.units(ctx)...)
	out = append(out, o.clusters(ctx)...)
	out = append(out, o.sshHosts()...)
	return out
}

func (o Options) containers(ctx context.Context) []Offer {
	items, err := o.Fetch(ctx, complete.Request{
		Field:     complete.FieldTarget,
		Collector: source.CollectorDocker,
	})
	if err != nil {
		return nil
	}
	var out []Offer
	for _, c := range running(items) {
		note := "container"
		if c.Detail != "" {
			note += " running " + c.Detail
		}
		out = append(out, Offer{
			Place: config.Place{
				Name:      c.Value,
				Type:      string(source.CollectorDocker),
				Container: c.Value,
			},
			Note: note,
		})
	}
	return trim(out)
}

func (o Options) units(ctx context.Context) []Offer {
	items, err := o.Fetch(ctx, complete.Request{
		Field:     complete.FieldTarget,
		Collector: source.CollectorJournal,
	})
	if err != nil {
		return nil
	}
	var out []Offer
	for _, c := range running(items) {
		note := "systemd unit"
		if c.Detail == "user" {
			note = "user unit"
		}
		out = append(out, Offer{
			Place: config.Place{
				Name: strings.TrimPrefix(c.Value, source.UserUnitPrefix),
				Type: string(source.CollectorJournal),
				Unit: c.Value,
			},
			Note: note,
		})
	}
	return trim(out)
}

// clusters offers one place per context of every readable kubeconfig, rather
// than one per kubeconfig: a context is the cluster, and a file naming three of
// them is three places.
func (o Options) clusters(ctx context.Context) []Offer {
	files, err := o.Fetch(ctx, complete.Request{Field: complete.FieldKubeConfig})
	if err != nil {
		return nil
	}
	var out []Offer
	for _, f := range files {
		contexts, err := o.Fetch(ctx, complete.Request{
			Field:      complete.FieldKubeContext,
			KubeConfig: f.Value,
		})
		if err != nil {
			continue
		}
		for _, c := range contexts {
			out = append(out, Offer{
				Place: config.Place{
					Name:       c.Value,
					Type:       string(source.CollectorKubectl),
					KubeConfig: f.Value,
					Context:    c.Value,
				},
				Note:       "kubernetes context in " + f.Value,
				Namespaces: o.clusterNamespaces(ctx, f.Value, c.Value),
			})
		}
	}
	return trim(out)
}

// clusterNamespaces asks the cluster what it holds. A context that cannot be
// reached only reveals itself by hanging, so this is bounded by the same
// timeout the prompt lists against and answers with nothing when it runs out.
func (o Options) clusterNamespaces(ctx context.Context, kubeconfig, kubeContext string) []string {
	items, err := o.Fetch(ctx, complete.Request{
		Field:       complete.FieldTarget,
		Collector:   source.CollectorKubectl,
		KubeConfig:  kubeconfig,
		KubeContext: kubeContext,
	})
	if err != nil {
		return nil
	}
	return namespaces(items)
}

// sshHosts offers each host the ssh config names as a journal read over ssh,
// which is the reading a host with no other clue about it supports. It names no
// unit: picking one opens the prompt with the host already dialed.
func (o Options) sshHosts() []Offer {
	var out []Offer
	for _, h := range o.Hosts() {
		out = append(out, Offer{
			Place: config.Place{
				Name: h.Value,
				Type: string(source.CollectorJournal),
				Via:  "ssh://" + h.Value,
			},
			Note: "ssh host",
		})
	}
	return trim(out)
}

// running keeps what is running now. A unit that is dead and a container that
// exited have logs, but they are not what somebody setting up a viewer is
// looking at, and offering them is what turns a short list into a long one.
func running(items []complete.Candidate) []complete.Candidate {
	var out []complete.Candidate
	for _, c := range items {
		if c.State == "running" {
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(a, b complete.Candidate) int {
		return strings.Compare(a.Value, b.Value)
	})
	return out
}

func trim(offers []Offer) []Offer {
	if len(offers) > maxPerKind {
		return offers[:maxPerKind]
	}
	return offers
}
