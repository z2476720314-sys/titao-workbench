package judge

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestModelReplySizeIsBoundedBeforeReturning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, (4<<20)+2)) }))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte(`{}`)))
	resp, err := DoVisionRequest(req, 5*time.Second)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("oversized response error=%v", err)
	}
}

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestModelJudgmentRetriesNativeWinsockErrors(t *testing.T) {
	for _, code := range []syscall.Errno{10053, 10054} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			original := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = original })
			calls := 0
			http.DefaultTransport = testRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return nil, &net.OpError{Op: "read", Net: "tcp", Err: code}
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(respWith(`{"pick":1}`, "")))}, nil
			})
			v := Choose(Config{BaseURL: "http://fixture", Model: "m", MaxTokens: 100, Timeout: time.Second}, cands(2), "pattern", Placement{})
			if v.Fallback || calls != 2 {
				t.Fatalf("native error=%d calls=%d fallback=%t", code, calls, v.Fallback)
			}
		})
	}
}

func TestModelJudgmentRetriesTruncatedResponseBody(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Length", "1000")
			w.Write([]byte(`{"choices":[`))
			return
		}
		w.Write([]byte(respWith(`{"pick":1}`, "")))
	}))
	defer srv.Close()
	if v := Choose(cfgFor(srv), cands(2), "pattern", Placement{}); v.Fallback || calls.Load() != 2 {
		t.Fatalf("truncated response: calls=%d fallback=%t", calls.Load(), v.Fallback)
	}
}

func TestTruncated4xxReplyIsNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Content-Length", "1000")
					w.WriteHeader(status)
					w.Write([]byte(`{"error":`))
					return
				}
				w.Write([]byte(respWith(`{"pick":1}`, "")))
			}))
			defer srv.Close()
			v := Choose(cfgFor(srv), cands(2), "pattern", Placement{})
			if !v.Fallback || calls.Load() != 1 || !strings.Contains(fmt.Sprint(v.Err), fmt.Sprintf("HTTP %d", status)) {
				t.Fatalf("status=%d calls=%d fallback=%t error=%v", status, calls.Load(), v.Fallback, v.Err)
			}
		})
	}
}

func TestModelJudgmentRetriesTransientFailureOnce(t *testing.T) {
	for _, status := range []int{502, 524} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			bodies := make(chan string, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				b, _ := io.ReadAll(r.Body)
				bodies <- string(b)
				if call == 1 {
					w.WriteHeader(status)
					return
				}
				w.Write([]byte(respWith(`{"pick":1,"reason":"完整"}`, "")))
			}))
			defer srv.Close()
			v := Choose(cfgFor(srv), cands(2), "pattern", Placement{})
			if v.Fallback || calls.Load() != 2 {
				t.Fatalf("calls=%d fallback=%t", calls.Load(), v.Fallback)
			}
			if <-bodies != <-bodies {
				t.Error("retry changed the request body")
			}
		})
	}
}

func TestModelJudgmentRetriesConnectionEOF(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		w.Write([]byte(respWith(`{"pick":1}`, "")))
	}))
	defer srv.Close()
	if v := Choose(cfgFor(srv), cands(2), "pattern", Placement{}); v.Fallback || calls.Load() != 2 {
		t.Fatalf("calls=%d fallback=%t", calls.Load(), v.Fallback)
	}
}

func TestModelJudgmentDoesNotRepeatInvalidRequestsOrExceedBudget(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(status) }))
			defer srv.Close()
			v := Choose(cfgFor(srv), cands(2), "pattern", Placement{})
			want := int32(1)
			if status == 502 {
				want = 2
			}
			if !v.Fallback || calls.Load() != want {
				t.Fatalf("calls=%d want=%d fallback=%t", calls.Load(), want, v.Fallback)
			}
		})
	}
}

func TestModelJudgmentDefersExplicitServerRetryDelay(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(503)
	}))
	defer srv.Close()
	if v := Choose(cfgFor(srv), cands(2), "pattern", Placement{}); !v.Fallback || calls.Load() != 1 {
		t.Fatalf("calls=%d fallback=%t", calls.Load(), v.Fallback)
	}
}

func TestModelJudgmentPreservesTimeout(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(502)
	}))
	defer srv.Close()
	c := cfgFor(srv)
	c.Timeout = 20 * time.Millisecond
	if v := Choose(c, cands(2), "pattern", Placement{}); !v.Fallback || calls.Load() != 1 {
		t.Fatalf("calls=%d fallback=%t", calls.Load(), v.Fallback)
	}
}
