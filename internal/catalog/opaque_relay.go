package catalog

import (
	"log"
	"strings"

	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
)

// validateOpaqueAnnouncementsV3 takes AnnouncementV3 records forwarded by a
// non-authoritative relay and returns only those that verify successfully
// against the caller's local cluster authority and discovery context. Records
// that fail verification are dropped (with a debug log) — the relay is not
// trusted; verification MUST happen locally.
//
// Trust boundary: this function is the single point at which "the relay may be
// lying" gets converted into "trusted service record". Any change here should
// preserve the invariant that only fully signature-verified announcements are
// returned as Service entries.
func validateOpaqueAnnouncementsV3(cfg cfgpkg.Config, opaque []discovery.AnnouncementV3) []Service {
	if len(opaque) == 0 {
		return nil
	}
	authorityPub, contexts, ok := opaqueValidationInputs(cfg)
	if !ok {
		if len(opaque) > 0 {
			log.Printf("ignored %d untrusted announcement_v3 records from relay: no local authority/context configured", len(opaque))
		}
		return nil
	}
	seen := make(map[string]struct{}, len(opaque))
	out := make([]Service, 0, len(opaque))
	for _, ann := range opaque {
		peerPub, err := ann.PeerID.ExtractPublicKey()
		if err != nil {
			log.Printf("ignored untrusted announcement_v3 peer=%s: extract signer key: %v", ann.PeerID, err)
			continue
		}
		validated, err := discovery.ValidateAnnouncementV3AcrossContexts(ann, peerPub, authorityPub, "", contexts...)
		if err != nil {
			log.Printf("ignored untrusted announcement_v3 peer=%s: %v", ann.PeerID, err)
			continue
		}
		key := validated.ServiceID
		if key == "" {
			key = validated.ServiceName + "|" + validated.PeerID.String()
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		log.Printf("validated relayed announcement_v3 service=%s peer=%s cluster=%s/%s", validated.ServiceName, validated.PeerID, validated.ClusterID, validated.NamespaceID)
		out = append(out, serviceFromValidatedAnnouncement(validated))
	}
	return out
}

// opaqueValidationInputs pulls the current cluster's authority public key and
// discovery contexts from the caller's config. Returns ok=false when we lack
// enough context to verify anything.
func opaqueValidationInputs(cfg cfgpkg.Config) ([]byte, []discovery.NamespaceDiscoveryContext, bool) {
	clusterName := strings.TrimSpace(cfg.CurrentCluster)
	if clusterName == "" {
		return nil, nil, false
	}
	cluster, ok := cfg.Clusters[clusterName]
	if !ok || strings.TrimSpace(cluster.AuthorityPublicKey) == "" {
		return nil, nil, false
	}
	authorityPub, err := discovery.ParseAuthorityPublicKey(cluster.AuthorityPublicKey)
	if err != nil {
		return nil, nil, false
	}
	runtime := cfg.DiscoveryRuntime()
	contexts := []discovery.NamespaceDiscoveryContext{}
	if runtime.Context != nil {
		contexts = append(contexts, *runtime.Context)
	}
	if runtime.PreviousContext != nil {
		contexts = append(contexts, *runtime.PreviousContext)
	}
	if len(contexts) == 0 {
		return nil, nil, false
	}
	return authorityPub, contexts, true
}

// mergeOpaqueServices adds services derived from opaque relay records to the
// primary list. Duplicates are resolved by preferring the primary list entry;
// opaque additions only extend visibility.
func mergeOpaqueServices(primary []Service, opaque []Service) []Service {
	if len(opaque) == 0 {
		return primary
	}
	existing := make(map[string]struct{}, len(primary))
	for _, s := range primary {
		existing[serviceMergeKey(s)] = struct{}{}
	}
	out := make([]Service, 0, len(primary)+len(opaque))
	out = append(out, primary...)
	for _, s := range opaque {
		if _, dup := existing[serviceMergeKey(s)]; dup {
			continue
		}
		out = append(out, s)
	}
	return out
}

func serviceMergeKey(s Service) string {
	if strings.TrimSpace(s.ServiceID) != "" {
		return "id:" + s.ServiceID
	}
	return "name:" + s.Name + "|peer:" + s.PeerID
}

// serviceFromValidatedAnnouncement builds a catalog.Service from a
// discovery.ValidatedAnnouncementV3, using the query.Service helper indirectly
// so we go through the same NormalizeService path as remote-returned services.
func serviceFromValidatedAnnouncement(v discovery.ValidatedAnnouncementV3) Service {
	dto := discoveryquery.Service{
		Kind:             v.Kind,
		ClusterID:        v.ClusterID,
		NamespaceID:      v.NamespaceID,
		ServiceKind:      v.ServiceKind,
		Name:             v.ServiceName,
		ServiceID:        v.ServiceID,
		ServicePublicKey: v.ServicePublicKey,
		ConnectPolicy:    v.ConnectPolicy,
		GrantService:     v.GrantService,
		PeerID:           v.PeerID.String(),
		Addresses:        append([]string(nil), v.Addresses...),
		Status:           "online",
		TTLSeconds:       int64(v.TTL.Seconds()),
		ExpiresInSeconds: int64(v.TTL.Seconds()),
		Capabilities:     append([]string(nil), v.Capabilities...),
	}
	return ServiceFromQueryService(dto)
}
