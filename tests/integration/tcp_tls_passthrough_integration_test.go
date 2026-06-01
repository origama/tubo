package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	bridgeapp "github.com/origama/tubo/internal/app/bridge"
	serviceapp "github.com/origama/tubo/internal/app/service"
	capability "github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/discovery"
	"github.com/origama/tubo/internal/p2p"
)

func TestTCPServiceHTTPSPassthrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tlsUpstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintf(w, "secure %s %s %s", r.Method, r.URL.RawQuery, string(body))
	}))
	tlsUpstream.StartTLS()
	defer tlsUpstream.Close()

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
	serviceID := "svc-tcp-tls"

	serviceApp, err := serviceapp.New(ctx, serviceapp.Config{
		Listen:               "/ip4/127.0.0.1/tcp/0",
		Seed:                 "service-tcp-tls-seed",
		ServiceName:          "tlsdemo",
		ServiceKind:          "tcp",
		ServiceID:            serviceID,
		Target:               "tcp://" + tlsUpstream.Listener.Addr().String(),
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
		Seed:         "bridge-tcp-tls-seed",
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

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := client.Get("https://" + bridgeAddr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("unexpected https health response: status=%d body=%q", resp.StatusCode, string(body))
	}

	postResp, err := client.Post("https://"+bridgeAddr+"/secure?via=tcp", "text/plain", strings.NewReader("hello-tls"))
	if err != nil {
		t.Fatal(err)
	}
	postBody, err := io.ReadAll(postResp.Body)
	postResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected https post status: %d body=%q", postResp.StatusCode, string(postBody))
	}
	if got := string(postBody); got != "secure POST via=tcp hello-tls" {
		t.Fatalf("unexpected https passthrough body: %q", got)
	}
}
