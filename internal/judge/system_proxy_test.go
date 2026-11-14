package judge

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"golang.org/x/net/http/httpproxy"
)

func TestVisionProxySelection(t *testing.T) {
	manual := manualProxySettings{Enabled: true, Server: "127.0.0.1:7897"}
	for _, tc := range []struct {
		name, target string
		env          httpproxy.Config
		system       manualProxySettings
		want         string
	}{
		{"enabled", "https://model.example/v1", httpproxy.Config{}, manual, "http://127.0.0.1:7897"},
		{"disabled", "https://model.example/v1", httpproxy.Config{}, manualProxySettings{Server: "127.0.0.1:7897"}, ""},
		{"PAC unsupported", "https://model.example/v1", httpproxy.Config{}, manualProxySettings{Enabled: true, Server: "127.0.0.1:7897", HasPAC: true}, ""},
		{"protocol http", "http://model.example/v1", httpproxy.Config{}, manualProxySettings{Enabled: true, Server: "http=http-proxy:80;https=tls-proxy:81"}, "http://http-proxy:80"},
		{"protocol https", "https://model.example/v1", httpproxy.Config{}, manualProxySettings{Enabled: true, Server: "http=http-proxy:80;https=tls-proxy:81"}, "http://tls-proxy:81"},
		{"missing protocol direct", "https://model.example/v1", httpproxy.Config{}, manualProxySettings{Enabled: true, Server: "http=http-proxy:80"}, ""},
		{"explicit env", "https://model.example/v1", httpproxy.Config{HTTPSProxy: "env-proxy:82"}, manual, "http://env-proxy:82"},
		{"explicit bypass", "https://model.example/v1", httpproxy.Config{HTTPSProxy: "env-proxy:82", NoProxy: "model.example"}, manual, ""},
		{"only explicit no_proxy", "https://other.example/v1", httpproxy.Config{NoProxy: "model.example"}, manual, ""},
		{"no env protocol fallback", "https://model.example/v1", httpproxy.Config{HTTPProxy: "env-proxy:82"}, manual, ""},
		{"localhost", "http://localhost:8000", httpproxy.Config{}, manual, ""},
		{"loopback", "http://127.0.0.1:8000", httpproxy.Config{}, manual, ""},
		{"ipv6 loopback", "http://[::1]:8000", httpproxy.Config{}, manual, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			got, err := selectVisionProxy(u, &tc.env, tc.system)
			if err != nil {
				t.Fatal(err)
			}
			value := ""
			if got != nil {
				value = got.String()
			}
			if value != tc.want {
				t.Fatalf("proxy=%q want%q", value, tc.want)
			}
		})
	}
}

func TestWindowsManualProxyBypass(t *testing.T) {
	manual := manualProxySettings{Enabled: true, Server: "proxy:80", Bypass: "*.internal.example;10.*;<local>;exact.example;secure.example:443"}
	for _, tc := range []struct {
		target string
		direct bool
	}{
		{"https://a.internal.example", true}, {"http://10.1.2.3", true}, {"http://intranet", true}, {"https://exact.example", true}, {"https://secure.example", true}, {"http://secure.example", false}, {"https://model.example", false},
	} {
		u, _ := url.Parse(tc.target)
		got, err := selectVisionProxy(u, &httpproxy.Config{}, manual)
		if err != nil || (got == nil) != tc.direct {
			t.Fatalf("%s proxy=%v err=%v", tc.target, got, err)
		}
	}
}

func TestVisionTransportIsPrivateAndRetainsDefaultReplacement(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	base := original.(*http.Transport).Clone()
	base.MaxIdleConns = 73
	http.DefaultTransport = base
	transport := visionTransport()
	clone, ok := transport.(*http.Transport)
	if !ok || clone == base || clone.MaxIdleConns != 73 || http.DefaultTransport != base {
		t.Fatal("visual client mutated or bypassed the default base transport")
	}
	if visionTransport() != transport {
		t.Fatal("private connection pool was not reused")
	}
	fake := testRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(`{}`))}, nil
	})
	http.DefaultTransport = fake
	if _, ok := visionTransport().(testRoundTripper); !ok {
		t.Fatal("custom default RoundTripper was not retained")
	}
}

func TestVisionRequestUsesExplicitProxyWithoutChangingDefault(t *testing.T) {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		t.Setenv(key, "")
	}
	seen := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen <- r.URL.Host; w.Write([]byte(`{"ok":true}`)) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	original := http.DefaultTransport
	req, _ := http.NewRequest(http.MethodPost, "http://fixture.invalid/v1", bytes.NewReader([]byte(`{}`)))
	resp, err := DoVisionRequest(req, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if host := <-seen; host != "fixture.invalid" {
		t.Fatalf("proxy received host %q", host)
	}
	if http.DefaultTransport != original {
		t.Fatal("application proxy changed DefaultTransport")
	}
}
