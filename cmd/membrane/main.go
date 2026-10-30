// Command membrane runs the Membrane daemon. Runtime config lives in the
// embedded state store under data_dir. It is served through a hot-reloaded
// Snapshot that fails closed and keeps the last-good config, and it is edited
// through the Membrane Console, off the egress path. The daemon itself is in
// internal/daemon.
package main

import (
	"os"

	"github.com/inferencepath/membrane/internal/daemon"
)

// Build metadata, set with -ldflags.
var (
	version   = "0.0.0-dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	os.Exit(daemon.Run(os.Args[1:], daemon.BuildInfo{
		Version:   version,
		Commit:    commit,
		BuildTime: buildTime,
	}, daemon.Options{}))
}
