package grants

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/origama/tubo/internal/capability"
)

const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDenied   = "denied"
	StatusExpired  = "expired"
)

type Request struct {
	ID                    string                           `json:"id"`
	ClusterName           string                           `json:"cluster_name"`
	ClusterID             string                           `json:"cluster_id"`
	NamespaceID           string                           `json:"namespace_id"`
	RequesterPeerID       string                           `json:"requester_peer_id"`
	ServiceName           string                           `json:"service_name"`
	ServiceID             string                           `json:"service_id"`
	ServiceKind           string                           `json:"service_kind,omitempty"`
	ServicePublicKey      string                           `json:"service_public_key"`
	ServiceOwnerSignature []byte                           `json:"service_owner_signature,omitempty"`
	RequestNonce          string                           `json:"request_nonce"`
	ServicePeerID         string                           `json:"service_peer_id"`
	RequestedPermissions  []string                         `json:"requested_permissions"`
	RequestedTTLSeconds   int64                            `json:"requested_ttl_seconds,omitempty"`
	Status                string                           `json:"status"`
	RequestedAt           time.Time                        `json:"requested_at"`
	ExpiresAt             time.Time                        `json:"expires_at"`
	DecidedAt             time.Time                        `json:"decided_at,omitempty"`
	DenialReason          string                           `json:"denial_reason,omitempty"`
	ServiceClaim          *capability.ServiceClaim         `json:"service_claim,omitempty"`
	PublishLease          *PublishLease                    `json:"publish_lease,omitempty"`
	MembershipCapability  *capability.MembershipCapability `json:"membership_capability,omitempty"`
	ServiceShareToken     string                           `json:"service_share_token,omitempty"`
}

type fileState struct {
	Requests []Request `json:"requests"`
}

type Store struct {
	path        string
	now         func() time.Time
	LockTimeout time.Duration
}

// PendingPolicy bounds active grant requests. Non-positive limits are disabled.
type PendingPolicy struct {
	MaxPendingRequests     int
	MaxPendingPerRequester int
	MaxPendingPerService   int
}

func NewStore(path string) *Store {
	return &Store{path: path, now: func() time.Time { return time.Now().UTC() }}
}

func DefaultStorePath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "tubo", "grants", "requests.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", "grants", "requests.json")
	}
	return filepath.Join(home, ".local", "share", "tubo", "grants", "requests.json")
}

func (s *Store) Path() string { return s.path }

func (s *Store) CreatePending(req Request) (Request, error) {
	return s.CreatePendingWithPolicy(req, PendingPolicy{})
}

// CreatePendingWithPolicy expires stale entries, checks dedupe/collision/limits,
// and inserts one request in the same locked transaction.
func (s *Store) CreatePendingWithPolicy(req Request, policy PendingPolicy) (Request, error) {
	if err := validateStoreRequest(req); err != nil {
		return Request{}, err
	}
	var result Request
	err := s.update(func(state *fileState) (bool, error) {
		changed := state.expire(s.now().UTC()) > 0
		for _, existing := range state.Requests {
			if existing.Status == StatusPending && equivalentActive(existing, req) {
				result = existing
				return changed, nil
			}
		}
		if err := enforcePendingPolicy(*state, req, policy); err != nil {
			return changed, err
		}
		if req.ID == "" {
			id, err := randomID("gr_")
			if err != nil {
				return changed, err
			}
			req.ID = id
		}
		now := s.now().UTC()
		if req.RequestedAt.IsZero() {
			req.RequestedAt = now
		}
		if req.ExpiresAt.IsZero() {
			req.ExpiresAt = req.RequestedAt.Add(24 * time.Hour)
		}
		req.Status = StatusPending
		state.Requests = append(state.Requests, req)
		state.sort()
		result = req
		return true, nil
	})
	return result, err
}

func (s *Store) ListPending() ([]Request, error) {
	var out []Request
	err := s.update(func(state *fileState) (bool, error) {
		changed := state.expire(s.now().UTC()) > 0
		for _, req := range state.Requests {
			if req.Status == StatusPending {
				out = append(out, req)
			}
		}
		return changed, nil
	})
	return out, err
}

func (s *Store) ListAll() ([]Request, error) {
	var out []Request
	err := s.update(func(state *fileState) (bool, error) {
		changed := state.expire(s.now().UTC()) > 0
		out = append([]Request(nil), state.Requests...)
		return changed, nil
	})
	return out, err
}

func (s *Store) Get(id string) (Request, bool, error) {
	var result Request
	var found bool
	err := s.update(func(state *fileState) (bool, error) {
		changed := state.expire(s.now().UTC()) > 0
		for _, req := range state.Requests {
			if req.ID == id {
				result, found = req, true
				break
			}
		}
		return changed, nil
	})
	return result, found, err
}

func (s *Store) Approve(id string, claim capability.ServiceClaim, lease *PublishLease, membership *capability.MembershipCapability, serviceShareToken string) (Request, error) {
	var result Request
	err := s.update(func(state *fileState) (bool, error) {
		now := s.now().UTC()
		changed := state.expire(now) > 0
		for i := range state.Requests {
			if state.Requests[i].ID != id {
				continue
			}
			if state.Requests[i].Status == StatusExpired {
				return changed, fmt.Errorf("grant request %q is expired", id)
			}
			if state.Requests[i].Status != StatusPending {
				return changed, fmt.Errorf("grant request %q is %s", id, state.Requests[i].Status)
			}
			state.Requests[i].Status = StatusApproved
			state.Requests[i].DecidedAt = now
			state.Requests[i].ServiceClaim = &claim
			state.Requests[i].PublishLease = lease
			state.Requests[i].MembershipCapability = membership
			state.Requests[i].ServiceShareToken = serviceShareToken
			if expiry, ok := approvedRequestExpiry(state.Requests[i]); ok {
				state.Requests[i].ExpiresAt = expiry
			}
			result = state.Requests[i]
			return true, nil
		}
		return changed, fmt.Errorf("grant request %q not found", id)
	})
	return result, err
}

func (s *Store) Deny(id, reason string) (Request, error) {
	var result Request
	err := s.update(func(state *fileState) (bool, error) {
		now := s.now().UTC()
		changed := state.expire(now) > 0
		for i := range state.Requests {
			if state.Requests[i].ID != id {
				continue
			}
			if state.Requests[i].Status != StatusPending {
				return changed, fmt.Errorf("grant request %q is %s", id, state.Requests[i].Status)
			}
			state.Requests[i].Status = StatusDenied
			state.Requests[i].DenialReason = reason
			state.Requests[i].DecidedAt = now
			result = state.Requests[i]
			return true, nil
		}
		return changed, fmt.Errorf("grant request %q not found", id)
	})
	return result, err
}

func (s *Store) ExpirePending() (int, error) {
	changed := 0
	err := s.update(func(state *fileState) (bool, error) {
		changed = state.expire(s.now().UTC())
		return changed > 0, nil
	})
	return changed, err
}

func (s *Store) update(mutate func(*fileState) (bool, error)) error {
	return withGrantStoreLock(s.path, s.LockTimeout, func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		changed, mutationErr := mutate(&state)
		if changed {
			if err := s.saveUnlocked(state); err != nil {
				return err
			}
		}
		return mutationErr
	})
}

// load and save remain package-local compatibility helpers for focused tests.
// Production mutations use update so read-modify-write stays one transaction.
func (s *Store) load() (fileState, error) {
	var state fileState
	err := withGrantStoreLock(s.path, s.LockTimeout, func() error {
		var err error
		state, err = s.loadUnlocked()
		return err
	})
	return state, err
}

func (s *Store) save(state fileState) error {
	return withGrantStoreLock(s.path, s.LockTimeout, func() error { return s.saveUnlocked(state) })
}

func (s *Store) loadUnlocked() (fileState, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	var state fileState
	if err := json.Unmarshal(b, &state); err != nil {
		return fileState{}, fmt.Errorf("decode grant request store %s: %w", s.path, err)
	}
	state.sort()
	return state, nil
}

func (s *Store) saveUnlocked(state fileState) error {
	state.sort()
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteGrantStore(s.path, append(b, '\n'))
}

func (s *fileState) expire(now time.Time) int {
	changed := 0
	for i := range s.Requests {
		if isRequestExpired(s.Requests[i], now) && s.Requests[i].Status != StatusExpired {
			if expiry, ok := requestExpiry(s.Requests[i]); ok {
				s.Requests[i].ExpiresAt = expiry
			}
			s.Requests[i].Status = StatusExpired
			changed++
		}
	}
	return changed
}

func (s *fileState) sort() {
	sort.SliceStable(s.Requests, func(i, j int) bool { return s.Requests[i].RequestedAt.Before(s.Requests[j].RequestedAt) })
}

func enforcePendingPolicy(state fileState, req Request, policy PendingPolicy) error {
	if policy.MaxPendingRequests <= 0 && policy.MaxPendingPerRequester <= 0 && policy.MaxPendingPerService <= 0 {
		return nil
	}
	pendingTotal := 0
	pendingRequester := 0
	pendingService := 0
	for _, existing := range state.Requests {
		if existing.ClusterID == req.ClusterID && existing.NamespaceID == req.NamespaceID && existing.ServiceID == req.ServiceID && existing.Status != StatusDenied && existing.Status != StatusExpired && existing.ServicePeerID != req.ServicePeerID {
			return fmt.Errorf("service %q already has an active grant request or claim for a different peer", req.ServiceID)
		}
		if existing.Status != StatusPending {
			continue
		}
		pendingTotal++
		if existing.RequesterPeerID == req.RequesterPeerID {
			pendingRequester++
		}
		if existing.ClusterID == req.ClusterID && existing.NamespaceID == req.NamespaceID && existing.ServiceID == req.ServiceID {
			pendingService++
		}
	}
	if policy.MaxPendingRequests > 0 && pendingTotal >= policy.MaxPendingRequests {
		return fmt.Errorf("too many pending grant requests: limit %d", policy.MaxPendingRequests)
	}
	if policy.MaxPendingPerRequester > 0 && pendingRequester >= policy.MaxPendingPerRequester {
		return fmt.Errorf("too many pending grant requests for requester: limit %d", policy.MaxPendingPerRequester)
	}
	if policy.MaxPendingPerService > 0 && pendingService >= policy.MaxPendingPerService {
		return fmt.Errorf("too many pending grant requests for service %q: limit %d", req.ServiceID, policy.MaxPendingPerService)
	}
	return nil
}

func equivalentActive(a, b Request) bool {
	// Retry submissions for the same logical publish request should reuse the
	// existing pending record even if the client generated a new nonce or used a
	// different transient requester PeerID. The stable identity for publication is
	// the service owner/public key plus the service runtime peer, not the short-
	// lived grant-client peer used to submit the request.
	return a.ClusterID == b.ClusterID &&
		a.NamespaceID == b.NamespaceID &&
		a.ServiceID == b.ServiceID &&
		a.ServicePeerID == b.ServicePeerID &&
		a.ServicePublicKey == b.ServicePublicKey
}

func validateStoreRequest(req Request) error {
	if req.ClusterName == "" || req.ClusterID == "" || req.NamespaceID == "" || req.RequesterPeerID == "" || req.ServiceName == "" || req.ServiceID == "" || req.ServicePublicKey == "" || len(req.ServiceOwnerSignature) == 0 || req.RequestNonce == "" || req.ServicePeerID == "" {
		return errors.New("grant request is missing required fields")
	}
	return nil
}

func requestExpiry(req Request) (time.Time, bool) {
	switch req.Status {
	case StatusPending:
		if req.ExpiresAt.IsZero() {
			return time.Time{}, false
		}
		return req.ExpiresAt.UTC(), true
	case StatusApproved:
		return approvedRequestExpiry(req)
	default:
		return time.Time{}, false
	}
}

func approvedRequestExpiry(req Request) (time.Time, bool) {
	var expiry time.Time
	if req.PublishLease != nil && !req.PublishLease.ExpiresAt.IsZero() {
		expiry = req.PublishLease.ExpiresAt.UTC()
	}
	if req.ServiceClaim != nil && !req.ServiceClaim.ExpiresAt.IsZero() {
		claimExpiry := req.ServiceClaim.ExpiresAt.UTC()
		if expiry.IsZero() || claimExpiry.Before(expiry) {
			expiry = claimExpiry
		}
	}
	if expiry.IsZero() && !req.ExpiresAt.IsZero() {
		expiry = req.ExpiresAt.UTC()
	}
	if expiry.IsZero() {
		return time.Time{}, false
	}
	return expiry, true
}

func isRequestExpired(req Request, now time.Time) bool {
	expiry, ok := requestExpiry(req)
	return ok && now.After(expiry)
}

// HasActivePublishLease returns true if the service has an approved grant request
// with a non-expired publish lease. This is used to verify that a service is
// authorized to accept connections before the cluster grant server mints connect leases.
// ActivePublishLeaseExpiry returns the expiry time of the active publish lease for the
// given service, along with a boolean indicating whether an active lease exists.
// A lease is active if it is approved, has a non-zero expiry, and hasn't expired yet.
// Zero-expiry leases are not considered active.
func (s *Store) ActivePublishLeaseExpiry(clusterID, namespaceID, serviceID string, now time.Time) (expiry time.Time, active bool, err error) {
	err = withGrantStoreLock(s.path, s.LockTimeout, func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		for _, req := range state.Requests {
			if req.Status != StatusApproved || req.ClusterID != clusterID || req.NamespaceID != namespaceID || req.ServiceID != serviceID || req.PublishLease == nil || req.PublishLease.ExpiresAt.IsZero() {
				continue
			}
			candidate := req.PublishLease.ExpiresAt.UTC()
			if now.Before(candidate) && (expiry.IsZero() || candidate.After(expiry)) {
				expiry = candidate
			}
		}
		return nil
	})
	return expiry, !expiry.IsZero(), err
}

// HasActivePublishLease returns true if the service has an active publish lease.
// Convenience wrapper around ActivePublishLeaseExpiry.
func (s *Store) HasActivePublishLease(clusterID, namespaceID, serviceID string, now time.Time) (bool, error) {
	_, active, err := s.ActivePublishLeaseExpiry(clusterID, namespaceID, serviceID, now)
	return active, err
}

func randomID(prefix string) (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}
