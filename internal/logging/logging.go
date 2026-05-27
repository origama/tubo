package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

type Config struct {
	Quiet     bool
	Verbosity int
	LogLevel  string
	Runtime   bool
}

var current Config

func Configure(cfg Config) error {
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	switch cfg.LogLevel {
	case "", "error", "warn", "warning", "info", "debug", "trace":
	default:
		return fmt.Errorf("invalid log level %q", cfg.LogLevel)
	}
	current = cfg
	if diagnosticsEnabled(cfg) {
		log.SetOutput(os.Stderr)
	} else {
		log.SetOutput(io.Discard)
	}
	return nil
}

func diagnosticsEnabled(cfg Config) bool {
	switch cfg.LogLevel {
	case "info", "debug", "trace":
		return true
	case "warn", "warning", "error":
		return false
	}
	if cfg.Runtime {
		return cfg.Verbosity >= 1
	}
	return cfg.Verbosity >= 2
}

func Resultf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, format, args...)
}

func Progressf(format string, args ...any) {
	if current.Quiet || current.Verbosity < 1 {
		return
	}
	fmt.Fprintf(os.Stderr, format, args...)
}

func Warnf(format string, args ...any) {
	if current.Quiet {
		return
	}
	fmt.Fprintf(os.Stderr, format, args...)
}

func Current() Config {
	return current
}
