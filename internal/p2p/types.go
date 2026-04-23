package p2p

const ProtocolID = "/p2p-api-tunnel/http/1.0.0"

type RequestMessage struct {
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	RawQuery    string            `json:"raw_query,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        []byte            `json:"body,omitempty"`
}

type ResponseMessage struct {
	StatusCode  int               `json:"status_code"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        []byte            `json:"body,omitempty"`
}
