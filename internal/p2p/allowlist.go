package p2p

import (
	"fmt"
	"os"
	"strings"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const allowedPeersEnv = "LIBP2P_ALLOWED_PEERS"

// LoadAllowedPeersFromEnv parses LIBP2P_ALLOWED_PEERS as a comma-separated peer.ID list.
// Returns the parsed set, whether the env is configured, and an error (if parsing fails).
func LoadAllowedPeersFromEnv() (map[peer.ID]struct{}, bool, error) {
	raw := strings.TrimSpace(os.Getenv(allowedPeersEnv))
	if raw == "" {
		return nil, false, nil
	}

	parts := strings.Split(raw, ",")
	allowed := make(map[peer.ID]struct{}, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		pid, err := peer.Decode(p)
		if err != nil {
			return nil, true, fmt.Errorf("parse %s entry %q: %w", allowedPeersEnv, p, err)
		}
		allowed[pid] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, true, fmt.Errorf("%s is set but contains no valid peer IDs", allowedPeersEnv)
	}
	return allowed, true, nil
}

// PeerAllowlistConnectionGater blocks secured inbound/outbound connections for peers
// not present in the configured allowlist.
type PeerAllowlistConnectionGater struct {
	allowed map[peer.ID]struct{}
}

var _ connmgr.ConnectionGater = (*PeerAllowlistConnectionGater)(nil)

func NewPeerAllowlistConnectionGater(allowed map[peer.ID]struct{}) *PeerAllowlistConnectionGater {
	cp := make(map[peer.ID]struct{}, len(allowed))
	for p := range allowed {
		cp[p] = struct{}{}
	}
	return &PeerAllowlistConnectionGater{allowed: cp}
}

func (g *PeerAllowlistConnectionGater) InterceptPeerDial(p peer.ID) (allow bool) {
	return g.isAllowed(p)
}

func (g *PeerAllowlistConnectionGater) InterceptAddrDial(p peer.ID, _ multiaddr.Multiaddr) (allow bool) {
	if p == "" {
		return true
	}
	return g.isAllowed(p)
}

func (g *PeerAllowlistConnectionGater) InterceptAccept(_ network.ConnMultiaddrs) (allow bool) {
	// Peer identity is not known yet at this stage.
	return true
}

func (g *PeerAllowlistConnectionGater) InterceptSecured(_ network.Direction, p peer.ID, _ network.ConnMultiaddrs) (allow bool) {
	return g.isAllowed(p)
}

func (g *PeerAllowlistConnectionGater) InterceptUpgraded(_ network.Conn) (allow bool, reason control.DisconnectReason) {
	return true, 0
}

func (g *PeerAllowlistConnectionGater) isAllowed(p peer.ID) bool {
	_, ok := g.allowed[p]
	return ok
}
