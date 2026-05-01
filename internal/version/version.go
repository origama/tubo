package version

import (
	"fmt"
	"strings"
)

var (
	// ProductVersion is the version of the tubo binary. Override at build time via ldflags.
	ProductVersion = "dev"
	// Commit is the git SHA for this build. Override at build time via ldflags.
	Commit = "unknown"
	// BuildDate is the UTC build timestamp for this build. Override at build time via ldflags.
	BuildDate = "unknown"
)

const (
	ProtocolMajor = 1
	ProtocolMinor = 0
)

func ProtocolVersion() string {
	return fmt.Sprintf("%d.%d", ProtocolMajor, ProtocolMinor)
}

func Summary() string {
	return strings.Join([]string{
		"product=" + ProductVersion,
		"protocol=" + ProtocolVersion(),
		"commit=" + Commit,
		"build_date=" + BuildDate,
	}, " ")
}
