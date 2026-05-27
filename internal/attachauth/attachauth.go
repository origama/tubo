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
		shareToken, err := r.deps.ArtifactStore.BuildShareToken(cfg, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc)
		if err != nil {
			return ResolveResult{}, err
		}
		result := base
		result.Decision = DecisionReady
		result.MembershipCapabilityFile = membershipFile
		result.ServiceShareToken = shareToken
		result.PublishLeaseReused = true
		if shareToken == "" {
			result.ShareRecoveryHint = shareMintHint(cfg.Service.Name, cfg.CurrentCluster, cfg.CurrentNamespace)
		}
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) && !isPublishLeaseExpiredError(err) {
		return ResolveResult{}, fmt.Errorf("service publish lease for cluster %q namespace %q service %q rejected: %w", cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, err)
	}

	claimErr := r.deps.ArtifactStore.VerifyServiceClaim(svc.ServiceClaimFile, authorityPub, cluster.ClusterID, cfg.CurrentNamespace, svc.ServiceID, servicePeerID)
	if claimErr != nil && !errors.Is(claimErr, os.ErrNotExist) && !isCapabilityExpiredError(claimErr) {
		return ResolveResult{}, fmt.Errorf("service claim for cluster %q namespace %q service %q rejected: %w", cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, claimErr)
	}
	shareToken, err := r.deps.ArtifactStore.BuildShareToken(cfg, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc)
	if err != nil {
		return ResolveResult{}, err
	}
	grantPeer := svc.GrantServicePeer
	if strings.TrimSpace(grantPeer) == "" {
		grantPeer = grantServicePeer(cluster)
	}
	if cluster.AuthorityPrivateKeyFile == "" && grantPeer == "" && claimErr == nil {
		membershipFile, err := r.deps.ArtifactStore.ResolveMembershipCapabilityFile(req.ConfigPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceSeed)
		if err != nil {
			return ResolveResult{}, err
		}
		result := base
		result.Decision = DecisionReady
		result.MembershipCapabilityFile = membershipFile
		result.ServiceShareToken = shareToken
		return result, nil
	}
	if cluster.AuthorityPrivateKeyFile != "" && r.deps.AuthoritySigner != nil {
		if err := r.deps.AuthoritySigner.MintLocalPublishLease(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc); err != nil {
			return ResolveResult{}, err
		}
		membershipFile, err := r.deps.ArtifactStore.ResolveMembershipCapabilityFile(req.ConfigPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceSeed)
		if err != nil {
			return ResolveResult{}, err
		}
		result := base
		result.Decision = DecisionReady
		result.MembershipCapabilityFile = membershipFile
		result.ServiceShareToken = shareToken
		result.MintedLocally = true
		if shareToken == "" {
			result.ShareRecoveryHint = shareMintHint(cfg.Service.Name, cfg.CurrentCluster, cfg.CurrentNamespace)
		}
		return result, nil
	}
	if grantPeer != "" && r.deps.GrantClient != nil {
		updatedCfg, updatedSvc, updatedShareToken, grantErr := r.deps.GrantClient.RequestPublishGrant(req.ConfigPath, cfg, svc, servicePeerID)
		if grantErr == nil {
			updatedCluster := updatedCfg.Clusters[updatedCfg.CurrentCluster]
			updatedMembershipFile, err := r.deps.ArtifactStore.ResolveMembershipCapabilityFile(req.ConfigPath, updatedCluster, updatedCfg.CurrentCluster, updatedCfg.CurrentNamespace, updatedSvc.ServiceSeed)
			if err != nil {
				return ResolveResult{}, err
			}
			result := ResolveResult{
				Decision:                 DecisionReady,
				Config:                   updatedCfg,
				Service:                  updatedSvc,
				ServicePeerID:            servicePeerID,
				MembershipCapabilityFile: updatedMembershipFile,
				ServiceClaimFile:         updatedSvc.ServiceClaimFile,
				ServicePublishLeaseFile:  updatedSvc.ServicePublishLeaseFile,
				ServiceShareToken:        updatedShareToken,
			}
			if updatedShareToken == "" {
				result.ShareRecoveryHint = shareMintHint(updatedCfg.Service.Name, updatedCfg.CurrentCluster, updatedCfg.CurrentNamespace)
			}
			return result, nil
		}
		result := ResolveResult{
			Config:                  updatedCfg,
			Service:                 updatedSvc,
			ServicePeerID:           servicePeerID,
			ServiceClaimFile:        updatedSvc.ServiceClaimFile,
			ServicePublishLeaseFile: updatedSvc.ServicePublishLeaseFile,
			ServiceShareToken:       updatedShareToken,
			UserMessage:             grantErr.Error(),
		}
		if updatedShareToken == "" {
			result.ShareRecoveryHint = shareRecoveryHint(cfg.Service.Name, cfg.CurrentCluster, cfg.CurrentNamespace, grantPeer, updatedSvc.GrantRequestID)
		}
		switch classifyGrantError(grantErr) {
		case DecisionPendingApproval:
			result.Decision = DecisionPendingApproval
			return result, nil
		case DecisionDenied:
			result.Decision = DecisionDenied
			return result, nil
		default:
			result.Decision = DecisionRetryable
			return result, nil
		}
	}
	result := base
	result.Decision = DecisionRetryable
	result.ServiceShareToken = shareToken
	result.UserMessage = "stored publish authorization requires refresh or mint"
	if shareToken == "" {
		result.ShareRecoveryHint = shareRecoveryHint(cfg.Service.Name, cfg.CurrentCluster, cfg.CurrentNamespace, grantPeer, svc.GrantRequestID)
	}
	return result, nil
}

func (r *resolver) Renew(_ context.Context, req RenewRequest) (ResolveResult, error) {
	if r.deps.ArtifactStore == nil {
		return ResolveResult{}, ErrNotImplemented
	}
	cfg := req.Config
	svc := req.Service
	cluster := cfg.Clusters[cfg.CurrentCluster]
	grantPeer := svc.GrantServicePeer
	if strings.TrimSpace(grantPeer) == "" {
		grantPeer = grantServicePeer(cluster)
	}
	if cluster.AuthorityPrivateKeyFile != "" && r.deps.AuthoritySigner != nil {
		if err := r.deps.AuthoritySigner.MintLocalPublishLease(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc); err != nil {
			return ResolveResult{}, err
		}
		membershipFile, err := r.deps.ArtifactStore.ResolveMembershipCapabilityFile(req.ConfigPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceSeed)
		if err != nil {
			return ResolveResult{}, err
		}
		shareToken, err := r.deps.ArtifactStore.BuildShareToken(cfg, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc)
		if err != nil {
			return ResolveResult{}, err
		}
		return ResolveResult{Decision: DecisionReady, Config: cfg, Service: svc, ServicePeerID: req.ServicePeerID, MembershipCapabilityFile: membershipFile, ServiceClaimFile: svc.ServiceClaimFile, ServicePublishLeaseFile: svc.ServicePublishLeaseFile, ServiceShareToken: shareToken, MintedLocally: true}, nil
	}
	if grantPeer != "" && r.deps.GrantClient != nil {
		updatedCfg, updatedSvc, updatedShareToken, err := r.deps.GrantClient.RenewPublishAuthorization(req.ConfigPath, cfg, svc, req.ServicePeerID)
		if err != nil {
			return ResolveResult{}, err
		}
		updatedCluster := updatedCfg.Clusters[updatedCfg.CurrentCluster]
		updatedMembershipFile, err := r.deps.ArtifactStore.ResolveMembershipCapabilityFile(req.ConfigPath, updatedCluster, updatedCfg.CurrentCluster, updatedCfg.CurrentNamespace, updatedSvc.ServiceSeed)
		if err != nil {
			return ResolveResult{}, err
		}
		return ResolveResult{Decision: DecisionReady, Config: updatedCfg, Service: updatedSvc, ServicePeerID: req.ServicePeerID, MembershipCapabilityFile: updatedMembershipFile, ServiceClaimFile: updatedSvc.ServiceClaimFile, ServicePublishLeaseFile: updatedSvc.ServicePublishLeaseFile, ServiceShareToken: updatedShareToken}, nil
	}
	return ResolveResult{}, fmt.Errorf("service publish lease renewal requires a grant service peer or local authority key")
}

func isPublishLeaseExpiredError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "publish lease expired")
}

func isCapabilityExpiredError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "capability expired")
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

func classifyGrantError(err error) Decision {
	if err == nil {
		return DecisionReady
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, " is pending;") || strings.Contains(msg, " pending;"):
		return DecisionPendingApproval
	case strings.Contains(msg, " denied:"):
		return DecisionDenied
	default:
		return DecisionRetryable
	}
}

func shareMintHint(serviceName, clusterName, namespaceName string) string {
	return fmt.Sprintf("run `tubo share service/%s --cluster %s --namespace %s` to mint a fresh invite token", serviceName, clusterName, namespaceName)
}

func shareRecoveryHint(serviceName, clusterName, namespaceName, grantPeer, grantRequestID string) string {
	if strings.TrimSpace(grantRequestID) != "" && strings.TrimSpace(grantPeer) != "" {
		return fmt.Sprintf("reprint the token with `tubo grants request service/%s --poll --peer %s --cluster %s --namespace %s` (request %s)", serviceName, grantPeer, clusterName, namespaceName, grantRequestID)
	}
	return shareMintHint(serviceName, clusterName, namespaceName)
}
