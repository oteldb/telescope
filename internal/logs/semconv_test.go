package logs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHighlightFieldReadsWellKnownKeys(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{"an ok status is green", "http.response.status_code", "200", ansiOK + "200" + ansiReset},
		{"a redirect is not a failure", "http_status", "301", ansiInfo + "301" + ansiReset},
		{"the caller is at fault", "status_code", "404", ansiWarn + "404" + ansiReset},
		{"the server is at fault", "status", "503", ansiErr + "503" + ansiReset},
		{"a camelCase spelling still reads", "statusCode", "500", ansiErr + "500" + ansiReset},
		{"a status that is a word is not a code", "status", "Running", "Running"},
		{"a method is a method", "http.request.method", "POST", ansiMethod + "POST" + ansiReset},
		{"kubectl spells it verb", "verb", "GET", ansiMethod + "GET" + ansiReset},
		{"a route reads as a path", "http.route", "/v1/users/:id", ansiPath + "/v1/users/:id" + ansiReset},
		{"a duration is a number", "duration_ms", "12.5", ansiNum + "12.5" + ansiReset},

		{"a grpc code carries its name", "rpc.grpc.status_code", "5", ansiErr + "5 NOT_FOUND" + ansiReset},
		{"zero is the one that worked", "grpc_code", "0", ansiOK + "0 OK" + ansiReset},
		{"a code nobody defined is left alone", "grpc.code", "42", "42"},
		{"a code written as a word is read as one", "grpc_status", "UNAVAILABLE", ansiErr + "UNAVAILABLE" + ansiReset},
		{"a service is a name", "rpc.service", "users.v1.Users", ansiName + "users.v1.Users" + ansiReset},
		{"so is the method it called", "rpc.method", "GetUser", ansiName + "GetUser" + ansiReset},

		{"a broker is a name", "messaging.system", "kafka", ansiName + "kafka" + ansiReset},
		{"a topic is a name", "messaging.destination.name", "orders", ansiName + "orders" + ansiReset},
		{"so is the group reading it", "consumer_group", "billing", ansiName + "billing" + ansiReset},
		{"a partition is a number", "messaging.kafka.destination.partition", "3", ansiNum + "3" + ansiReset},
		{"an offset is a number", "kafka_offset", "918273", ansiNum + "918273" + ansiReset},

		{"a namespace", "k8s.namespace.name", "kube-system", ansiNamespace + "kube-system" + ansiReset},
		{"the plainer spelling of it", "namespace", "default", ansiNamespace + "default" + ansiReset},
		{"a pod", "k8s.pod.name", "nginx-7d8f", ansiPod + "nginx-7d8f" + ansiReset},
		{"the label a shipper flattened", "k8s_pod_name", "nginx-7d8f", ansiPod + "nginx-7d8f" + ansiReset},
		{"a container", "container", "app", ansiContainer + "app" + ansiReset},
		{"a node", "k8s.node.name", "ip-10-0-1-7", ansiNode + "ip-10-0-1-7" + ansiReset},

		{"a key nobody knows", "user_id", "42", "42"},
		{"an empty value is nothing to color", "status", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := HighlightField(tt.key, tt.value)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.want != tt.value, ok, "a value that was not colored is not claimed")
		})
	}
}

func TestNormalizeKeyFoldsSpelling(t *testing.T) {
	for _, key := range []string{"k8s.pod.name", "k8s_pod_name", "K8S-POD-NAME", "k8s.pod_name"} {
		require.Equal(t, semPod, semanticOf(key), key)
	}
}
