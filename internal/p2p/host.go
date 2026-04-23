package p2p

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

type deterministicReader struct {
	seed    []byte
	counter uint64
	buf     []byte
	mu      sync.Mutex
}

func newDeterministicReader(seed string) io.Reader {
	return &deterministicReader{seed: []byte(seed)}
}

func (r *deterministicReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	written := 0
	for written < len(p) {
		if len(r.buf) == 0 {
			h := sha256.New()
			_, _ = h.Write(r.seed)
			_, _ = h.Write([]byte(fmt.Sprintf("#%d", r.counter)))
			r.buf = h.Sum(nil)
			r.counter++
		}
		n := copy(p[written:], r.buf)
		written += n
		r.buf = r.buf[n:]
	}
	return written, nil
}

func NewHost(listenAddr string) (host.Host, error) {
	return NewHostWithSeed(listenAddr, "")
}

func NewHostWithSeed(listenAddr, seed string) (host.Host, error) {
	if listenAddr == "" {
		listenAddr = "/ip4/127.0.0.1/tcp/0"
	}

	var reader io.Reader
	if seed == "" {
		reader = rand.Reader
	} else {
		reader = newDeterministicReader(seed)
	}

	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.Ed25519, -1, reader)
	if err != nil {
		return nil, err
	}

	return libp2p.New(
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.Identity(priv),
	)
}

func PeerIDFromSeed(seed string) (peer.ID, error) {
	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.Ed25519, -1, newDeterministicReader(seed))
	if err != nil {
		return "", err
	}
	return peer.IDFromPrivateKey(priv)
}

func AddrInfoFromListenAndSeed(listenAddr, seed string) (peer.AddrInfo, error) {
	peerID, err := PeerIDFromSeed(seed)
	if err != nil {
		return peer.AddrInfo{}, err
	}
	maddr, err := multiaddr.NewMultiaddr(listenAddr)
	if err != nil {
		return peer.AddrInfo{}, err
	}
	return peer.AddrInfo{ID: peerID, Addrs: []multiaddr.Multiaddr{maddr}}, nil
}

func PeerAddrs(h host.Host) []string {
	peerID := h.ID().String()
	addrs := h.Addrs()
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		p2pAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("%s/p2p/%s", a.String(), peerID))
		if err == nil {
			out = append(out, p2pAddr.String())
		}
	}
	return out
}

func AddrInfoFromString(addr string) (peer.AddrInfo, error) {
	maddr, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return peer.AddrInfo{}, err
	}
	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return peer.AddrInfo{}, err
	}
	return *info, nil
}

func WaitForPeer(ctxDone <-chan struct{}, d time.Duration) bool {
	select {
	case <-ctxDone:
		return true
	case <-time.After(d):
		return false
	}
}
