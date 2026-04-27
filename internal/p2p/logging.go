package p2p

import (
	"log"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
)

// LogNetworkEvents emits concise connection lifecycle logs for operational debugging.
func LogNetworkEvents(h host.Host, component string) {
	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, c network.Conn) {
			log.Printf("%s p2p connected peer=%s direction=%s remote_addr=%s", component, c.RemotePeer(), c.Stat().Direction, c.RemoteMultiaddr())
		},
		DisconnectedF: func(_ network.Network, c network.Conn) {
			log.Printf("%s p2p disconnected peer=%s direction=%s remote_addr=%s", component, c.RemotePeer(), c.Stat().Direction, c.RemoteMultiaddr())
		},
	})
}
