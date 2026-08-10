package complete

import "strings"

// kubeConfigPaths are the layouts worth probing. A typed path always works, so
// this list only has to cover the common cases, not every distribution.
var kubeConfigPaths = []string{
	"$KUBECONFIG",
	"$HOME/.kube/config",
	"$HOME/.kube/*.yaml",
	"$HOME/.kube/*.conf",
	"/etc/rancher/k3s/k3s.yaml",
	"/etc/rancher/rke2/rke2.yaml",
	"/etc/kubernetes/admin.conf",
	"/etc/kubernetes/kubelet.conf",
	"/var/lib/k0s/pki/admin.conf",
}

// kubeConfigProbe lists the readable kubeconfigs among [kubeConfigPaths],
// each with the context it selects.
//
// The probe deliberately runs unelevated even when the request asks for sudo:
// a sudoers rule that permits kubectl does not permit test(1) or cat(1), so
// attempting to elevate would fail rather than reveal more. Root-only configs
// therefore stay invisible here and must be typed by hand.
var kubeConfigProbe = `for f in ` + kubeConfigGlobs + `; do ` +
	`[ -r "$f" ] || continue; ` +
	`printf '%s\t%s\n' "$f" ` +
	`"$(sed -n 's/^current-context:[[:space:]]*//p' "$f" 2>/dev/null | head -1)"; ` +
	`done`

var kubeConfigGlobs = strings.Join(quoteProbePaths(kubeConfigPaths), " ")

// quoteProbePaths keeps globs and variables expandable while stopping a path
// with spaces from splitting into several words.
func quoteProbePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.ContainsAny(p, "*?") {
			// A quoted glob would not expand.
			out = append(out, p)
			continue
		}
		out = append(out, `"`+p+`"`)
	}
	return out
}

// kubeConfigLister enumerates kubeconfigs instead of log targets.
var kubeConfigLister = lister{sources: []listSource{{
	build: static(kubeConfigProbe),
	parse: parseKubeConfig,
}}}

// parseKubeConfig reads "path<tab>context".
func parseKubeConfig(line string) (Candidate, bool) {
	path, ctx, _ := strings.Cut(line, "\t")
	path = strings.TrimSpace(path)
	if path == "" {
		return Candidate{}, false
	}
	return Candidate{Value: path, Detail: strings.TrimSpace(ctx)}, true
}
