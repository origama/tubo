package runtime

// Session bookkeeping for the receiving L2 boundary (issue #48). These helpers
// keep the neutral receiving-boundary file(s) free of any direct
// *store.Store / internal/store import: they are the persistence adapter the
// serve path consumes for post-authorization liveness/accounting only, never
// for remote-open validation. No concrete authority/revocation store access is
// involved here.

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"time"

	"github.com/origama/tubo-rebuilt/internal/store"
)

// randomSessionID mints a fresh, unguessable session identifier for the
// authorized data plane. It mirrors the prototype authority.SessionID so the
// receiving file need not import a concrete authority package.
func randomSessionID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "ses_" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value), nil
}

// sessionAllowed reports whether a freshly minted session identifier is not
// revoked under the pipe's scope. It is liveness/accounting data, not a
// remote-open authorization decision; a revoke/epoch error fails closed.
func (s *tunnelServer) sessionAllowed(ctx context.Context, sessionID string) (bool, error) {
	revoked, err := s.state.IsRevoked(ctx, pipeScopeString(s.spec), "session", sessionID)
	if err != nil {
		return false, err
	}
	return !revoked, nil
}

// recordSession persists the session accounting record. It is a narrow
// consumer-side adapter call, never an authorization path.
func (s *tunnelServer) recordSession(ctx context.Context, sessionID string, in, out int64) {
	session := store.Session{ID: sessionID, PipeRef: s.spec.Ref.String(), InitialTransportPath: "relayed", CurrentTransportPath: "relayed", StartedAt: time.Now().UTC(), BytesIn: in, BytesOut: out}
	_ = s.state.PutSession(ctx, session)
}
