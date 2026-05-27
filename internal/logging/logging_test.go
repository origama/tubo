package logging

import "testing"

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
