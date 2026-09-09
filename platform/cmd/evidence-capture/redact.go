// Command evidence-capture produces the redacted, hash-verifiable evidence
// packets that SUB2_REAL_APPROVAL / NEWAPI_REAL_APPROVAL / REQLOG_USERREF_APPROVAL
// require before any real Sub2API/NewAPI GetUser reader or reqlog stable
// UserRef can be implemented (see
// docs/superpowers/specs/2026-08-28-platform-user-read-v2-design.md §0/§10
// and docs/superpowers/plans/2026-08-28-platform-user-read-v2.md Task 4/5/8).
//
// It never implements those readers itself and never flips any gate: it only
// captures, redacts and hashes what a human reviewer needs to approve or
// reject real access, and reads local reqlog files that a human already
// copied off the server. See docs/runbooks/USERS-REAL-APPROVAL.md.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/xufei5620/xingmang-platform/connectors/platformusers"
)

// captureSaltBytes is the size of the per-run salt used to derive pseudonyms
// for real user ids and names. 16 bytes (128 bits) is far more than needed
// against accidental collision; it is not a cryptographic secret (see
// newCaptureSalt), so there is no reason to make it larger.
const captureSaltBytes = 16

// newCaptureSalt returns fresh random bytes used to derive stable-within-this-run,
// one-way pseudonyms for user ids and names that appear in captured evidence.
//
// Why a fresh salt per run instead of no salt at all, and why this is
// redaction rather than a security boundary: the real ids this tool ever
// sees are small, low-entropy values (Sub2API's admin user list uses ids
// shaped like "u_10241"; NewAPI uses bare small integers - both confirmed
// against the already-verified real field shapes in
// connectors/platformusers/upstream.go). A plain sha256(id) with no salt is
// an encoding, not a secret: a reviewer could brute force it in seconds by
// hashing every id in the plausible range. Salting only stops a *casual*
// reader of one evidence directory from recognizing "that's the id I saw in
// the admin console yesterday" while still letting them confirm that the
// "one user detail" sample and the "users page" sample name the same user
// (same salt, same run, same hash). The salt itself is written in clear in
// that run's README.md next to the samples it covers - anyone who is meant
// to review the evidence already has both. Do not reuse this function or its
// output for anything that needs to resist a motivated adversary.
func newCaptureSalt() ([]byte, error) {
	b := make([]byte, captureSaltBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("evidence-capture: failed to generate capture salt: %w", err)
	}
	return b, nil
}

// hashWithSalt is the shared one-way derivation behind hashUserID/hashName.
// domain is a fixed literal ("id"/"name", never caller data) that keeps the
// two pseudonym spaces disjoint: an id and a name that happen to be the same
// raw string must not hash to the same output, or a reviewer could wrongly
// conclude a name field and an id field refer to "the same" underlying
// value.
func hashWithSalt(salt []byte, domain, raw string) string {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte{0})
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))
}

// hashUserID replaces a real upstream user id with a stable, one-way,
// per-run pseudonym prefixed "u_" so it is visually distinct from the
// platform's own canonical "u-<hex>" routing segment
// (connectors/platformusers/userref.go EncodeUserIDSegment) - that encoding
// is reversible by design (it exists for routing, not redaction) and must
// never be used to stand in for this hash.
func hashUserID(salt []byte, raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return "u_" + hashWithSalt(salt, "id", raw)[:16]
}

// hashName replaces a real display name / username with a stable per-run
// pseudonym.
func hashName(salt []byte, raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return "name_" + hashWithSalt(salt, "name", raw)[:12]
}

// scrubDigits keeps the *shape* of a numeric literal - sign, digit count,
// decimal point position - but zeroes every digit, so a redacted evidence
// file can still show "this field is an 8-scale decimal string" or "this is
// a 6-digit integer quota" without a real account's actual balance ending up
// in a file committed to git history forever. Non-digit characters pass
// through unchanged. Safe to use inside a JSON *string* value (redactAmountField's
// quoted-string branch); NOT safe to emit directly as a bare JSON number
// token - see scrubBareJSONNumber.
func scrubDigits(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteByte('0')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// scrubBareJSONNumber is scrubDigits for a value that must remain a valid,
// bare (unquoted) JSON number token. JSON's number grammar forbids a
// leading zero in the integer part unless the integer part is exactly "0"
// (RFC 8259 §6) - naively zeroing every digit of a multi-digit number like
// "5000000" produces "0000000", which Go's encoding/json correctly refuses
// to embed as a RawMessage. This keeps every digit's *position* zeroed
// (so the digit count still shows through) except the leading digit of the
// integer part, which becomes '1' instead of '0' when there is more than
// one digit - the smallest change that keeps the token valid JSON.
func scrubBareJSONNumber(raw string) string {
	scrubbed := scrubDigits(raw)
	sign := ""
	rest := scrubbed
	if strings.HasPrefix(rest, "-") {
		sign, rest = "-", rest[1:]
	}
	if len(rest) > 1 && rest[0] == '0' {
		rest = "1" + rest[1:]
	}
	return sign + rest
}

// rawJSONScalarAsString extracts the literal text of a JSON scalar (string
// or bare number/bool) without caring which it is - both Sub2API and NewAPI
// send ids as either a quoted string or a bare number depending on version
// (see connectors/platformusers/amount.go rawAmount.UnmarshalJSON, which
// this mirrors for the same reason). Returns ok=false for null, empty, or
// non-scalar (object/array) values.
func rawJSONScalarAsString(raw json.RawMessage) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", false
	}
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", false
		}
		return s, true
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	return trimmed, true
}

// FieldRedactor decides what a redacted evidence file gets to keep for one
// upstream JSON field. raw is the exact bytes of that field's JSON value.
// Returning ok=false drops the field entirely.
type FieldRedactor func(salt []byte, raw json.RawMessage) (out json.RawMessage, ok bool)

// ItemPolicy is an explicit allowlist of upstream field names this tool is
// willing to let into a redacted evidence file, and how each one is
// transformed. A field on the wire that is NOT a key in this map is dropped
// unconditionally by redactItem - it never reaches a FieldRedactor, never
// gets a chance to be included by a bug in a rule, and never appears in any
// file this tool writes. This is the single mechanism that keeps an
// unexpected upstream field (a future admin API version adds a phone
// number, a full key, anything) safe by default: it has to be named here on
// purpose before it can appear anywhere downstream (PROJECT-CONSTITUTION.md
// article 7: "拦不住就拒绝，不放行" - when in doubt, refuse, never pass
// through).
type ItemPolicy map[string]FieldRedactor

// redactItem applies policy to one upstream JSON object (a single user
// record). kept/dropped are sorted field-name lists (never values) so the
// evidence README can report "N additional fields were observed on the wire
// and intentionally not captured" without their values ever reaching this
// process's output.
func redactItem(raw json.RawMessage, policy ItemPolicy, salt []byte) (redacted json.RawMessage, kept, dropped []string, err error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, nil, nil, fmt.Errorf("evidence-capture: item is not a JSON object: %w", err)
	}
	out := make(map[string]json.RawMessage, len(obj))
	for key, val := range obj {
		rule, ok := policy[key]
		if !ok {
			dropped = append(dropped, key)
			continue
		}
		red, include := rule(salt, val)
		if !include {
			dropped = append(dropped, key)
			continue
		}
		out[key] = red
		kept = append(kept, key)
	}
	sort.Strings(kept)
	sort.Strings(dropped)
	redactedBytes, err := json.Marshal(out)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("evidence-capture: failed to re-marshal redacted item: %w", err)
	}
	return redactedBytes, kept, dropped, nil
}

// keepRaw passes a field through unchanged. Only used for fields whose
// *value space* is already known and small enough to not be sensitive on
// its own (status enums, null-or-timestamp soft-delete markers) - never for
// free-text or identifier fields.
func keepRaw() FieldRedactor {
	return func(_ []byte, raw json.RawMessage) (json.RawMessage, bool) {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "" {
			return nil, false
		}
		return raw, true
	}
}

// redactEmailField masks an email field using the platform's own, already
// reviewed MaskEmail (connectors/platformusers/redact.go) - this tool does
// not invent a second masking policy for the same data.
func redactEmailField() FieldRedactor {
	return func(_ []byte, raw json.RawMessage) (json.RawMessage, bool) {
		s, ok := rawJSONScalarAsString(raw)
		if !ok {
			return nil, false
		}
		masked := platformusers.MaskEmail(s)
		b, err := json.Marshal(masked)
		if err != nil {
			return nil, false
		}
		return b, true
	}
}

// redactIDField replaces a real user id with hashUserID's stable pseudonym.
func redactIDField() FieldRedactor {
	return func(salt []byte, raw json.RawMessage) (json.RawMessage, bool) {
		s, ok := rawJSONScalarAsString(raw)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, false
		}
		b, err := json.Marshal(hashUserID(salt, s))
		if err != nil {
			return nil, false
		}
		return b, true
	}
}

// redactNameField replaces a display name / username with hashName's stable
// pseudonym.
func redactNameField() FieldRedactor {
	return func(salt []byte, raw json.RawMessage) (json.RawMessage, bool) {
		s, ok := rawJSONScalarAsString(raw)
		if !ok {
			return nil, false
		}
		if strings.TrimSpace(s) == "" {
			return json.RawMessage(`""`), true
		}
		b, err := json.Marshal(hashName(salt, s))
		if err != nil {
			return nil, false
		}
		return b, true
	}
}

// redactAmountField scrubs a balance/quota value to shape-only (see
// scrubDigits) while preserving whether the upstream sent it as a JSON
// string or a bare JSON number - that distinction is itself evidence a
// future real parser needs (Sub2API sends decimal strings, NewAPI sends
// integer quota; see docs/superpowers/plans/2026-08-28-platform-user-read-v2.md
// Task 4/5). null/absent is kept as-is: "this account has no recorded
// balance field" is itself a fact worth preserving, not a value to scrub.
func redactAmountField() FieldRedactor {
	return func(_ []byte, raw json.RawMessage) (json.RawMessage, bool) {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "" {
			return nil, false
		}
		if trimmed == "null" {
			return raw, true
		}
		if strings.HasPrefix(trimmed, `"`) {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, false
			}
			b, err := json.Marshal(scrubDigits(s))
			if err != nil {
				return nil, false
			}
			return b, true
		}
		scrubbed := scrubBareJSONNumber(trimmed)
		if scrubbed == "" {
			scrubbed = "0"
		}
		return json.RawMessage(scrubbed), true
	}
}

// emailLikePattern flags an unmasked email local-part immediately adjacent
// to "@". MaskEmail's own output always has a literal '*' (part of
// platformusers.MaskedSegment, "***") immediately before "@" - '*' is not in
// this pattern's local-part character class, so a correctly masked email
// never matches here. That is not a coincidence this file relies on
// silently: TestScanForbiddenAllowsMaskedEmail pins it down.
var emailLikePattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// forbiddenSubstrings are case-insensitive keywords that must never appear
// in a redacted evidence file, matched against the fully redacted JSON text
// (not just field names) - see scanForbidden.
var forbiddenSubstrings = []string{
	"secret", "password", "passwd", "credential", "authorization",
	"bearer ", "api_key", "apikey", "private_key", "-----begin",
	"phone", "mobile", "bank", "iban", "tax_id", "taxid", "address",
	"full_key", "token_hash",
}

// scanForbidden is the last gate before this tool writes a redacted file to
// disk. It does not trust the field-by-field allowlist in ItemPolicy to be
// perfect - it re-scans the fully redacted, re-marshaled JSON bytes for
// anything that looks like an unmasked email or matches a denylisted
// keyword, and callers must refuse the write if it returns any violation.
// This mirrors design doc §8's requirement that "只做 Go struct 反射不算
// 通过" - struct-level redaction alone is not sufficient, the final JSON
// text itself must be scanned too
// (docs/superpowers/specs/2026-08-28-platform-user-read-v2-design.md).
// redactItemsPage applies redactItem to every element of a JSON array of
// upstream user records, using the same salt for all of them (so a repeated
// id within one page - or the same id appearing in the page and later
// re-derived as "the detail" - hashes identically). It also extracts the
// first successfully redacted item as a standalone "detail" record, per
// planFor's documented "detail comes from the page" design. kept/dropped
// are the union of field names seen across every item, for the evidence
// README's field-shape table.
func redactItemsPage(items []json.RawMessage, policy ItemPolicy, salt []byte) (redactedItems []json.RawMessage, kept, dropped []string, detail json.RawMessage, err error) {
	keptSet := map[string]bool{}
	droppedSet := map[string]bool{}
	for i, raw := range items {
		red, k, d, itemErr := redactItem(raw, policy, salt)
		if itemErr != nil {
			return nil, nil, nil, nil, fmt.Errorf("evidence-capture: item %d: %w", i, itemErr)
		}
		redactedItems = append(redactedItems, red)
		for _, key := range k {
			keptSet[key] = true
		}
		for _, key := range d {
			droppedSet[key] = true
		}
		if detail == nil {
			detail = red
		}
	}
	return redactedItems, sortedKeys(keptSet), sortedKeys(droppedSet), detail, nil
}

func scanForbidden(jsonBytes []byte) []string {
	var violations []string
	text := string(jsonBytes)
	if emailLikePattern.MatchString(text) {
		violations = append(violations, "output contains an unmasked email-shaped string")
	}
	lower := strings.ToLower(text)
	for _, word := range forbiddenSubstrings {
		if strings.Contains(lower, word) {
			violations = append(violations, "output contains forbidden keyword: "+word)
		}
	}
	return violations
}
