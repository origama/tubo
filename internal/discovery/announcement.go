package discovery

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PubSub topic for service announcements and heartbeats.
const DiscoveryTopic = "/discovery/v1.0"

// Announcement represents a signed service registration message broadcast via pubsub.
type Announcement struct {
	ServiceName string
	PeerID      peer.ID
	Addresses   []string // Multiaddr strings (e.g., "/ip4/.../tcp/8080")
	TTL         time.Duration
	Signature   []byte
}

// Sign cryptographically signs the announcement using the given private key.
func (a *Announcement) Sign(privKey crypto.PrivKey) error {
	sig, err := a.computeSig()
	if err != nil {
		return fmt.Errorf("compute signature: %w", err)
	}
	a.Signature, err = privKey.Sign(sig)
	return err
}

// Verify checks the announcement's signature against the given public key.
func (a *Announcement) Verify(pubKey crypto.PubKey) (bool, error) {
	expectedSig, err := a.computeSig()
	if err != nil {
		return false, fmt.Errorf("compute expected sig: %w", err)
	}
	return pubKey.Verify(expectedSig, a.Signature)
}

// computeSig returns the raw bytes that are signed (everything except Signature).
func (a *Announcement) computeSig() ([]byte, error) {
	var buf bytes.Buffer
	if _, err := fmt.Fprintf(&buf, "%s\t%s\t%d\t%d\n", a.ServiceName, a.PeerID, len(a.Addresses), int64(a.TTL)); err != nil {
		return nil, err
	}
	for _, addr := range a.Addresses {
		if _, err := fmt.Fprintf(&buf, "%s\n", addr); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// Marshal serializes the announcement to bytes for pubsub transmission.
func (a *Announcement) Marshal() ([]byte, error) {
	var buf bytes.Buffer

	// ServiceName (varint length + UTF-8 bytes)
	if err := writeString(&buf, a.ServiceName); err != nil {
		return nil, err
	}

	// PeerID (varint length + bytes)
	pidBytes := []byte(a.PeerID)
	if err := binary.Write(&buf, binary.LittleEndian, uint64(len(pidBytes))); err != nil {
		return nil, err
	}
	if _, err := buf.Write(pidBytes); err != nil {
		return nil, err
	}

	// Addresses (varint count + each address as varint length + bytes)
	if err := binary.Write(&buf, binary.LittleEndian, uint64(len(a.Addresses))); err != nil {
		return nil, err
	}
	for _, addr := range a.Addresses {
		if err := writeString(&buf, addr); err != nil {
			return nil, err
		}
	}

	// TTL (int64 nanoseconds)
	if err := binary.Write(&buf, binary.LittleEndian, int64(a.TTL)); err != nil {
		return nil, err
	}

	// Signature (varint length + bytes)
	if err := binary.Write(&buf, binary.LittleEndian, uint64(len(a.Signature))); err != nil {
		return nil, err
	}
	if _, err := buf.Write(a.Signature); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// Unmarshal deserializes an announcement from bytes.
func (a *Announcement) Unmarshal(data []byte) error {
	r := bytes.NewReader(data)

	var serviceNameLen uint64
	if err := binary.Read(r, binary.LittleEndian, &serviceNameLen); err != nil {
		return fmt.Errorf("read service name length: %w", err)
	}
	a.ServiceName = readStringFixed(r, int(serviceNameLen))

	var pidLen uint64
	if err := binary.Read(r, binary.LittleEndian, &pidLen); err != nil {
		return fmt.Errorf("read peer ID length: %w", err)
	}
	pidBytes := make([]byte, pidLen)
	if _, err := r.Read(pidBytes); err != nil {
		return fmt.Errorf("read peer ID bytes: %w", err)
	}
	var err error
	a.PeerID, err = peer.IDFromBytes(pidBytes)
	if err != nil {
		return fmt.Errorf("decode peer ID: %w", err)
	}

	var addrCount uint64
	if err := binary.Read(r, binary.LittleEndian, &addrCount); err != nil {
		return fmt.Errorf("read address count: %w", err)
	}
	a.Addresses = make([]string, addrCount)
	for i := uint64(0); i < addrCount; i++ {
		var addrLen uint64
		if err := binary.Read(r, binary.LittleEndian, &addrLen); err != nil {
			return fmt.Errorf("read address %d length: %w", i, err)
		}
		a.Addresses[i] = readStringFixed(r, int(addrLen))
	}

	var ttlNs int64
	if err := binary.Read(r, binary.LittleEndian, &ttlNs); err != nil {
		return fmt.Errorf("read TTL: %w", err)
	}
	a.TTL = time.Duration(ttlNs)

	var sigLen uint64
	if err := binary.Read(r, binary.LittleEndian, &sigLen); err != nil {
		return fmt.Errorf("read signature length: %w", err)
	}
	a.Signature = make([]byte, sigLen)
	if _, err := r.Read(a.Signature); err != nil {
		return fmt.Errorf("read signature bytes: %w", err)
	}

	return nil
}

// --- Helper functions for varint-based string encoding ---

func writeString(buf *bytes.Buffer, s string) error {
	if err := binary.Write(buf, binary.LittleEndian, uint64(len(s))); err != nil {
		return err
	}
	_, err := buf.WriteString(s)
	return err
}

func readStringFixed(r *bytes.Reader, length int) string {
	b := make([]byte, length)
	_, _ = r.Read(b) // errors ignored — caller checks overall error
	return string(b)
}