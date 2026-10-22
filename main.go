// Command thetix is the Thetix command line, a front end of the Thetix MCP server.
package main

import (
	"os"
	"runtime/debug"
	"strings"

	"github.com/thetaedge/thetix-cli/internal/cli"
)

// version is set at release time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	os.Exit(cli.Main(buildVersion(), os.Args[1:]))
}

// buildVersion is the release version, or for `go install ...@v0.2.0` the
// module version Go recorded in the binary.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && strings.HasPrefix(info.Main.Version, "v") {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return version
}
