package cli

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/origama/tubo/internal/domain"
	"github.com/origama/tubo/internal/store"
)

func TestLaunchdPlanAndPlistAreBoundedToPrimaryResource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	t.Setenv("HOME", filepath.Join(home, "user"))
	state, err := store.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ref, err := domain.ParseRef("authority/solar")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := launchdPlanFor(state, "/opt/tubo/bin/tubo", ref, "solar/default")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Label != "io.github.origama.tubo.authority.solar" || !strings.HasSuffix(plan.UnitPath, plan.Label+".plist") {
		t.Fatalf("plan: %#v", plan)
	}
	if got, want := strings.Join(plan.Args, " "), "/opt/tubo/bin/tubo authority start solar --scope solar/default"; got != want {
		t.Fatalf("args %q want %q", got, want)
	}
	data, err := launchdPlist(plan)
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	var decodeErr error
	for {
		_, decodeErr = decoder.Token()
		if decodeErr != nil {
			break
		}
	}
	if !errors.Is(decodeErr, io.EOF) {
		t.Fatalf("plist XML: %v\n%s", decodeErr, data)
	}
	text := string(data)
	for _, required := range []string{"TUBO_HOME", "TUBO_RUN_SOURCE", "launchd", "KeepAlive", "ThrottleInterval", plan.LogPath} {
		if !strings.Contains(text, required) {
			t.Fatalf("plist missing %q: %s", required, text)
		}
	}
	if strings.Contains(text, "service/") || strings.Contains(text, "127.0.0.1") {
		t.Fatalf("plist leaked local target: %s", text)
	}
}

func TestLaunchdPlanRejectsWrongAuthorityScopeAndUnsupportedRef(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	state, err := store.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	authority, _ := domain.ParseRef("authority/solar")
	if _, err := launchdPlanFor(state, "/bin/tubo", authority, "other/default"); err == nil {
		t.Fatal("wrong authority scope accepted")
	}
	service, _ := domain.ParseRef("service/origin")
	if _, err := launchdPlanFor(state, "/bin/tubo", service, ""); err == nil {
		t.Fatal("service supervision accepted")
	}
	pipe, _ := domain.ParseRef("pipe/demo")
	if _, err := launchdPlanFor(state, "relative/tubo", pipe, ""); err == nil {
		t.Fatal("relative executable accepted")
	}
}

func TestSupervisorBlocksManualLifecycleButAllowsManagerSource(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	state, err := store.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.PutSupervisor(context.Background(), store.Supervisor{PrimaryRef: "pipe/demo", Manager: "launchd", Label: "io.github.origama.tubo.pipe.demo", UnitPath: "/tmp/demo.plist"}); err != nil {
		t.Fatal(err)
	}
	if err := supervisorAllowsCurrentSource(context.Background(), state, "pipe/demo"); err == nil || !strings.Contains(err.Error(), "supervise uninstall") {
		t.Fatalf("manual lifecycle allowed: %v", err)
	}
	t.Setenv("TUBO_RUN_SOURCE", "launchd")
	if err := supervisorAllowsCurrentSource(context.Background(), state, "pipe/demo"); err != nil {
		t.Fatalf("launchd lifecycle refused: %v", err)
	}
}
