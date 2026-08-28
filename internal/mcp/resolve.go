package mcp

import (
	"slices"
	"strings"
	"time"

	"github.com/go-faster/errors"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

// stream is what a tool's place argument named, whether it named a place or a
// group. Both are read the same way once resolved, and an agent that was told
// the names by places should not have to know which list one came from.
//
// A place that does not open as it stands is still returned: what it is missing
// is a target to read, and a database asked what its fields are needs none.
func stream(cfg config.Config, name string) (source.Config, error) {
	return streamOf(cfg, name, "")
}

// streamOf is the same, given what to read there.
//
// A kubectl place is half a place until it is told which workload: the config
// declares the cluster and the namespace because those are the parts that stay
// still, and the pod is what changes between one question and the next. The
// start screen asks a person for it, and this is where a tool is asked — the
// alternative is that a place declared without a target: line can be listed and
// never read, which is a place an agent can see and cannot use.
//
// What it is not is a way in for a command. The target reaches [source.Config]
// through WithTarget, which puts it in the field its collector reads — a unit,
// a pod, a container — and never into the argv of something to run.
func streamOf(cfg config.Config, name, target string) (source.Config, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return source.Config{}, errors.Errorf("name a place: %s", declared(cfg))
	}
	for _, p := range cfg.Places {
		if p.Name != name {
			continue
		}
		if p.ReadsTraces() {
			// Worded for whoever asked rather than as the place puts it: what
			// a store says about itself points at the command line, and the
			// caller here has tools instead.
			return source.Config{}, errors.Errorf(
				"%q reads traces rather than lines: it is what the places whose logs "+
					"carry trace ids name, and places says which those are", name)
		}
		src, ready, err := p.Stream()
		if err != nil {
			// Unwrapped: the caller named the place a moment ago, and what a
			// place says about itself already names it where the name is not
			// obvious — see the token it could not read.
			return source.Config{}, err
		}
		return targeted(src, ready, name, target)
	}
	for _, g := range cfg.Groups {
		if g.Name != name {
			continue
		}
		src, ready, err := g.Stream()
		if err != nil {
			return source.Config{}, err
		}
		return targeted(src, ready, name, target)
	}
	if place, target, ok := splitTarget(cfg, name); ok {
		return source.Config{}, errors.Errorf(
			"no place named %q. %q is one, so %q is probably what to read there: "+
				"pass it as the target argument rather than in the name",
			name, place, target)
	}
	return source.Config{}, errors.Errorf("no place named %q: %s", name, declared(cfg))
}

// splitTarget reads a name that is a declared place with something after it.
//
// The start screen takes the two run together — a place is picked and the
// target typed after it — so a name arriving that way is somebody carrying that
// habit over rather than a name that is simply wrong. It cannot be split on the
// space and read: a place is named by a person and "humo observer" is an
// ordinary name, so the longest declared prefix is what decides, and only
// naming one is what makes this a guess worth printing.
func splitTarget(cfg config.Config, name string) (place, target string, ok bool) {
	for _, p := range cfg.Places {
		rest, found := strings.CutPrefix(name, p.Name+" ")
		if !found {
			continue
		}
		if rest = strings.TrimSpace(rest); rest != "" && len(p.Name) > len(place) {
			place, target, ok = p.Name, rest, true
		}
	}
	return place, target, ok
}

// targeted applies what to read and says what is still missing.
//
// A place that will not open is refused here rather than passed on, because
// what a collector says when it is run without a target is about its own
// command line — "error: expected 'logs (POD | TYPE/NAME)'" — and the caller
// has a place name and a tool schema, not a kubectl invocation.
func targeted(src source.Config, ready bool, name, target string) (source.Config, error) {
	if target = strings.TrimSpace(target); target != "" {
		src = src.WithTarget(target)
		ready = src.Validate() == nil
	}
	if ready {
		return src, nil
	}
	err := src.Validate()
	if err == nil {
		return src, nil
	}
	if target != "" {
		return source.Config{}, errors.Wrapf(err, "%q with target %q", name, target)
	}
	return source.Config{}, errors.Wrapf(err,
		"%q needs a target: give one as the target argument", name)
}

// declared is what could have been named instead, since a wrong name is most
// often a near miss and the list is short enough to write out.
func declared(cfg config.Config) string {
	var names []string
	for _, p := range cfg.Places {
		names = append(names, p.Name)
	}
	for _, g := range cfg.Groups {
		names = append(names, g.Name)
	}
	if len(names) == 0 {
		return "the config declares none"
	}
	return "the ones declared are " + strings.Join(names, ", ")
}

// withRange overrides the window a place is read over. A merge's children each
// carry their own, so the override has to reach them: the group is one view and
// one timeline, and a child left on the place's own window would answer for a
// different interval than the rest.
func withRange(src source.Config, spec string) (source.Config, error) {
	if strings.TrimSpace(spec) == "" {
		return src, nil
	}
	r, err := source.ParseRange(spec, time.Now())
	if err != nil {
		return source.Config{}, err
	}
	src.Range = r
	src.Merge = slices.Clone(src.Merge)
	for i := range src.Merge {
		src.Merge[i].Range = r
	}
	return src, nil
}
