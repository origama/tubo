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

func (idx peerAliasIndex) label(peerID string) string {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return "unknown peer"
	}
	if alias, ok := idx.byID[peerID]; ok && strings.TrimSpace(alias.Name) != "" {
		return alias.Name
	}
	return "unknown peer"
}

func (idx peerAliasIndex) name(peerID string) string {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return ""
	}
	if alias, ok := idx.byID[peerID]; ok {
		return alias.Name
	}
	return ""
}

func (idx peerAliasIndex) note(peerID string) string {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return ""
	}
	if alias, ok := idx.byID[peerID]; ok {
		return alias.Note
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

func printGrantRequestsHuman(requests []grantspkg.Request, title, storePath string) {
	aliasIdx, err := loadPeerAliasIndex()
	if err != nil {
		aliasIdx = peerAliasIndex{byID: map[string]peers.Alias{}}
	}
	groups := summarizeGrantRequests(requests, aliasIdx)
	if title != "" {
		fmt.Println(title)
	}
	if storePath != "" {
		fmt.Printf("source: authority/local store %s\n", storePath)
	}
	if len(groups) == 0 {
		fmt.Println("(no grant requests)")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tSTATUS\tWHO\tSERVICE\tKIND\tATTEMPTS\tFIRST SEEN\tLAST SEEN\tEXPIRES")
	for i, group := range groups {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", i+1, group.LatestStatus, displayGrantRequester(aliasIdx, group.RequesterPeerID), group.ServiceName, grantspkg.NormalizeServiceShareKind(group.ServiceKind), group.Attempts, humanizeAgo(group.FirstSeen), humanizeAgo(group.LastSeen), humanizeRelativeExpiry(group.LatestExpiresAt))
		fmt.Fprintf(w, "\trequester: %s\n", abbreviatePeerID(group.RequesterPeerID))
		fmt.Fprintf(w, "\tservice:   %s\n", abbreviateID(group.ServiceID))
		fmt.Fprintf(w, "\tpeer:      %s\n", abbreviatePeerID(group.ServicePeerID))
	}
	_ = w.Flush()
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
	return "unknown peer"
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
	if ts.IsZero() {
		return "-"
	}
	d := time.Since(ts.UTC())
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
	if ts.IsZero() {
		return "-"
	}
	d := time.Until(ts.UTC())
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
