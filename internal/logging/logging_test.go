package logging

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestDiagnosticsEnabledThresholds(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "one shot default quiet", cfg: Config{}, want: false},
		{name: "one shot debug verbosity", cfg: Config{Verbosity: 2}, want: true},
		{name: "runtime default quiet", cfg: Config{Runtime: true}, want: false},
		{name: "runtime verbose", cfg: Config{Runtime: true, Verbosity: 1}, want: true},
		{name: "explicit info log level", cfg: Config{LogLevel: "info"}, want: true},
		{name: "explicit warn log level", cfg: Config{LogLevel: "warn"}, want: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := diagnosticsEnabled(tt.cfg); got != tt.want {
				t.Fatalf("diagnosticsEnabled(%+v) = %v, want %v", tt.cfg, got, tt.want)
			}
		})
	}
}

func TestVerbosefThresholds(t *testing.T) {
	oldCfg := Current()
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = oldStderr
		_ = Configure(oldCfg)
	}()
	if err := Configure(Config{Verbosity: 3}); err != nil {
		t.Fatal(err)
	}
	Verbosef(3, "hello\n")
	Verbosef(4, "skip\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "hello") {
		t.Fatalf("expected verbose output, got %q", got)
	}
	if strings.Contains(got, "skip") {
		t.Fatalf("unexpected output for higher threshold, got %q", got)
	}
}
