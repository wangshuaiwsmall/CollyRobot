//go:build windows

package proxyconfig

import (
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func systemProxyForURL(target *url.URL) (*url.URL, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	defer key.Close()

	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil {
		return nil, fmt.Errorf("read ProxyEnable: %w", err)
	}
	if enabled == 0 {
		return nil, fmt.Errorf("Windows system proxy is disabled")
	}
	raw, _, err := key.GetStringValue("ProxyServer")
	if err != nil {
		return nil, fmt.Errorf("read ProxyServer: %w", err)
	}
	if bypassedByWindowsRules(target, key) {
		return nil, nil
	}
	selected := selectWindowsProxy(raw, target.Scheme)
	if selected == "" {
		return nil, fmt.Errorf("Windows system proxy has no entry for %s", target.Scheme)
	}
	if !strings.Contains(selected, "://") {
		selected = "http://" + selected
	}
	return parseProxyURL(selected)
}

func selectWindowsProxy(raw, scheme string) string {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "=") {
		return raw
	}
	values := make(map[string]string)
	for _, entry := range strings.Split(raw, ";") {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	if value := values[strings.ToLower(scheme)]; value != "" {
		return value
	}
	return values["http"]
}

func bypassedByWindowsRules(target *url.URL, key registry.Key) bool {
	raw, _, err := key.GetStringValue("ProxyOverride")
	if err != nil {
		return false
	}
	host := strings.ToLower(target.Hostname())
	for _, rule := range strings.Split(raw, ";") {
		rule = strings.ToLower(strings.TrimSpace(rule))
		if rule == "<local>" && !strings.Contains(host, ".") {
			return true
		}
		if strings.HasPrefix(rule, "*.") && strings.HasSuffix(host, strings.TrimPrefix(rule, "*")) {
			return true
		}
		if host == rule {
			return true
		}
	}
	return false
}
