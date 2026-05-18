package client

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Client is a parent-side HTTP client that connects to a child's
// backoffice server over a Unix domain socket.
type Client struct {
	sockPath   string
	httpClient *http.Client
	transport  *http.Transport
}

// New creates a new backoffice client for the given UDS path.
func New(sockPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		},
	}
	return &Client{
		sockPath:  sockPath,
		transport: transport,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
		},
	}
}

// Alive returns true if the backoffice is reachable.
func (this *Client) Alive(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://backoffice/index.json", nil)
	if err != nil {
		return false
	}
	resp, err := this.httpClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Transport returns the http.RoundTripper for use with httputil.ReverseProxy.
func (this *Client) Transport() http.RoundTripper {
	return this.transport
}

// SockPath returns the UDS path this client connects to.
func (this *Client) SockPath() string {
	return this.sockPath
}
