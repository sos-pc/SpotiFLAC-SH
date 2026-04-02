package util

import (
	"net/http"
	"time"
)

// NewHTTPClient returns an HTTP client that honours HTTP_PROXY/HTTPS_PROXY env vars.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}
}
