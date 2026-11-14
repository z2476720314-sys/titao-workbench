package judge

import (
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"

	"golang.org/x/net/http/httpproxy"
)

type manualProxySettings struct {
	Enabled        bool
	Server, Bypass string
	HasPAC         bool
}

var visionTransportCache struct {
	sync.Mutex
	base    *http.Transport
	private *http.Transport
}

func visionTransport() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	visionTransportCache.Lock()
	defer visionTransportCache.Unlock()
	if visionTransportCache.base != base {
		if visionTransportCache.private != nil {
			visionTransportCache.private.CloseIdleConnections()
		}
		visionTransportCache.base = base
		visionTransportCache.private = base.Clone()
		visionTransportCache.private.Proxy = visionProxy
	}
	return visionTransportCache.private
}

func hasExplicitProxy(env *httpproxy.Config) bool {
	return env.HTTPProxy != "" || env.HTTPSProxy != "" || env.NoProxy != ""
}

func visionProxy(req *http.Request) (*url.URL, error) {
	env := httpproxy.FromEnvironment()
	var system manualProxySettings
	if !hasExplicitProxy(env) {
		system = readSystemManualProxy()
	}
	return selectVisionProxy(req.URL, env, system)
}

func selectVisionProxy(target *url.URL, env *httpproxy.Config, system manualProxySettings) (*url.URL, error) {
	if hasExplicitProxy(env) {
		return env.ProxyFunc()(target)
	}
	if !system.Enabled || system.HasPAC || manualProxyBypass(target, system.Bypass) {
		return nil, nil
	}
	config := httpproxy.Config{HTTPProxy: strings.TrimSpace(system.Server), HTTPSProxy: strings.TrimSpace(system.Server)}
	if first, _, split := strings.Cut(system.Server, "="); split && !strings.Contains(first, "://") {
		config.HTTPProxy, config.HTTPSProxy = "", ""
		for _, entry := range strings.Split(system.Server, ";") {
			protocol, value, ok := strings.Cut(entry, "=")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(protocol)) {
			case "http":
				config.HTTPProxy = strings.TrimSpace(value)
			case "https":
				config.HTTPSProxy = strings.TrimSpace(value)
			}
		}
	}
	return config.ProxyFunc()(target)
}

func manualProxyBypass(target *url.URL, list string) bool {
	host := strings.ToLower(target.Hostname())
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return true
	}
	for _, entry := range strings.Split(list, ";") {
		pattern := strings.ToLower(strings.TrimSpace(entry))
		if pattern == "" {
			continue
		}
		if pattern == "<local>" {
			if ip == nil && !strings.Contains(host, ".") {
				return true
			}
			continue
		}
		if scheme, rest, ok := strings.Cut(pattern, "://"); ok {
			if scheme != target.Scheme {
				continue
			}
			pattern = rest
		}
		if bypassIP := net.ParseIP(strings.Trim(pattern, "[]")); bypassIP != nil {
			if ip != nil && bypassIP.Equal(ip) {
				return true
			}
			continue
		}
		name := host
		if strings.Contains(pattern, ":") {
			port := target.Port()
			if port == "" {
				if target.Scheme == "https" {
					port = "443"
				} else {
					port = "80"
				}
			}
			name = net.JoinHostPort(host, port)
		}
		if pattern == name {
			return true
		}
		if matched, _ := path.Match(pattern, name); matched {
			return true
		}
	}
	return false
}
