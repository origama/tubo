package attachauth

import (
	"context"
	"errors"

	cfgpkg "github.com/origama/tubo/internal/config"
)

var ErrNotImplemented = errors.New("attachauth: resolver not wired yet")

type Decision string

const (
	DecisionReady           Decision = "ready"
	DecisionPendingApproval Decision = "pending_approval"
	DecisionDenied          Decision = "denied"
	DecisionRetryable       Decision = "retryable_failure"
)

type ResolveRequest struct {
	ConfigPath string
	Config     cfgpkg.Config
}

type RenewRequest struct {
	ConfigPath    string
	Config        cfgpkg.Config
	Service       cfgpkg.NamespaceService
	ServicePeerID string
}

type ResolveResult struct {
	Decision                 Decision
	Config                   cfgpkg.Config
	Service                  cfgpkg.NamespaceService
	ServicePeerID            string
	MembershipCapabilityFile string
	ServiceClaimFile         string
	ServicePublishLeaseFile  string
	ServiceShareToken        string
	ShareRecoveryHint        string
	PublishLeaseReused       bool
	MintedLocally            bool
	UserMessage              string
}

type Resolver interface {
	Resolve(context.Context, ResolveRequest) (ResolveResult, error)
	Renew(context.Context, RenewRequest) (ResolveResult, error)
}

type Dependencies struct {
	IdentityStore   IdentityStore
	ArtifactStore   ArtifactStore
	AuthoritySigner AuthoritySigner
	GrantClient     GrantClient
	Clock           Clock
}

type resolver struct {
	deps Dependencies
}

func New(deps Dependencies) Resolver {
	return &resolver{deps: deps}
}

func (r *resolver) Resolve(_ context.Context, _ ResolveRequest) (ResolveResult, error) {
	return ResolveResult{}, ErrNotImplemented
}

func (r *resolver) Renew(_ context.Context, _ RenewRequest) (ResolveResult, error) {
	return ResolveResult{}, ErrNotImplemented
}
