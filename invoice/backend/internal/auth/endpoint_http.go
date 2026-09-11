package auth

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

var nonPublicEndpointPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

var currentPublicIPv6Prefix = netip.MustParsePrefix("2000::/3")

type endpointIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type endpointConnectionDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type endpointDialResult struct {
	connection net.Conn
	err        error
}

func protectedEndpointDialContext(privateExceptions []string) (func(context.Context, string, string) (net.Conn, error), error) {
	return protectedEndpointDialContextWithDependencies(
		privateExceptions,
		net.DefaultResolver,
		&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second},
	)
}

func protectedEndpointDialContextWithDependencies(privateExceptions []string, resolver endpointIPResolver, dialer endpointConnectionDialer) (func(context.Context, string, string) (net.Conn, error), error) {
	if resolver == nil || dialer == nil {
		return nil, errors.New("Endpoint resolver and dialer are required")
	}
	allowedPrivate := make(map[netip.Addr]struct{}, len(privateExceptions))
	for _, raw := range privateExceptions {
		address, err := netip.ParseAddr(raw)
		if err != nil || address.String() != raw || address != address.Unmap() || !address.IsPrivate() {
			return nil, errors.New("Endpoint private endpoint IP exception is invalid")
		}
		allowedPrivate[address] = struct{}{}
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host == "" || port == "" {
			return nil, errors.New("source endpoint dial address is invalid")
		}
		resolved, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		unique := make(map[netip.Addr]struct{}, len(resolved))
		addresses := make([]netip.Addr, 0, len(resolved))
		for _, candidate := range resolved {
			candidate = candidate.Unmap()
			if !isPublicEndpointIP(candidate) {
				if _, explicitlyAllowed := allowedPrivate[candidate]; !explicitlyAllowed {
					return nil, errors.New("source endpoint resolved to a non-public address")
				}
			}
			if _, exists := unique[candidate]; exists {
				continue
			}
			unique[candidate] = struct{}{}
			addresses = append(addresses, candidate)
			if len(addresses) > 32 {
				return nil, errors.New("source endpoint resolved to too many addresses")
			}
		}
		if len(addresses) == 0 {
			return nil, errors.New("source endpoint resolved to no usable address")
		}
		dialContext, cancel := context.WithCancel(ctx)
		results := make(chan endpointDialResult, len(addresses))
		for _, candidate := range addresses {
			target := net.JoinHostPort(candidate.String(), port)
			go func(target string) {
				connection, dialErr := dialer.DialContext(dialContext, network, target)
				results <- endpointDialResult{connection: connection, err: dialErr}
			}(target)
		}
		var lastErr error
		for received := 0; received < len(addresses); received++ {
			result := <-results
			if result.err == nil && result.connection != nil {
				cancel()
				go closeUnusedEndpointConnections(results, len(addresses)-received-1)
				return result.connection, nil
			}
			if result.connection != nil {
				_ = result.connection.Close()
			}
			if result.err == nil {
				result.err = errors.New("source endpoint dial returned no connection")
			}
			lastErr = result.err
		}
		cancel()
		return nil, lastErr
	}, nil
}

func closeUnusedEndpointConnections(results <-chan endpointDialResult, remaining int) {
	for index := 0; index < remaining; index++ {
		result := <-results
		if result.connection != nil {
			_ = result.connection.Close()
		}
	}
}

func isPublicEndpointIP(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() ||
		address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() ||
		address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	if address.Is6() && !currentPublicIPv6Prefix.Contains(address) {
		return false
	}
	for _, prefix := range nonPublicEndpointPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

type boundedEndpointTransport struct {
	base         http.RoundTripper
	allowedHosts map[string]struct{}
	maxBytes     int64
}

func (t *boundedEndpointTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.User != nil {
		return nil, errors.New("source outbound request must use HTTPS without userinfo")
	}
	if _, ok := t.allowedHosts[strings.ToLower(request.URL.Host)]; !ok {
		return nil, errors.New("source outbound request host is not allowed")
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	response.Body = &maximumReadCloser{body: response.Body, remaining: t.maxBytes}
	return response, nil
}

type maximumReadCloser struct {
	body      io.ReadCloser
	remaining int64
}

func (r *maximumReadCloser) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.body.Read(probe[:])
		if n > 0 {
			return 0, errors.New("source HTTP response exceeds configured size limit")
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.body.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func (r *maximumReadCloser) Close() error { return r.body.Close() }
