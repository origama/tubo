package runtime

// Receiving-boundary serve path for the A0 authorized /tubo/tunnel/2.0
// transport (issue #48). This is the L2 secure-connectivity receive surface:
// it performs bounded frame decode, Pipe/hop/loop enforcement, protocol
// freshness and capability bounds, then delegates remote-open authorization to
// the neutral openauth.OpenAuthorizer contract injected at construction. The
// file must NOT import concrete L3 authority, revocation/epoch, store, or
// discovery material: any remote-open authorization decision and cryptographic
// validation lives behind the openauth seam. Concrete prototype Scope-token
// validation is confined to the transitional scopeTokenOpenAuthorizer adapter,
// which issue #49 replaces with the canonical exact-Pipe Access Lease + Exact
// Pipe Authority.

import (
	"context"
	"errors"
	"net"
	"time"

	core "github.com/libp2p/go-libp2p/core/network"

	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/node"
	"github.com/origama/tubo-rebuilt/internal/openauth"
	wire "github.com/origama/tubo-rebuilt/internal/protocol"
)

// errTargetUnreachable is returned by authorizeOpen when the local service
// target dial failed; the serve path maps it to the target_unreachable wire
// code (distinct from authorization denial) so the pre-#48 behavior is
// preserved.
var errTargetUnreachable = errors.New("target_unreachable: local target unavailable")

func (s *tunnelServer) handle(stream core.Stream) {
	if err := s.serve(stream); err != nil {
		if errors.Is(err, errInitialCarrierTransferred) {
			return
		}
		_ = stream.Reset()
	} else {
		_ = stream.Close()
	}
}

func (s *tunnelServer) serve(stream core.Stream) error {
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	var hello wire.ClientHello
	if err := wire.ReadFrame(stream, &hello); err != nil {
		return err
	}
	if hello.Version != "2.0" || len(hello.Capabilities) > 64 || !boundedStrings(8192, append([]string{hello.ClientPublic}, hello.Capabilities...)...) {
		return errors.New("protocol mismatch or field exceeds limit")
	}
	challenge := randomNonce()
	if err := wire.WriteFrame(stream, wire.ServerHello{Version: "2.0", PipeID: s.spec.PipeID, Challenge: challenge, Capabilities: baseSessionCapabilities()}); err != nil {
		return err
	}
	var open wire.OpenPipe
	if err := wire.ReadFrame(stream, &open); err != nil {
		return err
	}
	if !boundedStrings(8192, append([]string{open.PipeID, open.ChallengeProof}, open.VisitedPipeIDs...)...) || len(open.AccessLease) > openauth.MaxLeaseBytes {
		return s.openError(stream, "invalid_input", "open field exceeds limit")
	}
	if open.PipeID != s.spec.PipeID {
		return s.openError(stream, "not_found", "pipe_id mismatch")
	}
	if open.HopLimit <= 0 || open.HopLimit > domain.DefaultMaxPipeHops || len(open.VisitedPipeIDs) >= open.HopLimit {
		return s.openError(stream, "hop_limit_exceeded", "logical hop limit exceeded")
	}
	seen := map[string]bool{}
	for _, id := range open.VisitedPipeIDs {
		if seen[id] || id == s.spec.PipeID {
			return s.openError(stream, "loop_detected", "repeated pipe_id")
		}
		seen[id] = true
	}

	now := time.Now().UTC()
	auth, target, err := s.authorizeOpen(context.Background(), hello.ClientPublic, challenge, open, now)
	if err != nil {
		if errors.Is(err, errTargetUnreachable) {
			return s.openError(stream, "target_unreachable", "local target unavailable")
		}
		return s.openError(stream, "authorization_denied", authorizationMessage(err))
	}
	targetOwned := false
	defer func() {
		if !targetOwned {
			_ = target.Close()
		}
	}()
	authorizationTimer := time.AfterFunc(time.Until(auth.ExpiresAt), func() { _ = target.Close() })
	defer func() { authorizationTimer.Stop() }()
	sessionID, err := randomSessionID()
	if err != nil {
		return err
	}
	allowed, err := s.sessionAllowed(context.Background(), sessionID)
	if err != nil || !allowed {
		return s.openError(stream, "authorization_denied", "session revoked")
	}

	if err := wire.WriteFrame(stream, authorizedOpenResult(sessionID)); err != nil {
		return err
	}
	_ = stream.SetDeadline(time.Time{})

	s.recordSession(context.Background(), sessionID, 0, 0)
	runtimeInfof("session opened pipe=%s session=%s path=relayed", s.spec.Ref, sessionID)

	// Re-grounded data plane: an owner-bound Session owns the Carrier and the
	// local target. SetActiveCarrier fences admission; the authorization timer
	// is transferred to Session.Stop so lease expiry terminates the Session
	// (closing carrier and target), never the raw stream twice.
	carrier := node.WrapStream(stream)
	sp, err := s.runtime.NewSession(sessionConfig{
		SessionID:    sessionID,
		PipeID:       s.spec.PipeID,
		Scope:        s.spec.Scope.String(),
		TargetConn:   target,
		InitialEpoch: 1,
		IsClient:     false,
	})
	if err != nil {
		return err
	}
	if !sp.SetActiveCarrier(carrier) {
		// Session already terminalized the wrapped Carrier; the handler must
		// not issue a second raw stream reset.
		return errInitialCarrierTransferred
	}
	targetOwned = true // Session now owns the local target terminal action.
	// Session carrier lease now exclusively owns stream terminal action.
	// Replace the raw pre-transfer expiry close with Session lifecycle Stop.
	authorizationTimer = transferSessionAuthorizationTimer(authorizationTimer, auth.ExpiresAt, sp)

	sp.runServerSession()
	in, out := sp.ByteCounts()
	s.recordSession(context.Background(), sessionID, in, out)
	runtimeInfof("session closed pipe=%s session=%s rx=%d tx=%d", s.spec.Ref, sessionID, in, out)
	// Session exclusively owns terminal action after active installation.
	return errInitialCarrierTransferred
}

// authorizeOpen performs the L2 receiving-boundary authorization decision:
// it builds the neutral OpenRequest, invokes the injected OpenAuthorizer, then
// independently binds the returned Authorization back to the open before any
// local target dial. This function is the deterministic unit under test for the
// issue #48 seam: any denial, mismatched result, or zero/expired ExpiresAt
// yields an error and dial count 0; a valid result dials the target exactly
// once.
func (s *tunnelServer) authorizeOpen(ctx context.Context, clientPublic, challenge string, open wire.OpenPipe, now time.Time) (openauth.Authorization, net.Conn, error) {
	var zero openauth.Authorization
	if s.authorizer == nil {
		return zero, nil, errors.New("authorization denied: no open authorizer")
	}
	auth, err := s.authorizer.AuthorizeOpen(ctx, openauth.OpenRequest{
		PipeID:         open.PipeID,
		ClientPublic:   clientPublic,
		Challenge:      challenge,
		ChallengeProof: open.ChallengeProof,
		AccessLease:    open.AccessLease,
		Now:            now,
	})
	if err != nil {
		return zero, nil, errors.New("authorization denied")
	}
	// Defense-in-depth result binding: the concrete authorizer still owns
	// cryptographic validation, but the receiving boundary independently
	// refuses any result that does not bind to this exact open and a
	// non-zero, future expiry.
	if auth.PipeID != open.PipeID || auth.Subject != clientPublic || auth.ExpiresAt.IsZero() || !auth.ExpiresAt.After(now) {
		return zero, nil, errors.New("authorization denied: invalid authorization result")
	}
	dial := s.dialFunc
	if dial == nil {
		dial = dialService
	}
	target, err := dial(s.service)
	if err != nil {
		return zero, nil, errTargetUnreachable
	}
	return auth, target, nil
}

func (s *tunnelServer) openError(stream core.Stream, code, message string) error {
	_ = wire.WriteFrame(stream, wire.OpenResult{OK: false, ErrorCode: code, Message: message, RetryClass: "terminal"})
	return errors.New(code + ": " + message)
}

// authorizationMessage maps any concrete authorizer error to the single safe
// denial cause on the wire. It never leaks internal authority/revocation state.
func authorizationMessage(error) string { return "authorization denied" }
