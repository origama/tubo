package main

import (
	"crypto/ed25519"

	attachauth "github.com/origama/tubo/internal/attachauth"
	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
)

type attachAuthIdentityStore struct{}

type attachAuthArtifactStore struct{}

type attachAuthAuthoritySigner struct{}

type attachAuthGrantClient struct{}

func newAttachAuthResolver() attachauth.Resolver {
	return attachauth.New(attachauth.Dependencies{
		IdentityStore:   attachAuthIdentityStore{},
		ArtifactStore:   attachAuthArtifactStore{},
		AuthoritySigner: attachAuthAuthoritySigner{},
		GrantClient:     attachAuthGrantClient{},
		Clock:           attachauth.SystemClock{},
	})
}

func (attachAuthIdentityStore) EnsureAttachServiceIdentity(configPath string, cfg cfgpkg.Config) (cfgpkg.Config, cfgpkg.NamespaceService, error) {
	return ensureAttachServiceIdentity(configPath, cfg)
}

func (attachAuthIdentityStore) ServicePeerID(seed string) (string, error) {
	peerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		return "", err
	}
	return peerID.String(), nil
}

func (attachAuthArtifactStore) VerifyPublishLease(path string, authorityPublicKey ed25519.PublicKey, clusterID, namespaceID, serviceID, servicePeerID string) error {
	return verifyPublishLeaseFile(path, authorityPublicKey, clusterID, namespaceID, serviceID, servicePeerID)
}

func (attachAuthArtifactStore) VerifyServiceClaim(path string, authorityPublicKey ed25519.PublicKey, clusterID, namespaceID, serviceID, servicePeerID string) error {
	return verifyServiceClaimFile(path, authorityPublicKey, clusterID, namespaceID, serviceID, servicePeerID)
}

func (attachAuthArtifactStore) ResolveMembershipCapabilityFile(configPath string, cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName, serviceSeed string) (string, error) {
	return resolveAttachMembershipCapabilityFile(configPath, cluster, clusterName, namespaceName, serviceName, serviceSeed)
}

func (attachAuthArtifactStore) BuildShareToken(cfg cfgpkg.Config, cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName string, svc cfgpkg.NamespaceService) (string, error) {
	return buildAttachServiceShareToken(cfg, cluster, clusterName, namespaceName, serviceName, svc)
}

func (attachAuthArtifactStore) ReadPublishLease(path string) (grantspkg.PublishLease, error) {
	return readPublishLeaseFile(path)
}

func (attachAuthAuthoritySigner) MintLocalPublishLease(cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName string, svc cfgpkg.NamespaceService) error {
	return mintLocalServicePublishLease(cluster, clusterName, namespaceName, serviceName, svc)
}

func (attachAuthGrantClient) RequestPublishGrant(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	return requestPublishGrantForAttach(configPath, cfg, svc, servicePeerID)
}

func (attachAuthGrantClient) RenewPublishAuthorization(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	return renewAttachPublishAuthorization(configPath, cfg, svc, servicePeerID)
}
