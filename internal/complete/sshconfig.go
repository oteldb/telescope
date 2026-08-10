package complete

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxIncludeDepth guards against Include cycles.
const maxIncludeDepth = 10

// userConfigDir and systemConfigDir are where OpenSSH resolves relative
// Include paths, for the user and system config chains respectively.
const (
	userConfigDir   = ".ssh"
	systemConfigDir = "/etc/ssh"
)

// Hosts returns the ssh destinations known locally: every non-pattern Host
// alias in the user and system ssh configs, then any plain names left in
// known_hosts.
//
// It is best effort: unreadable files and malformed directives are skipped.
func Hosts() []Candidate {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	userConfig := filepath.Join(home, userConfigDir, "config")
	systemConfig := filepath.Join(systemConfigDir, "ssh_config")

	out := configHosts(userConfig, filepath.Join(home, userConfigDir))
	out = append(out, configHosts(systemConfig, systemConfigDir)...)
	out = append(out, knownHosts(filepath.Join(home, userConfigDir, "known_hosts"))...)
	return dedupe(out)
}

// configHosts parses path and everything it includes. Relative Include paths
// resolve against base, matching OpenSSH.
func configHosts(path, base string) []Candidate {
	var (
		out  []Candidate
		seen = map[string]bool{}
		load func(string, int)
	)
	load = func(p string, depth int) {
		if depth > maxIncludeDepth || seen[p] {
			return
		}
		seen[p] = true

		f, err := os.Open(p)
		if err != nil {
			return
		}
		defer func() { _ = f.Close() }()

		out = append(out, parseSSHConfig(f, func(includes []string) {
			for _, inc := range expandIncludes(includes, base) {
				load(inc, depth+1)
			}
		})...)
	}
	load(path, 0)
	return out
}

// expandIncludes resolves Include arguments to concrete paths.
func expandIncludes(includes []string, base string) []string {
	var out []string
	for _, inc := range includes {
		p := inc
		switch {
		case strings.HasPrefix(inc, "~/"):
			if home, err := os.UserHomeDir(); err == nil {
				p = filepath.Join(home, inc[2:])
			}
		case !filepath.IsAbs(inc):
			p = filepath.Join(base, inc)
		}
		matches, err := filepath.Glob(p)
		if err != nil || len(matches) == 0 {
			out = append(out, p)
			continue
		}
		out = append(out, matches...)
	}
	return out
}

// parseSSHConfig extracts Host aliases from a single config file. Include
// directives are handed to onInclude rather than followed here.
func parseSSHConfig(r io.Reader, onInclude func([]string)) []Candidate {
	var (
		out      []Candidate
		aliases  []string
		hostName string
		user     string
	)
	flush := func() {
		detail := hostName
		if user != "" {
			detail = strings.TrimSpace(user + "@" + hostName)
		}
		for _, a := range aliases {
			if isPattern(a) {
				continue
			}
			out = append(out, Candidate{Value: a, Detail: detail})
		}
		aliases, hostName, user = nil, "", ""
	}

	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if before, _, found := strings.Cut(line, "#"); found {
			line = before
		}
		fields := strings.FieldsFunc(line, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '='
		})
		if len(fields) == 0 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "host":
			flush()
			aliases = fields[1:]
		case "match":
			// Match blocks carry no alias we could offer.
			flush()
		case "hostname":
			if len(fields) > 1 {
				hostName = fields[1]
			}
		case "user":
			if len(fields) > 1 {
				user = fields[1]
			}
		case "include":
			// Include splices in more config; the current block ends here.
			flush()
			if onInclude != nil && len(fields) > 1 {
				onInclude(fields[1:])
			}
		}
	}
	// Best effort: a malformed or unreadable config yields whatever parsed.
	_ = sc.Err()
	flush()
	return out
}

// knownHosts returns the plain host names recorded in a known_hosts file.
// Hashed entries carry no usable name and are skipped.
func knownHosts(path string) []Candidate {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var out []Candidate
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "|") {
			continue
		}
		names, _, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		for name := range strings.SplitSeq(names, ",") {
			// Entries may be bracketed with a port, as in [host]:2222.
			if h, _, found := strings.Cut(strings.TrimPrefix(name, "["), "]"); found {
				name = h
			}
			if name == "" || isPattern(name) {
				continue
			}
			out = append(out, Candidate{Value: name, Detail: "known_hosts"})
		}
	}
	_ = sc.Err()
	return out
}

func isPattern(s string) bool {
	return s == "" || strings.ContainsAny(s, "*?[]!")
}

// dedupe keeps the first occurrence of every value, so config entries win over
// known_hosts.
func dedupe(items []Candidate) []Candidate {
	seen := make(map[string]bool, len(items))
	out := items[:0]
	for _, c := range items {
		if seen[c.Value] {
			continue
		}
		seen[c.Value] = true
		out = append(out, c)
	}
	return out
}
