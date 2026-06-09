package integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/libp2p/go-libp2p/core/peer"
	bridgeapp "github.com/origama/tubo/internal/app/bridge"
	serviceapp "github.com/origama/tubo/internal/app/service"
	capability "github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/discovery"
	"github.com/origama/tubo/internal/p2p"
)

func TestTCPServiceEchoLargeAndConcurrent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			conn, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authAuthorized, err := ssh.NewPublicKey(authPub)
	if err != nil {
		t.Fatal(err)
	}
	authKeyText := string(ssh.MarshalAuthorizedKey(authAuthorized))
	clusterID := "cluster-a"
	namespaceID := "default"
	serviceID := "svc-tcp-a"

	serviceApp, err := serviceapp.New(ctx, serviceapp.Config{
		Listen:               "/ip4/127.0.0.1/tcp/0",
		Seed:                 "service-tcp-seed",
		ServiceName:          "tlsdemo",
		ServiceKind:          "tcp",
		ServiceID:            serviceID,
		Target:               "tcp://" + echoLn.Addr().String(),
		DiscoveryMode:        discovery.ModeNamespaceV2.String(),
		DiscoveryClusterID:   clusterID,
		DiscoveryNamespaceID: namespaceID,
		AuthorityPublicKey:   authKeyText,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer serviceApp.Host().Close()
	serviceAddr := p2p.PeerAddrs(serviceApp.Host())[0]

	grant, err := capability.SignConnectCapability(capability.ConnectCapability{
		ClusterID:   clusterID,
		NamespaceID: namespaceID,
		ServiceID:   serviceID,
		Permissions: []string{capability.PermissionConnect},
		ExpiresAt:   time.Now().Add(time.Hour),
	}, authPriv)
	if err != nil {
		t.Fatal(err)
	}

	app, err := bridgeapp.New(ctx, bridgeapp.Config{
		Listen:       "127.0.0.1:0",
		Seed:         "bridge-tcp-seed",
		P2PListen:    "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:  serviceAddr,
		ServiceKind:  "tcp",
		Autorelay:    false,
		HolePunching: false,
		ConnectGrant: &grant,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Start(ctx) }()
	bridgeAddr := waitForTCPBridgeAddr(t, app)

	roundTrip := func(payload []byte) {
		t.Helper()
		conn, err := net.DialTimeout("tcp", bridgeAddr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.Write(payload); err != nil {
			t.Fatal(err)
		}
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
		got, err := io.ReadAll(conn)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("payload mismatch: got %d bytes want %d", len(got), len(payload))
		}
	}

	roundTrip([]byte("hello-tcp"))
	roundTrip(bytes.Repeat([]byte("L"), 256*1024))

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := []byte(fmt.Sprintf("concurrent-%d-%s", i, strings.Repeat("x", 2048)))
			conn, err := net.DialTimeout("tcp", bridgeAddr, 5*time.Second)
			if err != nil {
				t.Errorf("dial %d: %v", i, err)
				return
			}
			defer conn.Close()
			if _, err := conn.Write(payload); err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				_ = tcpConn.CloseWrite()
			}
			got, err := io.ReadAll(conn)
			if err != nil {
				t.Errorf("read %d: %v", i, err)
				return
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("payload mismatch %d", i)
			}
		}(i)
	}
	wg.Wait()
}

func TestTCPServiceControlHalfCloseSurvivesConcurrentData(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var acceptSeq atomic.Int32
	serverErr := make(chan error, 2)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			seq := acceptSeq.Add(1)
			go func(c net.Conn, seq int32) {
				defer c.Close()
				switch seq {
				case 1:
					payload, err := io.ReadAll(c)
					if err != nil {
						serverErr <- fmt.Errorf("control read: %w", err)
						return
					}
					if len(payload) != 166 {
						serverErr <- fmt.Errorf("control payload len=%d want 166", len(payload))
						return
					}
					if _, err := c.Write([]byte("ACK\n")); err != nil {
						serverErr <- fmt.Errorf("control ack write: %w", err)
						return
					}
				case 2:
					payload, err := io.ReadAll(c)
					if err != nil {
						serverErr <- fmt.Errorf("data read: %w", err)
						return
					}
					if len(payload) != 8*1024*1024 {
						serverErr <- fmt.Errorf("data payload len=%d want %d", len(payload), 8*1024*1024)
						return
					}
				default:
					serverErr <- fmt.Errorf("unexpected extra connection %d", seq)
				}
			}(conn, seq)
		}
	}()

	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authAuthorized, err := ssh.NewPublicKey(authPub)
	if err != nil {
		t.Fatal(err)
	}
	authKeyText := string(ssh.MarshalAuthorizedKey(authAuthorized))
	clusterID := "cluster-halfclose"
	namespaceID := "default"
	serviceID := "svc-halfclose"

	serviceApp, err := serviceapp.New(ctx, serviceapp.Config{
		Listen:               "/ip4/127.0.0.1/tcp/0",
		Seed:                 "service-halfclose-seed",
		ServiceName:          "halfclose",
		ServiceKind:          "tcp",
		ServiceID:            serviceID,
		Target:               "tcp://" + ln.Addr().String(),
		DiscoveryMode:        discovery.ModeNamespaceV2.String(),
		DiscoveryClusterID:   clusterID,
		DiscoveryNamespaceID: namespaceID,
		AuthorityPublicKey:   authKeyText,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer serviceApp.Host().Close()
	serviceAddr := p2p.PeerAddrs(serviceApp.Host())[0]

	grant, err := capability.SignConnectCapability(capability.ConnectCapability{
		ClusterID:   clusterID,
		NamespaceID: namespaceID,
		ServiceID:   serviceID,
		Permissions: []string{capability.PermissionConnect},
		ExpiresAt:   time.Now().Add(time.Hour),
	}, authPriv)
	if err != nil {
		t.Fatal(err)
	}

	app, err := bridgeapp.New(ctx, bridgeapp.Config{
		Listen:       "127.0.0.1:0",
		Seed:         "bridge-halfclose-seed",
		P2PListen:    "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:  serviceAddr,
		ServiceKind:  "tcp",
		Autorelay:    false,
		HolePunching: false,
		ConnectGrant: &grant,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Start(ctx) }()
	bridgeAddr := waitForTCPBridgeAddr(t, app)

	controlConn, err := net.DialTimeout("tcp", bridgeAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer controlConn.Close()
	controlPayload := bytes.Repeat([]byte("c"), 166)
	if _, err := controlConn.Write(controlPayload); err != nil {
		t.Fatal(err)
	}
	if tcpConn, ok := controlConn.(*net.TCPConn); ok {
		_ = tcpConn.CloseWrite()
	}

	_ = controlConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(controlConn)
	if err != nil {
		t.Fatalf("control read: %v", err)
	}
	if string(got) != "ACK\n" {
		t.Fatalf("control response = %q, want %q", string(got), "ACK\n")
	}

	dataConn, err := net.DialTimeout("tcp", bridgeAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	dataPayload := bytes.Repeat([]byte("d"), 8*1024*1024)
	if _, err := dataConn.Write(dataPayload); err != nil {
		dataConn.Close()
		t.Fatal(err)
	}
	if tcpConn, ok := dataConn.(*net.TCPConn); ok {
		_ = tcpConn.CloseWrite()
	}
	_, _ = io.Copy(io.Discard, dataConn)
	_ = dataConn.Close()
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
}

func TestTCPBridgeRebindsPinnedServiceAfterRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upstream1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream1.Close()
	go serveTCPPrefix(upstream1, []byte("first:"))

	upstream2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream2.Close()
	go serveTCPPrefix(upstream2, []byte("second:"))

	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authAuthorized, err := ssh.NewPublicKey(authPub)
	if err != nil {
		t.Fatal(err)
	}
	authKeyText := string(ssh.MarshalAuthorizedKey(authAuthorized))
	clusterID := "cluster-rebind"
	namespaceID := "default"
	serviceID := "svc-rebind"

	serviceHealth1 := freePort(t)
	serviceCtx1, service1Cancel := context.WithCancel(ctx)
	defer service1Cancel()
	service1, err := serviceapp.New(serviceCtx1, serviceapp.Config{
		Listen:                 "/ip4/127.0.0.1/tcp/0",
		Seed:                   "bridge-rebind-service-1",
		ServiceName:            "rebind",
		ServiceKind:            "tcp",
		ServiceID:              serviceID,
		Target:                 "tcp://" + upstream1.Addr().String(),
		HealthListen:           fmt.Sprintf("127.0.0.1:%d", serviceHealth1),
		HeartbeatInterval:      500 * time.Millisecond,
		BootstrapRetryInterval: 500 * time.Millisecond,
		DiscoveryMode:          discovery.ModeNamespaceV2.String(),
		DiscoveryClusterID:     clusterID,
		DiscoveryNamespaceID:   namespaceID,
		AuthorityPublicKey:     authKeyText,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service1.Host().Close()
	go func() { _ = service1.Start(serviceCtx1) }()
	waitUntil(t, 15*time.Second, func() bool { return httpOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", serviceHealth1)) }, "service1 health")

	grant, err := capability.SignConnectCapability(capability.ConnectCapability{
		ClusterID:   clusterID,
		NamespaceID: namespaceID,
		ServiceID:   serviceID,
		Permissions: []string{capability.PermissionConnect},
		ExpiresAt:   time.Now().Add(time.Hour),
	}, authPriv)
	if err != nil {
		t.Fatal(err)
	}

	var rebindMu sync.RWMutex
	currentServiceAddr := p2p.PeerAddrs(service1.Host())[0]
	currentServicePath := "direct"
	resolver := func(context.Context) (peer.AddrInfo, string, string, error) {
		rebindMu.RLock()
		addr := currentServiceAddr
		path := currentServicePath
		rebindMu.RUnlock()
		addrInfo, err := p2p.AddrInfoFromString(addr)
		if err != nil {
			return peer.AddrInfo{}, "", "", err
		}
		return addrInfo, addr, path, nil
	}

	bridgeApp, err := bridgeapp.New(ctx, bridgeapp.Config{
		Listen:                "127.0.0.1:0",
		Seed:                  "bridge-rebind-seed",
		P2PListen:             "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:           currentServiceAddr,
		ServiceKind:           "tcp",
		ConnectGrant:          &grant,
		ConnectServiceID:      serviceID,
		ConnectRebindResolver: resolver,
		SelectedAddr:          currentServiceAddr,
		SelectedPath:          currentServicePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = bridgeApp.Start(ctx) }()
	bridgeAddr := waitForTCPBridgeAddr(t, bridgeApp)

	roundTrip := func(payload, wantPrefix string) {
		t.Helper()
		conn, err := net.DialTimeout("tcp", bridgeAddr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
		got, err := io.ReadAll(conn)
		if err != nil {
			t.Fatal(err)
		}
		want := []byte(wantPrefix + payload)
		if !bytes.Equal(got, want) {
			t.Fatalf("payload mismatch: got %q want %q", string(got), string(want))
		}
	}

	roundTrip("one", "first:")

	service1Cancel()
	serviceHealth2 := freePort(t)
	serviceCtx2, service2Cancel := context.WithCancel(ctx)
	defer service2Cancel()
	service2, err := serviceapp.New(serviceCtx2, serviceapp.Config{
		Listen:                 "/ip4/127.0.0.1/tcp/0",
		Seed:                   "bridge-rebind-service-2",
		ServiceName:            "rebind",
		ServiceKind:            "tcp",
		ServiceID:              serviceID,
		Target:                 "tcp://" + upstream2.Addr().String(),
		HealthListen:           fmt.Sprintf("127.0.0.1:%d", serviceHealth2),
		HeartbeatInterval:      500 * time.Millisecond,
		BootstrapRetryInterval: 500 * time.Millisecond,
		DiscoveryMode:          discovery.ModeNamespaceV2.String(),
		DiscoveryClusterID:     clusterID,
		DiscoveryNamespaceID:   namespaceID,
		AuthorityPublicKey:     authKeyText,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service2.Host().Close()
	go func() { _ = service2.Start(serviceCtx2) }()
	waitUntil(t, 15*time.Second, func() bool { return httpOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", serviceHealth2)) }, "service2 health")
	rebindMu.Lock()
	currentServiceAddr = p2p.PeerAddrs(service2.Host())[0]
	rebindMu.Unlock()

	roundTrip("two", "second:")
}

func serveTCPPrefix(ln net.Listener, prefix []byte) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			payload, _ := io.ReadAll(c)
			_, _ = c.Write(append(prefix, payload...))
		}(conn)
	}
}

func waitForTCPBridgeAddr(t *testing.T, app interface{ ListenAddr() string }) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if addr := app.ListenAddr(); addr != "" && addr != "127.0.0.1:0" && !strings.HasSuffix(addr, ":0") {
			return addr
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("bridge tcp app did not start listening")
	return ""
}
