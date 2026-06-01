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
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

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
