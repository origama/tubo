package p2p

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
)

func StripHopByHopHeaders(h http.Header) {
	for _, k := range []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(k)
	}
}

func EncodeJSON(w io.Writer, v any) error {
	return json.NewEncoder(w).Encode(v)
}

func DecodeJSON(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}

func HandleServiceStream(localTarget string) func(network.Stream) {
	return func(s network.Stream) {
		defer s.Close()

		dec := json.NewDecoder(bufio.NewReader(s))
		enc := json.NewEncoder(s)

		var req RequestMessage
		if err := dec.Decode(&req); err != nil {
			_ = enc.Encode(ResponseMessage{StatusCode: http.StatusBadRequest, Headers: map[string]string{"content-type": "text/plain"}, Body: []byte("decode request: " + err.Error())})
			return
		}

		upURL := strings.TrimRight(localTarget, "/") + req.Path
		if req.RawQuery != "" {
			upURL += "?" + req.RawQuery
		}

		upReq, err := http.NewRequest(req.Method, upURL, bytes.NewReader(req.Body))
		if err != nil {
			_ = enc.Encode(ResponseMessage{StatusCode: http.StatusInternalServerError, Headers: map[string]string{"content-type": "text/plain"}, Body: []byte("build upstream request: " + err.Error())})
			return
		}
		for k, v := range req.Headers {
			upReq.Header.Set(k, v)
		}
		StripHopByHopHeaders(upReq.Header)

		resp, err := http.DefaultClient.Do(upReq)
		if err != nil {
			_ = enc.Encode(ResponseMessage{StatusCode: http.StatusBadGateway, Headers: map[string]string{"content-type": "text/plain"}, Body: []byte("upstream failed: " + err.Error())})
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			_ = enc.Encode(ResponseMessage{StatusCode: http.StatusBadGateway, Headers: map[string]string{"content-type": "text/plain"}, Body: []byte("read upstream body: " + err.Error())})
			return
		}

		hdrs := make(map[string]string, len(resp.Header))
		for k, values := range resp.Header {
			if len(values) == 0 {
				continue
			}
			hdrs[k] = values[0]
		}
		StripHopByHopHeaders(resp.Header)
		if _, ok := hdrs["Content-Type"]; !ok && resp.Header.Get("Content-Type") != "" {
			hdrs["Content-Type"] = resp.Header.Get("Content-Type")
		}

		_ = enc.Encode(ResponseMessage{StatusCode: resp.StatusCode, Headers: hdrs, Body: body})
	}
}

func HandleClientRequest(s network.Stream, req RequestMessage) (ResponseMessage, error) {
	enc := json.NewEncoder(s)
	dec := json.NewDecoder(bufio.NewReader(s))
	if err := enc.Encode(req); err != nil {
		return ResponseMessage{}, fmt.Errorf("encode request: %w", err)
	}

	var resp ResponseMessage
	if err := dec.Decode(&resp); err != nil {
		return ResponseMessage{}, fmt.Errorf("decode response: %w", err)
	}
	return resp, nil
}
