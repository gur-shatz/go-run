package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gur-shatz/go-run/pkg/backoffice"
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

// Status fetches the /status endpoint from the child's backoffice.
func (this *Client) Status(ctx context.Context) (*backoffice.StatusInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://backoffice/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := this.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("backoffice status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("backoffice status: HTTP %d", resp.StatusCode)
	}

	var info backoffice.StatusInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("backoffice status decode: %w", err)
	}
	return &info, nil
}

// Alive returns true if the backoffice is reachable.
func (this *Client) Alive(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://backoffice/status", nil)
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
