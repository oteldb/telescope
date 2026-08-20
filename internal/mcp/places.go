package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

// The signals a place can be asked for. Metrics will be a third once telescope
// shows them: a tool that answers what no view can draw would make this the
// front door and the viewer the side one.
const (
	signalLogs   = "logs"
	signalTraces = "traces"
)

const placesDescription = "Lists where telescope can read from: the places and " +
	"groups the config declares, what speaks at each, which signals it holds and " +
	"whether it opens as it stands. A place that reads traces is one of these too, " +
	"and the places whose lines carry ids into it name it. Every other tool names " +
	"one of these by name; none of them takes a command line, so a place that is " +
	"not here cannot be read."

type placesInput struct{}

type placesOutput struct {
	Places []placeInfo `json:"places"`
	Groups []groupInfo `json:"groups"`
}

type placeInfo struct {
	Name string `json:"name"`
	Type string `json:"type" jsonschema:"What speaks there: journalctl, kubectl, docker, command, victorialogs or loki"`
	// Reads is a list rather than a pair of booleans so that a signal telescope
	// learns to read later is a new member and not a new key.
	Reads  []string `json:"reads" jsonschema:"The signals this place holds: logs, traces"`
	Via    string   `json:"via,omitempty" jsonschema:"How the collector is reached when it is not this machine, as ssh://host"`
	URL    string   `json:"url,omitempty" jsonschema:"The database queried, for a place read over HTTP"`
	Traces *store   `json:"traces,omitempty" jsonschema:"Where this place's traces are read from, and what answers there"`
	Target string   `json:"target,omitempty" jsonschema:"What it reads there: a unit, a pod, a container, or a query"`
	Query  string   `json:"query,omitempty" jsonschema:"The filter the place is read through unless another is given"`
	Range  string   `json:"range,omitempty" jsonschema:"The window the place is read over unless another is given"`
	Ready  bool     `json:"ready" jsonschema:"Whether it opens as it stands"`
	Needs  string   `json:"needs,omitempty" jsonschema:"What it must be given first, when it does not open as it stands"`
	Error  string   `json:"error,omitempty" jsonschema:"Why it cannot be opened at all, such as a token that could not be read"`
}

// store is a database a place reads from that is not the one its logs come
// from. It carries what speaks there as well as where it is: a URL says
// nothing about whether Tempo or Jaeger answers at it, and the two answer the
// same question differently.
type store struct {
	// Place is the store this place named, when it named one rather than
	// writing an address out: it is a place of its own, and asking about it by
	// that name is how the rest of it is found.
	Place string `json:"place,omitempty" jsonschema:"The place that reads these traces, when the store is one"`
	URL   string `json:"url"`
	Type  string `json:"type" jsonschema:"What answers there: tempo or jaeger"`
}

type groupInfo struct {
	Name   string   `json:"name"`
	Places []string `json:"places" jsonschema:"The places read as one timeline"`
	Query  string   `json:"query,omitempty"`
	Range  string   `json:"range,omitempty"`
	Ready  bool     `json:"ready"`
	Needs  string   `json:"needs,omitempty"`
	Error  string   `json:"error,omitempty"`
}

func addPlaces(s *sdk.Server, cfg config.Config) {
	addTool(s, "places", placesDescription, placesHandler(cfg))
}

func placesHandler(cfg config.Config) sdk.ToolHandlerFor[placesInput, placesOutput] {
	return func(context.Context, *sdk.CallToolRequest, placesInput) (*sdk.CallToolResult, placesOutput, error) {
		out := placesOutput{
			Places: make([]placeInfo, 0, len(cfg.Places)),
			Groups: make([]groupInfo, 0, len(cfg.Groups)),
		}
		for _, p := range cfg.Places {
			out.Places = append(out.Places, describePlace(p))
		}
		for _, g := range cfg.Groups {
			out.Groups = append(out.Groups, describeGroup(g))
		}
		return nil, out, nil
	}
}

func describePlace(p config.Place) placeInfo {
	if p.ReadsTraces() {
		return describeStore(p)
	}
	info := placeInfo{
		Name:  p.Name,
		Type:  p.Type,
		Reads: []string{signalLogs},
		Query: p.Query,
		Range: p.Range,
	}
	if traces, ok, err := p.TraceEndpoint(); ok && err == nil {
		info.Reads = append(info.Reads, signalTraces)
		info.Traces = &store{
			Place: p.Traces.Name,
			URL:   traces.URL,
			Type:  string(traces.Collector),
		}
	}

	// The stream is where a place says what it is in the terms everything below
	// config uses, and it carries the endpoint already resolved: asking the
	// place again would read its token a second time.
	src, ready, err := p.Stream()
	info.Ready = ready
	info.Target = config.Target(src)
	info.URL = src.Endpoint.URL
	if src.Transport == source.TransportSSH {
		info.Via = "ssh://" + src.Host
	}
	switch {
	case err != nil:
		info.Error = err.Error()
	case !ready:
		if err := src.Validate(); err != nil {
			info.Needs = err.Error()
		}
	}
	return info
}

// describeStore is a place that reads traces rather than lines.
//
// It is in the same list as the rest: a store is a place, with a name the
// others use to point at it, and one list keeps the pointing legible. What it
// does not have is a stream — [config.Place.Stream] refuses one, and reporting
// that refusal as the place's error would read as a place that is broken
// rather than one that holds another signal.
func describeStore(p config.Place) placeInfo {
	info := placeInfo{Name: p.Name, Type: p.Type, Reads: []string{signalTraces}}
	at, _, err := p.TraceEndpoint()
	info.URL = at.URL
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.Ready = true
	return info
}

func describeGroup(g config.Group) groupInfo {
	info := groupInfo{
		Name:   g.Name,
		Places: g.Places,
		Query:  g.Query,
		Range:  g.Range,
	}
	src, ready, err := g.Stream()
	info.Ready = ready
	switch {
	case err != nil:
		info.Error = err.Error()
	case !ready:
		if asks, ok := g.Asks(); ok {
			info.Needs = "a target for its " + string(asks) + " places"
		} else if err := src.Validate(); err != nil {
			info.Needs = err.Error()
		}
	}
	return info
}
