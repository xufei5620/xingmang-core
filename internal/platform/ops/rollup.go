package ops

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"
)

const (
	secondsPerDay     int64 = 24 * 60 * 60
	microsPerCoverage int64 = 1_000_000
	maxNumericDigits        = 39 // matches numeric(39,0) in the daily schema
)

// RawRollupSample is the immutable input consumed by the pure accumulator.
// PolicyVersion and ExpectedIntervalSeconds are copied from the raw row at
// write time; they are never guessed from current configuration.
type RawRollupSample struct {
	ID                      int64
	MetricKey               string
	Source                  string
	Environment             string
	ObservedAt              *time.Time
	SyncedAt                time.Time
	Status                  SyncStatus
	IsPartial               bool
	Watermark               string
	LastErrorCode           string
	Value                   map[string]any
	PolicyVersion           int16
	ExpectedIntervalSeconds *int32
}

// DailyAccumulator is a mergeable, deterministic representation of one
// (environment, metric, source, UTC day, policy version) bucket.
//
// Numeric fields use big.Int until persistence so values above 2^53 remain
// exact.  The SQL schema bounds them to numeric(39,0); the accumulator rejects
// larger values before a write can truncate them.
type DailyAccumulator struct {
	Environment string
	MetricKey   string
	Source      string
	BucketDay   time.Time

	PolicyVersion  int16
	PolicyHash     string
	ValueKind      ValueKind
	PrimaryKind    PrimaryKind
	SumMode        string
	Unit           string
	Scale          int64
	BucketTimezone string

	Currency    string
	CurrencySet []string

	FirstNumeric *big.Int
	LastNumeric  *big.Int
	MinNumeric   *big.Int
	MaxNumeric   *big.Int
	SumNumeric   *big.Int
	NumericCount int64

	SampleCount           int64
	FullSuccessCount      int64
	PartialSuccessCount   int64
	FailedCount           int64
	ExpectedSlotCount     *int64
	CoveredSlotCount      *int64
	DuplicateCount        int64
	CoveragePPM           *int32
	CoverageUnknownReason string
	NumericUnknownReason  string
	ErrorCounts           map[string]int64

	FirstFullValue   map[string]any
	LastFullValue    map[string]any
	LastPartialValue map[string]any

	FirstFullSampleID   int64
	LastFullSampleID    int64
	LastPartialSampleID int64
	FirstSyncedAt       *time.Time
	LastSyncedAt        *time.Time
	FirstObservedAt     *time.Time
	LastObservedAt      *time.Time
	FirstWatermark      string
	LastWatermark       string

	MinSampleID int64
	MaxSampleID int64
	// AggregatedAt is populated by the persistence layer when a bucket is
	// committed.  The pure accumulator leaves it zero so equal inputs produce
	// equal results and tests never depend on wall-clock timing.
	AggregatedAt time.Time

	coverageInterval    *int32
	coverageSlots       map[int64]struct{}
	seenSampleIDs       map[int64]struct{}
	lastPartialSyncedAt *time.Time
}

// NewDailyAccumulator creates a bucket from one raw sample.
func NewDailyAccumulator(policy RollupPolicy, sample RawRollupSample) (DailyAccumulator, error) {
	if err := validateAccumulatorPolicy(policy); err != nil {
		return DailyAccumulator{}, err
	}
	acc := DailyAccumulator{}
	if err := acc.mergeSample(policy, sample, true); err != nil {
		return DailyAccumulator{}, err
	}
	if err := acc.Validate(); err != nil {
		return DailyAccumulator{}, err
	}
	return acc, nil
}

// Merge appends strictly increasing raw IDs to a bucket.  The input contract
// mirrors the rollup scan's ORDER BY id.  A caller doing a late/low-ID merge
// must rebuild the bucket from the sorted raw reference rather than silently
// treating an out-of-order sample as a new frontier.
func (a DailyAccumulator) Merge(policy RollupPolicy, samples []RawRollupSample) (DailyAccumulator, error) {
	if err := validateAccumulatorPolicy(policy); err != nil {
		return DailyAccumulator{}, err
	}
	if a.PolicyVersion != 0 && (a.PolicyVersion != policy.Version || a.MetricKey != policy.MetricKey || (a.PolicyHash != "" && policy.PolicyHash != "" && a.PolicyHash != policy.PolicyHash)) {
		return DailyAccumulator{}, errors.New("policy does not match accumulator")
	}
	out := cloneAccumulator(a)
	if out.ErrorCounts == nil {
		out.ErrorCounts = make(map[string]int64)
	}
	if out.seenSampleIDs == nil {
		out.seenSampleIDs = make(map[int64]struct{})
		if out.MinSampleID > 0 {
			// The persisted shape only carries min/max.  Treating every ID in that
			// range as seen would reject legitimate sparse IDs, so only the current
			// max is used for monotonicity and duplicate protection below.
			out.seenSampleIDs[out.MaxSampleID] = struct{}{}
		}
	}
	for _, sample := range samples {
		if err := out.mergeSample(policy, sample, false); err != nil {
			return DailyAccumulator{}, err
		}
	}
	if err := out.Validate(); err != nil {
		return DailyAccumulator{}, err
	}
	return out, nil
}

// Validate checks the invariants required before persisting a daily row.  It
// is deliberately pure and does not rely on PostgreSQL helper routines; the
// database still enforces shape constraints while this method checks semantic
// relationships such as error-count parity and sorted currency sets.
func (a DailyAccumulator) Validate() error {
	if a.Environment == "" || a.MetricKey == "" || a.Source == "" || a.BucketDay.IsZero() {
		return errors.New("daily accumulator identity is incomplete")
	}
	if a.BucketTimezone != "UTC" || a.BucketDay.Location() != time.UTC || !a.BucketDay.Equal(utcBucketDay(a.BucketDay)) {
		return errors.New("daily accumulator bucket is not UTC")
	}
	if a.PolicyVersion <= 0 || a.Scale <= 0 {
		return errors.New("daily accumulator policy version/scale is invalid")
	}
	if a.PolicyHash != "" && (len(a.PolicyHash) != 64 || strings.ToLower(a.PolicyHash) != a.PolicyHash) {
		return errors.New("daily accumulator policy hash is not lowercase hex")
	}
	if a.SampleCount < 0 || a.FullSuccessCount < 0 || a.PartialSuccessCount < 0 || a.FailedCount < 0 || a.NumericCount < 0 || a.DuplicateCount < 0 {
		return errors.New("daily accumulator counts must be non-negative")
	}
	if a.SampleCount != a.FullSuccessCount+a.PartialSuccessCount+a.FailedCount {
		return errors.New("daily accumulator quality counts do not sum to sample_count")
	}
	if a.NumericCount > a.FullSuccessCount {
		return errors.New("numeric_count exceeds full_success_count")
	}
	if a.MinSampleID <= 0 || a.MaxSampleID < a.MinSampleID {
		return errors.New("daily accumulator sample id range is invalid")
	}
	if (a.SumMode == "forbidden" && a.SumNumeric != nil) || (a.SumMode != "forbidden" && a.SumMode != "additive") {
		return errors.New("daily accumulator sum mode invariant failed")
	}
	if a.MinNumeric != nil && a.MaxNumeric != nil && a.MinNumeric.Cmp(a.MaxNumeric) > 0 {
		return errors.New("daily accumulator min exceeds max")
	}
	if a.PrimaryKind == PrimaryCount {
		for _, numeric := range []*big.Int{a.FirstNumeric, a.LastNumeric, a.MinNumeric, a.MaxNumeric, a.SumNumeric} {
			if numeric != nil && numeric.Sign() < 0 {
				return errors.New("count accumulator contains a negative value")
			}
		}
	}
	if a.PrimaryKind != PrimaryMoneyMinor && len(a.CurrencySet) > 0 {
		return errors.New("non-money accumulator must not contain currencies")
	}
	if len(a.CurrencySet) > 0 {
		for i, currency := range a.CurrencySet {
			if currency == "" || (i > 0 && a.CurrencySet[i-1] >= currency) {
				return errors.New("currency_set must be sorted and unique")
			}
		}
	}
	if a.PrimaryKind == PrimaryMoneyMinor {
		switch len(a.CurrencySet) {
		case 0:
			if a.Currency != "" || a.FirstNumeric != nil || a.LastNumeric != nil || a.MinNumeric != nil || a.MaxNumeric != nil || a.SumNumeric != nil {
				return errors.New("money accumulator has numeric values without currency")
			}
		case 1:
			if a.Currency != a.CurrencySet[0] {
				return errors.New("money accumulator currency does not match currency_set")
			}
		default:
			if a.Currency != "" || a.FirstNumeric != nil || a.LastNumeric != nil || a.MinNumeric != nil || a.MaxNumeric != nil || a.SumNumeric != nil {
				return errors.New("mixed-currency accumulator must omit numeric values")
			}
		}
	}
	if a.ExpectedSlotCount == nil || a.CoveredSlotCount == nil || a.CoveragePPM == nil {
		if a.CoverageUnknownReason == "" {
			return errors.New("unknown coverage requires a reason")
		}
	} else if *a.ExpectedSlotCount <= 0 || *a.CoveredSlotCount < 0 || *a.CoveredSlotCount > *a.ExpectedSlotCount || *a.CoveragePPM < 0 || *a.CoveragePPM > 1_000_000 {
		return errors.New("coverage values are invalid")
	}
	if a.LastPartialSampleID == 0 && a.LastPartialValue != nil {
		return errors.New("last partial value is missing sample id")
	}
	if a.FirstFullSampleID == 0 && (a.FirstFullValue != nil || a.FirstSyncedAt != nil) {
		return errors.New("first full representative is incomplete")
	}
	if a.LastFullSampleID == 0 && (a.LastFullValue != nil || a.LastSyncedAt != nil) {
		return errors.New("last full representative is incomplete")
	}
	var errorTotal int64
	for code, count := range a.ErrorCounts {
		if code == "" || count < 0 {
			return errors.New("error_counts contains an invalid entry")
		}
		if count > math.MaxInt64-errorTotal {
			return errors.New("error_counts total overflows int64")
		}
		errorTotal += count
	}
	if errorTotal != a.FailedCount {
		return errors.New("error_counts total does not equal failed_count")
	}
	return nil
}

func validateAccumulatorPolicy(policy RollupPolicy) error {
	if policy.Version <= 0 || policy.MetricKey == "" {
		return errors.New("invalid rollup policy identity")
	}
	if _, gated := excludedRollupKeySet()[policy.MetricKey]; gated {
		return fmt.Errorf("%w: %s", ErrInvoiceRollupGated, policy.MetricKey)
	}
	if !ValidMetricKey(policy.MetricKey) {
		return fmt.Errorf("invalid rollup policy metric key %q", policy.MetricKey)
	}
	if policy.BucketTimezone != "UTC" {
		return fmt.Errorf("rollup bucket timezone must be UTC")
	}
	if policy.Scale <= 0 {
		return errors.New("rollup policy scale must be positive")
	}
	switch policy.ValueKind {
	case ValueGauge, ValueDailySnapshot, ValueAdditiveDelta, ValueDocumentStatus:
	default:
		return fmt.Errorf("unknown rollup value kind %q", policy.ValueKind)
	}
	switch policy.PrimaryKind {
	case PrimaryCount, PrimaryMoneyMinor, PrimaryFixedPoint, PrimaryNone:
	default:
		return fmt.Errorf("unknown rollup primary kind %q", policy.PrimaryKind)
	}
	if policy.PrimaryKind == PrimaryNone && policy.PrimaryJSONPointer != "" {
		return errors.New("primary none policy must have empty pointer")
	}
	if policy.PrimaryKind != PrimaryNone && policy.PrimaryJSONPointer == "" {
		return errors.New("primary policy requires a pointer")
	}
	if policy.PrimaryKind == PrimaryMoneyMinor && policy.CurrencyJSONPointer == "" {
		return errors.New("money policy requires a currency pointer")
	}
	if policy.PrimaryKind != PrimaryMoneyMinor && policy.CurrencyJSONPointer != "" {
		return errors.New("non-money policy must not have a currency pointer")
	}
	if policy.SumMode != "forbidden" && policy.SumMode != "additive" {
		return fmt.Errorf("unknown rollup sum mode %q", policy.SumMode)
	}
	if policy.ValueKind != ValueAdditiveDelta && policy.SumMode != "forbidden" {
		return errors.New("snapshot sum is forbidden")
	}
	if policy.ExpectedIntervalSeconds != nil && *policy.ExpectedIntervalSeconds <= 0 {
		return errors.New("policy expected interval must be positive")
	}
	return nil
}

func (a *DailyAccumulator) mergeSample(policy RollupPolicy, sample RawRollupSample, first bool) error {
	if sample.ID <= 0 {
		return fmt.Errorf("sample id %d must be positive", sample.ID)
	}
	if sample.MetricKey != policy.MetricKey {
		return fmt.Errorf("sample metric %q does not match policy %q", sample.MetricKey, policy.MetricKey)
	}
	if sample.Source == "" || sample.Environment == "" {
		return errors.New("sample source and environment are required")
	}
	if sample.SyncedAt.IsZero() {
		return errors.New("sample synced_at is required")
	}
	if sample.PolicyVersion != policy.Version {
		return fmt.Errorf("sample policy version %d does not match %d", sample.PolicyVersion, policy.Version)
	}
	if sample.Status != SyncOK && sample.Status != SyncFailed {
		return fmt.Errorf("sample status %q is invalid", sample.Status)
	}
	if (sample.Status == SyncFailed) != (strings.TrimSpace(sample.LastErrorCode) != "") {
		return fmt.Errorf("sample status/error code are inconsistent")
	}
	if sample.Status == SyncOK && sample.IsPartial && !policy.PartialValueAllowed {
		return errors.New("partial sample is not allowed by policy")
	}

	synced := sample.SyncedAt.UTC()
	day := utcBucketDay(synced)
	if first {
		a.Environment, a.MetricKey, a.Source = sample.Environment, sample.MetricKey, sample.Source
		a.BucketDay = day
		a.PolicyVersion = policy.Version
		a.PolicyHash = policy.PolicyHash
		a.ValueKind, a.PrimaryKind = policy.ValueKind, policy.PrimaryKind
		a.SumMode, a.Unit, a.Scale, a.BucketTimezone = policy.SumMode, policy.Unit, policy.Scale, policy.BucketTimezone
		a.MinSampleID, a.MaxSampleID = sample.ID, sample.ID
		a.ErrorCounts = make(map[string]int64)
		a.coverageSlots = make(map[int64]struct{})
		a.seenSampleIDs = make(map[int64]struct{})
	} else {
		if a.Environment != sample.Environment || a.MetricKey != sample.MetricKey || a.Source != sample.Source {
			return errors.New("sample stream does not match accumulator")
		}
		if !a.BucketDay.Equal(day) {
			return fmt.Errorf("sample UTC day %s does not match bucket %s", day.Format("2006-01-02"), a.BucketDay.Format("2006-01-02"))
		}
		if a.PolicyVersion != policy.Version {
			return errors.New("sample policy version does not match accumulator")
		}
		if sample.ID <= a.MaxSampleID {
			return fmt.Errorf("sample id %d is not strictly greater than accumulator max %d", sample.ID, a.MaxSampleID)
		}
		a.MaxSampleID = sample.ID
	}
	if _, duplicate := a.seenSampleIDs[sample.ID]; duplicate {
		return fmt.Errorf("duplicate sample id %d", sample.ID)
	}
	a.seenSampleIDs[sample.ID] = struct{}{}
	if sample.ID < a.MinSampleID {
		a.MinSampleID = sample.ID
	}

	a.SampleCount++
	switch {
	case sample.Status == SyncFailed:
		a.FailedCount++
		a.ErrorCounts[sample.LastErrorCode]++
	case sample.IsPartial:
		a.PartialSuccessCount++
		if sample.Value != nil && (a.LastPartialValue == nil || tupleAfter(synced, sample.ID, derefTime(a.lastPartialSyncedAt), a.LastPartialSampleID)) {
			a.LastPartialValue = cloneMap(sample.Value)
			a.LastPartialSampleID = sample.ID
			t := synced
			a.lastPartialSyncedAt = &t
		}
	default:
		a.FullSuccessCount++
	}

	// Failed rows represent an attempt, but their value is stale/unknown and is
	// never allowed into numeric or currency aggregates.
	if sample.Status == SyncOK && !sample.IsPartial {
		if err := a.mergeSuccessfulValue(policy, sample, synced); err != nil {
			return err
		}
	}
	a.mergeCoverage(sample, synced)
	a.mergeRepresentativeTimes(sample, synced)
	return nil
}

func (a *DailyAccumulator) mergeSuccessfulValue(policy RollupPolicy, sample RawRollupSample, synced time.Time) error {
	value := sample.Value
	if value == nil {
		value = map[string]any{}
	}
	// Capture the previous representative tuple before updating its sample ID;
	// numeric first/last comparisons must compare against the preceding row,
	// especially when several samples share the same synced_at.
	previousFirstAt, previousFirstID := derefTime(a.FirstSyncedAt), a.FirstFullSampleID
	previousLastAt, previousLastID := derefTime(a.LastSyncedAt), a.LastFullSampleID
	if !sample.IsPartial {
		if a.FirstFullValue == nil || tupleBefore(synced, sample.ID, previousFirstAt, previousFirstID) {
			a.FirstFullValue = cloneMap(value)
			a.FirstFullSampleID = sample.ID
		}
		if a.LastFullValue == nil || tupleAfter(synced, sample.ID, previousLastAt, previousLastID) {
			a.LastFullValue = cloneMap(value)
			a.LastFullSampleID = sample.ID
		}
	}
	if policy.PrimaryKind == PrimaryNone {
		return nil
	}
	primary, ok, err := integerAtPointer(value, policy.PrimaryJSONPointer)
	if err != nil {
		return err
	}
	if !ok {
		if policy.FullValueRequired && !sample.IsPartial {
			return fmt.Errorf("sample %d is missing required primary value", sample.ID)
		}
		return nil
	}
	if primary == nil {
		return nil
	}
	if policy.PrimaryKind == PrimaryCount && primary.Sign() < 0 {
		return fmt.Errorf("sample %d count is negative", sample.ID)
	}
	digits := strings.TrimPrefix(primary.String(), "-")
	if len(digits) > maxNumericDigits {
		return fmt.Errorf("sample %d primary exceeds numeric(%d,0)", sample.ID, maxNumericDigits)
	}

	if policy.PrimaryKind == PrimaryMoneyMinor {
		currency, ok, err := stringAtPointer(value, policy.CurrencyJSONPointer)
		if err != nil {
			return err
		}
		if !ok || currency == "" {
			if policy.FullValueRequired && !sample.IsPartial {
				return fmt.Errorf("sample %d is missing required currency", sample.ID)
			}
			return nil
		}
		a.addCurrency(currency)
	}
	a.NumericCount++
	if len(a.CurrencySet) > 1 {
		a.NumericUnknownReason = "mixed_currency"
		// Keep the count for quality diagnostics but clear every numeric result.
		a.FirstNumeric, a.LastNumeric, a.MinNumeric, a.MaxNumeric, a.SumNumeric = nil, nil, nil, nil, nil
		return nil
	}
	if a.FirstNumeric == nil || tupleBefore(synced, sample.ID, previousFirstAt, previousFirstID) {
		a.FirstNumeric = new(big.Int).Set(primary)
	}
	if a.LastNumeric == nil || tupleAfter(synced, sample.ID, previousLastAt, previousLastID) {
		a.LastNumeric = new(big.Int).Set(primary)
	}
	if a.MinNumeric == nil || primary.Cmp(a.MinNumeric) < 0 {
		a.MinNumeric = new(big.Int).Set(primary)
	}
	if a.MaxNumeric == nil || primary.Cmp(a.MaxNumeric) > 0 {
		a.MaxNumeric = new(big.Int).Set(primary)
	}
	if policy.SumMode == "additive" {
		if a.SumNumeric == nil {
			a.SumNumeric = new(big.Int)
		}
		a.SumNumeric.Add(a.SumNumeric, primary)
	} else {
		a.SumNumeric = nil
	}
	return nil
}

func (a *DailyAccumulator) addCurrency(currency string) {
	for _, existing := range a.CurrencySet {
		if existing == currency {
			if len(a.CurrencySet) == 1 {
				a.Currency = currency
			}
			return
		}
	}
	a.CurrencySet = append(a.CurrencySet, currency)
	sort.Strings(a.CurrencySet)
	if len(a.CurrencySet) == 1 {
		a.Currency = currency
	} else {
		a.Currency = ""
	}
}

func (a *DailyAccumulator) mergeCoverage(sample RawRollupSample, synced time.Time) {
	interval := sample.ExpectedIntervalSeconds
	if interval == nil || *interval <= 0 {
		a.setCoverageUnknown("cadence_unknown")
		return
	}
	if secondsPerDay%int64(*interval) != 0 {
		a.setCoverageUnknown("cadence_not_divisor")
		return
	}
	if a.coverageInterval == nil {
		v := *interval
		a.coverageInterval = &v
	} else if *a.coverageInterval != *interval {
		a.setCoverageUnknown("cadence_changed")
		return
	}
	if a.CoverageUnknownReason != "" {
		return
	}
	if a.coverageSlots == nil {
		a.coverageSlots = make(map[int64]struct{})
	}
	slot := floorDiv(synced.Unix(), int64(*interval))
	if _, exists := a.coverageSlots[slot]; exists {
		a.DuplicateCount++
	} else {
		a.coverageSlots[slot] = struct{}{}
	}
	expected := int64(secondsPerDay / int64(*interval))
	covered := int64(len(a.coverageSlots))
	a.ExpectedSlotCount, a.CoveredSlotCount = int64Ptr(expected), int64Ptr(covered)
	ppm := int32((covered * microsPerCoverage) / expected)
	a.CoveragePPM = &ppm
}

func (a *DailyAccumulator) setCoverageUnknown(reason string) {
	if a.CoverageUnknownReason == "" {
		a.CoverageUnknownReason = reason
	}
	a.ExpectedSlotCount, a.CoveredSlotCount, a.CoveragePPM = nil, nil, nil
}

func (a *DailyAccumulator) mergeRepresentativeTimes(sample RawRollupSample, synced time.Time) {
	// These fields describe the full-success representative values.  Keep the
	// temporal metadata in the same tuple order so equal timestamps are stable.
	if sample.Status != SyncOK || sample.IsPartial {
		return
	}
	if a.FirstSyncedAt == nil || tupleBefore(synced, sample.ID, *a.FirstSyncedAt, a.FirstFullSampleID) {
		t := synced
		a.FirstSyncedAt = &t
		if sample.ObservedAt != nil {
			v := sample.ObservedAt.UTC()
			a.FirstObservedAt = &v
		} else {
			a.FirstObservedAt = nil
		}
		a.FirstWatermark = sample.Watermark
	}
	if a.LastSyncedAt == nil || tupleAfter(synced, sample.ID, *a.LastSyncedAt, a.LastFullSampleID) {
		t := synced
		a.LastSyncedAt = &t
		if sample.ObservedAt != nil {
			v := sample.ObservedAt.UTC()
			a.LastObservedAt = &v
		} else {
			a.LastObservedAt = nil
		}
		a.LastWatermark = sample.Watermark
	}
}

func integerAtPointer(value map[string]any, pointer string) (*big.Int, bool, error) {
	if pointer == "" {
		return nil, false, nil
	}
	item, ok, err := valueAtJSONPointer(value, pointer)
	if err != nil || !ok {
		return nil, ok, err
	}
	integer, err := exactInteger(item)
	if err != nil {
		return nil, true, fmt.Errorf("primary %s: %w", pointer, err)
	}
	return integer, true, nil
}

func stringAtPointer(value map[string]any, pointer string) (string, bool, error) {
	item, ok, err := valueAtJSONPointer(value, pointer)
	if err != nil || !ok {
		return "", ok, err
	}
	s, ok := item.(string)
	if !ok {
		return "", true, fmt.Errorf("pointer %s is not a string", pointer)
	}
	return s, true, nil
}

func valueAtJSONPointer(root map[string]any, pointer string) (any, bool, error) {
	if pointer == "" {
		return root, true, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false, errors.New("invalid JSON pointer")
	}
	var current any = root
	for _, raw := range strings.Split(pointer[1:], "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, false, nil
			}
			current = next
		case []any:
			if part == "" {
				return nil, false, errors.New("array pointer has empty index")
			}
			var index int
			if _, err := fmt.Sscanf(part, "%d", &index); err != nil || index < 0 || index >= len(node) {
				return nil, false, errors.New("array pointer index is invalid")
			}
			current = node[index]
		default:
			return nil, false, nil
		}
	}
	return current, true, nil
}

func exactInteger(value any) (*big.Int, error) {
	var text string
	switch v := value.(type) {
	case json.Number:
		text = v.String()
	case string:
		text = v
	case int:
		text = fmt.Sprintf("%d", v)
	case int8:
		text = fmt.Sprintf("%d", v)
	case int16:
		text = fmt.Sprintf("%d", v)
	case int32:
		text = fmt.Sprintf("%d", v)
	case int64:
		text = fmt.Sprintf("%d", v)
	case uint:
		text = fmt.Sprintf("%d", v)
	case uint8:
		text = fmt.Sprintf("%d", v)
	case uint16:
		text = fmt.Sprintf("%d", v)
	case uint32:
		text = fmt.Sprintf("%d", v)
	case uint64:
		text = fmt.Sprintf("%d", v)
	case *big.Int:
		if v == nil {
			return nil, errors.New("integer is nil")
		}
		return new(big.Int).Set(v), nil
	default:
		return nil, fmt.Errorf("value %T is not an exact integer", value)
	}
	if text == "" || strings.ContainsAny(text, ".eE") {
		return nil, fmt.Errorf("value %q is not an integer", text)
	}
	out, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return nil, fmt.Errorf("value %q is not an integer", text)
	}
	return out, nil
}

func utcBucketDay(at time.Time) time.Time {
	u := at.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func tupleBefore(at time.Time, id int64, other time.Time, otherID int64) bool {
	return at.Before(other) || (at.Equal(other) && id < otherID)
}

func tupleAfter(at time.Time, id int64, other time.Time, otherID int64) bool {
	return at.After(other) || (at.Equal(other) && id > otherID)
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func int64Ptr(v int64) *int64 { return &v }

func floorDiv(n, d int64) int64 {
	if d <= 0 {
		return 0
	}
	q, r := n/d, n%d
	if r < 0 {
		q--
	}
	return q
}

func cloneAccumulator(in DailyAccumulator) DailyAccumulator {
	out := in
	out.CurrencySet = append([]string(nil), in.CurrencySet...)
	out.FirstSyncedAt = cloneTimePtr(in.FirstSyncedAt)
	out.LastSyncedAt = cloneTimePtr(in.LastSyncedAt)
	out.FirstObservedAt = cloneTimePtr(in.FirstObservedAt)
	out.LastObservedAt = cloneTimePtr(in.LastObservedAt)
	out.lastPartialSyncedAt = cloneTimePtr(in.lastPartialSyncedAt)
	out.ErrorCounts = make(map[string]int64, len(in.ErrorCounts))
	for key, count := range in.ErrorCounts {
		out.ErrorCounts[key] = count
	}
	out.FirstFullValue = cloneMap(in.FirstFullValue)
	out.LastFullValue = cloneMap(in.LastFullValue)
	out.LastPartialValue = cloneMap(in.LastPartialValue)
	out.FirstNumeric = cloneBigInt(in.FirstNumeric)
	out.LastNumeric = cloneBigInt(in.LastNumeric)
	out.MinNumeric = cloneBigInt(in.MinNumeric)
	out.MaxNumeric = cloneBigInt(in.MaxNumeric)
	out.SumNumeric = cloneBigInt(in.SumNumeric)
	out.coverageSlots = make(map[int64]struct{}, len(in.coverageSlots))
	for slot := range in.coverageSlots {
		out.coverageSlots[slot] = struct{}{}
	}
	out.seenSampleIDs = make(map[int64]struct{}, len(in.seenSampleIDs))
	for id := range in.seenSampleIDs {
		out.seenSampleIDs[id] = struct{}{}
	}
	return out
}

func cloneBigInt(in *big.Int) *big.Int {
	if in == nil {
		return nil
	}
	return new(big.Int).Set(in)
}

func cloneTimePtr(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	out := in.UTC()
	return &out
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneJSONValue(value)
	}
	return out
}

func cloneJSONValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneMap(v)
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = cloneJSONValue(v[i])
		}
		return out
	default:
		return value
	}
}
