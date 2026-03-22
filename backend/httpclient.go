package backend

import (
	"net/http"
	"time"
)

// NewHTTPClient retourne un client qui respecte HTTP_PROXY/HTTPS_PROXY
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}
}
