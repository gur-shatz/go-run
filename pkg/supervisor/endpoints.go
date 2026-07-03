package supervisor

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

type resolvedEndpoint struct {
	BaseURL string
	Path    string
}

func resolveEndpoint(baseURL, spec string) (resolvedEndpoint, error) {
	if spec == "" {
		return resolvedEndpoint{}, fmt.Errorf("endpoint is empty")
	}
	if u, err := url.Parse(spec); err == nil && u.IsAbs() {
		if u.Scheme != "http" && u.Scheme != "https" {
			return resolvedEndpoint{}, fmt.Errorf("endpoint %q must use http:// or https://", spec)
		}
		if u.Host == "" {
			return resolvedEndpoint{}, fmt.Errorf("endpoint %q has no host", spec)
		}
		return resolvedEndpoint{BaseURL: u.Scheme + "://" + u.Host, Path: endpointRequestURI(u)}, nil
	}

	if strings.HasPrefix(spec, ":") {
		base, err := url.Parse(baseURL)
		if err != nil {
			return resolvedEndpoint{}, fmt.Errorf("parse base url %q: %w", baseURL, err)
		}
		if base.Scheme != "http" && base.Scheme != "https" {
			return resolvedEndpoint{}, fmt.Errorf("base url %q must use http:// or https://", baseURL)
		}
		if base.Hostname() == "" {
			return resolvedEndpoint{}, fmt.Errorf("base url %q has no host", baseURL)
		}
		slash := strings.IndexByte(spec, '/')
		if slash < 0 {
			return resolvedEndpoint{}, fmt.Errorf("endpoint %q must be :port/path", spec)
		}
		port := strings.TrimPrefix(spec[:slash], ":")
		if port == "" {
			return resolvedEndpoint{}, fmt.Errorf("endpoint %q has empty port", spec)
		}
		return resolvedEndpoint{
			BaseURL: base.Scheme + "://" + net.JoinHostPort(base.Hostname(), port),
			Path:    spec[slash:],
		}, nil
	}

	path := spec
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return resolvedEndpoint{BaseURL: strings.TrimRight(baseURL, "/"), Path: path}, nil
}

func endpointRequestURI(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return path
}

func normalizeOverflowPath(spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.HasPrefix(spec, ":") {
		return "", fmt.Errorf("must be a path, not a port override")
	}
	if strings.HasPrefix(spec, "//") {
		return "", fmt.Errorf("must be a path, not a host-relative URL")
	}
	if u, err := url.Parse(spec); err == nil && u.IsAbs() {
		return "", fmt.Errorf("must be a path, not an absolute URL")
	}
	if !strings.HasPrefix(spec, "/") {
		spec = "/" + spec
	}
	u, err := url.ParseRequestURI(spec)
	if err != nil {
		return "", err
	}
	if u.Host != "" {
		return "", fmt.Errorf("must be a path, not a URL")
	}
	return endpointRequestURI(u), nil
}
