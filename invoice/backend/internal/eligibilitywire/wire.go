// Package eligibilitywire loads contracts/invoice-eligibility-wire.v1.json --
// the single source of truth for the eligibility_status and reason_code values
// the backend can put on the wire -- and renders the frontend's generated enum
// module from it.
//
// XM-INV-LOT-REASON-CONTRACT. Before this package the same four enums were
// hand-copied into four places in web/src/lib/http-api.ts (lines 151, 229, 293,
// 556) and again into web/src/types.ts. When XM-INV-ELIG-AUTO-RECONCILE added
// a fourth persisted status the copies stayed behind and every closed-set check
// that read them threw, blanking the user's whole invoice page. The rule now is
// the repository rule: one fact is pinned in exactly one place.
//
// The gates that keep it honest live in the _test.go files that use this
// package. They do not hand-list what the backend emits -- they run the real
// emitters over a cartesian product of inputs, collect what actually comes out,
// and compare that against the contract in BOTH directions (an emitted value
// missing from the contract is red; a contract value no emitter can produce is
// red too). A one-directional gate only ever grows, and a gate whose expected
// set is itself hand-maintained is a fourth copy, not a gate.
package eligibilitywire

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"
)

// Contract mirrors contracts/invoice-eligibility-wire.v1.json. The *_description
// keys in that file are documentation for humans reading the contract and are
// deliberately not decoded here.
type Contract struct {
	LotPersistedStatuses  []string            `json:"lot_persisted_statuses"`
	LotSyntheticStatuses  []string            `json:"lot_synthetic_statuses"`
	LotEligibilityStatus  []string            `json:"lot_eligibility_status"`
	LotReasonCode         []string            `json:"lot_reason_code"`
	LotStatusReasonPairs  map[string][]string `json:"lot_status_reason_pairs"`
	SummaryStatus         []string            `json:"summary_status"`
	SummaryReason         []string            `json:"summary_reason"`
	SummaryReasonMaxCount int                 `json:"summary_reason_max_count"`
	ServiceUnits          []ServiceUnit       `json:"service_units"`
}

// ServiceUnit is one source-side accounting unit and how to turn a raw
// service_units integer into the number the upstream's own user sees.
//
// XM-INV-UNIT-DISPLAY. Divisor is a STRING, not a number: service_units is a
// decimal of up to 78 digits and encoding/json would hand a float64 to anything
// that decoded this as a number -- in the one place in the response whose whole
// job is not to lose a digit. It is parsed with math/big here and with BigInt in
// the browser; neither side touches a float.
//
// The conversion changes the SCALE, never the KIND: the converted number is
// still a non-cash / pre-cutover source balance, not CNY, so it renders with no
// currency sign and always beside DisplayLabel. See service_units_description in
// the contract, and accounts_ledger.go's comment on why this system refuses to
// invent a CNY exchange rate for these units.
type ServiceUnit struct {
	Code         string `json:"code"`
	Divisor      string `json:"divisor"`
	Decimals     int    `json:"decimals"`
	DisplayLabel string `json:"display_label"`
	Description  string `json:"description"`
}

// ServiceUnitCodes is the contract's unit-code vocabulary, in contract order.
func (c Contract) ServiceUnitCodes() []string {
	out := make([]string, 0, len(c.ServiceUnits))
	for _, unit := range c.ServiceUnits {
		out = append(out, unit.Code)
	}
	return out
}

// repoRoot resolves the repository root from this source file's own path rather
// than from the process working directory, so every probe reaches the same
// contract no matter which package's test is running.
func repoRoot() (string, error) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("eligibilitywire: cannot resolve this file's path")
	}
	// <root>/backend/internal/eligibilitywire/wire.go
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "..")), nil
}

// ContractPath is the on-disk path of the wire contract.
func ContractPath() (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "contracts", "invoice-eligibility-wire.v1.json"), nil
}

// GeneratedTypeScriptPath is the on-disk path of the frontend module rendered
// from the contract.
func GeneratedTypeScriptPath() (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "web", "src", "lib", "eligibility-wire.generated.ts"), nil
}

// MigrationsDir is the on-disk path of the SQL migrations, which the persisted
// status probe scans.
func MigrationsDir() (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "backend", "migrations"), nil
}

// Load reads and validates the wire contract.
func Load() (Contract, error) {
	path, err := ContractPath()
	if err != nil {
		return Contract{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return Contract{}, fmt.Errorf("eligibilitywire: read contract: %w", err)
	}
	var contract Contract
	if err := json.Unmarshal(body, &contract); err != nil {
		return Contract{}, fmt.Errorf("eligibilitywire: parse contract: %w", err)
	}
	if err := contract.validate(); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func (c Contract) validate() error {
	groups := map[string][]string{
		"lot_persisted_statuses": c.LotPersistedStatuses,
		"lot_synthetic_statuses": c.LotSyntheticStatuses,
		"lot_eligibility_status": c.LotEligibilityStatus,
		"lot_reason_code":        c.LotReasonCode,
		"summary_status":         c.SummaryStatus,
		"summary_reason":         c.SummaryReason,
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		values := groups[name]
		if len(values) == 0 {
			return fmt.Errorf("eligibilitywire: contract group %s is empty", name)
		}
		seen := map[string]bool{}
		for _, value := range values {
			if value == "" {
				return fmt.Errorf("eligibilitywire: contract group %s contains an empty value", name)
			}
			if seen[value] {
				return fmt.Errorf("eligibilitywire: contract group %s repeats %q", name, value)
			}
			seen[value] = true
		}
	}
	if c.SummaryReasonMaxCount <= 0 {
		return fmt.Errorf("eligibilitywire: summary_reason_max_count must be positive, got %d", c.SummaryReasonMaxCount)
	}
	if len(c.LotStatusReasonPairs) == 0 {
		return fmt.Errorf("eligibilitywire: lot_status_reason_pairs is empty")
	}
	return c.validateServiceUnits()
}

// validateServiceUnits checks the shape of every unit definition. Each rule
// exists because breaking it produces a WRONG NUMBER on the user's page rather
// than a crash: a zero or unparseable divisor divides by nothing, a negative or
// oversized decimals makes the rendered scale disagree with the label, and an
// empty display_label leaves a bare figure with no statement of what it counts
// -- which is the exact ambiguity this slice exists to remove.
func (c Contract) validateServiceUnits() error {
	if len(c.ServiceUnits) == 0 {
		return fmt.Errorf("eligibilitywire: service_units is empty")
	}
	seen := map[string]bool{}
	for i, unit := range c.ServiceUnits {
		if unit.Code == "" {
			return fmt.Errorf("eligibilitywire: service_units[%d] has an empty code", i)
		}
		if seen[unit.Code] {
			return fmt.Errorf("eligibilitywire: service_units repeats code %q", unit.Code)
		}
		seen[unit.Code] = true
		divisor, ok := new(big.Int).SetString(unit.Divisor, 10)
		if !ok || divisor.Sign() <= 0 {
			return fmt.Errorf(
				"eligibilitywire: service_units[%q].divisor must be a positive base-10 integer written as a string, got %q",
				unit.Code, unit.Divisor)
		}
		if unit.Decimals < 0 || unit.Decimals > 8 {
			return fmt.Errorf(
				"eligibilitywire: service_units[%q].decimals must be between 0 and 8, got %d",
				unit.Code, unit.Decimals)
		}
		if strings.TrimSpace(unit.DisplayLabel) == "" {
			return fmt.Errorf("eligibilitywire: service_units[%q].display_label is empty", unit.Code)
		}
		// The label is read by the product owner and by users, neither of whom
		// reads English. A label with no Han character at all is how an
		// English placeholder ("Sub2API balance") reaches the page.
		if !containsHan(unit.DisplayLabel) {
			return fmt.Errorf(
				"eligibilitywire: service_units[%q].display_label %q has no Chinese in it; the label is user-visible copy",
				unit.Code, unit.DisplayLabel)
		}
	}
	return nil
}

func containsHan(value string) bool {
	for _, char := range value {
		if unicode.Is(unicode.Han, char) {
			return true
		}
	}
	return false
}

// generatedHeader is prefixed to the rendered TypeScript. It names the command
// that regenerates the file so nobody hand-edits it back into a fifth copy.
const generatedHeader = `// 由 contracts/invoice-eligibility-wire.v1.json 生成，请勿手工编辑。
// 重新生成：cd backend && go test ./internal/eligibilitywire/... -update
//
// XM-INV-LOT-REASON-CONTRACT：后端会回哪些 eligibility_status / reason_code 是
// 后端的事实，前端不再手抄。Go 侧的探针测试跑真实 emitter 的笛卡尔积、把实际
// 输出与该契约双向比对，任一侧漂移都会让 go test 先于前端门禁变红。
//
// 注意这只是「提交时」的闸。运行时（后端先上线、前端 bundle 还旧）另有一层降级：
// http-api.ts 对未知但形状合法的值放行并标记 degraded，让它可见、不可选、有中文
// 兜底文案，而不是整页 throw。两层防的是不同的事，不要因为有了这个文件就把降级删掉。
`

// RenderTypeScript renders the frontend enum module for the contract.
//
// The output uses CRLF: this repository is checked out with core.autocrlf=true,
// so *.ts files sit in the working tree with CRLF endings. Writing LF here would
// make the golden comparison fail on every clean checkout, and the tempting
// "fix" (adding *.generated.ts eol=lf to .gitattributes) would leave this one
// file inconsistent with every other .ts in the tree.
func RenderTypeScript(c Contract) string {
	var b strings.Builder
	b.WriteString(generatedHeader)
	b.WriteString("\n")
	writeConst(&b, "lotEligibilityStatuses", c.LotEligibilityStatus)
	b.WriteString("\n")
	writeConst(&b, "lotReasonCodes", c.LotReasonCode)
	b.WriteString("\n")
	writeConst(&b, "summaryStatuses", c.SummaryStatus)
	b.WriteString("\n")
	writeConst(&b, "summaryReasons", c.SummaryReason)
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("export const summaryReasonMaxCount = %d;\n", c.SummaryReasonMaxCount))
	b.WriteString("\n")
	writeServiceUnits(&b, c.ServiceUnits)
	b.WriteString("\n")
	writeType(&b, "LotEligibilityStatusWire", "lotEligibilityStatuses")
	writeType(&b, "LotReasonCodeWire", "lotReasonCodes")
	writeType(&b, "SummaryStatusWire", "summaryStatuses")
	writeType(&b, "SummaryReasonWire", "summaryReasons")
	b.WriteString("export type ServiceUnitCodeWire = (typeof serviceUnitDefinitions)[number][\"code\"];\n")
	return strings.ReplaceAll(b.String(), "\n", "\r\n")
}

// writeServiceUnits renders the unit table. `divisor` stays a string in the
// generated module for the same reason it is one in the contract: the frontend
// feeds it straight to BigInt, and a TypeScript number literal here would be
// the one place a 78-digit-capable pipeline quietly became float64.
func writeServiceUnits(b *strings.Builder, units []ServiceUnit) {
	b.WriteString("// divisor 是字符串：前端用 BigInt(divisor) 精确整除，不走 Number。\n")
	b.WriteString("// 换算后的数仍是源侧非现金余额，不是人民币——渲染时不加 ¥ / $，\n")
	b.WriteString("// 且必须与 displayLabel 一起出现。\n")
	b.WriteString("export const serviceUnitDefinitions = [\n")
	for _, unit := range units {
		b.WriteString("  {\n")
		b.WriteString(fmt.Sprintf("    code: %q,\n", unit.Code))
		b.WriteString(fmt.Sprintf("    divisor: %q,\n", unit.Divisor))
		b.WriteString(fmt.Sprintf("    decimals: %d,\n", unit.Decimals))
		b.WriteString(fmt.Sprintf("    displayLabel: %q,\n", unit.DisplayLabel))
		b.WriteString("  },\n")
	}
	b.WriteString("] as const;\n")
}

func writeConst(b *strings.Builder, name string, values []string) {
	b.WriteString(fmt.Sprintf("export const %s = [\n", name))
	for _, value := range values {
		b.WriteString(fmt.Sprintf("  %q,\n", value))
	}
	b.WriteString("] as const;\n")
}

func writeType(b *strings.Builder, name, source string) {
	b.WriteString(fmt.Sprintf("export type %s = (typeof %s)[number];\n", name, source))
}

// Set turns a slice into a set for the probes' two-directional comparisons.
func Set(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

// Diff reports what `actual` has that `expected` does not, and vice versa. Both
// halves matter: "the emitter produced a value the contract does not declare"
// and "the contract declares a value no emitter can produce" are each a drift,
// and a gate that only checks the first can never shrink.
func Diff(actual, expected map[string]bool) (extra, missing []string) {
	for value := range actual {
		if !expected[value] {
			extra = append(extra, value)
		}
	}
	for value := range expected {
		if !actual[value] {
			missing = append(missing, value)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	return extra, missing
}
