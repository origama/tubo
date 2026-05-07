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

type NetworkPayload struct {
	Name        string          `json:"name"`
	ID          string          `json:"id"`
	Visibility  string          `json:"visibility,omitempty"`
	Description string          `json:"description,omitempty"`
	Relays      []string        `json:"relays"`
	SwarmKey    SwarmKeyPayload `json:"swarm_key"`
	Network     NetworkOptions  `json:"network"`
	Validity    ValidityWindow  `json:"validity"`
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
