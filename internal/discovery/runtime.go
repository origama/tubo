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
)

func (m Mode) String() string { return string(m) }

// NamespaceTopic derives an opaque discovery topic for a cluster/namespace pair.
// The topic intentionally avoids leaking the human-readable cluster or namespace.
func NamespaceTopic(clusterID, namespaceID string) string {
	sum := sha256.Sum256([]byte(clusterID + "\x00" + namespaceID))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return "/discovery/v2/" + strings.ToLower(encoded)
}
