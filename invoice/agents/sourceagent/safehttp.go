package sourceagent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type DNSResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type OriginPolicy struct {
	AllowedHosts   []string
	AllowedPorts   []int
	AllowedCIDRs   []string
	AllowedMethods []string
	AllowPlainHTTP bool
	Resolver       DNSResolver
}

type TLSFiles struct {
	CertificateFile         string
	PrivateKeyFile          string
	CAFile                  string
	ServerName              string
	ReloadClientCertificate bool
}

// RestrictedHTTPClient owns one immutable exact origin. Callers can provide
// only a relative constant path and query values; redirects are disabled and
// every dial re-resolves and revalidates the destination IP against explicit
// CIDRs to prevent DNS rebinding and SSRF pivoting.
type RestrictedHTTPClient struct {
	origin               *url.URL
	policy               validatedOriginPolicy
	client               *http.Client
	hasClientCertificate bool
}

type validatedOriginPolicy struct {
	hosts    map[string]struct{}
	ports    map[int]struct{}
	networks []*net.IPNet
	resolver DNSResolver
	methods  map[string]struct{}
}

func NewRestrictedHTTPClient(origin string, policy OriginPolicy, tlsFiles TLSFiles, timeout time.Duration) (*RestrictedHTTPClient, error) {
	parsed, validated, err := validateOriginConfiguration(origin, policy)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := loadTLSConfig(tlsFiles)
	if err != nil {
		return nil, err
	}
	if tlsConfig.ServerName == "" && net.ParseIP(parsed.Hostname()) == nil {
		tlsConfig.ServerName = parsed.Hostname()
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	dialer := &net.Dialer{Timeout: minDuration(timeout, 5*time.Second), KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       tlsConfig,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   minDuration(timeout, 5*time.Second),
		ResponseHeaderTimeout: timeout,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, fmt.Errorf("%w: unsupported network", ErrUnsafeOrigin)
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid dial address", ErrUnsafeOrigin)
		}
		if !strings.EqualFold(strings.TrimSuffix(host, "."), parsed.Hostname()) || port != effectivePort(parsed) {
			return nil, fmt.Errorf("%w: dial target differs from configured origin", ErrUnsafeOrigin)
		}
		ips, err := resolveAllowedIPs(ctx, parsed.Hostname(), validated)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, fmt.Errorf("restricted upstream dial failed: %w", lastErr)
	}

	return &RestrictedHTTPClient{
		origin:               parsed,
		policy:               validated,
		hasClientCertificate: len(tlsConfig.Certificates) > 0,
		client: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("redirects are disabled")
			},
		},
	}, nil
}

func (c *RestrictedHTTPClient) NewRequest(ctx context.Context, method, path string, query url.Values) (*http.Request, error) {
	if c == nil || c.origin == nil || c.client == nil {
		return nil, errors.New("restricted HTTP client is not configured")
	}
	if method != http.MethodGet && method != http.MethodPost {
		return nil, errors.New("unsupported restricted HTTP method")
	}
	if _, allowed := c.policy.methods[method]; !allowed {
		return nil, errors.New("HTTP method is not enabled for this exact origin")
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#\\%") {
		return nil, fmt.Errorf("%w: invalid relative path", ErrUnsafeOrigin)
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return nil, fmt.Errorf("%w: dot path segments are prohibited", ErrUnsafeOrigin)
		}
	}
	endpoint := *c.origin
	endpoint.Path = path
	endpoint.RawPath = ""
	endpoint.RawQuery = query.Encode()
	endpoint.Fragment = ""
	return http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
}

func (c *RestrictedHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if c == nil || c.client == nil || req == nil || req.URL == nil {
		return nil, errors.New("restricted HTTP request is not configured")
	}
	if !sameOrigin(c.origin, req.URL) {
		return nil, fmt.Errorf("%w: request origin mismatch", ErrUnsafeOrigin)
	}
	if _, err := resolveAllowedIPs(req.Context(), c.origin.Hostname(), c.policy); err != nil {
		return nil, err
	}
	return c.client.Do(req)
}

func validateOriginConfiguration(raw string, policy OriginPolicy) (*url.URL, validatedOriginPolicy, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || !parsed.IsAbs() {
		return nil, validatedOriginPolicy{}, fmt.Errorf("%w: invalid absolute origin", ErrUnsafeOrigin)
	}
	if parsed.Scheme != "https" && !(policy.AllowPlainHTTP && parsed.Scheme == "http") {
		return nil, validatedOriginPolicy{}, fmt.Errorf("%w: HTTPS is required", ErrUnsafeOrigin)
	}
	if parsed.User != nil || parsed.Hostname() == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return nil, validatedOriginPolicy{}, fmt.Errorf("%w: origin must not contain credentials, path, query, or fragment", ErrUnsafeOrigin)
	}
	if strings.HasSuffix(parsed.Hostname(), ".") {
		return nil, validatedOriginPolicy{}, fmt.Errorf("%w: trailing-dot hosts are not accepted", ErrUnsafeOrigin)
	}
	hosts := make(map[string]struct{}, len(policy.AllowedHosts))
	for _, host := range policy.AllowedHosts {
		host = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(host, ".")))
		if host != "" {
			hosts[host] = struct{}{}
		}
	}
	if _, ok := hosts[strings.ToLower(parsed.Hostname())]; !ok {
		return nil, validatedOriginPolicy{}, fmt.Errorf("%w: host is not allowlisted", ErrUnsafeOrigin)
	}
	ports := make(map[int]struct{}, len(policy.AllowedPorts))
	for _, port := range policy.AllowedPorts {
		if port < 1 || port > 65535 {
			return nil, validatedOriginPolicy{}, fmt.Errorf("%w: invalid allowed port", ErrUnsafeOrigin)
		}
		ports[port] = struct{}{}
	}
	portNumber, err := strconv.Atoi(effectivePort(parsed))
	if err != nil {
		return nil, validatedOriginPolicy{}, fmt.Errorf("%w: invalid origin port", ErrUnsafeOrigin)
	}
	if _, ok := ports[portNumber]; !ok {
		return nil, validatedOriginPolicy{}, fmt.Errorf("%w: port is not allowlisted", ErrUnsafeOrigin)
	}
	networks := make([]*net.IPNet, 0, len(policy.AllowedCIDRs))
	for _, rawCIDR := range policy.AllowedCIDRs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(rawCIDR))
		if err != nil {
			return nil, validatedOriginPolicy{}, fmt.Errorf("%w: invalid allowed CIDR", ErrUnsafeOrigin)
		}
		ones, _ := network.Mask.Size()
		if ones == 0 {
			return nil, validatedOriginPolicy{}, fmt.Errorf("%w: world-open destination CIDR is prohibited", ErrUnsafeOrigin)
		}
		networks = append(networks, network)
	}
	if len(networks) == 0 {
		return nil, validatedOriginPolicy{}, fmt.Errorf("%w: at least one destination CIDR is required", ErrUnsafeOrigin)
	}
	resolver := policy.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	methods := make(map[string]struct{}, len(policy.AllowedMethods))
	if len(policy.AllowedMethods) == 0 {
		methods[http.MethodGet] = struct{}{}
	}
	for _, method := range policy.AllowedMethods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method != http.MethodGet && method != http.MethodPost {
			return nil, validatedOriginPolicy{}, fmt.Errorf("%w: unsupported allowed method", ErrUnsafeOrigin)
		}
		methods[method] = struct{}{}
	}
	validated := validatedOriginPolicy{hosts: hosts, ports: ports, networks: networks, resolver: resolver, methods: methods}
	resolveContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := resolveAllowedIPs(resolveContext, parsed.Hostname(), validated); err != nil {
		return nil, validatedOriginPolicy{}, err
	}
	parsed.Path = ""
	return parsed, validated, nil
}

func resolveAllowedIPs(ctx context.Context, host string, policy validatedOriginPolicy) ([]net.IP, error) {
	var ips []net.IP
	if literal := net.ParseIP(host); literal != nil {
		ips = []net.IP{literal}
	} else {
		resolved, err := policy.resolver.LookupIPAddr(ctx, host)
		if err != nil || len(resolved) == 0 {
			return nil, fmt.Errorf("%w: DNS resolution failed", ErrUnsafeOrigin)
		}
		for _, item := range resolved {
			ips = append(ips, item.IP)
		}
	}
	for _, ip := range ips {
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
			return nil, fmt.Errorf("%w: prohibited destination address", ErrUnsafeOrigin)
		}
		allowed := false
		for _, network := range policy.networks {
			if network.Contains(ip) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("%w: resolved address is outside allowlisted CIDRs", ErrUnsafeOrigin)
		}
	}
	return ips, nil
}

func loadTLSConfig(files TLSFiles) (*tls.Config, error) {
	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: strings.TrimSpace(files.ServerName),
	}
	certPath := strings.TrimSpace(files.CertificateFile)
	keyPath := strings.TrimSpace(files.PrivateKeyFile)
	if (certPath == "") != (keyPath == "") {
		return nil, errors.New("both mTLS certificate and private key files are required")
	}
	if certPath != "" {
		if !filepath.IsAbs(certPath) || !filepath.IsAbs(keyPath) {
			return nil, errors.New("mTLS file paths must be absolute")
		}
		certificate, err := loadPrivateKeyPair(certPath, keyPath)
		if err != nil {
			return nil, errors.New("load mTLS client certificate failed")
		}
		config.Certificates = []tls.Certificate{certificate}
		if files.ReloadClientCertificate {
			config.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
				rotated, err := loadPrivateKeyPair(certPath, keyPath)
				if err != nil {
					return nil, errors.New("reload mTLS client certificate failed")
				}
				return &rotated, nil
			}
		}
	}
	if caPath := strings.TrimSpace(files.CAFile); caPath != "" {
		if !filepath.IsAbs(caPath) {
			return nil, errors.New("TLS CA file path must be absolute")
		}
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, errors.New("load TLS CA file failed")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("TLS CA file contains no certificate")
		}
		config.RootCAs = pool
	}
	return config, nil
}

func loadPrivateKeyPair(certificatePath, privateKeyPath string) (tls.Certificate, error) {
	keyInfo, err := os.Lstat(privateKeyPath)
	if err != nil || !keyInfo.Mode().IsRegular() || keyInfo.Mode()&os.ModeSymlink != 0 || !secureStatePermissions(keyInfo) {
		return tls.Certificate{}, errors.New("mTLS private key file is missing or unsafe")
	}
	return tls.LoadX509KeyPair(certificatePath, privateKeyPath)
}

func sameOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return left.Scheme == right.Scheme && strings.EqualFold(left.Hostname(), right.Hostname()) && effectivePort(left) == effectivePort(right) && right.User == nil
}

func effectivePort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if value.Scheme == "https" {
		return "443"
	}
	return "80"
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
