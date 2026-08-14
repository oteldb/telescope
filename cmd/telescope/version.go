package main

import "github.com/go-faster/sdk/cliversion"

// buildVersion reads the build info rather than a linker flag: since Go 1.24 a
// build stamps the module version and the commit itself, so a release, a `go
// install` and a build from a checkout all say what they are without goreleaser
// having to write it in.
func buildVersion() string {
	info, _ := cliversion.GetInfo("github.com/oteldb/telescope")
	return info.String()
}
