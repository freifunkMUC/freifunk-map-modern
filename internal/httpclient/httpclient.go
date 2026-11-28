// Package httpclient builds HTTP clients that identify this crawler.
package httpclient

import (
	"net/http"
	"time"
)

// UserAgent is sent on every outbound request.
const UserAgent = "freifunk-map-modern/1.0"

type uaTransport struct{ base http.RoundTripper }

func (t uaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", UserAgent)
	}
	return t.base.RoundTrip(req)
}

// New returns a client with the given timeout that sets the User-Agent.
// A nil base uses http.DefaultTransport.
func New(timeout time.Duration, base http.RoundTripper) *http.Client {
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{Timeout: timeout, Transport: uaTransport{base}}
}
