package p2p

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/origama/tubo/internal/protocol"
)

func TestServiceStreamHandlersTimeoutIncompleteHandshake(t *testing.T) {
	tests := []struct {
		name    string
		handler network.StreamHandler
	}{
		{
			name: "http",
			handler: HandleServiceStreamWithOptions("http://127.0.0.1:1", nil, ServiceStreamOptions{
				HandshakeTimeout: 50 * time.Millisecond,
			}),
		},
		{
			name: "tcp",
			handler: HandleServiceTCPStreamWithOptions("tcp://127.0.0.1:1", nil, ServiceStreamOptions{
				HandshakeTimeout: 50 * time.Millisecond,
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			serviceHost, err := NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "timeout-service-"+tt.name)
			if err != nil {
				t.Fatal(err)
			}
			defer serviceHost.Close()
			serviceHost.SetStreamHandler(ProtocolID, tt.handler)

			clientHost, err := NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "timeout-client-"+tt.name)
			if err != nil {
				t.Fatal(err)
			}
			defer clientHost.Close()
			serviceInfo, err := AddrInfoFromString(PeerAddrs(serviceHost)[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := clientHost.Connect(ctx, serviceInfo); err != nil {
				t.Fatal(err)
			}
			stream, err := clientHost.NewStream(ctx, serviceHost.ID(), SupportedProtocolIDs()...)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			_ = stream.SetReadDeadline(time.Now().Add(2 * time.Second))

			started := time.Now()
			errFrame, err := protocol.NewStreamReader(stream).ReadError()
			if err != nil {
				t.Fatalf("read timeout error frame: %v", err)
			}
			message := strings.ToLower(errFrame.Message)
			if errFrame.Code != http.StatusBadRequest || (!strings.Contains(message, "timeout") && !strings.Contains(message, "deadline")) {
				t.Fatalf("timeout error frame = %#v", errFrame)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("handshake timeout took %s, want <= 1s", elapsed)
			}
		})
	}
}

func TestHandleServiceStreamBoundsUpstreamResponseHeaderAndCancelsRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
		close(requestCanceled)
	}))
	defer upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serviceHost, err := NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "timeout-upstream-service")
	if err != nil {
		t.Fatal(err)
	}
	defer serviceHost.Close()
	serviceHost.SetStreamHandler(ProtocolID, HandleServiceStreamWithOptions(upstream.URL, nil, ServiceStreamOptions{
		HandshakeTimeout:      time.Second,
		ResponseHeaderTimeout: 100 * time.Millisecond,
	}))

	clientHost, err := NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "timeout-upstream-client")
	if err != nil {
		t.Fatal(err)
	}
	defer clientHost.Close()
	serviceInfo, err := AddrInfoFromString(PeerAddrs(serviceHost)[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := clientHost.Connect(ctx, serviceInfo); err != nil {
		t.Fatal(err)
	}
	stream, err := clientHost.NewStream(ctx, serviceHost.ID(), SupportedProtocolIDs()...)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(2 * time.Second))

	started := time.Now()
	_, err = HandleClientRequest(stream, "bridge", http.MethodGet, "/stall", "", nil, nil, nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("HandleClientRequest error = %v, want upstream response-header timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("upstream timeout took %s, want <= 1s", elapsed)
	}
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("upstream request context was not canceled")
	}
}
