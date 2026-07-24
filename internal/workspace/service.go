package workspace

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
)

func (w *Workspace) EnsureService(configPath, name string) (EnsureServiceResult, error) {
	var computed cfgpkg.Config
	var serviceContext ServiceContext
	var created, changed bool
	_, err := w.UpdateConfig(configPath, func(cfg *cfgpkg.Config) error {
		updated, ctx, wasCreated, wasChanged, err := w.ensureServiceState(configPath, *cfg, name)
		if err != nil {
			return err
		}
		computed, serviceContext, created, changed = updated, ctx, wasCreated, wasChanged
		if changed {
			*cfg = updated
		}
		return nil
	})
	if err != nil {
		return EnsureServiceResult{}, err
	}
	serviceContext.Config = computed
	return EnsureServiceResult{Config: computed, Context: serviceContext, Created: created, Changed: changed}, nil
}

func (w *Workspace) EnsureAttachServiceIdentity(configPath string, runtimeCfg cfgpkg.Config) (cfgpkg.Config, cfgpkg.NamespaceService, error) {
	var computed cfgpkg.Config
	var service cfgpkg.NamespaceService
	_, err := w.UpdateConfig(configPath, func(cfg *cfgpkg.Config) error {
		// Runtime flags may override service/scope fields, but persisted resource
		// maps must come from latest config loaded under lock.
		latest := *cfg
		latest.Service = runtimeCfg.Service
		if runtimeCfg.CurrentCluster != "" {
			latest.CurrentCluster = runtimeCfg.CurrentCluster
		}
		if runtimeCfg.CurrentNamespace != "" {
			latest.CurrentNamespace = runtimeCfg.CurrentNamespace
		}
		overlayMissingRuntimeService(&latest, runtimeCfg, runtimeCfg.Service.Name)
		updated, ctx, _, changed, err := w.ensureServiceState(configPath, latest, runtimeCfg.Service.Name)
		if err != nil {
			return err
		}
		computed, service = updated, ctx.Service
		if changed {
			*cfg = updated
		}
		return nil
	})
	if err != nil {
		return runtimeCfg, cfgpkg.NamespaceService{}, err
	}
	// Keep command/env runtime overrides (health/listen/network), but return
	// resource maps committed from latest locked config.
	result := runtimeCfg
	result.CurrentOverlay = computed.CurrentOverlay
	result.CurrentCluster = computed.CurrentCluster
	result.CurrentNamespace = computed.CurrentNamespace
	result.Overlays = computed.Overlays
	result.Clusters = computed.Clusters
	return result, service, nil
}

func overlayMissingRuntimeService(latest *cfgpkg.Config, runtimeCfg cfgpkg.Config, serviceName string) {
	clusterName := strings.TrimSpace(latest.CurrentCluster)
	namespaceName := strings.TrimSpace(latest.CurrentNamespace)
	if clusterName == "" || namespaceName == "" || strings.TrimSpace(serviceName) == "" {
		return
	}
	latestCluster, latestOK := latest.Clusters[clusterName]
	runtimeCluster, runtimeOK := runtimeCfg.Clusters[clusterName]
	if !latestOK || !runtimeOK {
		return
	}
	latestNamespace, latestOK := latestCluster.Namespaces[namespaceName]
	runtimeNamespace, runtimeOK := runtimeCluster.Namespaces[namespaceName]
	if !latestOK || !runtimeOK {
		return
	}
	if _, exists := latestNamespace.Services[serviceName]; exists {
		return
	}
	runtimeService, exists := runtimeNamespace.Services[serviceName]
	if !exists {
		return
	}
	if latestNamespace.Services == nil {
		latestNamespace.Services = make(map[string]cfgpkg.NamespaceService)
	}
	latestNamespace.Services[serviceName] = runtimeService
	latestCluster.Namespaces[namespaceName] = latestNamespace
	latest.Clusters[clusterName] = latestCluster
}

func (w *Workspace) CreateService(configPath, name string) (CreateServiceResult, error) {
	cfg, err := w.LoadConfigOrError(configPath)
	if err != nil {
		return CreateServiceResult{}, err
	}
	clusterName := strings.TrimSpace(cfg.CurrentCluster)
	namespaceName := strings.TrimSpace(cfg.CurrentNamespace)
	if clusterName == "" {
		return CreateServiceResult{}, errors.New("no current cluster selected; run `tubo use cluster/<name>` first")
	}
	if namespaceName == "" {
		return CreateServiceResult{}, errors.New("no current namespace selected; run `tubo use namespace/<name>` first")
	}
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return CreateServiceResult{}, fmt.Errorf("current cluster %q not found in config", clusterName)
	}
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" || cluster.AuthorityPrivateKeyFile == "" {
		return CreateServiceResult{}, fmt.Errorf("cluster %q is missing identity metadata", clusterName)
	}
	alreadyExists := false
	if ns, ok := cluster.Namespaces[namespaceName]; ok {
		if svc, ok := ns.Services[name]; ok && svc.ServiceID != "" && svc.ServiceSeed != "" && svc.ServiceClaimFile != "" {
			alreadyExists = true
		}
	}
	ensured, err := w.EnsureService(configPath, name)
	if err != nil {
		return CreateServiceResult{}, err
	}
	ctx := ensured.Context
	cluster = ctx.Config.Clusters[ctx.ClusterName]
	if _, err := w.ResolveMembershipCapabilityFile(configPath, cluster, ctx.ClusterName, ctx.Namespace, ctx.Name, ctx.Service.ServiceSeed); err != nil {
		return CreateServiceResult{}, err
	}
	if alreadyExists {
		return CreateServiceResult{Context: ctx, AlreadyExists: true}, nil
	}
	if err := w.mintLocalPublishArtifacts(cluster, ctx.ClusterName, ctx.Namespace, ctx.Name, ctx.Service); err != nil {
		return CreateServiceResult{}, err
	}
	return CreateServiceResult{Context: ctx}, nil
}

func (w *Workspace) ResolveServiceContext(configPath, serviceRef, clusterName, namespaceName string) (ServiceContext, error) {
	cfg, err := w.LoadConfigOrError(configPath)
	if err != nil {
		return ServiceContext{}, err
	}
	return w.resolveServiceContext(cfg, strings.TrimSpace(clusterName), strings.TrimSpace(namespaceName), strings.TrimSpace(serviceRef))
}

func (w *Workspace) ResolveMembershipCapabilityFile(configPath string, cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName, serviceSeed string) (string, error) {
	capPath := DerivePaths(configPath).ServiceMembershipCapability(clusterName, namespaceName, serviceName)
	servicePeerID, err := p2p.PeerIDFromSeed(serviceSeed)
	if err != nil {
		return "", fmt.Errorf("derive service peer id: %w", err)
	}
	if _, err := w.store.Stat(capPath); err == nil {
		// File exists - verify the SubjectPeerID matches current service peer ID
		if data, readErr := w.store.ReadFile(capPath); readErr == nil {
			var existing capability.MembershipCapability
			if jsonErr := json.Unmarshal(data, &existing); jsonErr == nil {
				if existing.SubjectPeerID == servicePeerID.String() {
					return capPath, nil
				}
				// Peer ID mismatch - need to regenerate
			}
		}
		// If we can't read/parse or peer ID doesn't match, fall through to regenerate
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if cluster.AuthorityPrivateKeyFile != "" {
		priv, err := loadPrivateKey(w.store, cluster.AuthorityPrivateKeyFile)
		if err != nil {
			return "", fmt.Errorf("load cluster authority key: %w", err)
		}
		pubAuthorized, err := authorityPublicKeyString(priv)
		if err != nil {
			return "", err
		}
		if cluster.AuthorityPublicKey != pubAuthorized {
			return "", fmt.Errorf("cluster %q authority public key mismatch", clusterName)
		}
		membership, err := capability.SignMembershipCapability(capability.MembershipCapability{
			ClusterID:     cluster.ClusterID,
			NamespaceID:   namespaceName,
			SubjectPeerID: servicePeerID.String(),
			Permissions: []string{
				capability.PermissionSubscribe,
				capability.PermissionList,
				capability.PermissionPublish,
			},
			ExpiresAt: time.Now().Add(365 * 24 * time.Hour),
		}, priv)
		if err != nil {
			return "", err
		}
		if err := writeJSONFile(w.store, capPath, membership); err != nil {
			return "", err
		}
		return capPath, nil
	}
	return "", fmt.Errorf("service membership capability file missing for %s/%s/%s: %s", clusterName, namespaceName, serviceName, capPath)
}

func (w *Workspace) ensureServiceState(configPath string, cfg cfgpkg.Config, serviceName string) (cfgpkg.Config, ServiceContext, bool, bool, error) {
	if strings.TrimSpace(cfg.CurrentCluster) == "" {
		return cfg, ServiceContext{}, false, false, errors.New("no current cluster selected; run `tubo use cluster/<name>` first")
	}
	if strings.TrimSpace(cfg.CurrentNamespace) == "" {
		return cfg, ServiceContext{}, false, false, errors.New("no current namespace selected; run `tubo use namespace/<name>` first")
	}
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return cfg, ServiceContext{}, false, false, errors.New("service.name is required (set --name or SERVICE_NAME)")
	}
	cluster, ok := cfg.Clusters[cfg.CurrentCluster]
	if !ok {
		return cfg, ServiceContext{}, false, false, fmt.Errorf("current cluster %q not found in config", cfg.CurrentCluster)
	}
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" {
		return cfg, ServiceContext{}, false, false, fmt.Errorf("cluster %q is missing identity metadata", cfg.CurrentCluster)
	}
	if cluster.Namespaces == nil {
		return cfg, ServiceContext{}, false, false, fmt.Errorf("cluster %q has no namespaces", cfg.CurrentCluster)
	}
	namespace, ok := cluster.Namespaces[cfg.CurrentNamespace]
	if !ok {
		return cfg, ServiceContext{}, false, false, fmt.Errorf("current namespace %q not found in cluster %q", cfg.CurrentNamespace, cfg.CurrentCluster)
	}
	if namespace.Services == nil {
		namespace.Services = make(map[string]cfgpkg.NamespaceService)
	}
	paths := DerivePaths(configPath)
	svc, existed := namespace.Services[serviceName]
	created := !existed
	changed := false
	currentTarget := strings.TrimSpace(cfg.Service.Target)
	if isPlaceholderServiceTarget(currentTarget) {
		currentTarget = ""
	}
	if currentTarget == "" {
		currentTarget = strings.TrimSpace(svc.Target)
	}
	kind := strings.TrimSpace(string(cfg.Service.Kind))
	if kind == "" {
		if svc.Kind != "" {
			kind = string(svc.Kind)
		} else {
			kind = string(cfgpkg.NormalizeServiceKind("", currentTarget))
		}
	}
	cfg.Service.Kind = cfgpkg.ServiceKind(kind)
	cfg.Service.Target = currentTarget
	if svc.Kind != cfgpkg.ServiceKind(kind) {
		svc.Kind = cfgpkg.ServiceKind(kind)
		changed = true
	}
	if svc.Target == "" && currentTarget != "" {
		svc.Target = currentTarget
		changed = true
	} else if svc.Target != "" && currentTarget != "" && svc.Target != currentTarget {
		return cfg, ServiceContext{}, false, false, fmt.Errorf("service %q target mismatch in cluster %q namespace %q: target=%q want %q", serviceName, cfg.CurrentCluster, cfg.CurrentNamespace, currentTarget, svc.Target)
	}
	if svc.ServiceID != "" && svc.ServiceOwnerKeyFile == "" {
		return cfg, ServiceContext{}, false, false, fmt.Errorf("service %q is missing service_owner_key_file", serviceName)
	}
	if svc.ServiceID == "" {
		ownerKeyPath := svc.ServiceOwnerKeyFile
		if ownerKeyPath == "" {
			ownerKeyPath = paths.ServiceOwnerKey(cfg.CurrentCluster, cfg.CurrentNamespace, serviceName)
		}
		identity, _, err := serviceidentity.Ensure(ownerKeyPath)
		if err != nil {
			return cfg, ServiceContext{}, false, false, err
		}
		svc.ServiceID = identity.ServiceID
		svc.ServiceOwnerKeyFile = ownerKeyPath
		changed = true
	} else {
		if err := serviceidentity.ValidateServiceID(svc.ServiceID); err != nil {
			return cfg, ServiceContext{}, false, false, fmt.Errorf("service %q has invalid service_id: %w", serviceName, err)
		}
		if svc.ServiceOwnerKeyFile != "" {
			identity, _, err := serviceidentity.Load(svc.ServiceOwnerKeyFile)
			if err != nil {
				return cfg, ServiceContext{}, false, false, err
			}
			if identity.ServiceID != svc.ServiceID {
				return cfg, ServiceContext{}, false, false, fmt.Errorf("service %q identity mismatch in cluster %q namespace %q: service_id=%q want %q", serviceName, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceID, identity.ServiceID)
			}
		}
	}
	if svc.ServiceSeed == "" {
		seed, err := generateServiceSeed()
		if err != nil {
			return cfg, ServiceContext{}, false, false, err
		}
		svc.ServiceSeed = seed
		changed = true
	}
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		return cfg, ServiceContext{}, false, false, fmt.Errorf("service %q has invalid service_seed: %w", serviceName, err)
	}
	// Check if existing lease file has a mismatched PeerID and remove stale artifacts
	if svc.ServicePublishLeaseFile != "" {
		if leasePeerID, _ := readLeasePublisherPeerID(w.store, svc.ServicePublishLeaseFile); leasePeerID != "" && leasePeerID != servicePeerID.String() {
			_ = w.store.Remove(svc.ServicePublishLeaseFile)
			if svc.ServiceClaimFile != "" {
				_ = w.store.Remove(svc.ServiceClaimFile)
			}
			changed = true
		}
	}
	if svc.ServiceClaimFile == "" {
		svc.ServiceClaimFile = paths.ServiceClaim(cfg.CurrentCluster, cfg.CurrentNamespace, serviceName)
		changed = true
	}
	if svc.ServicePublishLeaseFile == "" {
		svc.ServicePublishLeaseFile = paths.ServicePublishLease(cfg.CurrentCluster, cfg.CurrentNamespace, serviceName)
		changed = true
	}
	if changed {
		namespace.Services[serviceName] = svc
		cluster.Namespaces[cfg.CurrentNamespace] = namespace
		cfg.Clusters[cfg.CurrentCluster] = cluster
	}
	cfg.Service.Target = svc.Target
	cfg.Service.Kind = svc.Kind
	ctx := ServiceContext{Config: cfg, ClusterName: cfg.CurrentCluster, Namespace: cfg.CurrentNamespace, Name: serviceName, Cluster: cluster, Service: svc}
	return cfg, ctx, created, changed, nil
}

func isPlaceholderServiceTarget(target string) bool {
	target = strings.TrimSpace(target)
	return target == "http://127.0.0.1:1" || target == "http://127.0.0.1:1/"
}

func (w *Workspace) resolveServiceContext(cfg cfgpkg.Config, clusterName, namespaceName, serviceRef string) (ServiceContext, error) {
	if clusterName == "" {
		clusterName = strings.TrimSpace(cfg.CurrentCluster)
	}
	if namespaceName == "" {
		namespaceName = strings.TrimSpace(cfg.CurrentNamespace)
	}
	if clusterName == "" {
		return ServiceContext{}, errors.New("no current cluster selected; run `tubo use cluster/<name>` first")
	}
	if namespaceName == "" {
		return ServiceContext{}, errors.New("no current namespace selected; run `tubo use namespace/<name>` first")
	}
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return ServiceContext{}, fmt.Errorf("cluster %q not found", clusterName)
	}
	namespace, ok := cluster.Namespaces[namespaceName]
	if !ok {
		return ServiceContext{}, fmt.Errorf("namespace %q not found in cluster %q", namespaceName, clusterName)
	}
	if namespace.Services == nil {
		return ServiceContext{}, fmt.Errorf("namespace %q has no services configured", namespaceName)
	}
	if svc, ok := namespace.Services[serviceRef]; ok {
		return ServiceContext{Config: cfg, ClusterName: clusterName, Namespace: namespaceName, Name: serviceRef, Cluster: cluster, Service: svc}, nil
	}
	for name, svc := range namespace.Services {
		if svc.ServiceID == serviceRef {
			return ServiceContext{Config: cfg, ClusterName: clusterName, Namespace: namespaceName, Name: name, Cluster: cluster, Service: svc}, nil
		}
	}
	return ServiceContext{}, fmt.Errorf("service %q not found in cluster %q namespace %q", serviceRef, clusterName, namespaceName)
}

func (w *Workspace) mintLocalPublishArtifacts(cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName string, svc cfgpkg.NamespaceService) error {
	if svc.ServiceClaimFile == "" || svc.ServicePublishLeaseFile == "" {
		return errors.New("service claim and publish lease files are required")
	}
	priv, err := loadPrivateKey(w.store, cluster.AuthorityPrivateKeyFile)
	if err != nil {
		return fmt.Errorf("load cluster authority key: %w", err)
	}
	pubAuthorized, err := authorityPublicKeyString(priv)
	if err != nil {
		return err
	}
	if cluster.AuthorityPublicKey != pubAuthorized {
		return fmt.Errorf("cluster %q authority public key mismatch", clusterName)
	}
	owner, _, err := serviceidentity.Load(svc.ServiceOwnerKeyFile)
	if err != nil {
		return fmt.Errorf("load service owner key: %w", err)
	}
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		return err
	}
	req, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{
		ClusterID:             cluster.ClusterID,
		NamespaceID:           namespaceName,
		ServiceID:             svc.ServiceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(owner.PublicKey),
		PublisherPeerID:       servicePeerID.String(),
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 randomNonce(),
	}, owner.PrivateKey)
	if err != nil {
		return err
	}
	artifacts, err := grantspkg.BuildApprovalArtifacts(priv, clusterName, cluster.ClusterID, namespaceName, serviceName, svc.ServiceID, servicePeerID.String(), string(cfgpkg.NormalizeServiceKind(svc.Kind, "")), 365*24*time.Hour, serviceShareTTL(), req.RequestedCapabilities, req.ServicePublicKey, req.Nonce, req.ServiceOwnerSignature)
	if err != nil {
		return err
	}
	if err := writeJSONFile(w.store, svc.ServiceClaimFile, artifacts.ServiceClaim); err != nil {
		return err
	}
	if err := writeJSONFile(w.store, svc.ServicePublishLeaseFile, artifacts.PublishLease); err != nil {
		return err
	}
	return nil
}

func authorityPublicKeyString(priv ed25519.PrivateKey) (string, error) {
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return "", errors.New("authority public key type mismatch")
	}
	pubKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubKey))), nil
}

func generateServiceSeed() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "service-" + hex.EncodeToString(buf), nil
}

func randomNonce() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("nonce-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func serviceShareTTL() time.Duration {
	if v := strings.TrimSpace(os.Getenv("TUBO_PUBLISH_LEASE_TTL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return grantspkg.ServiceShareDefaultTTL
}

// readLeasePublisherPeerID extracts the publisher_peer_id from a lease file.
// Returns empty string if the file doesn't exist or can't be parsed.
func readLeasePublisherPeerID(store Store, path string) (string, error) {
	data, err := store.ReadFile(path)
	if err != nil {
		return "", err
	}
	var lease struct {
		PublisherPeerID string `json:"publisher_peer_id"`
	}
	if err := json.Unmarshal(data, &lease); err != nil {
		return "", err
	}
	return lease.PublisherPeerID, nil
}
