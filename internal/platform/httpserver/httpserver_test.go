package httpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDrainCompletesInFlight(t *testing.T) {
	entered := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})
	s := New(Options{
		Service:         "test",
		Logger:          discardLogger(),
		Handler:         h,
		DrainDelay:      0,
		ShutdownTimeout: 5 * time.Second,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx, ln) }()

	type result struct {
		status int
		body   string
		err    error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		got <- result{status: resp.StatusCode, body: string(b), err: err}
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached the handler")
	}
	cancel()

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("in-flight request failed: %v", r.err)
		}
		if r.status != http.StatusOK || r.body != "done" {
			t.Fatalf("response = %d %q, want 200 done", r.status, r.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown cut off the in-flight request")
	}

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after shutdown")
	}
}

func TestReadinessFailsUntilReadyAndWhenACheckFails(t *testing.T) {
	probes := NewProbes(discardLogger())
	s := New(Options{Service: "test", Logger: discardLogger(), Probes: probes})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	if status, _, _ := get(t, ts.URL+"/healthz"); status != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", status)
	}
	if status, _, _ := get(t, ts.URL+"/readyz"); status != http.StatusServiceUnavailable {
		t.Fatalf("readyz before Ready = %d, want 503", status)
	}

	probes.Ready()
	if status, _, _ := get(t, ts.URL+"/readyz"); status != http.StatusOK {
		t.Fatalf("readyz = %d, want 200", status)
	}

	probes.Add("postgres", func(context.Context) error { return errors.New("connection refused") })
	status, _, body := get(t, ts.URL+"/readyz")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("readyz with a failing check = %d, want 503", status)
	}
	if !strings.Contains(body, "postgres") {
		t.Fatalf("readyz body does not name the failing check: %s", body)
	}
}

func TestPanicBecomesProblemJSONAndTheServerContinues(t *testing.T) {
	var calls atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s := New(Options{Service: "test", Logger: discardLogger(), Handler: h})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	status, header, body := get(t, ts.URL+"/work")
	if status != http.StatusInternalServerError {
		t.Fatalf("panic = %d, want 500", status)
	}
	if ct := header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q", ct)
	}
	if strings.Contains(body, "boom") {
		t.Fatalf("response leaks the panic: %s", body)
	}
	if status, _, _ := get(t, ts.URL+"/work"); status != http.StatusNoContent {
		t.Fatalf("second request = %d, want 204", status)
	}
}

func TestRequestIDIsPropagatedAndSanitised(t *testing.T) {
	s := New(Options{
		Service: "test",
		Logger:  discardLogger(),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Header.Set("X-Request-Id", "client-request-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Request-Id"); got != "client-request-1" {
		t.Fatalf("X-Request-Id = %q, want it echoed", got)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Header.Set("X-Request-Id", "bad id<with> junk")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Request-Id"); got != "badidwithjunk" {
		t.Fatalf("sanitised id = %q", got)
	}
}

func get(t *testing.T, url string) (status int, header http.Header, body string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, resp.Header, string(b)
}
