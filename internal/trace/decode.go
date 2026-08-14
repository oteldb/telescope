package trace

import "github.com/go-faster/errors"

// Decode reads whichever of the two formats arrived.
//
// Neither is asked for by name: a file has no content type, a store that
// declared its API still sits behind proxies that do not, and somebody who has
// a trace should not have to know which database wrote it.
//
// What decides is whether spans came out, not whether the decode returned an
// error, and that is the whole subtlety here. A Jaeger response is JSON with a
// "data" key, so the OTLP decoder reads it as a well-formed payload describing
// no spans at all and reports no error — it is only unknown fields, and unknown
// fields are how OTLP stays forwards-compatible. Dispatching on the error would
// mean a Jaeger response opening as an empty trace.
func Decode(data []byte) (*Tree, error) {
	otlp, otlpErr := DecodeOTLP(data)
	if tree, ok := firstWithSpans(otlp); ok {
		return tree, nil
	}
	jaeger, jaegerErr := DecodeJaeger(data)
	if tree, ok := firstWithSpans(jaeger); ok {
		return tree, nil
	}
	// Both failed to find anything. The OTLP complaint is the one to report,
	// since it is the format an endpoint answers with.
	if otlpErr != nil {
		return nil, otlpErr
	}
	if jaegerErr != nil {
		return nil, jaegerErr
	}
	return nil, errors.New("no spans in that trace")
}

func firstWithSpans(found []*Tree) (*Tree, bool) {
	for _, t := range found {
		if t.Len() > 0 {
			return t, true
		}
	}
	return nil, false
}
