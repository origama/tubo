package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/origama/tubo-rebuilt/internal/store"
)

type peerView struct {
	PeerID     string    `json:"peer_id"`
	Name       string    `json:"name,omitempty"`
	Note       string    `json:"note,omitempty"`
	Source     string    `json:"source"`
	Connected  bool      `json:"connected"`
	ObservedBy []string  `json:"observed_by,omitempty"`
	Addresses  []string  `json:"addresses,omitempty"`
	LastSeen   time.Time `json:"last_seen,omitempty"`
}

func buildPeerViews(ctx context.Context, state *store.Store) ([]peerView, error) {
	aliases, err := state.PeerAliases(ctx)
	if err != nil {
		return nil, err
	}
	values := map[string]*peerView{}
	for _, alias := range aliases {
		values[alias.PeerID] = &peerView{PeerID: alias.PeerID, Name: alias.Name, Note: alias.Note, Source: "alias", LastSeen: alias.UpdatedAt}
	}
	runs, err := state.Runs(ctx)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, run := range runs {
		identity, identityErr := store.ProcessIdentity(run.PID)
		live[run.PrimaryRef] = identityErr == nil && identity == run.ProcessIdentity && (run.Status == "ready" || run.Status == "degraded")
	}
	observed, err := state.RuntimePeers(ctx)
	if err != nil {
		return nil, err
	}
	addressSets := map[string]map[string]bool{}
	runSets := map[string]map[string]bool{}
	for _, item := range observed {
		if !live[item.RunRef] {
			continue
		}
		value := values[item.PeerID]
		if value == nil {
			value = &peerView{PeerID: item.PeerID, Source: "observed"}
			values[item.PeerID] = value
		} else if value.Source == "alias" {
			value.Source = "alias+observed"
		}
		value.Connected = true
		if item.LastSeen.After(value.LastSeen) {
			value.LastSeen = item.LastSeen
		}
		if addressSets[item.PeerID] == nil {
			addressSets[item.PeerID] = map[string]bool{}
			runSets[item.PeerID] = map[string]bool{}
		}
		runSets[item.PeerID][item.RunRef] = true
		for _, address := range item.Addresses {
			addressSets[item.PeerID][address] = true
		}
	}
	out := make([]peerView, 0, len(values))
	for id, value := range values {
		for run := range runSets[id] {
			value.ObservedBy = append(value.ObservedBy, run)
		}
		for address := range addressSets[id] {
			value.Addresses = append(value.Addresses, address)
		}
		sort.Strings(value.ObservedBy)
		sort.Strings(value.Addresses)
		out = append(out, *value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].PeerID < out[j].PeerID
		}
		if out[i].Name == "" {
			return false
		}
		if out[j].Name == "" {
			return true
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func renderPeerViews(out io.Writer, items []peerView) error {
	if len(items) == 0 {
		_, err := fmt.Fprintln(out, "No peers observed and no aliases configured.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "PEER ID\tALIAS\tSOURCE\tCONNECTED\tOBSERVED BY\tADDRESSES\tLAST SEEN"); err != nil {
		return err
	}
	for _, item := range items {
		name := item.Name
		if name == "" {
			name = "-"
		}
		observed := strings.Join(item.ObservedBy, ",")
		if observed == "" {
			observed = "-"
		}
		addresses := fmt.Sprint(len(item.Addresses))
		seen := "-"
		if !item.LastSeen.IsZero() {
			seen = item.LastSeen.Local().Format(time.RFC3339)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\t%s\t%s\n", item.PeerID, name, item.Source, item.Connected, observed, addresses, seen); err != nil {
			return err
		}
	}
	return w.Flush()
}
