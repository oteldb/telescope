package logs

import (
	"slices"
	"strconv"
	"strings"
)

// semantic is what a well-known key says its value is about. It is a coarser
// thing than the key: a reader is not looking for `messaging.destination.name`
// so much as for the topic, and the topic reads the same color whether the
// shipper spelled it the way the conventions do or the way the library that
// wrote it did.
type semantic int

const (
	semNone semantic = iota
	semHTTPStatus
	semHTTPMethod
	semHTTPRoute
	semDuration
	semRPCStatus
	semRPCName
	semDestination
	semNumber
	semNamespace
	semPod
	semContainer
	semNode
)

// semanticKeys maps a normalized key to what it carries.
//
// Both spellings of everything are here on purpose. The OpenTelemetry names are
// what a collector emits; the short ones are what the application actually
// logged, and a viewer that only recognized the conventions would color almost
// nothing in a real stream.
var semanticKeys = map[string]semantic{
	"http_response_status_code": semHTTPStatus,
	"http_status_code":          semHTTPStatus,
	"http_status":               semHTTPStatus,
	"status_code":               semHTTPStatus,
	"statuscode":                semHTTPStatus,
	"status":                    semHTTPStatus,
	"resp_status":               semHTTPStatus,
	"response_status":           semHTTPStatus,
	"response_code":             semHTTPStatus,
	"res_status":                semHTTPStatus,

	"http_request_method": semHTTPMethod,
	"http_method":         semHTTPMethod,
	"request_method":      semHTTPMethod,
	"req_method":          semHTTPMethod,
	"method":              semHTTPMethod,
	"verb":                semHTTPMethod,

	"http_route":  semHTTPRoute,
	"http_target": semHTTPRoute,
	"http_path":   semHTTPRoute,
	"http_url":    semHTTPRoute,
	"url_path":    semHTTPRoute,
	"url_full":    semHTTPRoute,
	"route":       semHTTPRoute,
	"path":        semHTTPRoute,
	"uri":         semHTTPRoute,
	"request_uri": semHTTPRoute,
	"req_path":    semHTTPRoute,

	"http_server_request_duration": semDuration,
	"http_client_request_duration": semDuration,
	"duration":                     semDuration,
	"duration_ms":                  semDuration,
	"duration_seconds":             semDuration,
	"latency":                      semDuration,
	"latency_ms":                   semDuration,
	"elapsed":                      semDuration,
	"elapsed_ms":                   semDuration,
	"response_time":                semDuration,
	"took":                         semDuration,

	"rpc_grpc_status_code": semRPCStatus,
	"grpc_status_code":     semRPCStatus,
	"grpc_status":          semRPCStatus,
	"grpc_code":            semRPCStatus,
	"rpc_status_code":      semRPCStatus,

	"rpc_service":         semRPCName,
	"rpc_method":          semRPCName,
	"rpc_system":          semRPCName,
	"grpc_service":        semRPCName,
	"grpc_method":         semRPCName,
	"grpc_full_method":    semRPCName,
	"messaging_system":    semRPCName,
	"messaging_operation": semRPCName,

	"messaging_destination_name":     semDestination,
	"messaging_destination":          semDestination,
	"messaging_topic":                semDestination,
	"messaging_kafka_topic":          semDestination,
	"kafka_topic":                    semDestination,
	"topic":                          semDestination,
	"messaging_kafka_consumer_group": semDestination,
	"messaging_consumer_group_name":  semDestination,
	"kafka_consumer_group":           semDestination,
	"consumer_group":                 semDestination,
	"group_id":                       semDestination,

	"messaging_kafka_destination_partition": semNumber,
	"messaging_kafka_partition":             semNumber,
	"messaging_destination_partition_id":    semNumber,
	"kafka_partition":                       semNumber,
	"partition":                             semNumber,
	"messaging_kafka_message_offset":        semNumber,
	"messaging_kafka_offset":                semNumber,
	"kafka_offset":                          semNumber,
	"offset":                                semNumber,

	"k8s_namespace_name":        semNamespace,
	"k8s_namespace":             semNamespace,
	"kubernetes_namespace_name": semNamespace,
	"kubernetes_namespace":      semNamespace,
	"namespace_name":            semNamespace,
	"namespace":                 semNamespace,

	"k8s_pod_name":        semPod,
	"k8s_pod":             semPod,
	"kubernetes_pod_name": semPod,
	"kubernetes_pod":      semPod,
	"pod_name":            semPod,
	"pod":                 semPod,

	"k8s_container_name":        semContainer,
	"k8s_container":             semContainer,
	"kubernetes_container_name": semContainer,
	"container_name":            semContainer,
	"container":                 semContainer,

	"k8s_node_name":        semNode,
	"k8s_node":             semNode,
	"kubernetes_node_name": semNode,
	"node_name":            semNode,
	"node":                 semNode,
}

// semanticOf classifies a key by what it is called.
func semanticOf(key string) semantic {
	if s, ok := semanticKeys[normalizeKey(key)]; ok {
		return s
	}
	return semNone
}

// normalizeKey folds away the differences that are only spelling. The dots of a
// semantic convention survive as underscores or dashes wherever the key had to
// pass through something that would not take a dot, and camelCase is what a
// hand-written field looks like; none of that changes what the key means.
func normalizeKey(key string) string {
	key = strings.ReplaceAll(key, ".", "_")
	key = strings.ReplaceAll(key, "-", "_")
	return strings.ToLower(key)
}

// HighlightField colors a value the way its key says it should be read, and
// reports whether the key said anything at all. A key nobody recognizes, or one
// whose value is not what the key promised, is left for the caller to draw.
func HighlightField(key, value string) (string, bool) {
	if value == "" {
		return value, false
	}
	text, color := semanticOf(key).render(value)
	if color == "" {
		return value, false
	}
	return color + text + ansiReset, true
}

// render is the value as it should be drawn and the color to draw it in. An
// empty color means the value did not turn out to be what the key claimed:
// `status` is a severity as often as it is a status code.
func (s semantic) render(value string) (text, color string) {
	switch s {
	case semHTTPStatus:
		return value, httpStatusColor(value)
	case semRPCStatus:
		return grpcStatus(value)
	case semHTTPMethod:
		return value, ansiMethod
	case semHTTPRoute:
		return value, ansiPath
	case semDuration, semNumber:
		return value, ansiNum
	case semRPCName, semDestination:
		return value, ansiName
	case semNamespace:
		return value, ansiNamespace
	case semPod:
		return value, ansiPod
	case semContainer:
		return value, ansiContainer
	case semNode:
		return value, ansiNode
	default:
		return value, ""
	}
}

// httpStatusColor reads a status by its class, which is the only part of it a
// reader scans for: what matters about a 503 is that the fifth hundred is
// somebody's fault, and which one is the second question.
func httpStatusColor(value string) string {
	n, err := strconv.Atoi(strings.TrimSpace(firstToken(value)))
	if err != nil {
		return ""
	}
	switch n / 100 {
	case 1:
		return ansiTime
	case 2:
		return ansiOK
	case 3:
		return ansiInfo
	case 4:
		return ansiWarn
	case 5:
		return ansiErr
	default:
		return ""
	}
}

func firstToken(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// grpcNames are the canonical gRPC status codes, by their number. Code 1 keeps
// the spelling the protocol gave it rather than the one American English would.
var grpcNames = []string{
	"OK",
	"CANCELLED", //nolint:misspell // google.rpc.Code.CANCELLED is spelled this way.
	"UNKNOWN",
	"INVALID_ARGUMENT",
	"DEADLINE_EXCEEDED",
	"NOT_FOUND",
	"ALREADY_EXISTS",
	"PERMISSION_DENIED",
	"RESOURCE_EXHAUSTED",
	"FAILED_PRECONDITION",
	"ABORTED",
	"OUT_OF_RANGE",
	"UNIMPLEMENTED",
	"INTERNAL",
	"UNAVAILABLE",
	"DATA_LOSS",
	"UNAUTHENTICATED",
}

// grpcStatus draws a gRPC status. A code arrives as a number far more often
// than as a word, and the number is unreadable on its own — 5 and 15 are a
// missing key and a corrupt one — so the name is written out beside it.
func grpcStatus(value string) (text, color string) {
	if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
		if n < 0 || n >= len(grpcNames) {
			return value, ""
		}
		return value + " " + grpcNames[n], grpcNameColor(grpcNames[n])
	}
	name := strings.ToUpper(strings.TrimSpace(value))
	if c := grpcNameColor(name); c != "" {
		return value, c
	}
	return value, ""
}

// grpcNameColor colors a status name, green for the one that means the call
// worked and red for the sixteen that mean it did not.
func grpcNameColor(name string) string {
	switch {
	case name == "OK":
		return ansiOK
	// The one-l spelling is what the Go library calls the same code.
	case name == "CANCELED", slices.Contains(grpcNames[1:], name):
		return ansiErr
	default:
		return ""
	}
}
