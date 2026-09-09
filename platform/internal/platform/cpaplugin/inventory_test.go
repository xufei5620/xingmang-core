package cpaplugin

import (
	"reflect"
	"strings"
	"testing"
)

func TestJoinInventoryDetectsPriorityShadowAndDrift(t *testing.T) {
	p := validAdmission()
	physical := PhysicalSnapshot{Complete: true, Files: []PhysicalPlugin{
		{PathClass: "root", PluginID: p.PluginID, SHA256: p.ArtifactSHA256, Size: p.ArtifactSize},
		{PathClass: "platform", PluginID: p.PluginID, SHA256: strings.Repeat("c", 64), Size: p.ArtifactSize},
	}}
	configs := ConfigProjection{Complete: true, Plugins: []ConfigPlugin{{PluginID: p.PluginID, ConfigSHA256: strings.Repeat("d", 64), EnabledExplicit: false, Enabled: true, Effective: true, Priority: 1}}}
	runtime := RuntimeProjection{Complete: true, Plugins: []RuntimePlugin{{PluginID: p.PluginID, Version: "9.9.9", Registered: true, Enabled: true, Effective: true, Capabilities: []string{"metadata.model.register"}, RouteDigest: p.ManagementRoutesSHA256, ResourceDigest: p.ResourceRoutesSHA256}}}
	got, err := JoinInventory(map[string]PluginAdmission{p.PluginID: p}, physical, configs, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) == 0 {
		t.Fatal("expected shadow/implicit/version findings")
	}
	if got.Files[0].PathClass != "platform" || !got.Files[0].Selected || !got.Files[1].Shadowed {
		t.Fatalf("priority selection not deterministic: %+v", got.Files)
	}
}

func TestJoinInventoryPluginFreePassRequiresCompleteDisabledEvidence(t *testing.T) {
	got, err := JoinInventory(map[string]PluginAdmission{}, PhysicalSnapshot{Complete: true}, ConfigProjection{Complete: true}, RuntimeProjection{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Coverage.Complete || !got.PluginFreePass {
		t.Fatalf("empty plugin-free inventory should pass: %+v", got)
	}
	got, err = JoinInventory(map[string]PluginAdmission{}, PhysicalSnapshot{Complete: false}, ConfigProjection{Complete: true}, RuntimeProjection{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.PluginFreePass || got.Coverage.Complete {
		t.Fatal("incomplete evidence must not pass")
	}
}

func TestJoinInventoryRejectsUnknownRowsAndDuplicateIDs(t *testing.T) {
	physical := PhysicalSnapshot{Complete: true, Files: []PhysicalPlugin{{PathClass: "root", PluginID: "unknown", SHA256: strings.Repeat("a", 64), Size: 1}, {PathClass: "root", PluginID: "unknown", SHA256: strings.Repeat("b", 64), Size: 1}}}
	got, err := JoinInventory(map[string]PluginAdmission{}, physical, ConfigProjection{Complete: true}, RuntimeProjection{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) < 2 {
		t.Fatalf("expected unknown and duplicate findings: %+v", got.Findings)
	}
}

func TestJoinInventoryNeverEmitsAbsolutePaths(t *testing.T) {
	got, err := JoinInventory(map[string]PluginAdmission{}, PhysicalSnapshot{Complete: true, Files: []PhysicalPlugin{{PathClass: `C:\secret\plugins`, PluginID: "unknown", SHA256: strings.Repeat("a", 64), Size: 1}}}, ConfigProjection{Complete: true}, RuntimeProjection{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range got.Files {
		if strings.Contains(f.PathClass, `\`) || strings.Contains(f.PathClass, "/") {
			t.Fatal("absolute path leaked into inventory")
		}
	}
}

func TestJoinInventoryRejectsRuntimeCapabilityOutsideAllowedAndStateDrift(t *testing.T) {
	p := validAdmission()
	runtime := RuntimeProjection{Complete: true, Plugins: []RuntimePlugin{{PluginID: p.PluginID, Version: p.ExactTag, Registered: true, Enabled: false, Effective: true, Capabilities: []string{"metadata.model.read"}, RouteDigest: p.ManagementRoutesSHA256, ResourceDigest: p.ResourceRoutesSHA256}}}
	got, err := JoinInventory(map[string]PluginAdmission{p.PluginID: p}, PhysicalSnapshot{Complete: true}, ConfigProjection{Complete: true}, runtime, []PluginStoreSource{validSource()})
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, finding := range got.Findings {
		codes[finding.Code] = true
	}
	if !codes["runtime_capability_not_allowed"] || !codes["runtime_state_invalid"] || !codes["runtime_capability_drift"] {
		t.Fatalf("runtime drift findings=%+v", got.Findings)
	}
}

func TestJoinInventorySamePrioritySelectionIsInputOrderIndependent(t *testing.T) {
	p := validAdmission()
	a := PhysicalPlugin{PathClass: "platform", PluginID: p.PluginID, SHA256: strings.Repeat("a", 64), Size: p.ArtifactSize}
	b := PhysicalPlugin{PathClass: "platform", PluginID: p.PluginID, SHA256: strings.Repeat("b", 64), Size: p.ArtifactSize}
	first, err := JoinInventory(map[string]PluginAdmission{p.PluginID: p}, PhysicalSnapshot{Complete: true, Files: []PhysicalPlugin{a, b}}, ConfigProjection{Complete: true}, RuntimeProjection{Complete: true}, []PluginStoreSource{validSource()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := JoinInventory(map[string]PluginAdmission{p.PluginID: p}, PhysicalSnapshot{Complete: true, Files: []PhysicalPlugin{b, a}}, ConfigProjection{Complete: true}, RuntimeProjection{Complete: true}, []PluginStoreSource{validSource()})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Files, second.Files) {
		t.Fatalf("same-priority selection depends on input order: %+v vs %+v", first.Files, second.Files)
	}
}

func TestJoinInventoryReportsAuthorDriftWhenAdmissionPinsAuthor(t *testing.T) {
	p := validAdmission()
	p.Author = "approved-author"
	runtime := RuntimeProjection{Complete: true, Plugins: []RuntimePlugin{{PluginID: p.PluginID, Version: p.ExactTag, Author: "other-author", Registered: true, Enabled: false, Effective: false, Capabilities: p.DeclaredCapabilities, RouteDigest: p.ManagementRoutesSHA256, ResourceDigest: p.ResourceRoutesSHA256}}}
	got, err := JoinInventory(map[string]PluginAdmission{p.PluginID: p}, PhysicalSnapshot{Complete: true}, ConfigProjection{Complete: true}, runtime, []PluginStoreSource{validSource()})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range got.Findings {
		if finding.Code == "runtime_author_drift" {
			found = true
		}
	}
	if !found {
		t.Fatalf("author drift not reported: %+v", got.Findings)
	}
}
