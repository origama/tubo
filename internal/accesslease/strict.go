package accesslease

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

// decodeStrictJSON is the shared strict, bounded decoder used by every
// credential in this package (Access Lease, Owner Binding, Exact Invite). It
// enforces, at the JSON level and before any typed unmarshal:
//
//   - the document is a flat JSON object (nested objects and arrays, the only
//     way to smuggle a forbidden Scope/endpoint/discovery/metadata map into a
//     flat credential, are rejected);
//   - every key present is an explicitly allowed member of that credential;
//   - no key appears more than once (duplicate/ambiguous keys rejected);
//   - each string member is length- and field-bounded BEFORE typed unmarshal
//     and before any cryptographic verification;
//   - nothing follows the closing brace (trailing payload rejected);
//   - the encoded field is length-bounded before and after the base64 decode.
func decodeStrictJSON(encoded string, allowed map[string]int) ([]byte, error) {
	if len(encoded) > MaxBytes {
		return nil, errors.New("accesslease: encoded field exceeds bound")
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errStrict
	}
	if len(data) > MaxBytes {
		return nil, errors.New("accesslease: payload exceeds byte bound")
	}
	if err := scanFlatObject(data, allowed); err != nil {
		return nil, err
	}
	return data, nil
}

// ScanStrictFlat validates data as a flat JSON object whose members are all
// explicitly allowed by name (with per-member maximum string length bounds),
// with no duplicate or unknown keys, no nested object/array value, and no
// trailing payload. It is the shared structural anti-metadata /
// anti-ambiguity / anti-allocation gate used both by the credential decoders
// here and by local exact-authority key-envelope loading. Data must already be
// subject to the caller's total byte bound.
func ScanStrictFlat(data []byte, allowed map[string]int) error {
	return scanFlatObject(data, allowed)
}

// scanFlatObject token-scans a JSON document demanding a flat object with only
// allow-listed scalar members, no duplicate keys, no nested value, and no
// trailing payload. It returns the raw token bytes it consumed so the caller
// can unmarshal exactly once, without re-reading the stream.
func scanFlatObject(data []byte, allowed map[string]int) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil {
		return errStrict
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '{' {
		return errStrict
	}
	seen := make(map[string]bool, len(allowed))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return errStrict
		}
		key, ok := keyToken.(string)
		if !ok {
			return errStrict
		}
		if seen[key] {
			return errors.New("accesslease: duplicate key " + key)
		}
		max, ok := allowed[key]
		if !ok {
			return errors.New("accesslease: unknown field " + key)
		}
		seen[key] = true
		valueToken, err := decoder.Token()
		if err != nil {
			return errStrict
		}
		if _, isDelim := valueToken.(json.Delim); isDelim {
			return errors.New("accesslease: nested value for " + key)
		}
		if s, isStr := valueToken.(string); isStr && len(s) > max {
			return errors.New("accesslease: field " + key + " exceeds bound")
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return errStrict
	}
	if e, ok := end.(json.Delim); !ok || e != '}' {
		return errStrict
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("accesslease: trailing payload")
	}
	return nil
}
