package query

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/origama/tubo/internal/discovery"
	"github.com/origama/tubo/internal/p2p"
)

func TestResponseForRequestListAndGet(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	pid, err := peer.Decode("12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd")
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Add(pid, "myapi", []string{"/ip4/127.0.0.1/tcp/40123/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd", "/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	h, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-test-host")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	list := responseForRequest(h, "relay", cache, Request{Type: RequestTypeList})
	if list.Metadata.ServedByRole != "relay" || len(list.Services) != 1 {
		t.Fatalf("unexpected list response: %#v", list)
	}
	if list.Services[0].Path != "direct" || len(list.Services[0].DirectAddresses) != 1 || len(list.Services[0].RelayedAddresses) != 1 {
		t.Fatalf("unexpected list service: %#v", list.Services[0])
	}

	get := responseForRequest(h, "relay", cache, Request{Type: RequestTypeGet, Name: "myapi"})
	if get.Service == nil || get.Service.Name != "myapi" {
		t.Fatalf("unexpected get response: %#v", get)
	}
	miss := responseForRequest(h, "relay", cache, Request{Type: RequestTypeGet, Name: "missing"})
	if miss.Error != "service not found" {
		t.Fatalf("unexpected miss response: %#v", miss)
	}
}

func TestResponseForRequestAnnounce(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	h, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-announce-host")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	service := Service{Name: "myapi", PeerID: h.ID().String(), Addresses: []string{"/ip4/127.0.0.1/tcp/40123/p2p/" + h.ID().String()}, TTLSeconds: 30}
	resp := responseForRequest(h, "relay", cache, Request{Type: RequestTypeAnnounce, Service: &service})
	if resp.Error != "" {
		t.Fatalf("unexpected announce error: %#v", resp)
	}
	if got := cache.Count(); got != 1 {
		t.Fatalf("cache count = %d, want 1", got)
	}
}

func TestRequestResponseJSONRoundTrip(t *testing.T) {
	buf := new(bytes.Buffer)
	wantReq := Request{Type: RequestTypeGet, Name: "myapi"}
	if err := json.NewEncoder(buf).Encode(wantReq); err != nil {
		t.Fatal(err)
	}
	var gotReq Request
	if err := json.NewDecoder(buf).Decode(&gotReq); err != nil {
		t.Fatal(err)
	}
	if gotReq != wantReq {
		t.Fatalf("request round trip = %#v, want %#v", gotReq, wantReq)
	}

	buf.Reset()
	wantResp := Response{Metadata: Metadata{ServedBy: "12D3", ServedByRole: "relay", CacheTime: time.Now().Format(time.RFC3339)}, Services: []Service{{Name: "myapi", Path: "direct"}}}
	if err := json.NewEncoder(buf).Encode(wantResp); err != nil {
		t.Fatal(err)
	}
	var gotResp Response
	if err := json.NewDecoder(buf).Decode(&gotResp); err != nil {
		t.Fatal(err)
	}
	if gotResp.Metadata.ServedByRole != wantResp.Metadata.ServedByRole || len(gotResp.Services) != 1 || gotResp.Services[0].Name != "myapi" {
		t.Fatalf("response round trip = %#v", gotResp)
	}
}

func TestListServicesAcrossRealStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-server")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	if err := cache.Add(server.ID(), "myapi", []string{p2p.PeerAddrs(server)[0]}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	server.SetStreamHandler(ProtocolID, HandleStream(server, "gateway", cache))

	info, err := p2p.AddrInfoFromString(p2p.PeerAddrs(server)[0])
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ListServices(ctx, client, info)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Metadata.ServedByRole != "gateway" || len(resp.Services) != 1 || resp.Services[0].Name != "myapi" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
