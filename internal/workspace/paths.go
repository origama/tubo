package workspace

import (
	"path/filepath"
	"strings"
)

type Paths struct {
	ConfigDir string
}

func DerivePaths(configPath string) Paths {
	return Paths{ConfigDir: filepath.Dir(configPath)}
}

func (p Paths) ClusterDir(cluster string) string {
	return filepath.Join(p.ConfigDir, "clusters", sanitizeName(cluster))
}

func (p Paths) NamespaceDir(cluster, namespace string) string {
	return filepath.Join(p.ClusterDir(cluster), "namespaces", sanitizeName(namespace))
}

func (p Paths) ServiceDir(cluster, namespace string) string {
	return filepath.Join(p.NamespaceDir(cluster, namespace), "services")
}

func (p Paths) ClusterAuthorityKey(cluster string) string {
	return filepath.Join(p.ClusterDir(cluster), "authority.key")
}

func (p Paths) ClusterMembershipCapability(cluster string) string {
	return filepath.Join(p.ClusterDir(cluster), "membership.cap.json")
}

func (p Paths) NamespaceMembershipCapability(cluster, namespace string) string {
	return filepath.Join(p.NamespaceDir(cluster, namespace), "membership.cap.json")
}

func (p Paths) ServiceMembershipCapability(cluster, namespace string) string {
	return filepath.Join(p.NamespaceDir(cluster, namespace), "cluster.membership.cap.json")
}

func (p Paths) ServiceClaim(cluster, namespace, service string) string {
	return filepath.Join(p.ServiceDir(cluster, namespace), sanitizeName(service)+".claim.json")
}

func (p Paths) ServicePublishLease(cluster, namespace, service string) string {
	return filepath.Join(p.ServiceDir(cluster, namespace), sanitizeName(service)+".publish-lease.json")
}

func (p Paths) ServiceOwnerKey(cluster, namespace, service string) string {
	return filepath.Join(p.ServiceDir(cluster, namespace), sanitizeName(service)+".owner.key")
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "-")
	if s == "" {
		return "unnamed"
	}
	return s
}
