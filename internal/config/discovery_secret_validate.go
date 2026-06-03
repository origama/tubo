package config

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

func validateManagedSecretRef(clusterName, namespaceName, field string, ref *ManagedSecretRef, allowExpires bool) error {
	prefix := fmt.Sprintf("clusters.%s.namespaces.%s.%s", clusterName, namespaceName, field)
	if ref == nil {
		return fmt.Errorf("%s: required", prefix)
	}
	if strings.TrimSpace(ref.Type) != SecretTypeNamespaceDiscovery {
		return fmt.Errorf("%s.type: unsupported value %q", prefix, ref.Type)
	}
	if strings.TrimSpace(ref.KeyID) == "" {
		return fmt.Errorf("%s.key_id: required", prefix)
	}
	if strings.TrimSpace(ref.File) == "" {
		return fmt.Errorf("%s.file: required", prefix)
	}
	if !allowExpires && !ref.ExpiresAt.IsZero() {
		return fmt.Errorf("%s.expires_at: current secret must not have an expiry", prefix)
	}
	secret, err := ReadNamespaceDiscoverySecretFile(ref.File)
	if err != nil {
		return fmt.Errorf("%s.file: %w", prefix, err)
	}
	if len(secret) != NamespaceDiscoverySecretLength {
		return fmt.Errorf("%s.file: namespace discovery secret must be %d bytes", prefix, NamespaceDiscoverySecretLength)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(ref.File); err == nil {
			if perm := info.Mode().Perm(); perm != 0o600 {
				return fmt.Errorf("%s.file: expected permissions 0600, got %04o", prefix, perm)
			}
		}
	}
	return nil
}
