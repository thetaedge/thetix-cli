// Command thetix is the Thetix command line, a front end of the Thetix MCP server.
package main

import (
	"os"

	"github.com/thetaedge/thetix-cli/internal/cli"
)

// version is set at release time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	os.Exit(cli.Main(version, os.Args[1:]))
}
