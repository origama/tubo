package networkbundle

import "encoding/json"

type Bundle struct {
	Kind            string          `json:"kind"`
	Version         int             `json:"version"`
	PayloadEncoding string          `json:"payload_encoding"`
	Payload         string          `json:"payload"`
	Signature       BundleSignature `json:"signature"`
}

type BundleSignature struct {
	Alg   string `json:"alg"`
	KeyID string `json:"key_id"`
	Value string `json:"value"`
}

type PublicClusterPayload struct {
	Name                 string   `json:"name"`
	ClusterID            string   `json:"cluster_id"`
	AuthorityPublicKey   string   `json:"authority_public_key"`
	DefaultNamespace     string   `json:"default_namespace"`
	GrantServiceProtocol string   `json:"grant_service_protocol"`
	GrantServicePeers    []string `json:"grant_service_peers"`
}

type NetworkPayload struct {
	Name          string                `json:"name"`
	ID            string                `json:"id"`
	Visibility    string                `json:"visibility,omitempty"`
	Description   string                `json:"description,omitempty"`
	Relays        []string              `json:"relays"`
	SwarmKey      SwarmKeyPayload       `json:"swarm_key"`
	Network       NetworkOptions        `json:"network"`
	PublicCluster *PublicClusterPayload `json:"public_cluster,omitempty"`
	Validity      ValidityWindow        `json:"validity"`
}

type SwarmKeyPayload struct {
	Type     string `json:"type"`
	Encoding string `json:"encoding"`
	Value    string `json:"value"`
}

type NetworkOptions struct {
	Autorelay         bool   `json:"autorelay"`
	HolePunching      bool   `json:"hole_punching"`
	ForceReachability string `json:"force_reachability,omitempty"`
}

type ValidityWindow struct {
	NotBefore string `json:"not_before"`
	NotAfter  string `json:"not_after"`
}

func Parse(data []byte) (*Bundle, error) {
	var bundle Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, err
	}
	return &bundle, nil
}
