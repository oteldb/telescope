package config

import (
	"github.com/go-faster/errors"
	"github.com/go-faster/figureout/schema/jsonschema"
	fyaml "github.com/go-faster/figureout/source/yaml"
)

// SchemaURL is where the generated schema is published, and what a config file
// points at to have an editor read it.
const SchemaURL = "https://raw.githubusercontent.com/oteldb/telescope/main/config.schema.json"

// Schema is the JSON Schema of the config file, for an editor to complete and
// check one against.
//
// It is generated for the YAML source rather than for the semantic values,
// since what it describes is a file: a key written as either a scalar or a
// mapping is two shapes there and one value here, and an editor is reading the
// file.
func Schema() ([]byte, error) {
	data, _, err := jsonschema.Generate(Descriptor,
		jsonschema.ForSource(fyaml.Source),
		jsonschema.Title("telescope"),
	)
	if err != nil {
		return nil, errors.Wrap(err, "generate schema")
	}
	return data, nil
}
