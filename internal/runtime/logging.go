package runtime

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func runtimeInfof(format string, args ...any) {
	if os.Getenv("TUBO_QUIET") == "1" {
		return
	}
	verbosity, _ := strconv.Atoi(os.Getenv("TUBO_VERBOSITY"))
	level := strings.ToLower(os.Getenv("TUBO_LOG_LEVEL"))
	if verbosity < 1 && level != "info" && level != "debug" && level != "trace" {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "%s info "+format+"\n", append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
}

func safeLogError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\n", " "), "\r", " ")
	if len(value) > 512 {
		return value[:512] + "..."
	}
	return value
}

func runtimeWarnf(format string, args ...any) {
	if os.Getenv("TUBO_QUIET") == "1" || os.Getenv("TUBO_LOG_LEVEL") == "error" {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "%s warn "+format+"\n", append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
}
