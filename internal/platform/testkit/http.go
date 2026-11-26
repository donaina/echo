package testkit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// Request builds an HTTP request for a test, failing the test rather than
// returning an error for the parts that cannot go wrong.
type Request struct {
	t       *testing.T
	method  string
	url     string
	headers http.Header
	body    []byte
}

// NewRequest starts a request with no body.
func NewRequest(t *testing.T, method, url string) *Request {
	t.Helper()
	return &Request{t: t, method: method, url: url, headers: make(http.Header)}
}

// Header sets a request header.
func (r *Request) Header(key, value string) *Request {
	r.headers.Set(key, value)
	return r
}

// JSON encodes v as the request body.
func (r *Request) JSON(v any) *Request {
	r.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		r.t.Fatalf("encode request body: %v", err)
	}
	r.body = b
	r.headers.Set("Content-Type", "application/json")
	return r
}

// Do sends the request and returns the response with its body already read,
// so the caller does not leak connections.
func (r *Request) Do() (*http.Response, []byte) {
	r.t.Helper()
	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequest(r.method, r.url, body)
	if err != nil {
		r.t.Fatalf("build request: %v", err)
	}
	for k, vs := range r.headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", r.method, r.url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		r.t.Fatalf("read response body: %v", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp, b
}
