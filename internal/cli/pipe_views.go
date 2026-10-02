package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/origama/tubo/internal/discovery"
	"github.com/origama/tubo/internal/domain"
	appRuntime "github.com/origama/tubo/internal/runtime"
	"github.com/origama/tubo/internal/store"
)

type pipeView struct {
	Ref          string              `json:"ref"`
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Source       string              `json:"source"`
	Freshness    string              `json:"freshness,omitempty"`
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
	RecordAge    string              `json:"record_age,omitempty"`
}

func recordAge(record discovery.PipeRecord, now time.Time) string {
	age := now.UTC().Sub(record.IssuedAt)
	if age < 0 {
		age = 0
	}
	return age.Round(time.Second).String()
}

func recordFresh(record discovery.PipeRecord, now time.Time) bool {
	age := now.UTC().Sub(record.IssuedAt)
	return age >= 0 && age < 5*time.Minute
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
			if record, err := appRuntime.ParseVerifiedRecord(ctx, state, stored); err == nil {
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

func buildPipeViews(ctx context.Context, state *store.Store, scopeFilter string, freshnessByScope map[string]string) ([]pipeView, error) {
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
		// Unscoped pipes are local-only and remain visible for any scope filter.
		if scopeFilter != "" && scope != "" && scope != scopeFilter {
			continue
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
		item := pipeView{Ref: spec.Ref.String(), ID: spec.PipeID, Name: spec.Ref.Name, Source: "local", Freshness: freshnessByScope[scope], Scope: scope, Ingress: spec.Ingress.Kind, Target: spec.Target.String(), Visibility: spec.Visibility, Access: spec.Access, Status: localPipeStatus(run, runErr), Path: path, PathSource: pathSource, Sessions: len(sessions)}
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
				continue
			}
			path, _ := advertisedPath(record.Addresses)
			expires := record.ExpiresAt
			if index, ok := indexes[key]; ok {
				items[index].Source = "local+network"
				items[index].PeerID = record.RuntimePeerID
				items[index].Capabilities = append([]string(nil), record.Capabilities...)
				items[index].ExpiresAt = &expires
				items[index].RecordAge = recordAge(record, time.Now().UTC())
				if items[index].Path == "unknown" {
					items[index].Path = path
					items[index].PathSource = "advertised"
				}
				continue
			}
			items = append(items, pipeView{Ref: "pipe/" + record.PipeID, ID: record.PipeID, Name: record.DisplayName, Source: "network", Freshness: freshnessByScope[record.Scope.String()], Scope: record.Scope.String(), Visibility: record.Visibility, Access: record.Access, Status: "available", Path: path, PathSource: "advertised", PeerID: record.RuntimePeerID, Capabilities: append([]string(nil), record.Capabilities...), ExpiresAt: &expires, RecordAge: recordAge(record, time.Now().UTC())})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		if items[i].Scope != items[j].Scope {
			return items[i].Scope < items[j].Scope
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func shortPipeID(id string) string {
	if len(id) > 20 {
		return id[:17] + "..."
	}
	return id
}

// renderPipeViews renders the pipe table. The default (compact) form fits a
// normal terminal: name, truncated ID, status, source, path, access, scope.
// --wide adds the full ID plus ingress, target, sessions, freshness, record
// age and path source.
func renderPipeViews(out io.Writer, items []pipeView, wide bool) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if wide {
		if _, err := fmt.Fprintln(w, "NAME\tID\tSTATUS\tSOURCE\tPATH\tINGRESS\tTARGET\tACCESS\tSCOPE\tSESSIONS\tFRESHNESS\tRECORD AGE\tPATH SOURCE"); err != nil {
			return err
		}
		for _, item := range items {
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", item.Name, item.ID, item.Status, item.Source, item.Path, dash(item.Ingress), dash(item.Target), dash(item.Access), dash(item.Scope), item.Sessions, dash(item.Freshness), dash(item.RecordAge), dash(item.PathSource)); err != nil {
				return err
			}
		}
		return w.Flush()
	}
	if _, err := fmt.Fprintln(w, "NAME\tID\tSTATUS\tSOURCE\tPATH\tACCESS\tSCOPE"); err != nil {
		return err
	}
	for _, item := range items {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", item.Name, shortPipeID(item.ID), item.Status, item.Source, item.Path, dash(item.Access), dash(item.Scope)); err != nil {
			return err
		}
	}
	return w.Flush()
}

func dash(value any) string {
	switch v := value.(type) {
	case string:
		if v == "" {
			return "-"
		}
		return v
	case domain.IngressKind:
		if v == "" {
			return "-"
		}
		return string(v)
	case domain.AccessPolicy:
		if v == "" {
			return "-"
		}
		return string(v)
	}
	return "-"
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

const pipeCacheFreshTTL = 5 * time.Minute

type pipeRefreshError struct {
	Class string `json:"class"`
}

type pipeScopeStatus struct {
	Scope            string            `json:"scope"`
	CacheState       string            `json:"cache_state"`
	Source           string            `json:"source,omitempty"`
	LastSuccess      *time.Time        `json:"last_success,omitempty"`
	RefreshAttempted bool              `json:"refresh_attempted"`
	RefreshError     *pipeRefreshError `json:"refresh_error,omitempty"`
}

func pipeCacheStatus(value store.ScopeFreshness, found bool, now time.Time) pipeScopeStatus {
	status := pipeScopeStatus{Scope: value.Scope, CacheState: "missing", Source: value.Source, LastSuccess: value.LastSuccess}
	if !found {
		return status
	}
	status.Scope = value.Scope
	if value.Complete && value.Source == store.SnapshotSourcePinnedAuthority && value.LastSuccess != nil && !value.LastSuccess.After(now) && now.Sub(*value.LastSuccess) < pipeCacheFreshTTL {
		status.CacheState = "fresh"
	} else {
		status.CacheState = "stale"
	}
	return status
}

func pipeRefreshErrorClass(err error) string {
	message := err.Error()
	for _, class := range []string{"invalid_input", "not_found", "authorization_denied", "authority_unreachable", "discovery_unreachable", "discovery_retry_after", "persistence_failed", "partial_snapshot", "invalid_snapshot"} {
		if strings.Contains(message, class) {
			return class
		}
	}
	return "discovery_unreachable"
}

func (a App) refreshPipeRecords(ctx context.Context, state *store.Store, scope string, force bool) error {
	options := appRuntime.FetchRecordsOptions{Force: force, NetworkOperationObserver: a.networkOperationObserver}
	if a.fetchRecords != nil {
		return a.fetchRecords(ctx, state, scope, options)
	}
	_, err := appRuntime.FetchRecordsWithOptions(ctx, state, scope, options)
	return err
}

// getPipes lists local Pipe definitions plus verified network records,
// cache-first. The human table is compact by default; --wide adds detail.
func (a App) getPipes(ctx context.Context, state *store.Store, f flags) error {
	if f.bools["refresh"] && f.bools["cached-only"] {
		return errors.New("invalid_input: --refresh and --cached-only cannot be used together")
	}
	scope := f.values["scope"]
	memberships, err := state.Memberships(ctx)
	if err != nil {
		return err
	}
	statuses := make([]pipeScopeStatus, 0, len(memberships))
	statusByScope := make(map[string]string, len(memberships))
	now := a.currentTime()
	for _, membership := range memberships {
		if scope != "" && membership.Scope != scope {
			continue
		}
		freshness, freshnessErr := state.ScopeFreshness(ctx, membership.Scope)
		found := freshnessErr == nil
		if freshnessErr != nil && !errors.Is(freshnessErr, domain.ErrNotFound) {
			return fmt.Errorf("persistence_failed: discovery freshness: %w", freshnessErr)
		}
		if !found {
			freshness.Scope = membership.Scope
		}
		status := pipeCacheStatus(freshness, found, now)
		if !f.bools["cached-only"] && (f.bools["refresh"] || status.CacheState != "fresh") {
			status.RefreshAttempted = true
			if refreshErr := a.refreshPipeRecords(ctx, state, membership.Scope, f.bools["refresh"]); refreshErr != nil {
				status.RefreshError = &pipeRefreshError{Class: pipeRefreshErrorClass(refreshErr)}
				updated, updatedErr := state.ScopeFreshness(ctx, membership.Scope)
				if updatedErr == nil {
					status = pipeCacheStatus(updated, true, a.currentTime())
					status.RefreshAttempted = true
					status.RefreshError = &pipeRefreshError{Class: pipeRefreshErrorClass(refreshErr)}
				}
			} else if updated, updatedErr := state.ScopeFreshness(ctx, membership.Scope); updatedErr == nil {
				status = pipeCacheStatus(updated, true, a.currentTime())
				status.RefreshAttempted = true
			}
		}
		statuses = append(statuses, status)
		statusByScope[membership.Scope] = status.CacheState
	}
	items, err := buildPipeViews(ctx, state, scope, statusByScope)
	if err != nil {
		return err
	}
	if f.bools["json"] {
		return json.NewEncoder(a.Out).Encode(map[string]any{"items": items, "scopes": statuses})
	}
	if err := renderPipeViews(a.Out, items, f.bools["wide"]); err != nil {
		return err
	}
	for _, status := range statuses {
		if status.RefreshError != nil {
			fmt.Fprintf(a.Err, "warning: discovery scope=%s cache=%s error_class=%s; showing verified cache\n", status.Scope, status.CacheState, status.RefreshError.Class)
		}
	}
	return nil
}

// pipeWatchEvent is one NDJSON record emitted by `tubo watch pipes --json`.
// It contains only a projection of verified local state; watch never opens a
// discovery socket or accepts an unverified remote event.
type pipeWatchEvent struct {
	EventType  string    `json:"event_type"`
	Scope      string    `json:"scope"`
	PipeID     string    `json:"pipe_id"`
	Name       string    `json:"name"`
	Changed    []string  `json:"changed,omitempty"`
	Item       *pipeView `json:"item,omitempty"`
	Previous   *pipeView `json:"previous,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

func (a App) watchPipesCmd(ctx context.Context, state *store.Store, f flags) error {
	interval := 2 * time.Second
	if raw := f.values["interval"]; raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 500*time.Millisecond {
			return errors.New("invalid_input: --interval must be a duration of at least 500ms")
		}
		interval = parsed
	}
	return a.watchPipes(ctx, state, f.values["scope"], interval, f.bools["json"])
}

// watchPipes polls the transactional local projection. Output is synchronous:
// a slow writer backpressures polling instead of accumulating a goroutine or
// an unbounded event queue.
func (a App) watchPipes(ctx context.Context, state *store.Store, scope string, interval time.Duration, jsonOutput bool) error {
	previous := map[string]pipeView{}
	poll := func() error {
		freshness, err := a.watchPipeFreshness(ctx, state, scope)
		if err != nil {
			return err
		}
		items, err := buildPipeViews(ctx, state, scope, freshness)
		if err != nil {
			return err
		}
		current := make(map[string]pipeView, len(items))
		for _, item := range items {
			current[pipeWatchKey(item)] = item
		}
		events := diffPipeViews(previous, current, a.currentTime())
		for _, event := range events {
			if err := a.emitPipeWatchEvent(jsonOutput, event); err != nil {
				return err
			}
		}
		previous = current
		return nil
	}
	if err := poll(); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := poll(); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			}
		}
	}
}

func (a App) watchPipeFreshness(ctx context.Context, state *store.Store, scopeFilter string) (map[string]string, error) {
	memberships, err := state.Memberships(ctx)
	if err != nil {
		return nil, err
	}
	freshness := make(map[string]string, len(memberships))
	for _, membership := range memberships {
		if scopeFilter != "" && membership.Scope != scopeFilter {
			continue
		}
		value, err := state.ScopeFreshness(ctx, membership.Scope)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf("persistence_failed: discovery freshness: %w", err)
		}
		if err != nil {
			value.Scope = membership.Scope
		}
		freshness[membership.Scope] = pipeCacheStatus(value, err == nil, a.currentTime()).CacheState
	}
	return freshness, nil
}

func pipeWatchKey(item pipeView) string { return item.Scope + "\x00" + item.ID }

func diffPipeViews(previous, current map[string]pipeView, observedAt time.Time) []pipeWatchEvent {
	keys := make([]string, 0, len(previous)+len(current))
	seen := make(map[string]bool, len(previous)+len(current))
	for key := range previous {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range current {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	events := make([]pipeWatchEvent, 0, len(keys))
	for _, key := range keys {
		before, beforeOK := previous[key]
		after, afterOK := current[key]
		switch {
		case !beforeOK && afterOK:
			value := after
			events = append(events, pipeWatchEvent{EventType: "added", Scope: after.Scope, PipeID: after.ID, Name: after.Name, Item: &value, ObservedAt: observedAt})
		case beforeOK && !afterOK:
			value := before
			eventType := "removed"
			if before.ExpiresAt != nil && !before.ExpiresAt.After(observedAt) {
				eventType = "expired"
			}
			events = append(events, pipeWatchEvent{EventType: eventType, Scope: before.Scope, PipeID: before.ID, Name: before.Name, Previous: &value, ObservedAt: observedAt})
		case beforeOK && afterOK:
			changed := changedPipeViewFields(before, after)
			if len(changed) == 0 {
				continue
			}
			oldValue, newValue := before, after
			events = append(events, pipeWatchEvent{EventType: pipeWatchChangeType(before, after, observedAt), Scope: after.Scope, PipeID: after.ID, Name: after.Name, Changed: changed, Item: &newValue, Previous: &oldValue, ObservedAt: observedAt})
		}
	}
	return events
}

// pipeWatchChangeType preserves distinct degraded/recovered observations while
// keeping unrelated projection changes as updated. Status is local verified
// runtime state; source and freshness remain available in Changed as evidence.
func pipeWatchChangeType(before, after pipeView, observedAt time.Time) string {
	switch {
	case before.ExpiresAt != nil && !before.ExpiresAt.After(observedAt) && pipeViewHasNetworkSource(before) && !pipeViewHasNetworkSource(after):
		return "expired"
	case before.Status != "degraded" && after.Status == "degraded":
		return "degraded"
	case before.Status == "degraded" && after.Status != "degraded":
		return "recovered"
	default:
		return "updated"
	}
}

func pipeViewHasNetworkSource(item pipeView) bool {
	return item.Source == "network" || item.Source == "local+network"
}

func changedPipeViewFields(before, after pipeView) []string {
	changed := make([]string, 0, 3)
	if before.Status != after.Status {
		changed = append(changed, "status")
	}
	if before.Source != after.Source {
		changed = append(changed, "source")
	}
	if before.Freshness != after.Freshness {
		changed = append(changed, "freshness")
	}
	return changed
}

func (a App) emitPipeWatchEvent(jsonOutput bool, event pipeWatchEvent) error {
	if jsonOutput {
		return json.NewEncoder(a.Out).Encode(event)
	}
	item := event.Item
	if item == nil {
		item = event.Previous
	}
	if item == nil { // defensive: each event must contain its current or prior view.
		return errors.New("internal error: pipe watch event without view")
	}
	line := fmt.Sprintf("%s scope=%s pipe_id=%s name=%s status=%s source=%s freshness=%s", event.EventType, event.Scope, event.PipeID, event.Name, item.Status, item.Source, item.Freshness)
	if len(event.Changed) > 0 {
		line += " changed=" + strings.Join(event.Changed, ",")
	}
	_, err := fmt.Fprintln(a.Out, line)
	return err
}
