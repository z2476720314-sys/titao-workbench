//go:build !windows

package judge

func readSystemManualProxy() manualProxySettings { return manualProxySettings{} }
