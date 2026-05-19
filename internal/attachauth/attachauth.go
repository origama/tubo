package attachauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
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

func (r *resolver) Resolve(_ context.Context, req ResolveRequest) (ResolveResult, error) {
	if r.deps.IdentityStore == nil || r.deps.ArtifactStore == nil {
		return ResolveResult{}, ErrNotImplemented
	}
	cfg, svc, err := r.deps.IdentityStore.EnsureAttachServiceIdentity(req.ConfigPath, req.Config)
	if err != nil {
		return ResolveResult{}, err
	}
	cluster := cfg.Clusters[cfg.CurrentCluster]
	servicePeerID, err := r.deps.IdentityStore.ServicePeerID(svc.ServiceSeed)
	if err != nil {
		return ResolveResult{}, err
	}
	authorityPub, err := discovery.ParseAuthorityPublicKey(cluster.AuthorityPublicKey)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("parse authority public key for cluster %q: %w", cfg.CurrentCluster, err)
	}
	base := ResolveResult{
		Config:                   cfg,
		Service:                  svc,
		ServicePeerID:            servicePeerID,
		ServiceClaimFile:         svc.ServiceClaimFile,
		ServicePublishLeaseFile:  svc.ServicePublishLeaseFile,
		MembershipCapabilityFile: "",
	}

	if err := r.deps.ArtifactStore.VerifyPublishLease(svc.ServicePublishLeaseFile, authorityPub, cluster.ClusterID, cfg.CurrentNamespace, svc.ServiceID, servicePeerID); err == nil {
		membershipFile, err := r.deps.ArtifactStore.ResolveMembershipCapabilityFile(req.ConfigPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceSeed)
		if err != nil {
			return ResolveResult{}, err
		}
		shareToken, err := r.deps.ArtifactStore.BuildShareToken(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc)
		if err != nil {
			return ResolveResult{}, err
		}
		result := base
		result.Decision = DecisionReady
		result.MembershipCapabilityFile = membershipFile
		result.ServiceShareToken = shareToken
		result.PublishLeaseReused = true
		if shareToken == "" {
			grantPeer := grantServicePeer(cluster)
			result.ShareRecoveryHint = shareRecoveryHint(cfg.Service.Name, cfg.CurrentCluster, cfg.CurrentNamespace, grantPeer, svc.GrantRequestID)
		}
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) && !isPublishLeaseExpiredError(err) {
		return ResolveResult{}, fmt.Errorf("service publish lease for cluster %q namespace %q service %q rejected: %w", cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, err)
	}

	if err := r.deps.ArtifactStore.VerifyServiceClaim(svc.ServiceClaimFile, authorityPub, cluster.ClusterID, cfg.CurrentNamespace, svc.ServiceID, servicePeerID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ResolveResult{}, fmt.Errorf("service claim for cluster %q namespace %q service %q rejected: %w", cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, err)
	}
	membershipFile, err := r.deps.ArtifactStore.ResolveMembershipCapabilityFile(req.ConfigPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceSeed)
	if err != nil {
		return ResolveResult{}, err
	}
	shareToken, err := r.deps.ArtifactStore.BuildShareToken(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc)
	if err != nil {
		return ResolveResult{}, err
	}
	if cluster.AuthorityPrivateKeyFile != "" && r.deps.AuthoritySigner != nil {
		if err := r.deps.AuthoritySigner.MintLocalPublishLease(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc); err != nil {
			return ResolveResult{}, err
		}
		result := base
		result.Decision = DecisionReady
		result.MembershipCapabilityFile = membershipFile
		result.ServiceShareToken = shareToken
		result.MintedLocally = true
		if shareToken == "" {
			grantPeer := grantServicePeer(cluster)
			result.ShareRecoveryHint = shareRecoveryHint(cfg.Service.Name, cfg.CurrentCluster, cfg.CurrentNamespace, grantPeer, svc.GrantRequestID)
		}
		return result, nil
	}
	result := base
	result.Decision = DecisionRetryable
	result.MembershipCapabilityFile = membershipFile
	result.ServiceShareToken = shareToken
	result.UserMessage = "stored publish authorization requires refresh or mint"
	if shareToken == "" {
		grantPeer := grantServicePeer(cluster)
		result.ShareRecoveryHint = shareRecoveryHint(cfg.Service.Name, cfg.CurrentCluster, cfg.CurrentNamespace, grantPeer, svc.GrantRequestID)
	}
	return result, nil
}

func (r *resolver) Renew(_ context.Context, _ RenewRequest) (ResolveResult, error) {
	return ResolveResult{}, ErrNotImplemented
}

func isPublishLeaseExpiredError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "publish lease expired")
}

func grantServicePeer(cluster cfgpkg.Cluster) string {
	if cluster.MembershipGrant == nil || cluster.MembershipGrant.GrantServiceProtocol != grantspkg.ProtocolID {
		return ""
	}
	for _, peer := range cluster.MembershipGrant.GrantServicePeers {
		if strings.TrimSpace(peer) != "" {
			return strings.TrimSpace(peer)
		}
	}
	return ""
}

func shareRecoveryHint(serviceName, clusterName, namespaceName, grantPeer, grantRequestID string) string {
	if strings.TrimSpace(grantPeer) == "" {
		return fmt.Sprintf("run `tubo share service/%s --cluster %s --namespace %s` from an authority node, or retry attach on the authority node if you need a copyable connect token", serviceName, clusterName, namespaceName)
	}
	if strings.TrimSpace(grantRequestID) != "" {
		return fmt.Sprintf("reprint the token with `tubo grants request service/%s --poll --peer %s --cluster %s --namespace %s` (request %s)", serviceName, grantPeer, clusterName, namespaceName, grantRequestID)
	}
	return fmt.Sprintf("request or poll the grant with `tubo grants request service/%s --peer %s --cluster %s --namespace %s`", serviceName, grantPeer, clusterName, namespaceName)
}
