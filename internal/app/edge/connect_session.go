package edge

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	capability "github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/protocol"
	"golang.org/x/crypto/ssh"
)

func (gw *Gateway) connectProofForService(ctx context.Context, entry *discovery.ServiceEntry) (*protocol.ConnectProof, error) {
	if gw == nil || entry == nil {
		return nil, nil
	}
	if strings.TrimSpace(gw.connectMembershipCapabilityFile) == "" && strings.TrimSpace(gw.connectMembershipGrantToken) == "" {
		if serviceRequiresProtectedConnect(entry.ConnectPolicy) {
			return nil, fmt.Errorf("protected service %q requires an authorized connect session", entry.ServiceName)
		}
		return nil, nil
	}
	grantPeers := grantServicePeers(entry)
	if len(grantPeers) == 0 {
		return nil, fmt.Errorf("service %q has no grant-service peers for connect authorization", entry.ServiceName)
	}
	clientPublicKey, err := connectClientPublicKey(gw.host)
	if err != nil {
		return nil, err
	}
	var membership *capability.MembershipCapability
	if path := strings.TrimSpace(gw.connectMembershipCapabilityFile); path != "" {
		cap, err := loadMembershipCapability(path)
		if err != nil {
			return nil, fmt.Errorf("load membership capability: %w", err)
		}
		if err := validateGatewayMembershipCapability(cap, gw.authorityPublicKey, entry.ClusterID, entry.NamespaceID, gw.host.ID().String()); err != nil {
			return nil, fmt.Errorf("connect membership capability rejected: %w", err)
		}
		membership = &cap
	}
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	artifacts, err := requestConnectLease(requestCtx, gw.host, grantPeers, entry.ClusterID, entry.NamespaceID, entry.ServiceID, clientPublicKey, membership, gw.connectMembershipGrantToken)
	if err != nil {
		return nil, err
	}
	leaseBytes, err := grantspkg.MarshalConnectAccessLease(artifacts.AccessLease)
	if err != nil {
		return nil, fmt.Errorf("marshal connect access lease: %w", err)
	}
	priv := gw.host.Peerstore().PrivKey(gw.host.ID())
	if priv == nil {
		return nil, fmt.Errorf("no private key for peer")
	}
	raw, err := priv.Raw()
	if err != nil {
		return nil, fmt.Errorf("raw private key: %w", err)
	}
	proof, err := protocol.NewConnectProofWithPayload(artifacts.AccessLease.ClusterID, artifacts.AccessLease.NamespaceID, artifacts.AccessLease.ServiceID, artifacts.AccessLease.ExpiresAt, leaseBytes, grantspkg.ConnectAccessLeaseHashBytes(leaseBytes), gw.host.ID().String(), ed25519.PrivateKey(raw))
	if err != nil {
		return nil, fmt.Errorf("build connect proof: %w", err)
	}
	return &proof, nil
}

func loadMembershipCapability(path string) (capability.MembershipCapability, error) {
	b, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return capability.MembershipCapability{}, err
	}
	var cap capability.MembershipCapability
	if err := json.Unmarshal(b, &cap); err != nil {
		return capability.MembershipCapability{}, err
	}
	return cap, nil
}

func validateGatewayMembershipCapability(cap capability.MembershipCapability, authorityPub ed25519.PublicKey, clusterID, namespaceID, gatewayPeerID string) error {
	if len(authorityPub) == 0 {
		return errors.New("authority public key is required to validate membership capability")
	}
	candidateNamespaces := []string{namespaceID}
	if cap.NamespaceID == "*" {
		candidateNamespaces = append(candidateNamespaces, "*")
	}
	var lastErr error
	for _, subject := range []string{gatewayPeerID, clusterID} {
		for _, candidateNamespace := range candidateNamespaces {
			if err := capability.VerifyMembershipCapability(cap, authorityPub, clusterID, candidateNamespace, subject); err == nil {
				if hasConnectPermission(cap.Permissions) {
					return nil
				}
				return errors.New("membership capability is missing connect permission")
			} else {
				lastErr = err
			}
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("membership capability rejected")
}

func requestConnectLease(ctx context.Context, h host.Host, grantPeers []string, clusterID, namespaceID, serviceID, clientPublicKey string, membership *capability.MembershipCapability, membershipGrantToken string) (grantspkg.ConnectLeaseArtifacts, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var attempted []string
	var lastErr error
	for _, rawPeer := range grantPeers {
		attempted = append(attempted, rawPeer)
		info, err := p2p.AddrInfoFromString(rawPeer)
		if err != nil {
			lastErr = err
			continue
		}
		artifacts, err := grantspkg.RequestConnectLease(requestCtx, h, info, clusterID, namespaceID, serviceID, clientPublicKey, membership, membershipGrantToken)
		if err == nil {
			return artifacts, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return grantspkg.ConnectLeaseArtifacts{}, fmt.Errorf("request connect lease from advertised grant endpoint(s) %s: %w", strings.Join(attempted, ", "), lastErr)
	}
	return grantspkg.ConnectLeaseArtifacts{}, fmt.Errorf("request connect lease: no advertised grant endpoint peers configured")
}

func connectClientPublicKey(h host.Host) (string, error) {
	pub := h.Peerstore().PubKey(h.ID())
	if pub == nil {
		return "", fmt.Errorf("no public key for peer")
	}
	raw, err := pub.Raw()
	if err != nil {
		return "", fmt.Errorf("raw public key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(ed25519.PublicKey(raw))
	if err != nil {
		return "", fmt.Errorf("encode connect client public key: %w", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), nil
}

func grantServicePeers(entry *discovery.ServiceEntry) []string {
	if entry == nil {
		return nil
	}
	if entry.GrantService != nil && len(entry.GrantService.Peers) > 0 {
		return append([]string(nil), entry.GrantService.Peers...)
	}
	return append([]string(nil), entry.Addresses...)
}

func serviceRequiresProtectedConnect(policy string) bool {
	policy = strings.TrimSpace(policy)
	return policy != "" && policy != "public"
}

func hasConnectPermission(perms []string) bool {
	for _, perm := range perms {
		if perm == capability.PermissionConnect {
			return true
		}
	}
	return false
}
