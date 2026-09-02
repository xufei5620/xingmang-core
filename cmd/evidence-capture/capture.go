package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// Exit codes for the capture subcommand. exitComplianceBlocked is its own
// code (not folded into exitFailed) so an operator or a wrapper script can
// tell "your credential's account has not accepted Sub2API's compliance
// terms yet - go do that, then rerun" apart from every other failure mode
// without parsing stderr text.
const (
	exitOK                = 0
	exitFailed            = 1
	exitUsage             = 2
	exitComplianceBlocked = 3
)

const (
	secretRootEnvVar  = "XM_SECRET_ROOT"
	defaultSecretRoot = "/run/xm/secrets"
	credentialCaller  = "tool:evidence-capture"
)

type captureFlags struct {
	platform      string
	endpoint      string
	credentialRef string
	allowlist     string
	secretRoot    string
	out           string
	sampleSize    int
	nowText       string
	dryRun        bool
	environment   string
}

func parseCaptureFlags(args []string, stderr io.Writer) (captureFlags, error) {
	fs := flag.NewFlagSet("evidence-capture capture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f captureFlags
	fs.StringVar(&f.platform, "platform", "", "sub2api or newapi (required)")
	fs.StringVar(&f.endpoint, "endpoint", "", "upstream root URL, https:// (required unless -dry-run)")
	fs.StringVar(&f.credentialRef, "credential-ref", "", "secret://<scope>/<name> already registered in the platform's credential store (required unless -dry-run; never a raw token)")
	fs.StringVar(&f.allowlist, "allowlist", "", "comma-separated hostnames this run may connect to (default: the endpoint's own host)")
	fs.StringVar(&f.secretRoot, "secret-root", "", "root of the file-backed credential store (default: $XM_SECRET_ROOT or "+defaultSecretRoot+")")
	fs.StringVar(&f.out, "out", "", "evidence output root (default: "+defaultEvidenceRoot+"/<platform>)")
	fs.IntVar(&f.sampleSize, "sample-size", defaultSampleSize, fmt.Sprintf("rows requested by the users-page probe, 1..%d (this tool captures shape evidence, not a census)", maxSampleSize))
	fs.StringVar(&f.nowText, "now", "", "override capture timestamp, UTC RFC3339Nano (deterministic review/testing only)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print the fixed request plan and exit; no network access, no credential resolution, no files written")
	fs.StringVar(&f.environment, "environment", "production", "environment label recorded in the credential access audit trail")
	if err := fs.Parse(args); err != nil {
		return captureFlags{}, err
	}
	if fs.NArg() != 0 {
		return captureFlags{}, fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}
	return f, nil
}

// captureDeps are the parts of runCapture's environment a test can swap out.
// Production wiring (runMain in main.go) leaves every field at its zero
// value, which resolves to the real filesystem, clock, RNG and network.
type captureDeps struct {
	getenv        func(string) string
	now           func() time.Time
	baseTransport http.RoundTripper // httptest transport in tests; nil (= http.DefaultTransport) in production
	secretsFor    func(root, environment string) (secrets.SecretProvider, error)
	randSalt      func() ([]byte, error)
}

func (d *captureDeps) fillDefaults() {
	if d.getenv == nil {
		d.getenv = os.Getenv
	}
	if d.now == nil {
		d.now = time.Now
	}
	if d.secretsFor == nil {
		d.secretsFor = defaultSecretsProvider
	}
	if d.randSalt == nil {
		d.randSalt = newCaptureSalt
	}
}

// defaultSecretsProvider resolves credentials from the file-backed store
// only (internal/platform/secrets.FileProvider at <root>/<scope>/<name>) -
// deliberately no environment-variable fallback the way
// cmd/platform-worker/secret_chain.go keeps for legacy compatibility. This
// tool's whole premise is "a credential reference already registered in the
// platform" (team brief), i.e. already written to that file store by
// platform-api's credential registration flow; there is no legacy env-var
// registry for evidence capture to fall back to, and adding one would just
// be a second, less-audited way to hand this tool a token.
func defaultSecretsProvider(root, environment string) (secrets.SecretProvider, error) {
	file := secrets.NewFileProvider(root)
	return secrets.NewAudited(file, secrets.NewSlogRecorder(defaultLogger()), environment), nil
}

func resolveSecretRoot(flagValue string, getenv func(string) string) (string, error) {
	root := strings.TrimSpace(flagValue)
	if root == "" {
		root = strings.TrimSpace(getenv(secretRootEnvVar))
	}
	if root == "" {
		return defaultSecretRoot, nil
	}
	// Accept either the host OS's native absolute-path rule or POSIX's:
	// production always runs this on Linux (secretRootEnvVar's own default
	// is a POSIX path, "/run/xm/secrets"), but this repo's tests - and any
	// operator's local dry run of the flag parsing - may run on Windows,
	// where filepath.IsAbs alone rejects a perfectly valid "/run/xm/secrets"
	// for lack of a drive letter. Same dual check as
	// cmd/platform-worker/secret_chain.go's parseSecretRoot, for the same
	// reason.
	if !filepath.IsAbs(root) && !path.IsAbs(root) {
		return "", fmt.Errorf("--secret-root (or %s) must be an absolute path, got %q", secretRootEnvVar, root)
	}
	return root, nil
}

// runCapture implements the capture subcommand end to end. It never returns
// an error for "the evidence run partially failed" - a compliance block or
// an upstream error is reported via the exit code and the written evidence,
// not a Go error, because the whole point of this tool is to keep running
// and record what happened rather than abort at the first bad probe (see
// fetchResult's doc comment). A non-nil error here means this run could not
// even start (bad flags, bad credential, cannot write output).
func runCapture(args []string, stdout, stderr io.Writer, deps captureDeps) int {
	deps.fillDefaults()

	f, err := parseCaptureFlags(args, stderr)
	if err != nil {
		return exitUsage
	}

	steps, err := planFor(f.platform, f.sampleSize)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	if f.dryRun {
		printDryRunPlan(stdout, f.platform, steps)
		return exitOK
	}

	if strings.TrimSpace(f.endpoint) == "" {
		fmt.Fprintln(stderr, "evidence-capture: --endpoint is required (unless -dry-run)")
		return exitUsage
	}
	if strings.TrimSpace(f.credentialRef) == "" {
		fmt.Fprintln(stderr, "evidence-capture: --credential-ref is required (unless -dry-run)")
		return exitUsage
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(f.endpoint)), "https://") {
		fmt.Fprintln(stderr, "evidence-capture: --endpoint must be https:// (ADR-018 gate 1)")
		return exitUsage
	}
	endpoint, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(f.endpoint), "/"))
	if err != nil {
		fmt.Fprintf(stderr, "evidence-capture: --endpoint could not be parsed: %v\n", err)
		return exitUsage
	}
	ref, err := secrets.ParseCredentialRef(strings.TrimSpace(f.credentialRef))
	if err != nil {
		fmt.Fprintf(stderr, "evidence-capture: --credential-ref: %v\n", err)
		return exitUsage
	}

	allowlist := splitNonEmpty(f.allowlist, ",")
	if len(allowlist) == 0 {
		allowlist = []string{endpoint.Hostname()}
	}

	secretRoot, err := resolveSecretRoot(f.secretRoot, deps.getenv)
	if err != nil {
		fmt.Fprintf(stderr, "evidence-capture: %v\n", err)
		return exitUsage
	}
	provider, err := deps.secretsFor(secretRoot, f.environment)
	if err != nil {
		fmt.Fprintf(stderr, "evidence-capture: failed to set up credential store: %v\n", err)
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	purpose := fmt.Sprintf("evidence-capture: %s real-sample read-only probe for %s/%s approval evidence", f.platform, strings.ToUpper(f.platform), "REAL_APPROVAL")
	secretValue, err := provider.Resolve(secrets.WithCaller(ctx, credentialCaller), ref, purpose)
	if err != nil {
		// secrets.FileProvider's own errors carry only the ref and a path,
		// never plaintext (internal/platform/secrets/fileprovider.go) - safe
		// to print as-is.
		fmt.Fprintf(stderr, "evidence-capture: failed to resolve credential %s: %v\n", ref.String(), err)
		return exitFailed
	}
	token := secretValue.Reveal()

	var authorize func(*http.Request)
	switch f.platform {
	case PlatformSub2API:
		authorize = sub2apiAuthorize(token)
	case PlatformNewAPI:
		authorize = newapiAuthorize(token)
	}
	token = "" // the only copy this process needs was the one just handed to authorize's closure

	httpClient := newReadOnlyHTTPClient(deps.baseTransport, allowlist)
	results := runProbes(ctx, httpClient, endpoint, steps, authorize)

	salt, err := deps.randSalt()
	if err != nil {
		fmt.Fprintf(stderr, "evidence-capture: %v\n", err)
		return exitFailed
	}

	outDir := f.out
	if strings.TrimSpace(outDir) == "" {
		outDir = filepath.Join(defaultEvidenceRoot, f.platform)
	}
	outDir = filepath.Join(outDir, timestampDir(now))

	report, dataFiles, buildErr := buildCaptureEvidence(f.platform, results, salt, now, endpoint, ref, allowlist, f.environment)
	if buildErr != nil {
		fmt.Fprintf(stderr, "evidence-capture: %v\n", buildErr)
		return exitFailed
	}

	for _, df := range dataFiles {
		if strings.HasSuffix(df.Name, ".redacted.json") {
			if violations := scanForbidden(df.Data); len(violations) > 0 {
				fmt.Fprintf(stderr, "evidence-capture: refusing to write %s - failed final safety scan:\n", df.Name)
				for _, v := range violations {
					fmt.Fprintln(stderr, "  - "+v)
				}
				return exitFailed
			}
		}
	}

	sums := computeSums(dataFiles)
	readme := renderCaptureReadme(report, dataFiles, sums)
	allFiles := append(append([]evidenceFile{}, dataFiles...),
		evidenceFile{Name: "SHA256SUMS", Data: sumsFileContent(dataFiles, sums)},
		evidenceFile{Name: "README.md", Data: readme},
	)

	if err := writeEvidenceDir(outDir, allFiles); err != nil {
		fmt.Fprintf(stderr, "evidence-capture: %v\n", err)
		return exitFailed
	}

	fmt.Fprintf(stdout, "evidence written to %s\n", outDir)
	for _, r := range results {
		status := "ok"
		if r.Err != nil {
			status = "FAILED: " + r.Err.Error()
		}
		fmt.Fprintf(stdout, "  %-20s %s (%s)\n", r.Step.Purpose, r.Step.requestLine(), status)
	}

	for _, r := range results {
		if r.ComplianceBlocked {
			return exitComplianceBlocked
		}
	}
	for _, r := range results {
		if r.Err != nil {
			return exitFailed
		}
	}
	return exitOK
}

func printDryRunPlan(stdout io.Writer, platform string, steps []ProbeStep) {
	fmt.Fprintf(stdout, "evidence-capture: dry run for platform %q - no network access, no credential resolution, no files written\n", platform)
	fmt.Fprintln(stdout, "request plan (in order):")
	for _, s := range steps {
		fmt.Fprintf(stdout, "  [%s] %s\n", s.Purpose, s.requestLine())
	}
	fmt.Fprintln(stdout, "redaction: emails masked via platformusers.MaskEmail, user ids and names replaced")
	fmt.Fprintln(stdout, "with a one-way per-run pseudonym, balances/quota reduced to shape only (digits")
	fmt.Fprintln(stdout, "zeroed), any field not explicitly allowlisted for this platform is dropped and")
	fmt.Fprintln(stdout, "only its field name is recorded. See README.md in a real run's output directory")
	fmt.Fprintln(stdout, "for the exact allowlist used.")
}

// splitNonEmpty splits raw on sep and drops empty/whitespace-only pieces.
func splitNonEmpty(raw, sep string) []string {
	var out []string
	for _, part := range strings.Split(raw, sep) {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseUTCRFC3339Nano(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || !strings.HasSuffix(value, "Z") {
		return time.Time{}, false
	}
	return parsed, true
}

// captureReport is the fully-assembled, already-redacted result of one
// capture run, independent of how it gets rendered (README.md prose vs.
// stdout summary vs. a future JSON output mode).
type captureReport struct {
	Platform      string
	Endpoint      string // host only, never a full URL with query
	CredentialRef string
	Allowlist     []string
	Environment   string
	CapturedAt    time.Time
	Version       string
	QuotaPerUnit  int64 // NewAPI only; 0 means "not applicable/not observed"
	Results       []fetchResult
	Kept          []string
	Dropped       []string
	SaltHex       string
}

func buildCaptureEvidence(platform string, results []fetchResult, salt []byte, now time.Time, endpoint *url.URL, ref secrets.CredentialRef, allowlist []string, environment string) (captureReport, []evidenceFile, error) {
	report := captureReport{
		Platform:      platform,
		Endpoint:      endpoint.Hostname(),
		CredentialRef: ref.String(),
		Allowlist:     allowlist,
		Environment:   environment,
		CapturedAt:    now,
		Results:       results,
		SaltHex:       fmt.Sprintf("%x", salt),
	}

	var files []evidenceFile
	files = append(files, evidenceFile{Name: "request-log.json", Data: mustMarshalIndent(buildRequestLog(results))})

	byPurpose := map[string]fetchResult{}
	for _, r := range results {
		byPurpose[r.Step.Purpose] = r
	}

	switch platform {
	case PlatformSub2API:
		if r, ok := byPurpose["version"]; ok && r.Err == nil {
			v, err := parseSub2APIVersion(r.Body)
			if err != nil {
				return report, nil, fmt.Errorf("version probe returned an unexpected shape: %w", err)
			}
			report.Version = v
			files = append(files, evidenceFile{Name: "version.json", Data: mustMarshalIndent(map[string]any{
				"platform": platform, "route": sub2apiRouteVersion, "version": v, "observed_at": r.ObservedAt,
			})})
		}
		if r, ok := byPurpose["users_page"]; ok && r.Err == nil {
			envelope, detail, kept, dropped, err := redactSub2APIUsersPage(r.Body, salt)
			if err != nil {
				return report, nil, fmt.Errorf("users_page probe returned an unexpected shape: %w", err)
			}
			report.Kept, report.Dropped = kept, dropped
			files = append(files, evidenceFile{Name: "users_page.redacted.json", Data: prettyJSON(envelope)})
			if detail != nil {
				files = append(files, evidenceFile{Name: "user_detail.redacted.json", Data: prettyJSON(detail)})
			}
		}
	case PlatformNewAPI:
		if r, ok := byPurpose["version_and_quota"]; ok && r.Err == nil {
			status, err := parseNewAPIStatus(r.Body)
			if err != nil {
				return report, nil, fmt.Errorf("version_and_quota probe returned an unexpected shape: %w", err)
			}
			report.Version = status.Version
			report.QuotaPerUnit = status.QuotaPerUnit
			files = append(files, evidenceFile{Name: "version.json", Data: mustMarshalIndent(map[string]any{
				"platform": platform, "route": newapiRouteStatus, "version": status.Version,
				"quota_per_unit": status.QuotaPerUnit, "observed_at": r.ObservedAt,
			})})
		}
		if r, ok := byPurpose["users_page"]; ok && r.Err == nil {
			envelope, detail, kept, dropped, err := redactNewAPIUsersPage(r.Body, salt)
			if err != nil {
				return report, nil, fmt.Errorf("users_page probe returned an unexpected shape: %w", err)
			}
			report.Kept, report.Dropped = kept, dropped
			files = append(files, evidenceFile{Name: "users_page.redacted.json", Data: prettyJSON(envelope)})
			if detail != nil {
				files = append(files, evidenceFile{Name: "user_detail.redacted.json", Data: prettyJSON(detail)})
			}
		}
	}

	return report, files, nil
}

type requestLogEntry struct {
	Purpose           string `json:"purpose"`
	Method            string `json:"method"`
	Path              string `json:"path"`
	Query             string `json:"query,omitempty"`
	Status            int    `json:"status,omitempty"`
	ObservedAt        string `json:"observed_at,omitempty"`
	LatencyMS         int64  `json:"latency_ms"`
	Error             string `json:"error,omitempty"`
	ComplianceBlocked bool   `json:"compliance_blocked,omitempty"`
}

func buildRequestLog(results []fetchResult) []requestLogEntry {
	out := make([]requestLogEntry, 0, len(results))
	for _, r := range results {
		e := requestLogEntry{
			Purpose:           r.Step.Purpose,
			Method:            r.Step.Method,
			Path:              r.Step.Path,
			Status:            r.Status,
			LatencyMS:         r.LatencyMS,
			ComplianceBlocked: r.ComplianceBlocked,
		}
		if len(r.Step.Query) > 0 {
			e.Query = r.Step.Query.Encode()
		}
		if !r.ObservedAt.IsZero() {
			e.ObservedAt = r.ObservedAt.Format(time.RFC3339Nano)
		}
		if r.Err != nil {
			e.Error = r.Err.Error()
		}
		out = append(out, e)
	}
	return out
}

func mustMarshalIndent(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Every call site passes a value built entirely from this
		// process's own strings/ints/times - a marshal failure here would
		// be a programming error, not a runtime condition to recover from.
		panic(fmt.Sprintf("evidence-capture: internal error marshaling evidence JSON: %v", err))
	}
	return append(b, '\n')
}

func prettyJSON(raw json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		// raw came from this process's own json.Marshal a few lines up the
		// call stack in redact*Page - it is always valid JSON.
		panic(fmt.Sprintf("evidence-capture: internal error indenting evidence JSON: %v", err))
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}
