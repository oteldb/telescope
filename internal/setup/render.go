package setup

import (
	"bytes"

	"github.com/go-faster/errors"
	"github.com/go-faster/yaml"

	"github.com/oteldb/telescope/internal/config"
	"github.com/oteldb/telescope/internal/source"
)

// schemaLine points an editor at the published schema, so the file init writes
// is one that completes and checks itself from the first edit after.
const schemaLine = "# yaml-language-server: $schema=" + config.SchemaURL + "\n"

// Render writes the offers as the file they will become, and then reads that
// file back through the loader before returning it. What init writes is
// something somebody will start telescope on: a document that does not load is
// worth failing on here, where what is wrong with it can still be said, rather
// than on a start screen that will not open.
func Render(offers []Offer) ([]byte, error) {
	places := &yaml.Node{Kind: yaml.SequenceNode}
	for _, o := range offers {
		node := place(o.Place)
		node.HeadComment = o.Note
		places.Content = append(places.Content, node)
	}
	doc := &yaml.Node{Kind: yaml.MappingNode}
	put(doc, "places", places)

	out := []byte(schemaLine)
	buf := bytes.NewBuffer(out)
	enc := yaml.NewEncoder(buf)
	// Two spaces, which is how every config file in the README is written and
	// what the file will be edited to look like anyway.
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, errors.Wrap(err, "encode config")
	}
	if err := enc.Close(); err != nil {
		return nil, errors.Wrap(err, "encode config")
	}
	out = buf.Bytes()
	if _, err := config.Parse(out); err != nil {
		return nil, errors.Wrap(err, "the config this would write does not load")
	}
	return out, nil
}

// place writes one place, in the order the keys read: what it is called, what
// speaks there, how it is reached, and then what it names.
//
// Only the keys init can produce have a spelling here. The ones it cannot —
// headers above all, which the config marks secret — have none on purpose: a
// renderer that can write a secret is one that eventually does.
func place(p config.Place) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	put(m, "name", str(p.Name))
	put(m, "type", str(p.Type))
	putString(m, "via", p.Via)
	if p.Sudo {
		put(m, "sudo", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
	}
	putString(m, "unit", p.Unit)
	putString(m, "kubeconfig", p.KubeConfig)
	putString(m, "context", p.Context)
	putString(m, "namespace", p.Namespace)
	putString(m, "container", p.Container)
	putString(m, "args", p.Args)
	putString(m, "url", p.URL)
	putString(m, "datasource", p.Datasource)
	if node := token(p.Token); node != nil {
		put(m, "token", node)
	}
	if node := traces(p.Traces); node != nil {
		put(m, "traces", node)
	}
	putString(m, "query", p.Query)
	return m
}

func token(t config.Token) *yaml.Node {
	if t.IsZero() {
		return nil
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	putString(m, "env", t.Env)
	putString(m, "file", t.File)
	if len(t.Exec) > 0 {
		list := &yaml.Node{Kind: yaml.SequenceNode}
		for _, arg := range t.Exec {
			list.Content = append(list.Content, str(arg))
		}
		put(m, "exec", list)
	}
	return m
}

// traces writes the store the short way when there is nothing to say beyond the
// url, which is what a Tempo is.
func traces(t config.TraceStore) *yaml.Node {
	switch {
	case t.IsZero():
		return nil
	case t.Collector() == source.CollectorTempo:
		return str(t.URL)
	default:
		m := &yaml.Node{Kind: yaml.MappingNode}
		put(m, "url", str(t.URL))
		put(m, "type", str(t.Type))
		return m
	}
}

func put(m *yaml.Node, key string, value *yaml.Node) {
	m.Content = append(m.Content, str(key), value)
}

func putString(m *yaml.Node, key, value string) {
	if value != "" {
		put(m, key, str(value))
	}
}

// str tags every scalar as a string, so a container called "no" survives the
// round trip through YAML's own idea of what that word means.
func str(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}
