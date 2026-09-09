package cpaboundary

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

const EvidenceVersionV1 = 1

// EvidenceBundle is a caller-supplied, redacted projection of local facts.
// The package never opens a host, container, socket, SSH session, or
// credential provider; the CLI is responsible only for reading this JSON.
type EvidenceBundle struct {
	Version              int                `json:"version"`
	ObservedAt           time.Time          `json:"observed_at,omitempty"`
	TargetVersion        string             `json:"target_version"`
	CPAImageDigest       string             `json:"cpa_image_digest"`
	RouteInventorySHA256 string             `json:"route_inventory_sha256"`
	Config               ConfigProjection   `json:"config"`
	Process              ProcessProjection  `json:"process"`
	Routes               RouteProjection    `json:"routes"`
	Firewall             FirewallProjection `json:"firewall"`
	Adapter              AdapterProjection  `json:"adapter"`
	Digests              DigestProjection   `json:"digests"`
	// RawFields is accepted only as a redaction sentinel for local tooling. It
	// is never echoed or included in EvidenceSHA256.
	RawFields map[string]string `json:"raw_fields,omitempty"`
}

// EvidenceBundleV1 is a descriptive alias retained for callers that use
// versioned type names.
type EvidenceBundleV1 = EvidenceBundle

type ConfigProjection struct {
	Complete                  bool   `json:"complete"`
	AllowRemoteKnown          bool   `json:"allow_remote_known"`
	AllowRemote               bool   `json:"allow_remote"`
	ManagementPasswordPresent bool   `json:"management_password_present"`
	RawPort                   int    `json:"raw_port"`
	RawBind                   string `json:"raw_bind"`
	SecretInConfig            bool   `json:"secret_in_config,omitempty"`
}

type ProcessProjection struct {
	Complete     bool              `json:"complete"`
	Args         []string          `json:"args,omitempty"`
	Environment  map[string]string `json:"environment,omitempty"`
	SecretInArgs bool              `json:"secret_in_args,omitempty"`
	SecretInEnv  bool              `json:"secret_in_env,omitempty"`
}

type RouteProjection struct {
	Complete          bool            `json:"complete"`
	InferenceHostname string          `json:"inference_hostname"`
	CallbackHostname  string          `json:"callback_hostname"`
	AllowRedirect     bool            `json:"allow_redirect"`
	IPv6Covered       bool            `json:"ipv6_covered"`
	Routes            []ObservedRoute `json:"routes"`
}

type ObservedRoute struct {
	Plane      string `json:"plane"`
	Hostname   string `json:"hostname"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Public     bool   `json:"public"`
	Redirect   bool   `json:"redirect,omitempty"`
	IPv6       bool   `json:"ipv6,omitempty"`
	Management bool   `json:"management,omitempty"`
}

type FirewallProjection struct {
	Complete           bool `json:"complete"`
	RawPortBlockedIPv4 bool `json:"raw_port_blocked_ipv4"`
	RawPortBlockedIPv6 bool `json:"raw_port_blocked_ipv6"`
}

type AdapterProjection struct {
	Complete        bool `json:"complete"`
	Public          bool `json:"public"`
	RequiresMTLS    bool `json:"requires_mtls"`
	HasMTLS         bool `json:"has_mtls"`
	RedirectAllowed bool `json:"redirect_allowed"`
	SecretInConfig  bool `json:"secret_in_config"`
	SecretInArgs    bool `json:"secret_in_args"`
}

type DigestProjection struct {
	Complete             bool   `json:"complete"`
	ImageSHA256          string `json:"image_sha256"`
	ConfigSHA256         string `json:"config_sha256"`
	RouteInventorySHA256 string `json:"route_inventory_sha256"`
}

// IsolationFinding is deliberately source-free. Resource is a stable class,
// not an echoed host/path/config line; EvidenceDigest identifies the supplied
// bundle without disclosing its contents.
type IsolationFinding struct {
	Code           string `json:"code"`
	Severity       string `json:"severity"`
	Resource       string `json:"resource"`
	EvidenceDigest string `json:"evidence_digest"`
	Remediation    string `json:"remediation"`
}

// IsolationReport is the deterministic result of AuditOffline. Complete
// means all required evidence projections were supplied; Decision is pass,
// fail, or partial and never implies that an external network test ran.
type IsolationReport struct {
	ContractHash   string             `json:"contract_hash"`
	CPAImageDigest string             `json:"cpa_image_digest"`
	EvidenceSHA256 string             `json:"evidence_sha256"`
	Findings       []IsolationFinding `json:"findings"`
	Complete       bool               `json:"complete"`
	Decision       string             `json:"decision"`
}

// LoadEvidenceBundle strictly decodes one explicit local evidence document.
func LoadEvidenceBundle(data []byte) (EvidenceBundle, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return EvidenceBundle{}, errors.New("cpa evidence: empty document")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return EvidenceBundle{}, fmt.Errorf("cpa evidence: %w", err)
	}
	// Only the wire version is structurally required here.  Target/version,
	// route and projection omissions are intentionally represented as an
	// incomplete AuditOffline report rather than hidden behind a decode error.
	for _, field := range []string{"version"} {
		if !topLevelFieldPresent(data, field) {
			return EvidenceBundle{}, fmt.Errorf("cpa evidence: missing %s", field)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var bundle EvidenceBundle
	if err := dec.Decode(&bundle); err != nil {
		return EvidenceBundle{}, fmt.Errorf("cpa evidence decode: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return EvidenceBundle{}, errors.New("cpa evidence has trailing JSON")
		}
		return EvidenceBundle{}, fmt.Errorf("cpa evidence trailing data: %w", err)
	}
	if err := bundle.Validate(); err != nil {
		return EvidenceBundle{}, err
	}
	return bundle, nil
}

// Validate checks shape and secret-safety of evidence. It does not require
// Complete projections; AuditOffline reports those as partial findings so a
// reviewer can see exactly which evidence was omitted.
func (e EvidenceBundle) Validate() error {
	if e.Version != EvidenceVersionV1 {
		return fmt.Errorf("cpa evidence: unsupported version %d", e.Version)
	}
	if strings.TrimSpace(e.TargetVersion) != "" && !exactVersion(e.TargetVersion) {
		return errors.New("cpa evidence: target_version must be one exact version when supplied")
	}
	if e.ObservedAt.IsZero() {
		return errors.New("cpa evidence: observed_at is required")
	}
	if e.ObservedAt.After(time.Now().UTC().Add(5 * time.Minute)) {
		return errors.New("cpa evidence: observed_at is in the future")
	}
	if _, offset := e.ObservedAt.Zone(); offset != 0 {
		return errors.New("cpa evidence: observed_at must be UTC")
	}
	if (e.CPAImageDigest != "" && !digestPattern.MatchString(e.CPAImageDigest)) || (e.RouteInventorySHA256 != "" && !digestPattern.MatchString(e.RouteInventorySHA256)) {
		return errors.New("cpa evidence: supplied image and route inventory digests must be lowercase SHA-256")
	}
	if e.Config.RawPort < 0 || e.Config.RawPort > 65535 {
		return errors.New("cpa evidence: raw_port is out of range")
	}
	if e.Config.RawBind != "" && strings.ContainsAny(e.Config.RawBind, "\r\n\t") {
		return errors.New("cpa evidence: raw_bind contains control characters")
	}
	if e.Routes.InferenceHostname == e.Routes.CallbackHostname && e.Routes.InferenceHostname != "" {
		return errors.New("cpa evidence: inference and callback hostnames must be distinct")
	}
	if e.Routes.Complete {
		if err := validateEvidenceHostname(e.Routes.InferenceHostname); err != nil {
			return fmt.Errorf("cpa evidence: inference hostname: %w", err)
		}
		if err := validateEvidenceHostname(e.Routes.CallbackHostname); err != nil {
			return fmt.Errorf("cpa evidence: callback hostname: %w", err)
		}
	}
	if e.Digests.ImageSHA256 != "" && !digestPattern.MatchString(e.Digests.ImageSHA256) {
		return errors.New("cpa evidence: digest projection image hash is invalid")
	}
	if e.Digests.ConfigSHA256 != "" && !digestPattern.MatchString(e.Digests.ConfigSHA256) {
		return errors.New("cpa evidence: digest projection config hash is invalid")
	}
	if e.Digests.RouteInventorySHA256 != "" && !digestPattern.MatchString(e.Digests.RouteInventorySHA256) {
		return errors.New("cpa evidence: digest projection route hash is invalid")
	}
	for key, value := range e.RawFields {
		if secretWord(key) || secretValue(value) {
			return errors.New("cpa evidence: raw_fields contains secret-looking material")
		}
	}
	for _, route := range e.Routes.Routes {
		if route.Plane == "" || route.Method == "" || route.Path == "" {
			return errors.New("cpa evidence: observed route requires plane, method and path")
		}
		if strings.ContainsAny(route.Hostname, "\r\n\t") {
			return errors.New("cpa evidence: observed route hostname contains control characters")
		}
	}
	return nil
}

// AuditOffline assesses only the supplied structured projections. Every
// finding is stable and source-free; no source text is copied into output.
func AuditOffline(boundary ManagementBoundary, evidence EvidenceBundle) (IsolationReport, error) {
	contractBytes, contractHash, err := boundary.Canonical()
	if err != nil {
		return IsolationReport{}, err
	}
	_ = contractBytes
	if err := evidence.Validate(); err != nil {
		return IsolationReport{}, err
	}
	evidenceHash := safeEvidenceDigest(evidence)
	report := IsolationReport{ContractHash: contractHash, CPAImageDigest: evidence.CPAImageDigest, EvidenceSHA256: evidenceHash, Complete: true, Decision: "pass", Findings: []IsolationFinding{}}
	add := func(code, severity, resource, remediation string) {
		report.Findings = append(report.Findings, IsolationFinding{Code: code, Severity: severity, Resource: resource, EvidenceDigest: evidenceHash, Remediation: remediation})
	}

	if strings.TrimSpace(evidence.TargetVersion) == "" {
		add("version_evidence_missing", "error", "target.version", "capture one exact target CPA version/build fact")
		report.Complete = false
	}
	if evidence.TargetVersion != "" && (evidence.TargetVersion != boundary.CPAMinVersion || evidence.TargetVersion != boundary.CPAMaxVersion) {
		add("version_drift", "error", "target.version", "refresh the boundary contract for the exact target version")
	}
	if evidence.RouteInventorySHA256 == "" {
		add("route_inventory_missing", "error", "route.inventory", "capture the exact approved route inventory digest")
		report.Complete = false
	} else if evidence.RouteInventorySHA256 != boundary.RouteInventorySHA256 || (evidence.Digests.RouteInventorySHA256 != "" && evidence.Digests.RouteInventorySHA256 != boundary.RouteInventorySHA256) {
		add("route_inventory_mismatch", "error", "route.inventory", "supply the exact approved route inventory digest")
	}
	if evidence.CPAImageDigest == "" {
		add("image_evidence_missing", "error", "image.digest", "capture the exact approved image digest")
		report.Complete = false
	}
	if evidence.RouteInventorySHA256 != "" && strings.Trim(evidence.RouteInventorySHA256, "0") == "" {
		add("route_inventory_unverified", "error", "route.inventory", "replace the placeholder digest with exact target-version evidence")
		report.Complete = false
	}
	if evidence.CPAImageDigest != evidence.Digests.ImageSHA256 && evidence.Digests.ImageSHA256 != "" {
		add("image_digest_mismatch", "error", "image.digest", "capture image digest from the approved target")
	}
	if !evidence.Config.Complete || !evidence.Process.Complete || !evidence.Routes.Complete || !evidence.Firewall.Complete || !evidence.Adapter.Complete || !evidence.Digests.Complete {
		add("evidence_incomplete", "error", "evidence.bundle", "complete every required redacted projection; live/network evidence remains not run")
		report.Complete = false
	}
	if !evidence.Config.AllowRemoteKnown || evidence.Config.AllowRemote {
		add("allow_remote_enabled", "error", "cpa.config", "prove allow-remote=false in the redacted config projection")
	}
	if evidence.Config.ManagementPasswordPresent {
		add("management_password_present", "error", "cpa.config", "remove MANAGEMENT_PASSWORD and capture an absent fact")
	}
	if evidence.Config.RawBind == "" {
		add("raw_bind_missing", "error", "cpa.raw_bind", "capture the exact loopback bind fact")
		report.Complete = false
	}
	if evidence.Config.RawBind != "" && !isLoopbackBind(evidence.Config.RawBind) {
		add("raw_port_public", "error", "cpa.raw_bind", "bind raw CPA port to loopback only")
	}
	if evidence.Config.RawPort != 8317 {
		add("raw_port_unknown", "error", "cpa.raw_port", "capture the exact CPA raw port fact (expected 8317 for the approved snapshot)")
		report.Complete = false
	}
	if !evidence.Firewall.RawPortBlockedIPv4 || !evidence.Firewall.RawPortBlockedIPv6 || !evidence.Routes.IPv6Covered {
		add("ipv6_rule_missing", "error", "firewall.raw_port", "supply both IPv4 and IPv6 raw-port deny evidence")
	}
	if evidence.Adapter.Public {
		add("adapter_public", "error", "adapter.listen", "bind management adapter to private or loopback address")
	}
	if !evidence.Adapter.RequiresMTLS || !evidence.Adapter.HasMTLS {
		add("adapter_mtls_missing", "error", "adapter.mtls", "require a valid private mTLS identity")
	}
	if evidence.Adapter.SecretInConfig || evidence.Adapter.SecretInArgs || evidence.Config.SecretInConfig || evidence.Process.SecretInArgs || evidence.Process.SecretInEnv || processContainsSecret(evidence.Process) || len(evidence.RawFields) > 0 {
		add("secret_exposure", "error", "runtime.projection", "remove secret material and provide a redacted fact only")
	}
	if evidence.Routes.AllowRedirect || evidence.Adapter.RedirectAllowed {
		add("redirect_allowed", "error", "route.redirect", "reject redirects and absolute destinations")
	}
	for _, route := range evidence.Routes.Routes {
		method := strings.ToUpper(route.Method)
		if route.Method != "GET" && route.Method != "POST" && route.Method != "HEAD" {
			add("route_method_invalid", "error", "route.corpus", "use canonical GET/POST/HEAD methods only")
		}
		if route.Redirect {
			add("redirect_allowed", "error", "route.redirect", "reject redirects and absolute destinations")
		}
		if _, pathErr := normalizeExactPath(route.Path); pathErr != nil {
			add("encoded_or_wildcard_route", "error", "route.corpus", "use exact normalized paths only")
		}
		management := route.Management || strings.HasPrefix(route.Path, ManagementPrefix)
		if route.Public {
			if route.Plane == "callback" && route.Hostname != evidence.Routes.CallbackHostname {
				add("callback_host_mismatch", "error", "route.callback", "bind callback routes to the exact per-instance callback hostname")
			}
			if route.Plane == "inference" && route.Hostname != evidence.Routes.InferenceHostname {
				add("inference_host_mismatch", "error", "route.inference", "bind inference routes to the exact inference hostname")
			}
		}
		if management && route.Public {
			exactCallback := route.Hostname == evidence.Routes.CallbackHostname && route.Path == CallbackPath && (method == "GET" || method == "POST")
			if route.Plane == "inference" || route.Hostname == evidence.Routes.InferenceHostname || !exactCallback {
				if route.Hostname == evidence.Routes.CallbackHostname && strings.HasPrefix(route.Path, ManagementPrefix) {
					add("callback_sibling", "error", "route.callback", "allow only exact callback GET/POST on the callback hostname")
				} else {
					add("management_wildcard", "error", "route.management", "deny the management prefix on every public plane")
				}
			}
		}
	}
	// A valid callback exception must be represented as both exact methods in
	// the structured route export. Missing exact routes is incomplete rather
	// than an implicit pass.
	if evidence.Routes.Complete && !hasExactCallbacks(evidence.Routes) {
		add("callback_exact_routes_missing", "error", "route.callback", "capture exact callback GET and POST facts")
		report.Complete = false
	}
	// Stable ordering makes output suitable for hashing and snapshot tests.
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].Code != report.Findings[j].Code {
			return report.Findings[i].Code < report.Findings[j].Code
		}
		return report.Findings[i].Resource < report.Findings[j].Resource
	})
	if len(report.Findings) != 0 {
		report.Decision = "fail"
		if !report.Complete {
			report.Decision = "partial"
		}
	} else if !report.Complete {
		report.Decision = "partial"
	}
	return report, nil
}

// Audit is a descriptive alias retained for local callers.
func Audit(boundary ManagementBoundary, evidence EvidenceBundle) (IsolationReport, error) {
	return AuditOffline(boundary, evidence)
}

func hasExactCallbacks(routes RouteProjection) bool {
	get, post := false, false
	for _, route := range routes.Routes {
		if route.Plane != "callback" || route.Hostname != routes.CallbackHostname || route.Path != CallbackPath || !route.Public {
			continue
		}
		switch strings.ToUpper(route.Method) {
		case "GET":
			get = true
		case "POST":
			post = true
		}
	}
	return get && post
}

func validateEvidenceHostname(host string) error {
	host = strings.TrimSpace(host)
	if host == "" || host != strings.ToLower(host) || strings.ContainsAny(host, "*?/\\:#\r\n\t ") {
		return errors.New("hostname must be one lowercase exact host without port or wildcard")
	}
	return nil
}

func isLoopbackBind(bind string) bool {
	bind = strings.ToLower(strings.TrimSpace(bind))
	if bind == "127.0.0.1" || bind == "::1" || bind == "localhost" {
		return true
	}
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return false
	}
	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}

func processContainsSecret(process ProcessProjection) bool {
	for key, value := range process.Environment {
		if secretWord(key) || secretValue(value) {
			return true
		}
	}
	for _, arg := range process.Args {
		if secretValue(arg) || strings.Contains(strings.ToLower(arg), "--password") || strings.Contains(strings.ToLower(arg), "--token") || strings.Contains(strings.ToLower(arg), "--secret") {
			return true
		}
	}
	return false
}

func secretWord(value string) bool {
	lower := strings.ToLower(strings.ReplaceAll(value, "-", "_"))
	for _, marker := range []string{"password", "passwd", "secret", "token", "authorization", "cookie", "api_key", "private_key", "credential"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func secretValue(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"bearer ", "basic ", "password=", "passwd=", "secret=", "token=", "authorization:", "cookie:", "-----begin "} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func safeEvidenceDigest(evidence EvidenceBundle) string {
	// Clear all process text before hashing. A digest of a secret-bearing
	// projection is not needed for an offline report and could become an
	// oracle; findings still carry a stable digest of the redacted shape.
	evidence.Process.Args = nil
	evidence.Process.Environment = nil
	evidence.RawFields = nil
	canonical, err := json.Marshal(evidence)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
