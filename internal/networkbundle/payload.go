package networkbundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

func DecodePayload(payloadBytes []byte) (*NetworkPayload, error) {
	var payload NetworkPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func ValidatePayload(payload *NetworkPayload) error {
	return ValidatePayloadAt(payload, time.Now().UTC())
}

func ValidatePayloadAt(payload *NetworkPayload, now time.Time) error {
	if payload == nil {
		return errors.New("nil payload")
	}
	if payload.Name == "" {
		return errors.New("payload name is required")
	}
	if payload.ID == "" {
		return errors.New("payload id is required")
	}
	if len(payload.Relays) == 0 {
		return errors.New("payload relays must not be empty")
	}
	for _, relay := range payload.Relays {
		maddr, err := multiaddr.NewMultiaddr(relay)
		if err != nil {
			return fmt.Errorf("invalid relay multiaddr %q: %w", relay, err)
		}
		if _, err := peer.AddrInfoFromP2pAddr(maddr); err != nil {
			return fmt.Errorf("invalid relay bootstrap peer %q: %w", relay, err)
		}
	}
	if payload.PublicCluster != nil {
		if err := validatePublicClusterPayload(payload.PublicCluster); err != nil {
			return err
		}
	}
	if payload.SwarmKey.Type != "libp2p-pnet" {
		return fmt.Errorf("unsupported swarm_key.type %q", payload.SwarmKey.Type)
	}
	if payload.SwarmKey.Encoding != "text" {
		return fmt.Errorf("unsupported swarm_key.encoding %q", payload.SwarmKey.Encoding)
	}
	if !looksLikeSwarmKey(payload.SwarmKey.Value) {
		return errors.New("invalid swarm key format")
	}
	notBefore, err := time.Parse(time.RFC3339, payload.Validity.NotBefore)
	if err != nil {
		return fmt.Errorf("invalid validity.not_before: %w", err)
	}
	notAfter, err := time.Parse(time.RFC3339, payload.Validity.NotAfter)
	if err != nil {
		return fmt.Errorf("invalid validity.not_after: %w", err)
	}
	if notAfter.Before(notBefore) {
		return errors.New("invalid validity window: not_after before not_before")
	}
	if now.Before(notBefore) {
		return errors.New("bundle is not valid yet")
	}
	if now.After(notAfter) {
		return errors.New("bundle validity expired")
	}
	return nil
}

func validatePublicClusterPayload(publicCluster *PublicClusterPayload) error {
	if publicCluster.Name == "" {
		return errors.New("public_cluster.name is required")
	}
	if publicCluster.ClusterID == "" {
		return errors.New("public_cluster.cluster_id is required")
	}
	if publicCluster.AuthorityPublicKey == "" {
		return errors.New("public_cluster.authority_public_key is required")
	}
	if publicCluster.DefaultNamespace == "" {
		return errors.New("public_cluster.default_namespace is required")
	}
	if publicCluster.GrantServiceProtocol == "" {
		return errors.New("public_cluster.grant_service_protocol is required")
	}
	if len(publicCluster.GrantServicePeers) == 0 {
		return errors.New("public_cluster.grant_service_peers must not be empty")
	}
	for _, peerAddr := range publicCluster.GrantServicePeers {
		maddr, err := multiaddr.NewMultiaddr(peerAddr)
		if err != nil {
			return fmt.Errorf("invalid public_cluster grant_service_peer %q: %w", peerAddr, err)
		}
		if _, err := peer.AddrInfoFromP2pAddr(maddr); err != nil {
			return fmt.Errorf("invalid public_cluster grant_service_peer %q: %w", peerAddr, err)
		}
	}
	return nil
}

func looksLikeSwarmKey(value string) bool {
	return strings.Contains(value, "/key/swarm/psk/1.0.0/") && strings.Contains(value, "/base16/")
}
