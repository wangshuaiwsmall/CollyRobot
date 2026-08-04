//go:build !windows

package proxyconfig

import (
	"fmt"
	"net/url"
)

func systemProxyForURL(_ *url.URL) (*url.URL, error) {
	return nil, fmt.Errorf("system proxy mode is only supported on Windows")
}
