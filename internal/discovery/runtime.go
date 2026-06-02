package discovery

import (
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

type Mode string

const (
	ModeLegacyV1    Mode = "legacy-v1"
	ModeNamespaceV2 Mode = "namespace-v2"
	ModeNamespaceV3 Mode = "namespace-v3"
)

func (m Mode) String() string { return string(m) }

// NamespaceTopic derives an opaque discovery topic for a cluster/namespace pair.
// The topic intentionally avoids leaking the human-readable cluster or namespace.
func NamespaceTopic(clusterID, namespaceID string) string {
	sum := sha256.Sum256([]byte(clusterID + "\x00" + namespaceID))
	return encodeOpaqueTopic("/discovery/v2/", sum[:])
}

func encodeOpaqueTopic(prefix string, derived []byte) string {
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(derived)
	return prefix + strings.ToLower(encoded)
}
