package catalog

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
)

func TestServiceResourceFromEntryPreservesConnectMetadata(t *testing.T) {
	pid, err := peer.Decode("12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd")
	if err != nil {
		t.Fatal(err)
	}
	service := ServiceResourceFromEntry(&discovery.ServiceEntry{
		ServiceName:   "myapi",
		ServiceID:     "service-123",
		ServiceKind:   "tcp",
		ConnectPolicy: "namespace_members",
		GrantService:  &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{"/ip4/9.8.7.6/tcp/4001/p2p/12D3KooWGrant"}},
		PeerID:        pid,
		Addresses:     []string{"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"},
		Capabilities:  []string{"hello-v1", "raw-tcp-v1"},
		TTL:           30 * time.Second,
		Registered:    time.Now().Add(-time.Second),
	})
	if service.ConnectPolicy != "namespace_members" {
		t.Fatalf("connect policy = %q", service.ConnectPolicy)
	}
	if service.GrantService == nil || service.GrantService.Protocol != grantspkg.ProtocolID {
		t.Fatalf("grant service = %#v", service.GrantService)
	}
	if service.ServiceKind != "tcp" || len(service.Capabilities) != 2 {
		t.Fatalf("service metadata = %#v", service)
	}
}

func TestServiceFromQueryServicePreservesConnectMetadata(t *testing.T) {
	service := ServiceFromQueryService(discoveryquery.Service{
		Kind:          "service",
		ServiceKind:   "tcp",
		Name:          "myapi",
		ServiceID:     "service-123",
		ConnectPolicy: "invite_only",
		GrantService:  &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{"/ip4/9.8.7.6/tcp/4001/p2p/12D3KooWGrant"}},
		PeerID:        "12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd",
		Addresses:     []string{"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"},
		Status:        "online",
		Path:          "direct",
		TTLSeconds:    30,
		Capabilities:  []string{"hello-v1", "raw-tcp-v1"},
		RegisteredAt:  time.Now().UTC().Format(time.RFC3339),
	})
	if service.ConnectPolicy != "invite_only" {
		t.Fatalf("connect policy = %q", service.ConnectPolicy)
	}
	if service.GrantService == nil || len(service.GrantService.Peers) != 1 {
		t.Fatalf("grant service = %#v", service.GrantService)
	}
	if service.ServiceKind != "tcp" || len(service.Capabilities) != 2 {
		t.Fatalf("service metadata = %#v", service)
	}
}
