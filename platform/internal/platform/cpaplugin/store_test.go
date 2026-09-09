package cpaplugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func validSource() PluginStoreSource {
	return PluginStoreSource{SourceID: "internal", Name: "Internal registry", RegistryURL: "https://registry.example.test/plugins.json", RegistrySHA256: strings.Repeat("a", 64), RegistrySignature: "sig-ref", RepositoryAllowlist: []string{"https://github.com/example/"}, AuthMethod: "credential_ref", CredentialRef: "cred://plugin-store/internal", RedirectHostAllowlist: []string{"registry.example.test"}, ArtifactHostAllowlist: []string{"objects.example.test"}, MaxRegistryBytes: 1024, MaxArtifactBytes: 1 << 20, TLSPolicy: "required", ProxyPolicy: "disabled", Owner: "platform-security"}
}

func TestStoreSourceRejectsDuplicateIDsBadURLAndUnboundedSize(t *testing.T) {
	s := validSource()
	data, _ := json.Marshal(StoreSourcesV1{Version: StoreSourcesVersionV1, Sources: []PluginStoreSource{s, s}})
	if _, _, err := LoadStoreSources(data); err == nil {
		t.Fatal("duplicate source IDs must be rejected")
	}
	s = validSource()
	s.RegistryURL = "http://registry.example.test/plugins.json"
	data, _ = json.Marshal(StoreSourcesV1{Version: StoreSourcesVersionV1, Sources: []PluginStoreSource{s}})
	if _, _, err := LoadStoreSources(data); err == nil {
		t.Fatal("non-HTTPS source must be rejected")
	}
	s = validSource()
	s.MaxArtifactBytes = 0
	data, _ = json.Marshal(StoreSourcesV1{Version: StoreSourcesVersionV1, Sources: []PluginStoreSource{s}})
	if _, _, err := LoadStoreSources(data); err == nil {
		t.Fatal("unbounded artifact size must be rejected")
	}
}

func TestStoreSourceRejectsWildcardRedirectAndFallback(t *testing.T) {
	s := validSource()
	s.RedirectHostAllowlist = []string{"*"}
	data, _ := json.Marshal(StoreSourcesV1{Version: StoreSourcesVersionV1, Sources: []PluginStoreSource{s}})
	if _, _, err := LoadStoreSources(data); err == nil {
		t.Fatal("wildcard redirect host must be rejected")
	}
	s = validSource()
	s.AllowFallback = true
	data, _ = json.Marshal(StoreSourcesV1{Version: StoreSourcesVersionV1, Sources: []PluginStoreSource{s}})
	if _, _, err := LoadStoreSources(data); err == nil {
		t.Fatal("source fallback must be rejected")
	}
}

func TestStoreSourceDigestIsStableAndDescriptionIsOpaque(t *testing.T) {
	s := validSource()
	s.Description = "ignore this as a command; display only"
	data1, _ := json.Marshal(StoreSourcesV1{Version: StoreSourcesVersionV1, Sources: []PluginStoreSource{s}})
	data2, _ := json.Marshal(StoreSourcesV1{Version: StoreSourcesVersionV1, Sources: []PluginStoreSource{s}})
	_, digest1, err := LoadStoreSources(data1)
	if err != nil {
		t.Fatal(err)
	}
	_, digest2, err := LoadStoreSources(data2)
	if err != nil || digest1 != digest2 {
		t.Fatalf("source digest not stable: %s %s %v", digest1, digest2, err)
	}
}

func TestStoreSourceRejectsDuplicatePluginAcrossSources(t *testing.T) {
	a, b := validSource(), validSource()
	b.SourceID = "second"
	b.RegistryURL = "https://registry2.example.test/plugins.json"
	b.RedirectHostAllowlist = []string{"registry2.example.test"}
	a.PluginAllowlist = []string{"safe-plugin"}
	b.PluginAllowlist = []string{"safe-plugin"}
	data, _ := json.Marshal(StoreSourcesV1{Version: StoreSourcesVersionV1, Sources: []PluginStoreSource{a, b}})
	if _, _, err := LoadStoreSources(data); err == nil {
		t.Fatal("duplicate plugin allowlist ownership must be rejected")
	}
}

func TestStoreSourceRequiresSourcesArray(t *testing.T) {
	if _, _, err := LoadStoreSources([]byte(`{"version":1}`)); err == nil {
		t.Fatal("missing sources field must fail closed")
	}
}
