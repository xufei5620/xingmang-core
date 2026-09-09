package cpaplugin

import (
	"fmt"
	"sort"
	"strings"
)

// PhysicalPlugin is the redacted observation of one file in a CPA discovery
// directory.  PathClass is an allowlisted precedence class, never an
// absolute path.
type PhysicalPlugin struct {
	PathClass string `json:"path_class"`
	PluginID  string `json:"plugin_id"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Selected  bool   `json:"selected"`
	Shadowed  bool   `json:"shadowed"`
}

// PhysicalSnapshot is a complete or partial local file walk supplied by the
// caller.  JoinInventory never performs the walk itself.
type PhysicalSnapshot struct {
	Complete      bool             `json:"complete"`
	Source        string           `json:"source,omitempty"`
	GlobalEnabled bool             `json:"global_plugins_enabled,omitempty"`
	Files         []PhysicalPlugin `json:"files"`
}

type ConfigPlugin struct {
	PluginID        string `json:"plugin_id"`
	ConfigSHA256    string `json:"config_sha256"`
	EnabledExplicit bool   `json:"enabled_explicit"`
	Enabled         bool   `json:"enabled"`
	Effective       bool   `json:"effective"`
	Priority        int    `json:"priority"`
	RestartRequired bool   `json:"restart_required,omitempty"`
}

type ConfigProjection struct {
	Complete      bool           `json:"complete"`
	Source        string         `json:"source,omitempty"`
	GlobalEnabled bool           `json:"global_plugins_enabled"`
	Plugins       []ConfigPlugin `json:"plugins"`
}

type RuntimePlugin struct {
	PluginID        string   `json:"plugin_id"`
	Version         string   `json:"version"`
	PluginVersion   string   `json:"plugin_version,omitempty"`
	Author          string   `json:"author"`
	Repository      string   `json:"repository,omitempty"`
	Registered      bool     `json:"registered"`
	Enabled         bool     `json:"enabled"`
	Effective       bool     `json:"effective"`
	Capabilities    []string `json:"capabilities"`
	RouteDigest     string   `json:"route_digest"`
	ResourceDigest  string   `json:"resource_digest"`
	RestartRequired bool     `json:"restart_required,omitempty"`
}

type RuntimeProjection struct {
	Complete      bool            `json:"complete"`
	Source        string          `json:"source,omitempty"`
	GlobalEnabled bool            `json:"global_plugins_enabled,omitempty"`
	Plugins       []RuntimePlugin `json:"plugins"`
}

type Finding struct {
	Code     string `json:"code"`
	PluginID string `json:"plugin_id,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type Coverage struct {
	Complete bool     `json:"complete"`
	Missing  []string `json:"missing,omitempty"`
	Sources  []string `json:"sources,omitempty"`
}

type PluginInventory struct {
	GlobalEnabled  bool             `json:"global_plugins_enabled"`
	Files          []PhysicalPlugin `json:"files"`
	Configs        []ConfigPlugin   `json:"configs"`
	Runtime        []RuntimePlugin  `json:"runtime"`
	Findings       []Finding        `json:"findings"`
	Coverage       Coverage         `json:"coverage"`
	PluginFreePass bool             `json:"plugin_free_pass"`
}

// JoinInventory joins policy, physical, config, and runtime projections.  A
// value of any is accepted at the boundary so callers can pass either the
// snapshot structs or their []Plugin slices; unsupported values fail closed.
// The function is pure and never accesses a filesystem, network, CPA, or
// credential provider.
func JoinInventory(policyValue any, physicalValue any, configValue any, runtimeValue any, storeSourceValues ...[]PluginStoreSource) (PluginInventory, error) {
	policy, err := normalizePolicy(policyValue)
	if err != nil {
		return PluginInventory{}, err
	}
	physical, err := normalizePhysical(physicalValue)
	if err != nil {
		return PluginInventory{}, err
	}
	config, err := normalizeConfig(configValue)
	if err != nil {
		return PluginInventory{}, err
	}
	runtime, err := normalizeRuntime(runtimeValue)
	if err != nil {
		return PluginInventory{}, err
	}

	storeComplete := len(policy) == 0 && len(storeSourceValues) == 0
	var stores []PluginStoreSource
	if len(storeSourceValues) > 0 {
		stores = storeSourceValues[0]
		storeComplete = stores != nil && (len(policy) == 0 || len(stores) > 0)
		if stores != nil {
			envelope := StoreSourcesV1{Version: StoreSourcesVersionV1, Sources: stores}
			if err := envelope.Validate(); err != nil {
				return PluginInventory{}, err
			}
		}
	}

	inv := PluginInventory{
		GlobalEnabled: config.GlobalEnabled || physical.GlobalEnabled || runtime.GlobalEnabled,
		Files:         append([]PhysicalPlugin(nil), physical.Files...),
		Configs:       append([]ConfigPlugin(nil), config.Plugins...),
		Runtime:       append([]RuntimePlugin(nil), runtime.Plugins...),
		Coverage:      Coverage{Complete: physical.Complete && config.Complete && runtime.Complete && storeComplete},
	}
	inv.Coverage.Sources = []string{"physical", "config", "runtime", "store_sources", "admission"}
	if !physical.Complete {
		inv.Coverage.Missing = append(inv.Coverage.Missing, "physical")
	}
	if !config.Complete {
		inv.Coverage.Missing = append(inv.Coverage.Missing, "config")
	}
	if !runtime.Complete {
		inv.Coverage.Missing = append(inv.Coverage.Missing, "runtime")
	}
	if !storeComplete {
		inv.Coverage.Missing = append(inv.Coverage.Missing, "store_sources")
	}
	if len(inv.Coverage.Missing) > 0 {
		inv.addFinding("coverage_incomplete", "", "error", "one or more inventory sources are incomplete")
	}
	for key, admission := range policy {
		if key != admission.PluginID {
			inv.addFinding("policy_key_mismatch", admission.PluginID, "error", "policy map key differs from plugin_id")
		}
		if storeComplete && len(stores) > 0 {
			found := false
			pluginAllowed := true
			for _, source := range stores {
				if source.SourceID == admission.SourceID {
					found = true
					if len(source.PluginAllowlist) > 0 {
						pluginAllowed = false
						for _, allowedID := range source.PluginAllowlist {
							if allowedID == admission.PluginID {
								pluginAllowed = true
								break
							}
						}
					}
					break
				}
			}
			if !found {
				inv.addFinding("source_missing", admission.PluginID, "error", "admission source is absent from store source projection")
			} else if !pluginAllowed {
				inv.addFinding("source_plugin_mismatch", admission.PluginID, "error", "plugin is not allowlisted by its admission source")
			}
		}
	}

	// Physical discovery precedence is deterministic and independent of input
	// ordering.  A same-class duplicate is a conflict; only one winner may be
	// selected.
	sort.SliceStable(inv.Files, func(i, j int) bool {
		ri, rj := pathClassRank(inv.Files[i].PathClass), pathClassRank(inv.Files[j].PathClass)
		if ri != rj {
			return ri > rj
		}
		if inv.Files[i].PluginID != inv.Files[j].PluginID {
			return inv.Files[i].PluginID < inv.Files[j].PluginID
		}
		if inv.Files[i].PathClass != inv.Files[j].PathClass {
			return inv.Files[i].PathClass < inv.Files[j].PathClass
		}
		if inv.Files[i].SHA256 != inv.Files[j].SHA256 {
			return inv.Files[i].SHA256 < inv.Files[j].SHA256
		}
		return inv.Files[i].Size < inv.Files[j].Size
	})
	selected := map[string]int{}
	for i := range inv.Files {
		file := &inv.Files[i]
		file.Selected, file.Shadowed = false, false
		if !pluginIDPattern.MatchString(file.PluginID) {
			inv.addFinding("unknown_file", file.PluginID, "error", "physical file has invalid plugin id")
		}
		if strings.ContainsAny(file.PathClass, `/\\`) || pathClassRank(file.PathClass) == 0 {
			file.PathClass = "unknown"
			inv.addFinding("unknown_path_class", file.PluginID, "error", "physical path class is not allowlisted")
		}
		if !digestPattern.MatchString(file.SHA256) {
			inv.addFinding("file_hash_invalid", file.PluginID, "error", "physical file hash is not canonical")
		}
		if file.Size <= 0 {
			inv.addFinding("file_size_invalid", file.PluginID, "error", "physical file size must be positive")
		}
		if _, ok := policy[file.PluginID]; !ok {
			inv.addFinding("unknown_file", file.PluginID, "error", "physical file is not present in admission policy")
		}
		if previous, ok := selected[file.PluginID]; !ok {
			if pathClassRank(file.PathClass) > 0 {
				selected[file.PluginID] = i
				file.Selected = true
			}
		} else {
			winner := &inv.Files[previous]
			if pathClassRank(file.PathClass) == pathClassRank(winner.PathClass) {
				file.Shadowed = true
				inv.addFinding("shadow_conflict", file.PluginID, "error", "duplicate plugin files at the same priority")
			} else {
				file.Shadowed = true
			}
			inv.addFinding("shadowed_path", file.PluginID, "error", "lower-priority plugin file is shadowed")
		}
	}
	for id, index := range selected {
		file := inv.Files[index]
		admission, ok := policy[id]
		if !ok {
			continue
		}
		if file.SHA256 != admission.ArtifactSHA256 {
			inv.addFinding("file_hash_drift", id, "error", "selected artifact hash differs from admission")
		}
		if file.Size != admission.ArtifactSize {
			inv.addFinding("file_size_drift", id, "error", "selected artifact size differs from admission")
		}
	}
	observed := make(map[string]bool, len(selected))
	for id := range selected {
		observed[id] = true
	}

	seenConfig := map[string]struct{}{}
	for i := range inv.Configs {
		cfg := &inv.Configs[i]
		if _, duplicate := seenConfig[cfg.PluginID]; duplicate {
			inv.addFinding("duplicate_config", cfg.PluginID, "error", "duplicate plugin config")
		}
		seenConfig[cfg.PluginID] = struct{}{}
		if _, ok := policy[cfg.PluginID]; !ok {
			inv.addFinding("unknown_config", cfg.PluginID, "error", "config is not present in admission policy")
		}
		if !digestPattern.MatchString(cfg.ConfigSHA256) {
			inv.addFinding("config_hash_invalid", cfg.PluginID, "error", "config hash is not canonical")
		}
		if admission, ok := policy[cfg.PluginID]; ok && cfg.ConfigSHA256 != admission.ConfigSchemaSHA256 {
			inv.addFinding("config_hash_drift", cfg.PluginID, "error", "config digest differs from admission")
		}
		if !cfg.EnabledExplicit {
			inv.addFinding("implicit_enabled", cfg.PluginID, "error", "enabled flag must be explicit")
		}
		if cfg.Priority < 0 {
			inv.addFinding("config_priority_invalid", cfg.PluginID, "error", "config priority cannot be negative")
		}
		if !cfg.Enabled && cfg.Effective {
			inv.addFinding("config_runtime_drift", cfg.PluginID, "error", "runtime is effective while config is disabled")
		}
		if cfg.Effective && !inv.GlobalEnabled {
			inv.addFinding("global_disabled_effective", cfg.PluginID, "error", "plugin is effective while global plugins are disabled")
		}
		if cfg.RestartRequired {
			inv.addFinding("restart_required", cfg.PluginID, "error", "plugin state requires an explicit restart before it can be trusted")
		}
	}

	seenRuntime := map[string]struct{}{}
	for i := range inv.Runtime {
		r := &inv.Runtime[i]
		if _, duplicate := seenRuntime[r.PluginID]; duplicate {
			inv.addFinding("duplicate_runtime", r.PluginID, "error", "duplicate runtime plugin")
		}
		seenRuntime[r.PluginID] = struct{}{}
		admission, ok := policy[r.PluginID]
		if !ok {
			inv.addFinding("unknown_runtime", r.PluginID, "error", "runtime plugin is not present in admission policy")
			continue
		}
		if !r.Registered && (r.Enabled || r.Effective) {
			inv.addFinding("runtime_state_invalid", r.PluginID, "error", "unregistered plugin cannot be enabled or effective")
		}
		observedVersion := r.PluginVersion
		if observedVersion == "" {
			observedVersion = r.Version
		}
		if observedVersion != admission.ExactTag {
			inv.addFinding("runtime_version_drift", r.PluginID, "error", "runtime plugin version differs from exact admission tag")
		}
		if r.Repository != "" && r.Repository != admission.Repository {
			inv.addFinding("runtime_repository_drift", r.PluginID, "error", "runtime repository differs from admission")
		}
		if admission.Author != "" && r.Author != admission.Author {
			inv.addFinding("runtime_author_drift", r.PluginID, "error", "runtime author differs from admission")
		}
		if !equalStringSet(r.Capabilities, admission.DeclaredCapabilities) {
			inv.addFinding("runtime_capability_drift", r.PluginID, "error", "runtime capabilities differ from declared capabilities")
		}
		allowed := stringSet(admission.AllowedCapabilities)
		for _, capability := range r.Capabilities {
			if _, ok := allowed[capability]; !ok {
				inv.addFinding("runtime_capability_not_allowed", r.PluginID, "error", "runtime capability is outside the explicit allowed set")
				break
			}
		}
		if r.RouteDigest != admission.ManagementRoutesSHA256 {
			inv.addFinding("runtime_route_drift", r.PluginID, "error", "runtime management route digest differs from admission")
		}
		if r.ResourceDigest != admission.ResourceRoutesSHA256 {
			inv.addFinding("runtime_resource_drift", r.PluginID, "error", "runtime resource route digest differs from admission")
		}
		if r.Effective && !inv.GlobalEnabled {
			inv.addFinding("global_disabled_effective", r.PluginID, "error", "runtime plugin is effective while global plugins are disabled")
		}
		if r.Effective && !r.Enabled {
			inv.addFinding("runtime_state_invalid", r.PluginID, "error", "effective plugin must be explicitly enabled")
		}
		if r.RestartRequired {
			inv.addFinding("restart_required", r.PluginID, "error", "runtime reports that a restart is required")
		}
	}
	for id := range policy {
		if !observed[id] {
			for _, cfg := range inv.Configs {
				if cfg.PluginID == id {
					observed[id] = true
				}
			}
			for _, runtimePlugin := range inv.Runtime {
				if runtimePlugin.PluginID == id {
					observed[id] = true
				}
			}
		}
		if !observed[id] {
			inv.addFinding("admission_unobserved", id, "warning", "admission has no corresponding physical, config, or runtime observation")
		}
	}

	sort.Slice(inv.Configs, func(i, j int) bool { return inv.Configs[i].PluginID < inv.Configs[j].PluginID })
	sort.Slice(inv.Runtime, func(i, j int) bool { return inv.Runtime[i].PluginID < inv.Runtime[j].PluginID })
	sort.Slice(inv.Findings, func(i, j int) bool {
		if inv.Findings[i].Code != inv.Findings[j].Code {
			return inv.Findings[i].Code < inv.Findings[j].Code
		}
		if inv.Findings[i].PluginID != inv.Findings[j].PluginID {
			return inv.Findings[i].PluginID < inv.Findings[j].PluginID
		}
		return inv.Findings[i].Message < inv.Findings[j].Message
	})
	inv.PluginFreePass = len(policy) == 0 && !inv.GlobalEnabled && inv.Coverage.Complete && len(inv.Files) == 0 && len(inv.Configs) == 0 && len(inv.Runtime) == 0 && len(inv.Findings) == 0
	return inv, nil
}

// JoinInventoryWithSources is the explicit four-source form used by the
// assessor. It keeps the historical four-argument JoinInventory call source
// compatible while making store-source coverage visible to new callers.
func JoinInventoryWithSources(policyValue any, sources []PluginStoreSource, physicalValue any, configValue any, runtimeValue any) (PluginInventory, error) {
	return JoinInventory(policyValue, physicalValue, configValue, runtimeValue, sources)
}

func (i *PluginInventory) addFinding(code, pluginID, severity, message string) {
	i.Findings = append(i.Findings, Finding{Code: code, PluginID: pluginID, Severity: severity, Message: message})
}

func pathClassRank(class string) int {
	switch class {
	case "variant", "platform-variant", "os-arch-variant":
		return 3
	case "platform", "os-arch":
		return 2
	case "root":
		return 1
	default:
		return 0
	}
}

func equalStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	a, b := sortedCopy(left), sortedCopy(right)
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func normalizePolicy(value any) (map[string]PluginAdmission, error) {
	switch typed := value.(type) {
	case nil:
		return map[string]PluginAdmission{}, nil
	case map[string]PluginAdmission:
		result := clonePolicy(typed)
		seen := make(map[string]struct{}, len(result))
		seenFolded := make(map[string]string, len(result))
		for key, admission := range result {
			if key != admission.PluginID {
				return nil, fmt.Errorf("policy map key %q differs from plugin_id %q", key, admission.PluginID)
			}
			if err := admission.Validate(); err != nil {
				return nil, fmt.Errorf("admission %q: %w", key, err)
			}
			if _, exists := seen[key]; exists {
				return nil, fmt.Errorf("duplicate plugin ID %q", key)
			}
			folded := strings.ToLower(key)
			if previous, exists := seenFolded[folded]; exists {
				return nil, fmt.Errorf("plugin IDs %q and %q collide case-insensitively", previous, key)
			}
			seen[key] = struct{}{}
			seenFolded[folded] = key
		}
		return result, nil
	case AdmissionPolicyV1:
		if err := typed.Validate(); err != nil {
			return nil, err
		}
		result := make(map[string]PluginAdmission, len(typed.Admissions))
		for _, admission := range typed.Admissions {
			result[admission.PluginID] = admission
		}
		return result, nil
	case *AdmissionPolicyV1:
		if typed == nil {
			return map[string]PluginAdmission{}, nil
		}
		return normalizePolicy(*typed)
	default:
		return nil, fmt.Errorf("unsupported admission policy value %T", value)
	}
}

func clonePolicy(input map[string]PluginAdmission) map[string]PluginAdmission {
	result := make(map[string]PluginAdmission, len(input))
	for key, admission := range input {
		admission.DeclaredCapabilities = append([]string(nil), admission.DeclaredCapabilities...)
		admission.AllowedCapabilities = append([]string(nil), admission.AllowedCapabilities...)
		admission.ForbiddenCapabilities = append([]string(nil), admission.ForbiddenCapabilities...)
		result[key] = admission
	}
	return result
}

func normalizePhysical(value any) (PhysicalSnapshot, error) {
	switch typed := value.(type) {
	case nil:
		return PhysicalSnapshot{}, nil
	case PhysicalSnapshot:
		return typed, nil
	case *PhysicalSnapshot:
		if typed == nil {
			return PhysicalSnapshot{}, nil
		}
		return *typed, nil
	case []PhysicalPlugin:
		return PhysicalSnapshot{Complete: true, Files: typed}, nil
	default:
		return PhysicalSnapshot{}, fmt.Errorf("unsupported physical snapshot value %T", value)
	}
}

func normalizeConfig(value any) (ConfigProjection, error) {
	switch typed := value.(type) {
	case nil:
		return ConfigProjection{}, nil
	case ConfigProjection:
		return typed, nil
	case *ConfigProjection:
		if typed == nil {
			return ConfigProjection{}, nil
		}
		return *typed, nil
	case []ConfigPlugin:
		return ConfigProjection{Complete: true, Plugins: typed}, nil
	default:
		return ConfigProjection{}, fmt.Errorf("unsupported config projection value %T", value)
	}
}

func normalizeRuntime(value any) (RuntimeProjection, error) {
	switch typed := value.(type) {
	case nil:
		return RuntimeProjection{}, nil
	case RuntimeProjection:
		return typed, nil
	case *RuntimeProjection:
		if typed == nil {
			return RuntimeProjection{}, nil
		}
		return *typed, nil
	case []RuntimePlugin:
		return RuntimeProjection{Complete: true, Plugins: typed}, nil
	default:
		return RuntimeProjection{}, fmt.Errorf("unsupported runtime projection value %T", value)
	}
}
