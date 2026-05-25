package service

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	grantspkg "github.com/origama/tubo/internal/grants"
)

type serviceGrantEndpoint struct {
	serviceID   string
	clusterID   string
	namespaceID string
	server      *grantspkg.Server
}

func newServiceGrantEndpoint(cfg Config, serviceID string) (*serviceGrantEndpoint, error) {
	if strings.TrimSpace(serviceID) == "" || strings.TrimSpace(cfg.DiscoveryClusterID) == "" || strings.TrimSpace(cfg.DiscoveryNamespaceID) == "" {
		return nil, errors.New("service-scoped grant endpoint requires service and scope identifiers")
	}
	var authorityPriv ed25519.PrivateKey
	if strings.TrimSpace(cfg.AuthorityPrivateKeyFile) != "" {
		var err error
		authorityPriv, err = loadAuthorityPrivateKey(cfg.AuthorityPrivateKeyFile)
		if err != nil {
			return nil, err
		}
	}
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{
		ClusterName:         first(cfg.ClusterName, cfg.DiscoveryClusterID),
		ClusterID:           cfg.DiscoveryClusterID,
		NamespaceID:         cfg.DiscoveryNamespaceID,
		Store:               grantspkg.NewStore(serviceGrantStorePath(cfg, serviceID)),
		AuthorityPrivateKey: authorityPriv,
	})
	if err != nil {
		return nil, err
	}
	return &serviceGrantEndpoint{serviceID: serviceID, clusterID: cfg.DiscoveryClusterID, namespaceID: cfg.DiscoveryNamespaceID, server: server}, nil
}

func (e *serviceGrantEndpoint) HandleStream(stream network.Stream) {
	defer stream.Close()
	msg, err := grantspkg.DecodeMessage(stream)
	if err != nil {
		_ = grantspkg.EncodeMessage(stream, grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: "invalid", Reason: err.Error()})
		return
	}
	resp := e.handleMessage(msg, stream.Conn().RemotePeer())
	if err := grantspkg.EncodeMessage(stream, resp); err != nil {
		_ = stream.Reset()
	}
}

func (e *serviceGrantEndpoint) handleMessage(msg grantspkg.Message, requester peer.ID) grantspkg.Message {
	switch msg.Type {
	case grantspkg.TypeShareRedeem:
		payload, err := grantspkg.ParseAndVerifyServiceShareToken(msg.ShareInviteToken)
		if err != nil {
			return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: "share-redeem", Reason: err.Error()}
		}
		if payload.ClusterID != e.clusterID || payload.NamespaceID != e.namespaceID || payload.TargetServiceID != e.serviceID {
			return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: payload.JTI, Reason: "share invite does not match attached service scope"}
		}
		return e.server.HandleMessage(msg, requester)
	case grantspkg.TypeConnectRefresh:
		if msg.ConnectRefreshLease == nil {
			return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: "connect-refresh", Reason: "connect_refresh_lease is required"}
		}
		lease := msg.ConnectRefreshLease
		if lease.ClusterID != e.clusterID || lease.NamespaceID != e.namespaceID || lease.ServiceID != e.serviceID {
			return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: lease.JTI, Reason: "connect refresh lease does not match attached service scope"}
		}
		return e.server.HandleMessage(msg, requester)
	default:
		return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: fallbackGrantRequestID(msg.RequestID), Reason: "attached-service grant endpoint only exposes service-scoped connect operations"}
	}
}

func advertisedGrantServiceEndpoint(addrs []string) *grantspkg.GrantServiceEndpoint {
	peers := grantspkg.PreferredAdvertisedGrantServicePeers(addrs)
	if len(peers) == 0 {
		return nil
	}
	return &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: peers}
}

func serviceGrantStorePath(cfg Config, serviceID string) string {
	base := strings.TrimSpace(cfg.ServicePublishLeaseFile)
	if base == "" {
		base = strings.TrimSpace(cfg.ServiceClaimFile)
	}
	if base != "" {
		return filepath.Join(filepath.Dir(base), serviceID+".grant-endpoint.requests.json")
	}
	return filepath.Join(os.TempDir(), "tubo-"+serviceID+".grant-endpoint.requests.json")
}

func loadAuthorityPrivateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("cluster authority private key is not PEM encoded")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	switch k := key.(type) {
	case ed25519.PrivateKey:
		return k, nil
	case *ed25519.PrivateKey:
		return *k, nil
	default:
		return nil, fmt.Errorf("unsupported cluster authority private key type %T", key)
	}
}

func fallbackGrantRequestID(id string) string {
	if strings.TrimSpace(id) == "" {
		return "invalid"
	}
	return id
}
