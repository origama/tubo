package runtime

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// This file is the deterministic, CI-runnable expression of the fail-closed
// session-resume quarantine (issue #39) on the A0 path. The production tunnel
// adapter (pipe.go) must not advertise, offer, select, accept, or handle the
// session resume capability (/tubo/tunnel-resume/2.1, session-resume-v1), and
// no resume machinery may remain in the A0 path: the session engine
// (session.go/ownership.go), the protocol surface (frame.go), and the tunnel
// adapter must all be free of it. These tests are source-based and CI-stable:
// they fail closed if any resume surface is reintroduced.

func codeWithoutComments(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	raw := string(data)
	var code strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			code.WriteString(line)
			code.WriteString("\n")
		}
	}
	return raw, strings.ToLower(code.String()), nil
}

func TestProductionTunnelAdapterHasNoResumeSurface(t *testing.T) {
	raw, codeLower, err := codeWithoutComments("pipe.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"resume",
		"resumeoffer",
		"resumesecret",
		"tunnelresume",
		"capsessionresume",
		"session-resume-v1",
		"/tubo/tunnel-resume/2.1",
		"handleresumestream",
		"streamrole",
		"setcontrolstream",
		"startresumehandshake",
		"initiateresumerecovery",
		"tryplannedswitch",
	}
	for _, tok := range forbidden {
		if strings.Contains(codeLower, tok) {
			t.Fatalf("production tunnel adapter references forbidden resume surface %q", tok)
		}
	}

	// The only stream handlers registered are the authorized /tubo/tunnel/2.0
	// handler, /tubo/exact-redemption/1, and /tubo/exact-renewal/1. A resume
	// protocol ID is never registered, so libp2p resets any unsolicited
	// /tubo/tunnel-resume/2.1 stream.
	count := strings.Count(strings.ToLower(raw), "setstreamhandler")
	if count != 4 {
		t.Fatalf("expected exactly 4 SetStreamHandler calls (scoped tunnel + scope-less tunnel + redemption + renewal), got %d", count)
	}
	if !strings.Contains(strings.ToLower(raw), "wire.tunnelid") {
		t.Fatalf("production adapter did not register the authorized tunnel handler")
	}
	if !strings.Contains(strings.ToLower(raw), "wire.exactredemptionid") {
		t.Fatalf("production adapter did not register the exact redemption handler")
	}
	if !strings.Contains(strings.ToLower(raw), "wire.exactrenewalid") {
		t.Fatalf("production adapter did not register the exact renewal handler")
	}
}

func TestSessionEngineHasNoResumeMachinery(t *testing.T) {
	for _, path := range []string{"session.go", "ownership.go"} {
		if _, codeLower, err := codeWithoutComments(path); err != nil {
			t.Fatal(err)
		} else {
			forbidden := []string{
				"resume",
				"resumesecret",
				"controlstream",
				"candidatestream",
				"candidateepoch",
				"detached",
				"ackticker",
				"sendacks",
				"handleack",
				"ackproof",
				"ackbytes",
				"retainedwindow",
				"quiescing",
				"plannedswitch",
				"switchprepare",
				"switchabort",
				"barrier",
				"activate",
				"transcriptdigest",
				"opencontrolstream",
				"resumerecovery",
				"proof",
				"nonce",
				"replay",
				"streamrole",
			}
			for _, tok := range forbidden {
				if strings.Contains(codeLower, tok) {
					t.Fatalf("%s references forbidden resume machinery %q", path, tok)
				}
			}
		}
	}
}

func TestProtocolModuleHasNoResumeSurface(t *testing.T) {
	_, codeLower, err := codeWithoutComments("../protocol/frame.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"resume",
		"resumeoffer",
		"streamrole",
		"switchesuggested",
		"switchprepare",
		"switchabort",
		"barrierack",
		"capsessionresume",
		"tunnelresumeid",
		"maxresumeframe",
		"ackbytes",
		"detachedgrace",
		"transcriptdigest",
	}
	for _, tok := range forbidden {
		if strings.Contains(codeLower, tok) {
			t.Fatalf("protocol frame.go references forbidden resume surface %q", tok)
		}
	}
}

func TestBaseSessionCapabilitiesHaveNoResumeCap(t *testing.T) {
	for _, c := range baseSessionCapabilities() {
		if strings.Contains(strings.ToLower(c), "resume") {
			t.Fatalf("capability %q advertises resume", c)
		}
		if c == "session-resume-v1" {
			t.Fatalf("capability %q advertises session resume", c)
		}
	}
}

func TestAuthorizedOpenResultCarriesNoResumeOffer(t *testing.T) {
	r := authorizedOpenResult("sid-123")
	if !r.OK {
		t.Fatalf("OpenOK not set: %+v", r)
	}
	if r.SessionID != "sid-123" || r.Protocol != "2.0" {
		t.Fatalf("unexpected OpenOK: %+v", r)
	}
	// The wire OpenOK must contain no offer/secret field (structurally).
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"resume_offer", "resume_secret", "control_protocol", "resume_protocol"} {
		if strings.Contains(strings.ToLower(string(b)), tok) {
			t.Fatalf("authorized OpenOK wire carries forbidden field %q: %s", tok, string(b))
		}
	}
}
