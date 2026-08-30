package cpaplugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
)

// PluginStoreSource describes a pinned, read-only source.  It authorizes no
// fetch by itself; R214-2 owns any quarantine transport after a separate
// approval.  CredentialRef is metadata only and never contains a secret.
type PluginStoreSource struct {
	SourceID              string   `json:"source_id"`
	Name                  string   `json:"name"`
	RegistryURL           string   `json:"registry_url"`
	RegistrySHA256        string   `json:"registry_sha256"`
	RegistrySignature     string   `json:"registry_signature"`
	RepositoryAllowlist   []string `json:"repository_allowlist"`
	PluginAllowlist       []string `json:"plugin_allowlist,omitempty"`
	AuthMethod            string   `json:"auth_method"`
	CredentialRef         string   `json:"credential_ref"`
	RedirectHostAllowlist []string `json:"redirect_host_allowlist"`
	ArtifactHostAllowlist []string `json:"artifact_host_allowlist"`
	MaxRegistryBytes      int64    `json:"max_registry_bytes"`
	MaxArtifactBytes      int64    `json:"max_artifact_bytes"`
	TLSPolicy             string   `json:"tls_policy"`
	ProxyPolicy           string   `json:"proxy_policy"`
	Owner                 string   `json:"owner"`
	AllowFallback         bool     `json:"allow_fallback,omitempty"`
	// Description and Readme are opaque display text.  They are intentionally
	// never interpolated into paths, commands, or approval decisions.
	Description string `json:"description,omitempty"`
	Readme      string `json:"readme,omitempty"`
}

// PluginStoreSourceV1 is the public versioned name used by contracts.
type PluginStoreSourceV1 = PluginStoreSource

type StoreSourcesV1 struct {
	Version int                 `json:"version"`
	Sources []PluginStoreSource `json:"sources"`
}

type PluginStoreSourcesV1 = StoreSourcesV1
type PluginStoreSourcePolicyV1 = StoreSourcesV1

// LoadStoreSources validates source policy and returns its canonical digest.
func LoadStoreSources(data []byte) ([]PluginStoreSource, string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, "", errors.New("empty plugin store source policy")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, "", err
	}
	for _, field := range []string{"version", "sources"} {
		if !topLevelFieldPresent(data, field) {
			return nil, "", fmt.Errorf("plugin store source policy is missing %s", field)
		}
	}
	if raw, ok := topLevelRawField(data, "sources"); !ok || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || bytes.TrimSpace(raw)[0] != '[' {
		return nil, "", errors.New("sources must be an array")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var envelope StoreSourcesV1
	if err := dec.Decode(&envelope); err != nil {
		return nil, "", fmt.Errorf("decode plugin store source policy: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, "", errors.New("plugin store source policy has trailing JSON")
		}
		return nil, "", fmt.Errorf("decode trailing source policy data: %w", err)
	}
	if err := envelope.Validate(); err != nil {
		return nil, "", err
	}
	// Canonicalize ordering and allowlists before hashing so semantically
	// identical source policies have one evidence digest.
	sort.Slice(envelope.Sources, func(i, j int) bool { return envelope.Sources[i].SourceID < envelope.Sources[j].SourceID })
	for i := range envelope.Sources {
		sort.Strings(envelope.Sources[i].RepositoryAllowlist)
		sort.Strings(envelope.Sources[i].PluginAllowlist)
		sort.Strings(envelope.Sources[i].RedirectHostAllowlist)
		sort.Strings(envelope.Sources[i].ArtifactHostAllowlist)
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize plugin store source policy: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return append([]PluginStoreSource(nil), envelope.Sources...), hex.EncodeToString(sum[:]), nil
}

func LoadPluginStoreSources(data []byte) ([]PluginStoreSource, string, error) {
	return LoadStoreSources(data)
}

func LoadPluginStoreSourcePolicy(data []byte) ([]PluginStoreSource, string, error) {
	return LoadStoreSources(data)
}

func (p StoreSourcesV1) Validate() error {
	if p.Version != StoreSourcesVersionV1 {
		return fmt.Errorf("unsupported plugin store source policy version %d", p.Version)
	}
	seen := make(map[string]struct{}, len(p.Sources))
	pluginOwners := make(map[string]string)
	for i, source := range p.Sources {
		if err := source.Validate(); err != nil {
			return fmt.Errorf("source[%d]: %w", i, err)
		}
		if _, exists := seen[source.SourceID]; exists {
			return fmt.Errorf("duplicate source ID %q", source.SourceID)
		}
		seen[source.SourceID] = struct{}{}
		for _, pluginID := range source.PluginAllowlist {
			if !pluginIDPattern.MatchString(pluginID) {
				return fmt.Errorf("source %q has invalid plugin allowlist ID %q", source.SourceID, pluginID)
			}
			if owner, exists := pluginOwners[pluginID]; exists {
				return fmt.Errorf("plugin ID %q appears in sources %q and %q", pluginID, owner, source.SourceID)
			}
			pluginOwners[pluginID] = source.SourceID
		}
	}
	return nil
}

func (s PluginStoreSource) Validate() error {
	if !pluginIDPattern.MatchString(s.SourceID) {
		return fmt.Errorf("source ID %q is invalid", s.SourceID)
	}
	for field, value := range map[string]string{"name": s.Name, "registry_url": s.RegistryURL, "registry_signature": s.RegistrySignature, "auth_method": s.AuthMethod, "credential_ref": s.CredentialRef, "tls_policy": s.TLSPolicy, "proxy_policy": s.ProxyPolicy, "owner": s.Owner} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\t") {
			return fmt.Errorf("%s is required and must not contain control characters", field)
		}
	}
	if s.AllowFallback {
		return errors.New("source fallback is forbidden")
	}
	if strings.EqualFold(s.RegistrySignature, "unsigned") || strings.EqualFold(s.RegistrySignature, "none") {
		return errors.New("registry signature is required")
	}
	if !digestPattern.MatchString(s.RegistrySHA256) {
		return errors.New("registry_sha256 must be 64 lowercase hex characters")
	}
	if err := validateHTTPSURL(s.RegistryURL); err != nil {
		return fmt.Errorf("registry_url: %w", err)
	}
	registryHost := ""
	if parsed, parseErr := url.Parse(s.RegistryURL); parseErr == nil {
		registryHost = parsed.Hostname()
	}
	if s.AuthMethod != "credential_ref" {
		return errors.New("auth_method must be credential_ref")
	}
	if !validCredentialRef(s.CredentialRef) {
		return errors.New("credential_ref must be an opaque secret/cred/env reference")
	}
	if s.MaxRegistryBytes <= 0 || s.MaxRegistryBytes > 512<<20 || s.MaxArtifactBytes <= 0 || s.MaxArtifactBytes > 2<<30 || s.MaxArtifactBytes < s.MaxRegistryBytes {
		return errors.New("registry/artifact byte limits must be finite and bounded")
	}
	if s.TLSPolicy != "required" {
		return errors.New("tls_policy must be required")
	}
	if s.ProxyPolicy != "disabled" && s.ProxyPolicy != "explicit" {
		return errors.New("proxy_policy must be disabled or explicit")
	}
	if len(s.RepositoryAllowlist) == 0 || len(s.ArtifactHostAllowlist) == 0 || len(s.RedirectHostAllowlist) == 0 {
		return errors.New("repository, artifact host and redirect allowlists are required")
	}
	seenRepositories := map[string]struct{}{}
	for _, repository := range s.RepositoryAllowlist {
		if err := validateHTTPSURL(repository); err != nil {
			return fmt.Errorf("repository allowlist: %w", err)
		}
		if strings.ContainsAny(repository, "*") {
			return errors.New("repository allowlist must not contain wildcards")
		}
		if _, duplicate := seenRepositories[repository]; duplicate {
			return fmt.Errorf("duplicate repository allowlist entry %q", repository)
		}
		seenRepositories[repository] = struct{}{}
	}
	for label, hosts := range map[string][]string{"redirect": s.RedirectHostAllowlist, "artifact": s.ArtifactHostAllowlist} {
		seen := map[string]struct{}{}
		for _, host := range hosts {
			if err := validateHost(host); err != nil {
				return fmt.Errorf("%s host allowlist: %w", label, err)
			}
			if _, exists := seen[host]; exists {
				return fmt.Errorf("duplicate %s host %q", label, host)
			}
			seen[host] = struct{}{}
		}
	}
	registryAllowed := false
	for _, host := range s.RedirectHostAllowlist {
		if host == registryHost {
			registryAllowed = true
			break
		}
	}
	if !registryAllowed {
		return errors.New("registry host must be present in redirect host allowlist")
	}
	return nil
}

func validCredentialRef(value string) bool {
	if strings.ContainsAny(value, " \r\n\t") || len(value) > 256 {
		return false
	}
	for _, prefix := range []string{"cred://", "secret://", "env://"} {
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return true
		}
	}
	return false
}

func validateHTTPSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must be an HTTPS URL without credentials, query or fragment")
	}
	if strings.ContainsAny(u.Host, "*\\/\r\n\t ") || u.Hostname() != strings.ToLower(u.Hostname()) {
		return errors.New("URL host must be one lowercase exact host without wildcard")
	}
	return nil
}

func validateHost(host string) error {
	if host == "" || strings.ContainsAny(host, "*/\\:?\r\n\t ") || strings.Contains(host, "..") {
		return errors.New("must be one exact host without wildcard or path")
	}
	if host != strings.ToLower(host) {
		return errors.New("host must use lowercase canonical form")
	}
	if strings.EqualFold(host, "localhost") {
		return errors.New("localhost is not an approved external source host")
	}
	return nil
}
