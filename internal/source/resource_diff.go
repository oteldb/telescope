package source

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// ResourceChange names a JSON pointer and its values on either side of a watch
// update. A missing side is omitted; JSON null remains a value.
type ResourceChange struct {
	Path   string          `json:"path"`
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
}

func resourceChanges(before, after map[string]json.RawMessage, path string) []ResourceChange {
	keys := make([]string, 0, len(before)+len(after))
	for k := range before {
		keys = append(keys, k)
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var out []ResourceChange
	for _, k := range keys {
		oldValue, newValue := before[k], after[k]
		if bytes.Equal(oldValue, newValue) {
			continue
		}
		pointer := path + "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(k)
		var oldObject, newObject map[string]json.RawMessage
		if json.Unmarshal(oldValue, &oldObject) == nil && oldObject != nil &&
			json.Unmarshal(newValue, &newObject) == nil && newObject != nil {
			out = append(out, resourceChanges(oldObject, newObject, pointer)...)
			continue
		}
		out = append(out, ResourceChange{Path: pointer, Before: oldValue, After: newValue})
	}
	return out
}

func resourceState(obj map[string]json.RawMessage) []byte {
	// These change on almost every write, and managedFields can dwarf the
	// resource itself. Neither says what changed for the person watching it.
	var metadata map[string]json.RawMessage
	if json.Unmarshal(obj["metadata"], &metadata) == nil {
		delete(metadata, "resourceVersion")
		delete(metadata, "managedFields")
		obj["metadata"], _ = json.Marshal(metadata)
	}
	// Re-encoding normalizes whitespace and map order before comparison.
	var value any
	encoded, _ := json.Marshal(obj)
	d := json.NewDecoder(bytes.NewReader(encoded))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return encoded
	}
	encoded, _ = json.Marshal(value)
	return encoded
}
