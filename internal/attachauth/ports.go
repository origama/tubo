package attachauth

import (
	"crypto/ed25519"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
)

type IdentityStore interface {
	EnsureAttachServiceIdentity(configPath string, cfg cfgpkg.Config) (cfgpkg.Config, cfgpkg.NamespaceService, error)
	ServicePeerID(seed string) (string, error)
}

type ArtifactStore interface {
	VerifyPublishLease(path string, authorityPublicKey ed25519.PublicKey, clusterID, namespaceID, serviceID, servicePeerID string) error
	VerifyServiceClaim(path string, authorityPublicKey ed25519.PublicKey, clusterID, namespaceID, serviceID, servicePeerID string) error
	ResolveMembershipCapabilityFile(configPath string, cluster cfgpkg.Cluster, clusterName, namespaceName, serviceSeed string) (string, error)
	BuildShareToken(cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName string, svc cfgpkg.NamespaceService) (string, error)
	ReadPublishLease(path string) (grantspkg.PublishLease, error)
}

type AuthoritySigner interface {
	MintLocalPublishLease(cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName string, svc cfgpkg.NamespaceService) error
}

type GrantClient interface {
	RequestPublishGrant(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error)
	RenewPublishAuthorization(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error)
}

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}
