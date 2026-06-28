package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	libhost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	"github.com/origama/tubo/internal/p2p"
)

var (
	listServicesWithAuthorizationFunc = discoveryquery.ListServicesWithAuthorization
	getServiceWithAuthorizationFunc   = discoveryquery.GetServiceWithAuthorization
)

type progressRecorder struct {
	messages []string
	emit     ProgressFunc
}

func newProgressRecorder(emit ProgressFunc) *progressRecorder {
	return &progressRecorder{emit: emit}
}

func (r *progressRecorder) message(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.messages = append(r.messages, msg)
	if r.emit != nil {
		r.emit(ProgressUpdate{Message: msg})
	}
}

func (r *progressRecorder) verbose(minVerbosity int, format string, args ...any) {
	if r.emit == nil {
		return
	}
	r.emit(ProgressUpdate{Message: fmt.Sprintf(format, args...), Verbosity: minVerbosity})
}

type remoteQueryAttempt struct {
	Peer          string
	PathClass     string
	Timeout       time.Duration
	Records       int
	Metadata      *discoveryquery.Metadata
	ResponseError string
	Err           error
}

func (a remoteQueryAttempt) reachedAuthority() bool {
	return a.Metadata != nil || strings.TrimSpace(a.ResponseError) != ""
}

func (a remoteQueryAttempt) authRejected() bool {
	return strings.TrimSpace(a.ResponseError) != "" && isDiscoveryAuthorizationError(errors.New(a.ResponseError))
}

func (a remoteQueryAttempt) timedOut() bool {
	if a.Err == nil {
		return false
	}
	if errors.Is(a.Err, context.DeadlineExceeded) {
		return true
	}
	return strings.Contains(strings.ToLower(a.Err.Error()), "deadline exceeded")
}

func discoveryPeerPathClass(raw string) string {
	peer := strings.TrimSpace(raw)
	switch {
	case peer == "":
		return "unknown"
	case strings.Contains(peer, "/p2p-circuit/"):
		return "relayed"
	case strings.Contains(peer, "/ip4/") || strings.Contains(peer, "/ip6/") || strings.Contains(peer, "/dns/") || strings.Contains(peer, "/dns4/") || strings.Contains(peer, "/dns6/"):
		return "direct"
	default:
		return "unknown"
	}
}

func canonicalDiscoveryPeers(in []string) []string {
	relayed := make([]string, 0, len(in))
	direct := make([]string, 0, len(in))
	unknown := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		peer := strings.TrimSpace(raw)
		if peer == "" {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		switch discoveryPeerPathClass(peer) {
		case "relayed":
			relayed = append(relayed, peer)
		case "direct":
			direct = append(direct, peer)
		default:
			unknown = append(unknown, peer)
		}
	}
	out := make([]string, 0, len(relayed)+len(direct)+len(unknown))
	out = append(out, relayed...)
	out = append(out, direct...)
	out = append(out, unknown...)
	return out
}

func discoveryPeerAttemptTimeout(total time.Duration, peerCount int) time.Duration {
	if total <= 0 {
		return DefaultTimeout
	}
	if peerCount <= 1 {
		return total
	}
	perPeer := total / time.Duration(peerCount)
	if perPeer > 5*time.Second {
		perPeer = 5 * time.Second
	}
	if perPeer < time.Second {
		perPeer = time.Second
	}
	if perPeer > total {
		return total
	}
	return perPeer
}

func discoveryPeerAttemptBudget(remaining time.Duration, remainingPeers int, perPeerCap time.Duration) time.Duration {
	if remainingPeers <= 1 {
		return remaining
	}
	attempt := remaining / time.Duration(remainingPeers)
	if attempt <= 0 {
		return remaining
	}
	if perPeerCap > 0 && attempt > perPeerCap {
		attempt = perPeerCap
	}
	if attempt > remaining {
		return remaining
	}
	return attempt
}

func queryRemoteDiscovery(cfg cfgpkg.Config, timeout time.Duration, recorder *progressRecorder, query func(context.Context, libhost.Host, peer.AddrInfo, *capability.MembershipCapability, string) (discoveryquery.Response, error)) (discoveryquery.Response, []remoteQueryAttempt, error) {
	peers, err := discoveryPeersForConfig(cfg)
	if err != nil {
		return discoveryquery.Response{}, nil, err
	}
	if len(peers) == 0 {
		return discoveryquery.Response{}, nil, errors.New("no discovery peers configured")
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	psk, _, err := p2p.LoadPrivateNetworkPSK(cfg.Network.PrivateKeyFile, cfg.Network.PrivateKeyB64)
	if err != nil {
		return discoveryquery.Response{}, nil, fmt.Errorf("load private network key: %w", err)
	}
	authMembership, authGrantToken, err := discoveryQueryAuthorizationForConfig(cfg)
	if err != nil {
		return discoveryquery.Response{}, nil, err
	}
	seed := discoveryQuerySeedForConfig(cfg)
	h, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", seed, psk)
	if err != nil {
		return discoveryquery.Response{}, nil, fmt.Errorf("create remote query host: %w", err)
	}
	defer h.Close()
	perPeerTimeout := discoveryPeerAttemptTimeout(timeout, len(peers))
	deadline := time.Now().Add(timeout)
	attempts := make([]remoteQueryAttempt, 0, len(peers))
	var lastErr error
	for i, raw := range peers {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		attemptTimeout := discoveryPeerAttemptBudget(remaining, len(peers)-i, perPeerTimeout)
		attempt := remoteQueryAttempt{Peer: raw, PathClass: discoveryPeerPathClass(raw), Timeout: attemptTimeout}
		recorder.message("querying cluster discovery peer %d/%d (%s)...", i+1, len(peers), attempt.PathClass)
		recorder.verbose(1, "discovery peer %d/%d addr=%s timeout=%s", i+1, len(peers), raw, attemptTimeout)
		info, err := p2p.AddrInfoFromString(raw)
		if err != nil {
			attempt.Err = fmt.Errorf("invalid discovery peer %q: %w", raw, err)
			attempts = append(attempts, attempt)
			recorder.message("discovery peer %d/%d (%s) is invalid", i+1, len(peers), attempt.PathClass)
			recorder.verbose(1, "discovery peer %d/%d addr=%s invalid: %v", i+1, len(peers), raw, err)
			lastErr = attempt.Err
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		resp, err := query(ctx, h, info, authMembership, authGrantToken)
		cancel()
		if err != nil {
			attempt.Err = err
			attempts = append(attempts, attempt)
			if attempt.timedOut() {
				recorder.message("discovery peer %d/%d (%s) timed out after %s", i+1, len(peers), attempt.PathClass, attemptTimeout.Round(100*time.Millisecond))
			} else {
				recorder.message("discovery peer %d/%d (%s) failed", i+1, len(peers), attempt.PathClass)
			}
			recorder.verbose(1, "discovery peer %d/%d addr=%s error=%v", i+1, len(peers), raw, err)
			lastErr = err
			continue
		}
		attempt.Metadata = &resp.Metadata
		if resp.Error != "" {
			attempt.ResponseError = resp.Error
			attempts = append(attempts, attempt)
			if attempt.authRejected() {
				recorder.message("discovery peer %d/%d (%s) rejected discovery authorization", i+1, len(peers), attempt.PathClass)
			} else {
				recorder.message("discovery peer %d/%d (%s) returned an error", i+1, len(peers), attempt.PathClass)
			}
			recorder.verbose(1, "discovery peer %d/%d addr=%s response_error=%s served_by=%s role=%s", i+1, len(peers), raw, resp.Error, resp.Metadata.ServedBy, resp.Metadata.ServedByRole)
			lastErr = errors.New(resp.Error)
			continue
		}
		attempt.Records = len(resp.Services)
		if resp.Service != nil {
			attempt.Records = 1
		}
		attempts = append(attempts, attempt)
		recorder.message("discovery peer %d/%d (%s) returned %d records", i+1, len(peers), attempt.PathClass, attempt.Records)
		recorder.verbose(1, "discovery peer %d/%d addr=%s served_by=%s role=%s records=%d", i+1, len(peers), raw, resp.Metadata.ServedBy, resp.Metadata.ServedByRole, attempt.Records)
		return resp, attempts, nil
	}
	if lastErr == nil {
		lastErr = errors.New("remote discovery query failed")
	}
	return discoveryquery.Response{}, attempts, lastErr
}

func fetchRemoteServiceCacheDetailed(cfg cfgpkg.Config, timeout time.Duration, progress ProgressFunc) ([]Service, *discoveryquery.Metadata, []string, []remoteQueryAttempt, error) {
	recorder := newProgressRecorder(progress)
	resp, attempts, err := queryRemoteDiscovery(cfg, timeout, recorder, listServicesWithAuthorizationFunc)
	if err != nil {
		return nil, nil, recorder.messages, attempts, err
	}
	services := make([]Service, 0, len(resp.Services))
	for _, service := range resp.Services {
		services = append(services, ServiceFromQueryService(service))
	}
	SortServices(services)
	recorder.message("received %d records from cluster discovery authority", len(services))
	return services, &resp.Metadata, recorder.messages, attempts, nil
}

func fetchRemoteServiceDetailed(cfg cfgpkg.Config, serviceName string, timeout time.Duration, progress ProgressFunc) (Service, *discoveryquery.Metadata, []string, []remoteQueryAttempt, error) {
	recorder := newProgressRecorder(progress)
	resp, attempts, err := queryRemoteDiscovery(cfg, timeout, recorder, func(ctx context.Context, h libhost.Host, info peer.AddrInfo, membership *capability.MembershipCapability, grantToken string) (discoveryquery.Response, error) {
		return getServiceWithAuthorizationFunc(ctx, h, info, serviceName, membership, grantToken)
	})
	if err != nil {
		return Service{}, nil, recorder.messages, attempts, err
	}
	if resp.Service == nil {
		return Service{}, &resp.Metadata, recorder.messages, attempts, errors.New("service not found")
	}
	service := ServiceFromQueryService(*resp.Service)
	recorder.message("received service %s", service.Name)
	return service, &resp.Metadata, recorder.messages, attempts, nil
}

func remoteAttemptsReachedAuthority(attempts []remoteQueryAttempt) bool {
	for _, attempt := range attempts {
		if attempt.reachedAuthority() {
			return true
		}
	}
	return false
}

func remoteAttemptsAllUnreachable(attempts []remoteQueryAttempt) bool {
	if len(attempts) == 0 {
		return false
	}
	for _, attempt := range attempts {
		if attempt.reachedAuthority() || attempt.Err == nil {
			return false
		}
	}
	return true
}

func remoteAttemptsAuthRejected(attempts []remoteQueryAttempt) bool {
	for _, attempt := range attempts {
		if attempt.authRejected() {
			return true
		}
	}
	return false
}

func scopedDiscoveryMessage(cfg cfgpkg.Config, scope Scope, retained, total int) string {
	return fmt.Sprintf("retained %d scoped records for %s", retained, discoveryScopeLabel(cfg, scope))
}

func emptyScopedDiscoveryMessage(cfg cfgpkg.Config, scope Scope, total int) string {
	label := discoveryScopeLabel(cfg, scope)
	switch {
	case total == 0:
		return fmt.Sprintf("no services found in %s; discovery authority cache is empty", label)
	default:
		return fmt.Sprintf("no services found in %s; discovery authority returned %d records but none matched the requested scope", label, total)
	}
}

func emptyUnreachableDiscoveryMessage(cfg cfgpkg.Config, scope Scope, attempts []remoteQueryAttempt) string {
	return fmt.Sprintf("no services found in %s; all %d configured discovery peers were unreachable", discoveryScopeLabel(cfg, scope), len(attempts))
}

func discoveryScopeLabel(cfg cfgpkg.Config, scope Scope) string {
	cluster := strings.TrimSpace(scope.Cluster)
	if cluster == "" {
		cluster = strings.TrimSpace(cfg.CurrentCluster)
	}
	namespace := strings.TrimSpace(scope.Namespace)
	if namespace == "" && !scope.AllNamespaces {
		namespace = strings.TrimSpace(cfg.CurrentNamespace)
	}
	if scope.AllNamespaces {
		if cluster != "" {
			return cluster + " across all namespaces"
		}
		return "all namespaces"
	}
	if cluster != "" && namespace != "" {
		return cluster + "/" + namespace
	}
	if cluster != "" {
		return cluster
	}
	if namespace != "" {
		return namespace
	}
	return "current scope"
}
