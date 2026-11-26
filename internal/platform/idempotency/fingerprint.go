// Package idempotency makes a money request safe to retry: the same key and
// the same request return the stored response and move money once. The key is
// scoped to the caller and the route; the fingerprint is over a canonical
// form of the body, so two spellings of one JSON object are one request.
package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Fingerprint identifies a request. Two requests with one key and one
// fingerprint are the same intent; a different fingerprint is a client bug.
func Fingerprint(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(strings.ToUpper(method)))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(canonicalJSON(body))
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalJSON sorts object keys, removes insignificant whitespace, and
// normalises integer-valued numbers. A body that is not JSON is hashed as it
// arrived, because there is nothing to canonicalise.
func canonicalJSON(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return trimmed
	}
	var out bytes.Buffer
	writeCanonical(&out, v)
	return out.Bytes()
}

func writeCanonical(out *bytes.Buffer, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeJSONString(out, k)
			out.WriteByte(':')
			writeCanonical(out, x[k])
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				out.WriteByte(',')
			}
			writeCanonical(out, item)
		}
		out.WriteByte(']')
	case json.Number:
		out.WriteString(normaliseNumber(x.String()))
	case string:
		writeJSONString(out, x)
	case bool:
		if x {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nil:
		out.WriteString("null")
	default:
		encoded, _ := json.Marshal(x)
		out.Write(encoded)
	}
}

func writeJSONString(out *bytes.Buffer, s string) {
	encoded, _ := json.Marshal(s)
	out.Write(encoded)
}

// normaliseNumber makes 1000, 1000.0 and 1e3 one value, without turning a
// large integer into a float.
func normaliseNumber(raw string) string {
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return strconv.FormatInt(n, 10)
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return raw
}
