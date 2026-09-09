package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/reqlogformat"
)

// The reqlog subcommand never makes a network call and never touches the
// live recorder - it only reads a LOCAL COPY of index.jsonl (per day
// directory) and tokenmap.json that a human has already copied off the
// server (see docs/runbooks/USERS-REAL-APPROVAL.md for exactly where those
// live: /root/reqlog/data and /root/reqlog/tokenmap.json,
// docs/runbooks/REQLOG-RECORDER.md). This satisfies "never call live
// upstream instances" trivially: there is no upstream in this code path at
// all, only files already at rest.

type reqlogFlags struct {
	dataDir        string
	tokenMapPath   string
	tokenMapV2Path string
	retentionDays  int
	sampleSize     int
	out            string
	nowText        string
	dryRun         bool
}

func parseReqlogFlags(args []string, stderr io.Writer) (reqlogFlags, error) {
	fs := flag.NewFlagSet("evidence-capture reqlog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f reqlogFlags
	fs.StringVar(&f.dataDir, "data-dir", "", "local copy of reqlog's day-partitioned data directory, e.g. a copy of /root/reqlog/data (required unless -dry-run)")
	fs.StringVar(&f.tokenMapPath, "tokenmap", "", "local copy of tokenmap.json, e.g. a copy of /root/reqlog/tokenmap.json (required unless -dry-run)")
	fs.StringVar(&f.tokenMapV2Path, "tokenmap-v2", "", "optional local copy of tokenmap.v2.json (CR-0008's parallel companion file); when given and it parses as schema_version=2 with at least one non-empty user_id, carries_source_user_id is reported true")
	fs.IntVar(&f.retentionDays, "retention-days", reqlog.RetentionDays, "expected retention window in days, compared against what is actually observed on disk (default matches connectors/reqlog.RetentionDays)")
	fs.IntVar(&f.sampleSize, "sample-size", 20, "redacted index rows to include in the fixture (newest day first)")
	fs.StringVar(&f.out, "out", "", "evidence output root (default: "+defaultEvidenceRoot+"/reqlog)")
	fs.StringVar(&f.nowText, "now", "", "override the observation timestamp, UTC RFC3339Nano (deterministic review/testing only)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print what would be read and how it would be redacted; opens no files")
	if err := fs.Parse(args); err != nil {
		return reqlogFlags{}, err
	}
	if fs.NArg() != 0 {
		return reqlogFlags{}, fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}
	return f, nil
}

type reqlogDeps struct {
	now      func() time.Time
	randSalt func() ([]byte, error)
}

func (d *reqlogDeps) fillDefaults() {
	if d.now == nil {
		d.now = time.Now
	}
	if d.randSalt == nil {
		d.randSalt = newCaptureSalt
	}
}

func runReqlog(args []string, stdout, stderr io.Writer, deps reqlogDeps) int {
	deps.fillDefaults()

	f, err := parseReqlogFlags(args, stderr)
	if err != nil {
		return exitUsage
	}

	if f.dryRun {
		printReqlogDryRun(stdout, f)
		return exitOK
	}
	if strings.TrimSpace(f.dataDir) == "" {
		fmt.Fprintln(stderr, "evidence-capture: --data-dir is required (unless -dry-run)")
		return exitUsage
	}
	if strings.TrimSpace(f.tokenMapPath) == "" {
		fmt.Fprintln(stderr, "evidence-capture: --tokenmap is required (unless -dry-run)")
		return exitUsage
	}
	if f.retentionDays <= 0 {
		fmt.Fprintln(stderr, "evidence-capture: --retention-days must be positive")
		return exitUsage
	}
	if f.sampleSize <= 0 || f.sampleSize > 200 {
		fmt.Fprintln(stderr, "evidence-capture: --sample-size must be in 1..200")
		return exitUsage
	}

	now := deps.now().UTC()
	if strings.TrimSpace(f.nowText) != "" {
		parsed, ok := parseUTCRFC3339Nano(f.nowText)
		if !ok {
			fmt.Fprintln(stderr, "evidence-capture: --now must be UTC RFC3339Nano (e.g. 2026-08-28T09:00:00Z)")
			return exitUsage
		}
		now = parsed
	}

	dayDirs, err := listDayDirs(f.dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "evidence-capture: failed to list %s: %v\n", f.dataDir, err)
		return exitFailed
	}
	tokenMap, err := loadLocalTokenMap(f.tokenMapPath)
	if err != nil {
		fmt.Fprintf(stderr, "evidence-capture: failed to read %s: %v\n", f.tokenMapPath, err)
		return exitFailed
	}
	var tokenMapV2 *tokenMapV2File
	if strings.TrimSpace(f.tokenMapV2Path) != "" {
		v2, err := loadLocalTokenMapV2(f.tokenMapV2Path)
		if err != nil {
			fmt.Fprintf(stderr, "evidence-capture: failed to read %s: %v\n", f.tokenMapV2Path, err)
			return exitFailed
		}
		tokenMapV2 = v2
	}

	retention := computeRetentionEvidence(dayDirs, f.retentionDays, now)

	var allRecords []dayRecord
	var warnings []string
	for _, day := range dayDirs {
		recs, dayWarnings, err := loadDayIndex(f.dataDir, day)
		if err != nil {
			fmt.Fprintf(stderr, "evidence-capture: failed to read index for %s: %v\n", day, err)
			return exitFailed
		}
		for _, r := range recs {
			allRecords = append(allRecords, dayRecord{Day: day, Record: r})
		}
		warnings = append(warnings, dayWarnings...)
	}

	tokenEvidence := computeTokenMapEvidence(tokenMap)
	assocEvidence := computeAssociationEvidence(allRecords, tokenMap)

	salt, err := deps.randSalt()
	if err != nil {
		fmt.Fprintf(stderr, "evidence-capture: %v\n", err)
		return exitFailed
	}

	sample := sampleRecords(allRecords, f.sampleSize)
	redactedRows := make([]redactedIndexRow, 0, len(sample))
	for _, dr := range sample {
		redactedRows = append(redactedRows, redactIndexRow(dr.Record, tokenMap, salt))
	}
	sampleJSONL, err := marshalJSONL(redactedRows)
	if err != nil {
		fmt.Fprintf(stderr, "evidence-capture: %v\n", err)
		return exitFailed
	}
	if violations := scanForbidden(sampleJSONL); len(violations) > 0 {
		fmt.Fprintln(stderr, "evidence-capture: refusing to write index_sample.redacted.jsonl - failed final safety scan:")
		for _, v := range violations {
			fmt.Fprintln(stderr, "  - "+v)
		}
		return exitFailed
	}

	tokenMapV2Evidence := computeTokenMapV2Evidence(tokenMapV2)
	carriesSourceUserID := tokenMapV2 != nil && tokenMapV2.SchemaVersion == 2 && tokenMapV2Evidence.WithUserID > 0

	tokenMapShapeFields := map[string]any{
		"schema":                 "flat map[string]string; key = token prefix, value = \"<username-or-email>@<source>\"",
		"total_entries":          tokenEvidence.TotalEntries,
		"suffix_newapi":          tokenEvidence.SuffixNewAPI,
		"suffix_sub2api":         tokenEvidence.SuffixSub2API,
		"malformed_suffix":       tokenEvidence.MalformedSuffix,
		"carries_source_user_id": carriesSourceUserID,
		"finding":                reqlogTokenMapShapeFinding(tokenMapV2, carriesSourceUserID, tokenMapV2Evidence),
	}
	if tokenMapV2 != nil {
		tokenMapShapeFields["v2_schema"] = "optional companion file tokenmap.v2.json (CR-0008): " +
			"{schema_version:2, entries:{<token_prefix>:{username, source, user_id}}}"
		tokenMapShapeFields["v2_schema_version_observed"] = tokenMapV2.SchemaVersion
		tokenMapShapeFields["v2_total_entries"] = tokenMapV2Evidence.TotalEntries
		tokenMapShapeFields["v2_entries_with_user_id"] = tokenMapV2Evidence.WithUserID
	}
	tokenMapShape := mustMarshalIndent(tokenMapShapeFields)
	if violations := scanForbidden(tokenMapShape); len(violations) > 0 {
		fmt.Fprintln(stderr, "evidence-capture: refusing to write tokenmap_shape.redacted.json - failed final safety scan:")
		for _, v := range violations {
			fmt.Fprintln(stderr, "  - "+v)
		}
		return exitFailed
	}

	outDir := f.out
	if strings.TrimSpace(outDir) == "" {
		outDir = filepath.Join(defaultEvidenceRoot, "reqlog")
	}
	outDir = filepath.Join(outDir, timestampDir(now))

	dataFiles := []evidenceFile{
		{Name: "index_sample.redacted.jsonl", Data: sampleJSONL},
		{Name: "tokenmap_shape.redacted.json", Data: tokenMapShape},
		{Name: "retention_and_association.json", Data: mustMarshalIndent(map[string]any{
			"retention":                   retention,
			"association":                 assocEvidence,
			"cursor_semantics":            cursorSemanticsNote,
			"index_line_warnings_skipped": len(warnings),
		})},
	}

	sums := computeSums(dataFiles)
	readme := renderReqlogReadme(retention, tokenEvidence, assocEvidence, dayDirs, sums, now, fmt.Sprintf("%x", salt),
		tokenMapV2, carriesSourceUserID, tokenMapV2Evidence)
	allFiles := append(append([]evidenceFile{}, dataFiles...),
		evidenceFile{Name: "SHA256SUMS", Data: sumsFileContent(dataFiles, sums)},
		evidenceFile{Name: "README.md", Data: readme},
	)

	if err := writeEvidenceDir(outDir, allFiles); err != nil {
		fmt.Fprintf(stderr, "evidence-capture: %v\n", err)
		return exitFailed
	}

	fmt.Fprintf(stdout, "evidence written to %s\n", outDir)
	fmt.Fprintf(stdout, "  day dirs found: %d (oldest %s, newest %s)\n", len(dayDirs), retention.OldestDay, retention.NewestDay)
	fmt.Fprintf(stdout, "  records scanned: %d, resolved: %d, no prefix: %d, unresolved prefix: %d\n",
		assocEvidence.TotalRecords, assocEvidence.ResolvedCount, assocEvidence.NoPrefixCount, assocEvidence.UnresolvedPrefixCount)
	return exitOK
}

func printReqlogDryRun(stdout io.Writer, f reqlogFlags) {
	fmt.Fprintln(stdout, "evidence-capture: dry run for the reqlog subcommand - opens no files, makes no network calls")
	dataDir := f.dataDir
	if dataDir == "" {
		dataDir = "<--data-dir>"
	}
	tm := f.tokenMapPath
	if tm == "" {
		tm = "<--tokenmap>"
	}
	fmt.Fprintf(stdout, "would scan every YYYYMMDD subdirectory of %s for an index.jsonl\n", dataDir)
	fmt.Fprintf(stdout, "would read %s as a flat JSON object (token prefix -> \"name@source\")\n", tm)
	if f.tokenMapV2Path != "" {
		fmt.Fprintf(stdout, "would also read %s as CR-0008's optional companion file "+
			"({schema_version:2, entries:{prefix:{username,source,user_id}}}) and report "+
			"carries_source_user_id=true only if it parses as schema_version=2 with at least one "+
			"non-empty user_id\n", f.tokenMapV2Path)
	} else {
		fmt.Fprintln(stdout, "no --tokenmap-v2 given: would report carries_source_user_id=false (same as before CR-0008)")
	}
	fmt.Fprintln(stdout, "would compute: retention span vs --retention-days, cursor semantics (from source,")
	fmt.Fprintln(stdout, "not from the sample), tokenmap schema findings, and per-record association")
	fmt.Fprintln(stdout, "outcome counts (resolved / no prefix / prefix not in tokenmap) - never the")
	fmt.Fprintln(stdout, "resolved usernames or emails themselves.")
	fmt.Fprintln(stdout, "would write a redacted sample of up to --sample-size index rows with id/client_ip/")
	fmt.Fprintln(stdout, "token_prefix hashed or masked, and drop ua/preview/end_note/upstream_request_id/")
	fmt.Fprintln(stdout, "resp_body_id/path/method entirely.")
}

// dayRecord pairs a parsed index row with the day directory it came from,
// since reqlogformat.Record itself does not carry its own directory name.
type dayRecord struct {
	Day    string
	Record reqlogformat.Record
}

// listDayDirs mirrors connectors/reqlog/file_client.go's dayDirs(): only
// entries matching reqlogformat.DayDirPattern, sorted newest-first.
func listDayDirs(dataDir string) ([]string, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, err
	}
	var days []string
	for _, e := range entries {
		if e.IsDir() && reqlogformat.DayDirPattern.MatchString(e.Name()) {
			days = append(days, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	return days, nil
}

// loadDayIndex mirrors connectors/reqlog/file_client.go's loadDayIndex: bad
// lines are skipped, not fatal, but this version collects a warning per skip
// instead of only logging it, so the evidence output can report how many
// lines (if any) did not parse.
func loadDayIndex(dataDir, day string) ([]reqlogformat.Record, []string, error) {
	path := filepath.Join(dataDir, day, "index.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer f.Close()

	var out []reqlogformat.Record
	var warnings []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r reqlogformat.Record
		if err := json.Unmarshal(line, &r); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s line %d: not valid JSON, skipped", day, lineNo))
			continue
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, warnings, err
	}
	return out, warnings, nil
}

func loadLocalTokenMap(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("not a flat JSON object of strings: %w", err)
	}
	return m, nil
}

// tokenMapV2File / tokenMapV2FileEntry mirror the on-disk shape CR-0008
// defines for the optional companion file tokenmap.v2.json (same shape
// cmd/reqlog-recorder writes and connectors/reqlog/file_client.go reads).
// This tool keeps its own copy of the shape rather than importing either of
// those packages - cmd/reqlog-recorder is a `main` package that cannot be
// imported, and connectors/reqlog's own copy is unexported - the JSON shape
// itself is the contract shared across all three, same as tokenmap.json
// (v1, a flat map[string]string) already is.
type tokenMapV2File struct {
	SchemaVersion int                            `json:"schema_version"`
	Entries       map[string]tokenMapV2FileEntry `json:"entries"`
}

type tokenMapV2FileEntry struct {
	Username string `json:"username"`
	Source   string `json:"source"`
	UserID   string `json:"user_id"`
}

// loadLocalTokenMapV2 reads an optional local copy of tokenmap.v2.json.
// Unlike loadLocalTokenMap, this is only called when the operator explicitly
// passed --tokenmap-v2, so a read/parse failure here is reported loudly
// (exitFailed by the caller) rather than silently treated as "absent" -
// the operator asked for this file to be included in the evidence.
func loadLocalTokenMapV2(path string) (*tokenMapV2File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v tokenMapV2File
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("not a valid tokenmap.v2.json object: %w", err)
	}
	return &v, nil
}

// tokenMapV2Evidence reports facts about an (optional) tokenmap.v2.json
// sample - counts only, never the usernames/emails/user_ids themselves,
// same redaction discipline as tokenMapEvidence for the v1 file.
type tokenMapV2Evidence struct {
	TotalEntries int `json:"total_entries"`
	WithUserID   int `json:"with_user_id"`
}

// computeTokenMapV2Evidence tolerates a nil file (the --tokenmap-v2 flag
// was not given) by returning the zero value - callers gate on the flag
// having been passed, not on this evidence being non-zero, to keep "not
// provided" and "provided but empty" distinguishable upstream.
func computeTokenMapV2Evidence(v2 *tokenMapV2File) tokenMapV2Evidence {
	var ev tokenMapV2Evidence
	if v2 == nil {
		return ev
	}
	ev.TotalEntries = len(v2.Entries)
	for _, entry := range v2.Entries {
		if strings.TrimSpace(entry.UserID) != "" {
			ev.WithUserID++
		}
	}
	return ev
}

// reqlogTokenMapShapeFinding renders the "finding" text for
// tokenmap_shape.redacted.json. It always states the schema-level fact
// about tokenmap.json (v1): that shape alone never carries an upstream user
// id. When a tokenmap.v2.json sample was also provided (CR-0008's parallel
// companion file), it appends what this specific run observed about it,
// rather than leaving the pre-CR-0008 finding text stale and misleading
// once the gap it describes has actually been closed for a given recorder
// instance.
func reqlogTokenMapShapeFinding(v2 *tokenMapV2File, carriesSourceUserID bool, v2Ev tokenMapV2Evidence) string {
	base := "the tokenmap.json (v1) value shape has no field for an upstream numeric/opaque user id - " +
		"only a display identifier (username or email) suffixed with the source. " +
		"cmd/reqlog-recorder/tokenmap.go's export queries only SELECT username/email (not id) into " +
		"this v1 file. This is a schema-level fact about tokenmap.json itself, true regardless of " +
		"which tokenmap.json was sampled."
	if v2 == nil {
		return base + " No tokenmap.v2.json companion file was provided to this run (--tokenmap-v2), so " +
			"this run cannot say whether this recorder instance can supply platform + source_user_id " +
			"(REQLOG_USERREF_APPROVAL's evidence requirement) via CR-0008's companion file - only that " +
			"the v1 file alone cannot."
	}
	if carriesSourceUserID {
		return fmt.Sprintf(base+" CR-0008 (docs/change-requests/CR-0008-reqlog-tokenmap-upstream-user-id.md) "+
			"closes this gap with a parallel tokenmap.v2.json file: this run found schema_version=%d, "+
			"%d entries, %d of them carrying a non-empty user_id. This recorder instance CAN supply "+
			"platform + source_user_id via the v2 companion file.",
			v2.SchemaVersion, v2Ev.TotalEntries, v2Ev.WithUserID)
	}
	return fmt.Sprintf(base+" A tokenmap.v2.json companion file was provided (schema_version=%d, %d "+
		"entries) but none of the sampled entries carry a non-empty user_id, so this recorder instance "+
		"still cannot supply platform + source_user_id from this sample.",
		v2.SchemaVersion, v2Ev.TotalEntries)
}

// retentionEvidence describes what is actually on disk, not a pass/fail
// verdict - see computeRetentionEvidence's doc comment for why.
type retentionEvidence struct {
	DayDirsFound            int    `json:"day_dirs_found"`
	OldestDay               string `json:"oldest_day"`
	NewestDay               string `json:"newest_day"`
	ObservedSpanDays        int    `json:"observed_span_days"`
	ConfiguredRetentionDays int    `json:"configured_retention_days"`
	SpanWithinConfigured    bool   `json:"span_within_configured"`
	Note                    string `json:"note"`
}

const retentionDraftNote = "RetentionDays is a DRAFT constant in connectors/reqlog/contract.go, " +
	"sourced from an implementation report's stated \"30-day auto cleanup\" and explicitly marked " +
	"\"未对真实部署核对\" (not verified against a real deployment). observed_span_days only reports " +
	"what is currently on disk in the copy this tool was pointed at; it cannot by itself prove a " +
	"cleanup job is (or is not) running on schedule - a short span is equally consistent with " +
	"\"cleanup works\" and \"this deployment is younger than the retention window\"."

// computeRetentionEvidence reports facts about what is on disk (day dirs
// found, their span) rather than asserting whether retention "is correct" -
// see retentionDraftNote for why a short observed span cannot be read as
// proof either way.
func computeRetentionEvidence(dayDirs []string, configuredRetentionDays int, now time.Time) retentionEvidence {
	ev := retentionEvidence{
		DayDirsFound:            len(dayDirs),
		ConfiguredRetentionDays: configuredRetentionDays,
		Note:                    retentionDraftNote,
	}
	if len(dayDirs) == 0 {
		return ev
	}
	// dayDirs is sorted newest-first (listDayDirs).
	ev.NewestDay = dayDirs[0]
	ev.OldestDay = dayDirs[len(dayDirs)-1]
	newest, errNewest := time.ParseInLocation("20060102", ev.NewestDay, reqlogformat.CST)
	oldest, errOldest := time.ParseInLocation("20060102", ev.OldestDay, reqlogformat.CST)
	if errNewest == nil && errOldest == nil {
		ev.ObservedSpanDays = int(newest.Sub(oldest).Hours()/24) + 1
		ev.SpanWithinConfigured = ev.ObservedSpanDays <= configuredRetentionDays
	}
	return ev
}

type tokenMapEvidence struct {
	TotalEntries    int `json:"total_entries"`
	SuffixNewAPI    int `json:"suffix_newapi"`
	SuffixSub2API   int `json:"suffix_sub2api"`
	MalformedSuffix int `json:"malformed_suffix"`
}

func computeTokenMapEvidence(tokenMap map[string]string) tokenMapEvidence {
	var ev tokenMapEvidence
	ev.TotalEntries = len(tokenMap)
	for _, v := range tokenMap {
		switch {
		case strings.HasSuffix(v, "@newapi"):
			ev.SuffixNewAPI++
		case strings.HasSuffix(v, "@sub2api"):
			ev.SuffixSub2API++
		default:
			ev.MalformedSuffix++
		}
	}
	return ev
}

type associationEvidence struct {
	TotalRecords          int `json:"total_records"`
	NoPrefixCount         int `json:"no_prefix_count"`
	UnresolvedPrefixCount int `json:"unresolved_prefix_count"`
	ResolvedCount         int `json:"resolved_count"`
}

// computeAssociationEvidence classifies every record by resolution outcome
// using the exact same lookup connectors/reqlog/file_client.go's
// resolveUsername performs (tokenMap[prefix], suffix-stripped) - but reports
// only counts, never the resolved username/email itself.
func computeAssociationEvidence(records []dayRecord, tokenMap map[string]string) associationEvidence {
	var ev associationEvidence
	ev.TotalRecords = len(records)
	for _, dr := range records {
		prefix := dr.Record.TokenPfx
		if prefix == "" {
			ev.NoPrefixCount++
			continue
		}
		if _, ok := tokenMap[prefix]; ok {
			ev.ResolvedCount++
		} else {
			ev.UnresolvedPrefixCount++
		}
	}
	return ev
}

const cursorSemanticsNote = "Cursor is the opaque string \"reqlog:<offset>\" " +
	"(connectors/reqlog/fake.go cursorPrefix + encodeCursor/decodeCursor) wrapping a plain " +
	"integer offset into a result set that connectors/reqlog/file_client.go's matchedSummaries " +
	"fully re-scans and re-sorts (by OccurredAt, descending) on EVERY call, before the offset is " +
	"applied. It is not a stable position against concurrent writes: a request recorded between " +
	"page 1 and page 2 of the same query shifts every later offset by one, so a record at the " +
	"old page boundary can repeat across pages or be skipped entirely - the same class of hazard " +
	"already documented for the platformusers real client's offset-based Sub2API/NewAPI " +
	"pagination (docs/handoffs/slices/XM-USERS-REAL.md). This is a fact read from source, not " +
	"derived from the sample data in this directory - it does not change from one capture run to " +
	"the next."

// sampleRecords takes up to n records, newest day first, in on-disk order
// within each day (which file_client.go itself later re-sorts by
// OccurredAt - this function does not need to replicate that, it only needs
// a deterministic, bounded sample for the redacted fixture).
func sampleRecords(records []dayRecord, n int) []dayRecord {
	if len(records) <= n {
		return records
	}
	return records[:n]
}

// redactedIndexRow is the shape of one line in index_sample.redacted.jsonl.
// Deliberately excludes ua, upstream_request_id, resp_body_id, end_note,
// preview, path and method - none of them are needed to answer the
// retention/cursor/association questions REQLOG_USERREF_APPROVAL's evidence
// door asks about (design doc §10), and preview/end_note in particular can
// carry excerpted request/response content on the real recorder (see
// reqlogformat.Record's own field comments), which this subcommand must
// never touch at all, redacted or not.
type redactedIndexRow struct {
	IDHash           string `json:"id_hash"`
	Day              string `json:"day"`
	TsMs             int64  `json:"ts_ms"`
	Source           string `json:"source"`
	Status           int    `json:"status"`
	DurMs            int64  `json:"dur_ms"`
	TtfbMs           int64  `json:"ttfb_ms,omitempty"`
	Model            string `json:"model,omitempty"`
	Stream           bool   `json:"stream,omitempty"`
	ReqSize          int    `json:"req_size,omitempty"`
	RespSize         int    `json:"resp_size,omitempty"`
	Truncated        bool   `json:"truncated,omitempty"`
	InTok            int    `json:"in_tok,omitempty"`
	OutTok           int    `json:"out_tok,omitempty"`
	CacheTok         int    `json:"cache_tok,omitempty"`
	ClientIPMasked   string `json:"client_ip_masked,omitempty"`
	TokenPrefixHash  string `json:"prefix_pseudonym,omitempty"`
	UsernameResolved bool   `json:"username_resolved"`
}

func redactIndexRow(r reqlogformat.Record, tokenMap map[string]string, salt []byte) redactedIndexRow {
	row := redactedIndexRow{
		IDHash:    "rec_" + hashWithSalt(salt, "reqlog-id", r.ID)[:16],
		TsMs:      r.TsMs,
		Source:    r.Source,
		Status:    r.Status,
		DurMs:     r.DurMs,
		TtfbMs:    r.TtfbMs,
		Model:     r.Model,
		Stream:    r.Stream,
		ReqSize:   r.ReqSize,
		RespSize:  r.RespSize,
		Truncated: r.Truncated,
		InTok:     r.InTok,
		OutTok:    r.OutTok,
		CacheTok:  r.CacheTok,
	}
	if r.ClientIP != "" {
		row.ClientIPMasked = reqlog.MaskIP(r.ClientIP)
	}
	if r.TokenPfx != "" {
		row.TokenPrefixHash = "pfx_" + hashWithSalt(salt, "reqlog-token-prefix", r.TokenPfx)[:12]
		_, row.UsernameResolved = tokenMap[r.TokenPfx]
	}
	return row
}

func marshalJSONL(rows []redactedIndexRow) ([]byte, error) {
	var b bytes.Buffer
	for _, row := range rows {
		line, err := json.Marshal(row)
		if err != nil {
			return nil, fmt.Errorf("evidence-capture: failed to marshal redacted index row: %w", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}
