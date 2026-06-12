package main

import (
	"strings"

	peerspkg "github.com/origama/tubo/internal/peers"
)

type peerAliasIndex struct {
	byID map[string]peerspkg.Alias
}

func loadPeerAliasIndex() peerAliasIndex {
	aliases, err := peerspkg.NewStore(peerspkg.DefaultStorePath()).List()
	if err != nil {
		return peerAliasIndex{byID: map[string]peerspkg.Alias{}}
	}
	byID := make(map[string]peerspkg.Alias, len(aliases))
	for _, alias := range aliases {
		byID[alias.PeerID] = alias
	}
	return peerAliasIndex{byID: byID}
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

func displayPeerSummary(aliasIdx peerAliasIndex, peerID string) string {
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
