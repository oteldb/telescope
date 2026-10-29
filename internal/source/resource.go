package source

import (
	"fmt"
	"regexp"
	"strings"
)

var resourceTarget = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*(/[a-zA-Z0-9][a-zA-Z0-9.-]*)?$`)

func (c Config) validateResource() error {
	if t := strings.TrimSpace(c.Target); t != "" && !resourceTarget.MatchString(t) {
		return fmt.Errorf("watch target must be resource or resource/name, including an API group if needed")
	}
	if c.Container != "" {
		return fmt.Errorf("a resource watch does not read container logs")
	}
	return nil
}

func (c Config) resourceCommand() string {
	args := []string{"kubectl"}
	if k := strings.TrimSpace(c.KubeConfig); k != "" {
		args = append(args, "--kubeconfig="+Quote(k))
	}
	if k := strings.TrimSpace(c.KubeContext); k != "" {
		args = append(args, "--context="+Quote(k))
	}
	target := strings.TrimSpace(c.Target)
	if target == "" {
		target = "events"
	}
	args = append(args, "get", Quote(target))
	switch ns := strings.TrimSpace(c.Namespace); ns {
	case "*":
		args = append(args, "--all-namespaces")
	case "":
	default:
		args = append(args, "--namespace="+Quote(ns))
	}
	if c.following() {
		args = append(args, "--watch", "--output-watch-events=true")
	}
	args = append(args, "-o", "json")
	return c.elevated(args)
}
