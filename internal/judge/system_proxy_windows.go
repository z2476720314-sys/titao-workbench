package judge

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

func readSystemManualProxy() manualProxySettings {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return manualProxySettings{}
	}
	defer key.Close()
	enabled, _, _ := key.GetIntegerValue("ProxyEnable")
	server, _, _ := key.GetStringValue("ProxyServer")
	bypass, _, _ := key.GetStringValue("ProxyOverride")
	pac, _, _ := key.GetStringValue("AutoConfigURL")
	return manualProxySettings{Enabled: enabled == 1, Server: server, Bypass: bypass, HasPAC: strings.TrimSpace(pac) != ""}
}
