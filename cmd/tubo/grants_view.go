package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/peers"
)

type grantRequestGroup struct {
	Key               string    `json:"key"`
	ClusterName       string    `json:"cluster_name"`
	ClusterID         string    `json:"cluster_id"`
	NamespaceID       string    `json:"namespace_id"`
	RequesterPeerID   string    `json:"requester_peer_id"`
	RequesterAlias    string    `json:"requester_alias,omitempty"`
	ServiceName       string    `json:"service_name"`
	ServiceID         string    `json:"service_id"`
	ServiceKind       string    `json:"service_kind,omitempty"`
	ServicePeerID     string    `json:"service_peer_id"`
	FirstSeen         time.Time `json:"first_seen"`
	LastSeen          time.Time `json:"last_seen"`
	Attempts          int       `json:"attempts"`
	Pending           int       `json:"pending"`
	Approved          int       `json:"approved"`
	Denied            int       `json:"denied"`
	Expired           int       `json:"expired"`
	LatestRequestID   string    `json:"latest_request_id"`
	LatestStatus      string    `json:"latest_status"`
	LatestRequestedAt time.Time `json:"latest_requested_at"`
	LatestExpiresAt   time.Time `json:"latest_expires_at"`
	LatestDecidedAt   time.Time `json:"latest_decided_at"`
}

type grantRequestListJSON struct {
	Mode     string              `json:"mode"`
	Store    string              `json:"store"`
	Requests []grantspkg.Request `json:"requests"`
	Groups   []grantRequestGroup `json:"groups,omitempty"`
}

type grantRequestDescribeJSON struct {
	Store           string              `json:"store"`
	Request         grantspkg.Request   `json:"request"`
	Group           grantRequestGroup   `json:"group"`
	RelatedRequests []grantspkg.Request `json:"related_requests"`
	RequesterAlias  string              `json:"requester_alias,omitempty"`
	Review          grantReviewNotes    `json:"review"`
}

type grantReviewNotes struct {
	SuggestedVerification []string `json:"suggested_verification,omitempty"`
	ApproveCommand        string   `json:"approve_command,omitempty"`
	DenyCommand           string   `json:"deny_command,omitempty"`
}

type peerAliasIndex struct {
	byID map[string]peers.Alias
}

func loadPeerAliasIndex() (peerAliasIndex, error) {
	store := peers.NewStore(peers.DefaultStorePath())
	aliases, err := store.List()
	if err != nil {
		return peerAliasIndex{}, err
	}
	byID := make(map[string]peers.Alias, len(aliases))
	for _, alias := range aliases {
		byID[alias.PeerID] = alias
	}
	return peerAliasIndex{byID: byID}, nil
}

func (idx peerAliasIndex) name(peerID string) string {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return ""
	}
	if alias, ok := idx.byID[peerID]; ok {
		return strings.TrimSpace(alias.Name)
	}
	return ""
}

func (idx peerAliasIndex) note(peerID string) string {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return ""
	}
	if alias, ok := idx.byID[peerID]; ok {
		return strings.TrimSpace(alias.Note)
	}
	return ""
}

func grantRequestGroupKey(req grantspkg.Request) string {
	return strings.Join([]string{req.ClusterID, req.NamespaceID, req.RequesterPeerID, req.ServiceID, req.ServicePeerID}, "\x00")
}

func summarizeGrantRequests(requests []grantspkg.Request, aliasIdx peerAliasIndex) []grantRequestGroup {
	groups := make(map[string]*grantRequestGroup, len(requests))
	for _, req := range requests {
		key := grantRequestGroupKey(req)
		group := groups[key]
		if group == nil {
			group = &grantRequestGroup{
				Key:             key,
				ClusterName:     req.ClusterName,
				ClusterID:       req.ClusterID,
				NamespaceID:     req.NamespaceID,
				RequesterPeerID: req.RequesterPeerID,
				RequesterAlias:  aliasIdx.name(req.RequesterPeerID),
				ServiceName:     req.ServiceName,
				ServiceID:       req.ServiceID,
				ServiceKind:     grantspkg.NormalizeServiceShareKind(req.ServiceKind),
				ServicePeerID:   req.ServicePeerID,
			}
			groups[key] = group
		}
		group.Attempts++
		if group.FirstSeen.IsZero() || req.RequestedAt.Before(group.FirstSeen) {
			group.FirstSeen = req.RequestedAt
		}
		if req.RequestedAt.After(group.LastSeen) || group.LastSeen.IsZero() {
			group.LastSeen = req.RequestedAt
			group.LatestRequestID = req.ID
			group.LatestStatus = req.Status
			group.LatestRequestedAt = req.RequestedAt
			group.LatestExpiresAt = req.ExpiresAt
			group.LatestDecidedAt = req.DecidedAt
		}
		switch req.Status {
		case grantspkg.StatusPending:
			group.Pending++
		case grantspkg.StatusApproved:
			group.Approved++
		case grantspkg.StatusDenied:
			group.Denied++
		case grantspkg.StatusExpired:
			group.Expired++
		}
		if req.DecidedAt.After(group.LatestDecidedAt) {
			group.LatestDecidedAt = req.DecidedAt
		}
	}
	out := make([]grantRequestGroup, 0, len(groups))
	for _, group := range groups {
		out = append(out, *group)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		if out[i].ServiceName != out[j].ServiceName {
			return out[i].ServiceName < out[j].ServiceName
		}
		return out[i].LatestRequestID < out[j].LatestRequestID
	})
	return out
}

func groupRequestsByKey(requests []grantspkg.Request) map[string][]grantspkg.Request {
	groups := make(map[string][]grantspkg.Request, len(requests))
	for _, req := range requests {
		key := grantRequestGroupKey(req)
		groups[key] = append(groups[key], req)
	}
	for key := range groups {
		sort.SliceStable(groups[key], func(i, j int) bool {
			if !groups[key][i].RequestedAt.Equal(groups[key][j].RequestedAt) {
				return groups[key][i].RequestedAt.Before(groups[key][j].RequestedAt)
			}
			return groups[key][i].ID < groups[key][j].ID
		})
	}
	return groups
}

func printGrantRequestsWide(requests []grantspkg.Request, title, storePath string) {
	sort.SliceStable(requests, func(i, j int) bool {
		if !requests[i].RequestedAt.Equal(requests[j].RequestedAt) {
			return requests[i].RequestedAt.Before(requests[j].RequestedAt)
		}
		if requests[i].ServiceID != requests[j].ServiceID {
			return requests[i].ServiceID < requests[j].ServiceID
		}
		return requests[i].ID < requests[j].ID
	})
	if title != "" {
		fmt.Println(title)
	}
	if storePath != "" {
		fmt.Printf("history source: authority/local store %s\n", storePath)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tSCOPE\tSERVICE\tSERVICE_KIND\tSERVICE_ID\tREQUESTER\tSERVICE_PEER\tEXPIRES")
	for _, req := range requests {
		scope := req.ClusterName + "/" + req.NamespaceID
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", req.ID, req.Status, scope, req.ServiceName, grantspkg.NormalizeServiceShareKind(req.ServiceKind), req.ServiceID, req.RequesterPeerID, req.ServicePeerID, req.ExpiresAt.Format(time.RFC3339))
	}
	_ = w.Flush()
}

func printGrantPendingHuman(requests []grantspkg.Request, title string, aliasIdx peerAliasIndex, verbose bool) {
	groups := summarizeGrantRequests(requests, aliasIdx)
	if title != "" {
		fmt.Println(title)
	}
	fmt.Println("source: authority/local store")
	if len(groups) == 0 {
		fmt.Println("(no grant requests)")
		return
	}
	for i, group := range groups {
		fmt.Printf("%d. %s wants to publish %s (%s) in %s/%s\n", i+1, displayGrantRequester(aliasIdx, group.RequesterPeerID), group.ServiceName, grantspkg.NormalizeServiceShareKind(group.ServiceKind), group.ClusterName, group.NamespaceID)
		fmt.Printf("   requester: %s\n", abbreviatePeerID(group.RequesterPeerID))
		fmt.Printf("   attempts: %d · last seen %s · expires in %s\n", group.Attempts, humanizeAgo(group.LastSeen), humanizeRelativeExpiry(group.LatestExpiresAt))
		if verbose {
			fmt.Printf("   peer: %s\n", abbreviatePeerID(group.ServicePeerID))
		}
		if group.LatestRequestID != "" {
			fmt.Printf("\n   approve: tubo grants approve %s --ttl 168h\n", group.LatestRequestID)
			fmt.Printf("   deny:    tubo grants deny %s --reason \"<reason>\"\n", group.LatestRequestID)
			fmt.Printf("   inspect: tubo grants describe %s\n", group.LatestRequestID)
		}
		if i != len(groups)-1 {
			fmt.Println()
		}
	}
}

func splitGrantHistorySections(groups []grantRequestGroup, all bool) (approved, pending, denied, expired []grantRequestGroup, hiddenExpired int) {
	for _, group := range groups {
		switch group.LatestStatus {
		case grantspkg.StatusApproved:
			approved = append(approved, group)
		case grantspkg.StatusPending:
			pending = append(pending, group)
		case grantspkg.StatusDenied:
			denied = append(denied, group)
		case grantspkg.StatusExpired:
			expired = append(expired, group)
		default:
			pending = append(pending, group)
		}
	}
	sortGrantGroups(approved)
	sortGrantGroups(pending)
	sortGrantGroups(denied)
	sortGrantGroups(expired)
	if !all && len(expired) > 5 {
		hiddenExpired = len(expired) - 5
		expired = expired[:5]
	}
	return approved, pending, denied, expired, hiddenExpired
}

func sortGrantGroups(groups []grantRequestGroup) {
	sort.SliceStable(groups, func(i, j int) bool {
		if !groups[i].LastSeen.Equal(groups[j].LastSeen) {
			return groups[i].LastSeen.After(groups[j].LastSeen)
		}
		if groups[i].ServiceName != groups[j].ServiceName {
			return groups[i].ServiceName < groups[j].ServiceName
		}
		return groups[i].RequesterPeerID < groups[j].RequesterPeerID
	})
}

func printGrantHistoryHuman(requests []grantspkg.Request, title, storePath string, all, verbose bool) {
	aliasIdx, err := loadPeerAliasIndex()
	if err != nil {
		aliasIdx = peerAliasIndex{byID: map[string]peers.Alias{}}
	}
	groups := summarizeGrantRequests(requests, aliasIdx)
	approved, pending, denied, expired, hiddenExpired := splitGrantHistorySections(groups, all)
	if title != "" {
		fmt.Println(title)
	}
	fmt.Println("source: authority/local store")
	if len(groups) == 0 {
		fmt.Println("(no grant requests)")
		return
	}
	renderSection := func(header string, items []grantRequestGroup) {
		if len(items) == 0 {
			return
		}
		fmt.Println()
		fmt.Println(header)
		for _, group := range items {
			fmt.Printf("  %s %s %s — %s\n", grantHistoryBullet(group), displayGrantRequester(aliasIdx, group.RequesterPeerID), group.ServiceName, grantHistoryStatusPhrase(group))
			fmt.Printf("    requester %s · service peer %s · %d attempts · last seen %s\n", abbreviatePeerID(group.RequesterPeerID), abbreviatePeerID(group.ServicePeerID), group.Attempts, humanizeAgo(group.LastSeen))
			if verbose {
				fmt.Printf("    scope %s/%s\n", group.ClusterName, group.NamespaceID)
				if group.ServiceID != "" {
					fmt.Printf("    service id %s\n", abbreviateID(group.ServiceID))
				}
				if group.LatestRequestID != "" {
					fmt.Printf("    request %s\n", group.LatestRequestID)
				}
			}
		}
	}
	renderSection("Active approvals", approved)
	renderSection("Pending requests", pending)
	renderSection("Denied requests", denied)
	renderSection("Recent expired groups", expired)
	if hiddenExpired > 0 {
		fmt.Printf("\nOlder expired groups hidden: %d\n", hiddenExpired)
		fmt.Println("Show everything:")
		fmt.Println("  tubo grants history --all")
		fmt.Println("  tubo grants history --wide")
	}
}

func grantHistoryBullet(group grantRequestGroup) string {
	switch group.LatestStatus {
	case grantspkg.StatusApproved:
		return "✓"
	case grantspkg.StatusDenied:
		return "✗"
	case grantspkg.StatusExpired:
		return "·"
	case grantspkg.StatusPending:
		return "?"
	default:
		return "·"
	}
}

func grantHistoryStatusPhrase(group grantRequestGroup) string {
	now := time.Now().UTC()
	switch group.LatestStatus {
	case grantspkg.StatusApproved:
		if !group.LatestExpiresAt.IsZero() && group.LatestExpiresAt.After(now) {
			return "valid for " + humanizeRelativeExpiryFrom(group.LatestExpiresAt, now)
		}
		return "approved"
	case grantspkg.StatusPending:
		if !group.LatestExpiresAt.IsZero() && group.LatestExpiresAt.After(now) {
			return "pending, expires in " + humanizeRelativeExpiryFrom(group.LatestExpiresAt, now)
		}
		return "pending"
	case grantspkg.StatusDenied:
		if !group.LatestDecidedAt.IsZero() {
			return "denied " + humanizeElapsedFrom(group.LatestDecidedAt, now) + " ago"
		}
		return "denied"
	case grantspkg.StatusExpired:
		if !group.LatestExpiresAt.IsZero() {
			return "expired " + humanizeElapsedFrom(group.LatestExpiresAt, now) + " ago"
		}
		return "expired"
	default:
		return strings.TrimSpace(group.LatestStatus)
	}
}

func printGrantRequestWide(req grantspkg.Request) {
	fmt.Printf("ID: %s\n", req.ID)
	fmt.Printf("Status: %s\n", req.Status)
	fmt.Printf("Cluster: %s (%s)\n", req.ClusterName, req.ClusterID)
	fmt.Printf("Namespace: %s\n", req.NamespaceID)
	fmt.Printf("Requester PeerID: %s\n", req.RequesterPeerID)
	fmt.Printf("Service: %s (%s)\n", req.ServiceName, req.ServiceID)
	fmt.Printf("Service Kind: %s\n", grantspkg.NormalizeServiceShareKind(req.ServiceKind))
	fmt.Printf("Service PeerID: %s\n", req.ServicePeerID)
	fmt.Printf("Permissions: %s\n", strings.Join(req.RequestedPermissions, ","))
	fmt.Printf("Status Expires: %s\n", req.ExpiresAt.Format(time.RFC3339))
	if req.DenialReason != "" {
		fmt.Printf("Denial Reason: %s\n", req.DenialReason)
	}
}

func printGrantRequestReview(req grantspkg.Request, related []grantspkg.Request, storePath string) {
	aliasIdx, err := loadPeerAliasIndex()
	if err != nil {
		aliasIdx = peerAliasIndex{byID: map[string]peers.Alias{}}
	}
	group := summarizeGrantRequests([]grantspkg.Request{req}, aliasIdx)
	groupView := grantRequestGroup{}
	if len(group) > 0 {
		groupView = group[0]
	}
	if len(related) > 0 {
		groupView = summarizeGrantRequests(related, aliasIdx)[0]
	}
	fmt.Printf("Grant request %s\n\n", req.ID)
	fmt.Printf("Status: %s\n", req.Status)
	fmt.Printf("Scope: %s/%s\n", req.ClusterName, req.NamespaceID)
	fmt.Printf("Requested: %s\n", req.RequestedAt.UTC().Format(time.RFC3339))
	fmt.Printf("Expires: %s\n\n", req.ExpiresAt.UTC().Format(time.RFC3339))
	fmt.Println("Requester")
	fmt.Printf("  Alias: %s\n", displayGrantRequester(aliasIdx, req.RequesterPeerID))
	fmt.Printf("  Peer ID: %s\n", req.RequesterPeerID)
	if note := strings.TrimSpace(aliasIdx.note(req.RequesterPeerID)); note != "" {
		fmt.Printf("  Note: %s\n", note)
	}
	fmt.Printf("  Seen before: %s\n", yesNo(groupView.Attempts > 1))
	fmt.Printf("  Previous decisions: %d approved, %d denied, %d expired\n\n", groupView.Approved, groupView.Denied, groupView.Expired)
	fmt.Println("Service")
	fmt.Printf("  Name: %s\n", req.ServiceName)
	fmt.Printf("  Kind: %s\n", grantspkg.NormalizeServiceShareKind(req.ServiceKind))
	fmt.Printf("  Service ID: %s\n", req.ServiceID)
	fmt.Printf("  Service peer: %s\n", req.ServicePeerID)
	fmt.Println()
	fmt.Println("Requested permissions")
	if len(req.RequestedPermissions) == 0 {
		fmt.Println("  - none")
	} else {
		for _, perm := range req.RequestedPermissions {
			fmt.Printf("  - %s\n", perm)
		}
	}
	fmt.Println()
	fmt.Println("Suggested verification")
	fmt.Println("  Ask the requester to confirm:")
	fmt.Printf("  - requester peer suffix: %s\n", peerSuffix(req.RequesterPeerID))
	fmt.Printf("  - service peer suffix: %s\n", peerSuffix(req.ServicePeerID))
	fmt.Printf("  - service name: %s\n", req.ServiceName)
	fmt.Println()
	fmt.Println("Approve:")
	fmt.Printf("  tubo grants approve %s --ttl 168h\n", req.ID)
	fmt.Println("Deny:")
	fmt.Printf("  tubo grants deny %s --reason \"<reason>\"\n", req.ID)
	if storePath != "" {
		fmt.Printf("\nHistory source: authority/local store %s\n", storePath)
	}
}

func displayGrantRequester(aliasIdx peerAliasIndex, peerID string) string {
	if alias := strings.TrimSpace(aliasIdx.name(peerID)); alias != "" {
		return alias
	}
	return abbreviatePeerID(peerID)
}

func abbreviateID(value string) string {
	return abbreviateMiddle(strings.TrimSpace(value), 20)
}

func abbreviatePeerID(value string) string {
	return abbreviateMiddle(strings.TrimSpace(value), 24)
}

func abbreviateMiddle(value string, max int) string {
	if len(value) <= max || max < 8 {
		return value
	}
	left := (max - 1) / 2
	right := max - 1 - left
	return value[:left] + "…" + value[len(value)-right:]
}

func peerSuffix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	if len(value) <= 5 {
		return value
	}
	return "…" + value[len(value)-5:]
}

func humanizeAgo(ts time.Time) string {
	return humanizeAgoFrom(ts, time.Now().UTC())
}

func humanizeAgoFrom(ts, now time.Time) string {
	if ts.IsZero() {
		return "-"
	}
	d := now.Sub(ts.UTC())
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return ts.UTC().Format(time.RFC3339)
	}
}

func humanizeRelativeExpiry(ts time.Time) string {
	return humanizeRelativeExpiryFrom(ts, time.Now().UTC())
}

func humanizeRelativeExpiryFrom(ts, now time.Time) string {
	if ts.IsZero() {
		return "-"
	}
	d := ts.UTC().Sub(now)
	if d <= 0 {
		return "expired"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func humanizeElapsedFrom(ts, now time.Time) string {
	if ts.IsZero() {
		return "-"
	}
	d := now.Sub(ts.UTC())
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func relatedGrantRequests(all []grantspkg.Request, req grantspkg.Request) []grantspkg.Request {
	key := grantRequestGroupKey(req)
	out := make([]grantspkg.Request, 0, len(all))
	for _, candidate := range all {
		if grantRequestGroupKey(candidate) == key {
			out = append(out, candidate)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].RequestedAt.Equal(out[j].RequestedAt) {
			return out[i].RequestedAt.Before(out[j].RequestedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func printGrantListJSON(mode, storePath string, requests []grantspkg.Request, groups []grantRequestGroup) error {
	payload := grantRequestListJSON{Mode: mode, Store: storePath, Requests: requests, Groups: groups}
	return printJSON(payload)
}

func printGrantDescribeJSON(storePath string, req grantspkg.Request, group grantRequestGroup, related []grantspkg.Request, alias string) error {
	payload := grantRequestDescribeJSON{
		Store:           storePath,
		Request:         req,
		Group:           group,
		RelatedRequests: related,
		RequesterAlias:  alias,
		Review: grantReviewNotes{
			SuggestedVerification: []string{"requester peer suffix: " + peerSuffix(req.RequesterPeerID), "service peer suffix: " + peerSuffix(req.ServicePeerID), "service name: " + req.ServiceName},
			ApproveCommand:        fmt.Sprintf("tubo grants approve %s --ttl 168h", req.ID),
			DenyCommand:           fmt.Sprintf("tubo grants deny %s --reason \"<reason>\"", req.ID),
		},
	}
	return printJSON(payload)
}
