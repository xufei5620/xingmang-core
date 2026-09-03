package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
)

// Every test in this file reads only local fixtures under testdata/reqlog/ -
// no network, no live recorder, matching this subcommand's own design (see
// reqlog.go's package doc comment).

const reqlogTestdataDir = "testdata/reqlog"

func TestListDayDirsSortedNewestFirstIgnoresNonDayEntries(t *testing.T) {
	days, err := listDayDirs(reqlogTestdataDir)
	if err != nil {
		t.Fatalf("listDayDirs: %v", err)
	}
	want := []string{"20260829", "20260828"}
	if len(days) != len(want) || days[0] != want[0] || days[1] != want[1] {
		t.Fatalf("listDayDirs = %v, want %v (newest first, tokenmap.sample.json excluded)", days, want)
	}
}

func TestLoadDayIndexSkipsMalformedLinesWithWarning(t *testing.T) {
	recs, warnings, err := loadDayIndex(reqlogTestdataDir, "20260829")
	if err != nil {
		t.Fatalf("loadDayIndex: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 (one line is deliberately malformed)", len(recs))
	}
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1 for the malformed line", len(warnings))
	}
}

func TestLoadDayIndexMissingDayIsEmptyNotError(t *testing.T) {
	recs, warnings, err := loadDayIndex(reqlogTestdataDir, "20990101")
	if err != nil || recs != nil || warnings != nil {
		t.Fatalf("loadDayIndex(missing day) = (%v,%v,%v), want (nil,nil,nil)", recs, warnings, err)
	}
}

func TestLoadLocalTokenMap(t *testing.T) {
	m, err := loadLocalTokenMap(filepath.Join(reqlogTestdataDir, "tokenmap.sample.json"))
	if err != nil {
		t.Fatalf("loadLocalTokenMap: %v", err)
	}
	if len(m) != 3 {
		t.Fatalf("got %d entries, want 3", len(m))
	}
	if m["test-prefix-newapi-one"] != "test-user-one@newapi" {
		t.Errorf("unexpected value for known prefix: %q", m["test-prefix-newapi-one"])
	}
}

func TestLoadLocalTokenMapV2(t *testing.T) {
	v2, err := loadLocalTokenMapV2(filepath.Join(reqlogTestdataDir, "tokenmap.v2.sample.json"))
	if err != nil {
		t.Fatalf("loadLocalTokenMapV2: %v", err)
	}
	if v2.SchemaVersion != 2 {
		t.Errorf("SchemaVersion = %d, want 2", v2.SchemaVersion)
	}
	if len(v2.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(v2.Entries))
	}
	entry, ok := v2.Entries["test-prefix-newapi-one"]
	if !ok {
		t.Fatal("missing entry for test-prefix-newapi-one")
	}
	if entry.UserID != "1001" || entry.Source != "newapi" || entry.Username != "test-user-one" {
		t.Errorf("unexpected entry: %+v", entry)
	}
}

func TestLoadLocalTokenMapV2RejectsNonObjectJSON(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "tokenmap.v2.json")
	if err := os.WriteFile(bad, []byte(`["not", "an", "object"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLocalTokenMapV2(bad); err == nil {
		t.Fatal("expected an error for a non-object tokenmap.v2.json")
	}
}

func TestComputeTokenMapV2EvidenceCounts(t *testing.T) {
	v2, err := loadLocalTokenMapV2(filepath.Join(reqlogTestdataDir, "tokenmap.v2.sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	ev := computeTokenMapV2Evidence(v2)
	if ev.TotalEntries != 2 || ev.WithUserID != 2 {
		t.Fatalf("computeTokenMapV2Evidence = %+v, want {2 2}", ev)
	}

	empty := computeTokenMapV2Evidence(nil)
	if empty != (tokenMapV2Evidence{}) {
		t.Fatalf("computeTokenMapV2Evidence(nil) = %+v, want zero value", empty)
	}

	noID, err := loadLocalTokenMapV2(filepath.Join(reqlogTestdataDir, "tokenmap.v2.sample.no-user-id.json"))
	if err != nil {
		t.Fatal(err)
	}
	evNoID := computeTokenMapV2Evidence(noID)
	if evNoID.TotalEntries != 1 || evNoID.WithUserID != 0 {
		t.Fatalf("computeTokenMapV2Evidence(no-user-id) = %+v, want {1 0}", evNoID)
	}
}

func TestComputeTokenMapEvidenceCountsSuffixes(t *testing.T) {
	m, err := loadLocalTokenMap(filepath.Join(reqlogTestdataDir, "tokenmap.sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	ev := computeTokenMapEvidence(m)
	if ev.TotalEntries != 3 || ev.SuffixNewAPI != 1 || ev.SuffixSub2API != 1 || ev.MalformedSuffix != 1 {
		t.Fatalf("computeTokenMapEvidence = %+v, want {3 1 1 1}", ev)
	}
}

func loadAllTestRecords(t *testing.T) []dayRecord {
	t.Helper()
	days, err := listDayDirs(reqlogTestdataDir)
	if err != nil {
		t.Fatal(err)
	}
	var all []dayRecord
	for _, day := range days {
		recs, _, err := loadDayIndex(reqlogTestdataDir, day)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			all = append(all, dayRecord{Day: day, Record: r})
		}
	}
	return all
}

func TestComputeAssociationEvidenceCounts(t *testing.T) {
	all := loadAllTestRecords(t)
	if len(all) != 5 {
		t.Fatalf("fixture setup: got %d total records across both days, want 5", len(all))
	}
	tokenMap, err := loadLocalTokenMap(filepath.Join(reqlogTestdataDir, "tokenmap.sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	ev := computeAssociationEvidence(all, tokenMap)
	want := associationEvidence{TotalRecords: 5, ResolvedCount: 3, NoPrefixCount: 1, UnresolvedPrefixCount: 1}
	if ev != want {
		t.Fatalf("computeAssociationEvidence = %+v, want %+v", ev, want)
	}
}

func TestComputeRetentionEvidenceSpan(t *testing.T) {
	days, err := listDayDirs(reqlogTestdataDir)
	if err != nil {
		t.Fatal(err)
	}
	ev := computeRetentionEvidence(days, reqlog.RetentionDays, time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC))
	if ev.OldestDay != "20260828" || ev.NewestDay != "20260829" {
		t.Fatalf("oldest/newest = %s/%s, want 20260828/20260829", ev.OldestDay, ev.NewestDay)
	}
	if ev.ObservedSpanDays != 2 {
		t.Fatalf("ObservedSpanDays = %d, want 2 (28th and 29th inclusive)", ev.ObservedSpanDays)
	}
	if !ev.SpanWithinConfigured {
		t.Fatalf("SpanWithinConfigured = false, want true (2 days «= %d)", reqlog.RetentionDays)
	}
}

func TestComputeRetentionEvidenceEmpty(t *testing.T) {
	ev := computeRetentionEvidence(nil, reqlog.RetentionDays, time.Now())
	if ev.DayDirsFound != 0 || ev.OldestDay != "" || ev.NewestDay != "" {
		t.Fatalf("computeRetentionEvidence(nil) = %+v, want zero day info", ev)
	}
}

func TestRedactIndexRowDropsHighRiskFieldsAndMasksIP(t *testing.T) {
	all := loadAllTestRecords(t)
	tokenMap, err := loadLocalTokenMap(filepath.Join(reqlogTestdataDir, "tokenmap.sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	salt := []byte("reqlog-row-test-salt")

	// Find the day2 row that carries a "preview" field in the fixture - the
	// highest-risk field this subcommand must never surface.
	var target dayRecord
	found := false
	for _, dr := range all {
		if dr.Record.ID == "101500-000002" {
			target, found = dr, true
		}
	}
	if !found {
		t.Fatal("fixture setup: expected record 101500-000002 not found")
	}

	row := redactIndexRow(target.Record, tokenMap, salt)
	line, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	text := string(line)
	for _, mustNotContain := range []string{
		"excerpt", "test-prefix-sub2api-one", "2001:db8:1234:5678", "okhttp",
	} {
		if strings.Contains(text, mustNotContain) {
			t.Errorf("redacted row leaked %q: %s", mustNotContain, text)
		}
	}
	if !strings.Contains(text, "2001:db8:1234:x") {
		t.Errorf("redacted row should carry the /48-masked IPv6, got: %s", text)
	}
	if !row.UsernameResolved {
		t.Error("this row's prefix is in the tokenmap - UsernameResolved should be true")
	}
	if row.TokenPrefixHash == "" {
		t.Error("TokenPrefixHash should be set for a non-empty token prefix")
	}
	if violations := scanForbidden(line); len(violations) != 0 {
		t.Fatalf("scanForbidden flagged a redacted index row: %v", violations)
	}
}

func TestRedactIndexRowNoPrefixLeavesResolvedFalse(t *testing.T) {
	all := loadAllTestRecords(t)
	var target dayRecord
	for _, dr := range all {
		if dr.Record.ID == "093000-000003" { // the row with token_prefix:""
			target = dr
		}
	}
	row := redactIndexRow(target.Record, map[string]string{}, []byte("salt"))
	if row.UsernameResolved {
		t.Error("a record with no token prefix must never resolve")
	}
	if row.TokenPrefixHash != "" {
		t.Error("a record with no token prefix should have no prefix hash either")
	}
}

func TestRunReqlogEndToEnd(t *testing.T) {
	outRoot := t.TempDir()
	fixedNow := time.Date(2026, 8, 30, 6, 0, 0, 0, time.UTC)
	deps := reqlogDeps{
		now:      func() time.Time { return fixedNow },
		randSalt: func() ([]byte, error) { return []byte("end-to-end-reqlog-salt"), nil },
	}
	var stdout, stderr bytes.Buffer
	args := []string{
		"--data-dir", reqlogTestdataDir,
		"--tokenmap", filepath.Join(reqlogTestdataDir, "tokenmap.sample.json"),
		"--out", outRoot,
	}
	code := runReqlog(args, &stdout, &stderr, deps)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	outDir := filepath.Join(outRoot, timestampDir(fixedNow))
	for _, name := range []string{"index_sample.redacted.jsonl", "tokenmap_shape.redacted.json", "retention_and_association.json", "SHA256SUMS", "README.md"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("expected file %s: %v", name, err)
		}
	}

	sample, err := os.ReadFile(filepath.Join(outDir, "index_sample.redacted.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sampleText := string(sample)
	for _, mustNotContain := range []string{
		"test-user-one", "test-user-two", "test-user-three",
		"excerpt", "test-prefix-newapi-one", "test-prefix-sub2api-one",
		"203.0.113.42", "2001:db8:1234:5678",
	} {
		if strings.Contains(sampleText, mustNotContain) {
			t.Errorf("index_sample.redacted.jsonl leaked %q", mustNotContain)
		}
	}

	shapeBytes, err := os.ReadFile(filepath.Join(outDir, "tokenmap_shape.redacted.json"))
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		TotalEntries        int  `json:"total_entries"`
		CarriesSourceUserID bool `json:"carries_source_user_id"`
	}
	if err := json.Unmarshal(shapeBytes, &shape); err != nil {
		t.Fatal(err)
	}
	if shape.TotalEntries != 3 || shape.CarriesSourceUserID {
		t.Errorf("tokenmap_shape.redacted.json = %+v, want {3 false}", shape)
	}

	readme, err := os.ReadFile(filepath.Join(outDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "REQLOG_USERREF_APPROVAL") {
		t.Error("README does not name the approval event this evidence feeds")
	}
	if strings.Contains(string(readme), "test-user-one") {
		t.Fatal("README leaked a resolved identity")
	}
}

// TestRunReqlogEndToEndWithTokenMapV2CarriesUserID covers CR-0008 acceptance
// criterion 3: given a --tokenmap-v2 sample whose entries carry a non-empty
// user_id, `carries_source_user_id` must flip to true (unlike
// TestRunReqlogEndToEnd, which omits --tokenmap-v2 entirely and must stay
// false/unchanged from before CR-0008).
func TestRunReqlogEndToEndWithTokenMapV2CarriesUserID(t *testing.T) {
	outRoot := t.TempDir()
	fixedNow := time.Date(2026, 8, 30, 6, 0, 0, 0, time.UTC)
	deps := reqlogDeps{
		now:      func() time.Time { return fixedNow },
		randSalt: func() ([]byte, error) { return []byte("end-to-end-reqlog-v2-salt"), nil },
	}
	var stdout, stderr bytes.Buffer
	args := []string{
		"--data-dir", reqlogTestdataDir,
		"--tokenmap", filepath.Join(reqlogTestdataDir, "tokenmap.sample.json"),
		"--tokenmap-v2", filepath.Join(reqlogTestdataDir, "tokenmap.v2.sample.json"),
		"--out", outRoot,
	}
	code := runReqlog(args, &stdout, &stderr, deps)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	outDir := filepath.Join(outRoot, timestampDir(fixedNow))
	shapeBytes, err := os.ReadFile(filepath.Join(outDir, "tokenmap_shape.redacted.json"))
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		CarriesSourceUserID bool `json:"carries_source_user_id"`
		V2TotalEntries      int  `json:"v2_total_entries"`
		V2EntriesWithUserID int  `json:"v2_entries_with_user_id"`
	}
	if err := json.Unmarshal(shapeBytes, &shape); err != nil {
		t.Fatal(err)
	}
	if !shape.CarriesSourceUserID {
		t.Error("carries_source_user_id should be true when the v2 sample's entries carry a non-empty user_id")
	}
	if shape.V2TotalEntries != 2 || shape.V2EntriesWithUserID != 2 {
		t.Errorf("v2 counts = %+v, want {2 2}", shape)
	}

	// The raw user ids ("1001"/"2002") and identities are never surfaced -
	// only aggregate counts, same redaction discipline as the v1 fields.
	for _, name := range []string{"tokenmap_shape.redacted.json", "README.md"} {
		data, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, mustNotContain := range []string{"1001", "2002", "test-user-one", "test-user-two"} {
			if strings.Contains(text, mustNotContain) {
				t.Errorf("%s leaked %q", name, mustNotContain)
			}
		}
	}
}

// TestRunReqlogEndToEndWithTokenMapV2NoUserID covers CR-0008 acceptance
// criterion 3's other half: a v2 file that parses fine but whose sampled
// entries carry no non-empty user_id must still report
// carries_source_user_id=false, not true just because the file exists.
func TestRunReqlogEndToEndWithTokenMapV2NoUserID(t *testing.T) {
	outRoot := t.TempDir()
	fixedNow := time.Date(2026, 8, 30, 6, 0, 0, 0, time.UTC)
	deps := reqlogDeps{
		now:      func() time.Time { return fixedNow },
		randSalt: func() ([]byte, error) { return []byte("end-to-end-reqlog-v2-no-id-salt"), nil },
	}
	var stdout, stderr bytes.Buffer
	args := []string{
		"--data-dir", reqlogTestdataDir,
		"--tokenmap", filepath.Join(reqlogTestdataDir, "tokenmap.sample.json"),
		"--tokenmap-v2", filepath.Join(reqlogTestdataDir, "tokenmap.v2.sample.no-user-id.json"),
		"--out", outRoot,
	}
	code := runReqlog(args, &stdout, &stderr, deps)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	outDir := filepath.Join(outRoot, timestampDir(fixedNow))
	shapeBytes, err := os.ReadFile(filepath.Join(outDir, "tokenmap_shape.redacted.json"))
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		CarriesSourceUserID bool `json:"carries_source_user_id"`
	}
	if err := json.Unmarshal(shapeBytes, &shape); err != nil {
		t.Fatal(err)
	}
	if shape.CarriesSourceUserID {
		t.Error("carries_source_user_id should stay false when no v2 entry carries a non-empty user_id")
	}
}

// TestRunReqlogTokenMapV2ReadFailureIsFailed covers the case where the
// operator explicitly passed --tokenmap-v2 but the file cannot be read/
// parsed - unlike an absent flag (which means "no v2 data"), an explicitly
// requested file that cannot be honored should fail loudly, not silently
// degrade to "as if it were absent".
func TestRunReqlogTokenMapV2ReadFailureIsFailed(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{
		"--data-dir", reqlogTestdataDir,
		"--tokenmap", filepath.Join(reqlogTestdataDir, "tokenmap.sample.json"),
		"--tokenmap-v2", filepath.Join(reqlogTestdataDir, "does-not-exist.json"),
		"--out", t.TempDir(),
	}
	code := runReqlog(args, &stdout, &stderr, reqlogDeps{})
	if code != exitFailed {
		t.Fatalf("exit code = %d, want exitFailed; stderr=%s", code, stderr.String())
	}
}

func TestRunReqlogDryRunOpensNoFiles(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runReqlog([]string{"--dry-run"}, &stdout, &stderr, reqlogDeps{})
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no network calls") {
		t.Errorf("dry-run output should say plainly it makes no network calls:\n%s", stdout.String())
	}
}

func TestRunReqlogUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing data-dir", []string{"--tokenmap", filepath.Join(reqlogTestdataDir, "tokenmap.sample.json")}},
		{"missing tokenmap", []string{"--data-dir", reqlogTestdataDir}},
		{"bad retention-days", []string{"--data-dir", reqlogTestdataDir, "--tokenmap", filepath.Join(reqlogTestdataDir, "tokenmap.sample.json"), "--retention-days", "0"}},
		{"bad sample-size", []string{"--data-dir", reqlogTestdataDir, "--tokenmap", filepath.Join(reqlogTestdataDir, "tokenmap.sample.json"), "--sample-size", "0"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runReqlog(c.args, &stdout, &stderr, reqlogDeps{})
			if code != exitUsage {
				t.Errorf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
			}
		})
	}
}
