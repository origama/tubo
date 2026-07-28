package runtime

import "os"

func RunSource() string {
	switch os.Getenv("TUBO_RUN_SOURCE") {
	case "launchd", "systemd", "detached":
		return os.Getenv("TUBO_RUN_SOURCE")
	default:
		return "foreground"
	}
}
