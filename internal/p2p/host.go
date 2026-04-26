package p2p

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
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
	return NewHostWithSeedAndPSK(listenAddr, seed, nil)
}

func NewHostWithSeedAndPSK(listenAddr, seed string, psk pnet.PSK) (host.Host, error) {
	return NewHostWithSeedAndPSKAndOptions(listenAddr, seed, psk)
}

func NewHostWithSeedAndPSKAndOptions(listenAddr, seed string, psk pnet.PSK, extraOpts ...libp2p.Option) (host.Host, error) {
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

	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.Identity(priv),
	}
	if len(psk) > 0 {
		opts = append(opts, libp2p.PrivateNetwork(psk))
	}
	opts = append(opts, extraOpts...)

	return libp2p.New(opts...)
}

// LoadPrivateNetworkPSKFromEnv loads a libp2p private network key from env vars.
// Supported env vars:
// - LIBP2P_PRIVATE_NETWORK_KEY: path to swarm.key file format
// - LIBP2P_PRIVATE_NETWORK_KEY_B64: base64-encoded raw 32-byte PSK
func LoadPrivateNetworkPSKFromEnv() (pnet.PSK, bool, error) {
	keyPath := os.Getenv("LIBP2P_PRIVATE_NETWORK_KEY")
	keyB64 := os.Getenv("LIBP2P_PRIVATE_NETWORK_KEY_B64")

	if keyPath != "" && keyB64 != "" {
		return nil, false, fmt.Errorf("set either LIBP2P_PRIVATE_NETWORK_KEY or LIBP2P_PRIVATE_NETWORK_KEY_B64, not both")
	}

	if keyPath != "" {
		f, err := os.Open(keyPath)
		if err != nil {
			return nil, false, fmt.Errorf("open LIBP2P_PRIVATE_NETWORK_KEY: %w", err)
		}
		defer f.Close()

		psk, err := pnet.DecodeV1PSK(f)
		if err != nil {
			return nil, false, fmt.Errorf("decode LIBP2P_PRIVATE_NETWORK_KEY: %w", err)
		}
		if len(psk) == 0 {
			return nil, false, fmt.Errorf("decoded empty PSK from LIBP2P_PRIVATE_NETWORK_KEY")
		}
		return psk, true, nil
	}

	if keyB64 != "" {
		raw, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(keyB64)
			if err != nil {
				return nil, false, fmt.Errorf("decode LIBP2P_PRIVATE_NETWORK_KEY_B64: %w", err)
			}
		}
		if len(raw) != 32 {
			return nil, false, fmt.Errorf("LIBP2P_PRIVATE_NETWORK_KEY_B64 must decode to 32 bytes, got %d", len(raw))
		}
		psk := make([]byte, len(raw))
		copy(psk, raw)
		return psk, true, nil
	}

	return nil, false, nil
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
