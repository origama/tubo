package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/origama/tubo-rebuilt/internal/discovery"
	"github.com/origama/tubo-rebuilt/internal/domain"
	appRuntime "github.com/origama/tubo-rebuilt/internal/runtime"
	"github.com/origama/tubo-rebuilt/internal/store"
)

type pipeView struct {
	Ref          string              `json:"ref"`
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Source       string              `json:"source"`
	Scope        string              `json:"scope,omitempty"`
	Ingress      domain.IngressKind  `json:"ingress,omitempty"`
	Target       string              `json:"target,omitempty"`
	Visibility   domain.Visibility   `json:"visibility"`
	Access       domain.AccessPolicy `json:"access,omitempty"`
	Status       string              `json:"status"`
	Path         string              `json:"path"`
	PathSource   string              `json:"path_source,omitempty"`
	Sessions     int                 `json:"sessions"`
	PeerID       string              `json:"peer_id,omitempty"`
	Capabilities []string            `json:"capabilities,omitempty"`
	ExpiresAt    *time.Time          `json:"expires_at,omitempty"`
}

func advertisedPath(addresses []string) (string, map[string]int) {
	counts := map[string]int{"direct": 0, "relayed": 0}
	for _, address := range addresses {
		if strings.Contains(address, "/p2p-circuit") {
			counts["relayed"]++
		} else {
			counts["direct"]++
		}
	}
	switch {
	case counts["direct"] > 0 && counts["relayed"] > 0:
		return "mixed", counts
	case counts["direct"] > 0:
		return "direct", counts
	case counts["relayed"] > 0:
		return "relayed", counts
	default:
		return "unknown", counts
	}
}

func sessionPath(sessions []store.Session) (string, map[string]int) {
	counts := map[string]int{}
	for _, session := range sessions {
		counts[session.InitialTransportPath]++
	}
	if len(sessions) == 0 || sessions[0].InitialTransportPath == "" {
		return "", counts
	}
	return sessions[0].InitialTransportPath, counts
}

func localPipeStatus(run store.Run, err error) string {
	if err != nil {
		return "defined"
	}
	if run.Status == "ready" || run.Status == "starting" || run.Status == "degraded" {
		identity, identityErr := store.ProcessIdentity(run.PID)
		if identityErr != nil || identity != run.ProcessIdentity {
			return "stale"
		}
	}
	return run.Status
}

func localAdvertisedRecord(ctx context.Context, state *store.Store, spec domain.PipeSpec) (discovery.PipeRecord, bool) {
	if spec.Scope != nil {
		if stored, err := state.RecordInScope(ctx, spec.Scope.String(), spec.PipeID); err == nil {
			if record, err := discovery.Parse(stored.Payload); err == nil {
				return record, true
			}
		}
	}
	if spec.Target.Kind == domain.KindPipe {
		if stored, err := state.Record(ctx, spec.Target.Name); err == nil {
			if record, err := appRuntime.ParseVerifiedRecord(ctx, state, stored); err == nil {
				return record, true
			}
		}
	}
	return discovery.PipeRecord{}, false
}

func buildPipeViews(ctx context.Context, state *store.Store, scopeFilter string) ([]pipeView, error) {
	locals, err := state.Pipes(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]pipeView, 0, len(locals))
	indexes := map[string]int{}
	for _, spec := range locals {
		scope := ""
		if spec.Scope != nil {
			scope = spec.Scope.String()
		}
		run, runErr := state.Run(ctx, spec.Ref.String())
		sessions, err := state.Sessions(ctx, spec.Ref.String())
		if err != nil {
			return nil, err
		}
		path, _ := sessionPath(sessions)
		pathSource := "last_session"
		record, hasRecord := localAdvertisedRecord(ctx, state, spec)
		if path == "" && hasRecord {
			path, _ = advertisedPath(record.Addresses)
			pathSource = "advertised"
		}
		if path == "" {
			path, pathSource = "unknown", ""
		}
		item := pipeView{Ref: spec.Ref.String(), ID: spec.PipeID, Name: spec.Ref.Name, Source: "local", Scope: scope, Ingress: spec.Ingress.Kind, Target: spec.Target.String(), Visibility: spec.Visibility, Access: spec.Access, Status: localPipeStatus(run, runErr), Path: path, PathSource: pathSource, Sessions: len(sessions)}
		items = append(items, item)
		indexes[scope+"\x00"+spec.PipeID] = len(items) - 1
	}
	memberships, err := state.Memberships(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, membership := range memberships {
		if scopeFilter != "" && membership.Scope != scopeFilter {
			continue
		}
		records, err := state.Records(ctx, membership.Scope)
		if err != nil {
			return nil, err
		}
		for _, stored := range records {
			key := membership.Scope + "\x00" + stored.PipeID
			if seen[key] {
				continue
			}
			seen[key] = true
			record, err := appRuntime.ParseVerifiedRecord(ctx, state, stored)
			if err != nil {
				return nil, err
			}
			path, _ := advertisedPath(record.Addresses)
			expires := record.ExpiresAt
			if index, ok := indexes[key]; ok {
				items[index].Source = "local+network"
				items[index].PeerID = record.RuntimePeerID
				items[index].Capabilities = append([]string(nil), record.Capabilities...)
				items[index].ExpiresAt = &expires
				if items[index].Path == "unknown" {
					items[index].Path = path
					items[index].PathSource = "advertised"
				}
				continue
			}
			items = append(items, pipeView{Ref: "pipe/" + record.PipeID, ID: record.PipeID, Name: record.DisplayName, Source: "network", Scope: record.Scope.String(), Visibility: record.Visibility, Access: record.Access, Status: "available", Path: path, PathSource: "advertised", PeerID: record.RuntimePeerID, Capabilities: append([]string(nil), record.Capabilities...), ExpiresAt: &expires})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].ID < items[j].ID
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

func renderPipeViews(out io.Writer, items []pipeView) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tID\tSOURCE\tSCOPE\tINGRESS\tTARGET\tACCESS\tSTATUS\tSESSIONS\tPATH\tPATH SOURCE"); err != nil {
		return err
	}
	for _, item := range items {
		scope := item.Scope
		if scope == "" {
			scope = "-"
		}
		ingress := string(item.Ingress)
		if ingress == "" {
			ingress = "-"
		}
		target := item.Target
		if target == "" {
			target = "-"
		}
		access := string(item.Access)
		if access == "" {
			access = "-"
		}
		source := item.PathSource
		if source == "" {
			source = "-"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", item.Name, item.ID, item.Source, scope, ingress, target, access, item.Status, item.Sessions, item.Path, source); err != nil {
			return err
		}
	}
	return w.Flush()
}

func transportSummary(ctx context.Context, state *store.Store, spec domain.PipeSpec, sessions []store.Session) map[string]any {
	last, sessionCounts := sessionPath(sessions)
	result := map[string]any{"session_counts": sessionCounts}
	if record, ok := localAdvertisedRecord(ctx, state, spec); ok {
		advertised, counts := advertisedPath(record.Addresses)
		result["advertised_path"] = advertised
		result["advertised_candidates"] = counts
		if last == "" {
			result["current"] = advertised
			result["source"] = "advertised"
		}
	}
	if last != "" {
		result["current"] = last
		result["source"] = "last_session"
	}
	if _, ok := result["current"]; !ok {
		result["current"] = "unknown"
	}
	return result
}
