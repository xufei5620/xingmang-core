package cpa

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" database/sql driver
	_ "github.com/ncruces/go-sqlite3/embed"  // embeds the pure-Go (WASM) SQLite runtime — no CGO

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/cpasnapshot"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// fileBackendVersion is the file backend's own format identifier, not CPA's
// version — usage.sqlite carries no schema-version marker this package knows
// of, so there is nothing upstream to detect (same reasoning as
// connectors/reqlog/file_client.go's fileBackendVersion).
const fileBackendVersion = "file/1"

// busyTimeoutMillis bounds how long a read waits behind a concurrent writer
// (cpa-manager-plus) before failing, per the task brief's "sqlite 只读打开并
// 容忍 busy（重试/超时）". A WAL reader normally never blocks on a writer at
// all; this only matters for the rare case of a checkpoint in progress.
const busyTimeoutMillis = 5000

// defaultDatabaseFileName is the host producer's atomically published file.
const defaultDatabaseFileName = "usage.sqlite"

// FileConfig configures NewFileClient.
type FileConfig struct {
	// DataDir is the read-only mounted published snapshot directory. The file
	// is standalone DELETE-journal SQLite; the active WAL directory never
	// enters the platform container. Required.
	DataDir string
	// FileName overrides the database file name within DataDir; defaults to
	// "usage.sqlite".
	FileName string
	// Logger records non-fatal issues (unresolved token columns, etc.).
	// Defaults to slog.Default().
	Logger *slog.Logger
	// Now overrides the clock (tests); defaults to time.Now.
	Now func() time.Time
}

type fileClient struct {
	dsn    string
	logger *slog.Logger
	now    func() time.Time
}

// NewFileClient constructs the read-only SQLite-backed CPA client (XM-CPA0).
//
// Construction only validates strings; it never touches the filesystem —
// whether the mount is actually readable is Health()'s job, not this
// function's (same discipline as connectors/reqlog.NewFileClient).
func NewFileClient(cfg FileConfig) (ReadClient, error) {
	dir := strings.TrimSpace(cfg.DataDir)
	if dir == "" {
		return nil, fmt.Errorf("cpa: FileConfig.DataDir 不能为空")
	}
	name := strings.TrimSpace(cfg.FileName)
	if name == "" {
		name = defaultDatabaseFileName
	}
	if strings.ContainsAny(dir, "\x00?#%") {
		return nil, fmt.Errorf("cpa: FileConfig.DataDir 含 SQLite URI 控制字符")
	}
	if name == "." || name == ".." || filepath.IsAbs(name) || filepath.VolumeName(name) != "" ||
		filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00?#%") {
		return nil, fmt.Errorf("cpa: FileConfig.FileName 必须是 DataDir 内的单一安全文件名")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	path := filepath.ToSlash(filepath.Join(dir, name))
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=query_only(ON)&_pragma=busy_timeout(%d)", path, busyTimeoutMillis)
	return &fileClient{dsn: dsn, logger: logger, now: now}, nil
}

func (c *fileClient) clock() time.Time { return c.now().UTC() }

// open connects read-only, capped to a single connection: this client always
// runs a short burst of queries and closes, so pooling would only add
// resource-tracking surface with no benefit (same call shape as
// connectors/reqlog's per-call os.Open).
func (c *fileClient) open(ctx context.Context) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", c.dsn)
	if err != nil {
		return nil, fmt.Errorf("cpa: 打开 usage.sqlite 失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cpa: usage.sqlite 不可读（只读挂载缺失、路径不存在，或被 busy_timeout 用尽仍占用）: %w", err)
	}
	return db, nil
}

// Version reports the file backend's own format version (see
// fileBackendVersion's doc comment for why this is not CPA's version).
func (c *fileClient) Version(ctx context.Context) (connector.VersionInfo, error) {
	if err := ctx.Err(); err != nil {
		return connector.VersionInfo{}, connector.NewError(connector.KindUnavailable, "cpa.service.version_read", err)
	}
	return connector.VersionInfo{
		Detected:    fileBackendVersion,
		Fingerprint: fileBackendVersion,
		Supported:   true,
		DetectedAt:  c.clock(),
	}, nil
}

// Health opens the database, pings it, and confirms usage_events' token
// columns resolve — a database that opens but has an unrecognized schema
// shape is a real degradation the healthcheck should surface, not silence.
func (c *fileClient) Health(ctx context.Context) (connector.HealthResult, error) {
	if err := ctx.Err(); err != nil {
		return connector.HealthResult{}, connector.NewError(connector.KindUnavailable, "cpa.health.read", err)
	}
	start := c.clock()
	unhealthy := func(detail string) connector.HealthResult {
		return connector.HealthResult{
			Healthy:   false,
			CheckedAt: c.clock(),
			LatencyMS: c.clock().Sub(start).Milliseconds(),
			ErrorKind: connector.KindUnavailable,
			Detail:    detail,
		}
	}
	db, err := c.open(ctx)
	if err != nil {
		return unhealthy("usage.sqlite 不可打开或不可读"), nil
	}
	defer db.Close()
	if _, err = cpasnapshot.ReadMetadata(ctx, db); err != nil {
		return unhealthy("已发布 CPA 快照缺少有效 generation/observed_at 元数据"), nil
	}

	_, missing, err := resolveTokenColumns(ctx, db)
	if err != nil {
		return unhealthy("usage_events 表结构不可读"), nil
	}
	detail := ""
	if len(missing) > 0 {
		detail = fmt.Sprintf(
			"token 列未按候选名单解析: %s（对应用量/成本按 0 计并标 partial；见 contracts/connectors/cpa.read.v1.md §8）",
			strings.Join(missing, ","))
		c.logger.Warn("cpa_file_client_token_columns_unresolved",
			slog.String("missing_roles", strings.Join(missing, ",")))
		return connector.HealthResult{
			Healthy:   false,
			CheckedAt: c.clock(),
			LatencyMS: c.clock().Sub(start).Milliseconds(),
			ErrorKind: connector.KindBadResponse,
			Detail:    detail,
		}, nil
	}
	return connector.HealthResult{
		Healthy:   true,
		CheckedAt: c.clock(),
		LatencyMS: c.clock().Sub(start).Milliseconds(),
		Detail:    detail,
	}, nil
}

// Capabilities always returns the full ReadCapabilities set: every one of
// them is backed by a real query in this file, unlike reqlog's file backend
// which has to subtract capabilities its disk format cannot provide.
func (c *fileClient) Capabilities(ctx context.Context) ([]registry.Capability, error) {
	if err := ctx.Err(); err != nil {
		return nil, connector.NewError(connector.KindUnavailable, "cpa.capabilities", err)
	}
	out := make([]registry.Capability, len(ReadCapabilities))
	copy(out, ReadCapabilities)
	return out, nil
}

// dayBoundsUTC parses a YYYY-MM-DD business day into its UTC [start, end)
// bounds. CPA has no documented business-day timezone convention (unlike
// e.g. platformusers' CST convention) — usage.sqlite's timestamp_ms is
// read as-is and treated as UTC epoch milliseconds (contract §8: unverified
// against a live deployment, flagged as a follow-up if CLI Proxy API turns
// out to stamp local time instead).
func dayBoundsUTC(day string) (start, end time.Time, err error) {
	if strings.TrimSpace(day) == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("cpa: day 不能为空")
	}
	start, err = time.Parse("2006-01-02", day)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("cpa: day 必须形如 YYYY-MM-DD: %w", err)
	}
	start = start.UTC()
	return start, start.AddDate(0, 0, 1), nil
}

// UsageSummary aggregates usage_events by (provider, model) for one UTC day.
func (c *fileClient) UsageSummary(ctx context.Context, day string) (UsageSummary, error) {
	const op = "cpa.usage.read"
	if err := ctx.Err(); err != nil {
		return UsageSummary{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	start, end, err := dayBoundsUTC(day)
	if err != nil {
		return UsageSummary{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	db, err := c.open(ctx)
	if err != nil {
		return UsageSummary{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	defer db.Close()
	metadata, err := cpasnapshot.ReadMetadata(ctx, db)
	if err != nil {
		return UsageSummary{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	cols, missing, err := resolveTokenColumns(ctx, db)
	if err != nil {
		return UsageSummary{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	query := `SELECT provider, model, COUNT(*), ` +
		sumExpr(cols.input) + `, ` + sumExpr(cols.output) + `, ` +
		sumExpr(cols.cacheRead) + `, ` + sumExpr(cols.cacheCreation) +
		` FROM usage_events WHERE timestamp_ms >= ? AND timestamp_ms < ?` +
		` GROUP BY provider, model ORDER BY provider, model`
	rows, err := db.QueryContext(ctx, query, start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return UsageSummary{}, connector.NewError(connector.KindBadResponse, op, fmt.Errorf("查询 usage_events 失败: %w", err))
	}
	var usageRows []ProviderModelUsage
	for rows.Next() {
		var provider, model sql.NullString
		var r ProviderModelUsage
		if scanErr := rows.Scan(&provider, &model, &r.RequestCount,
			&r.TokensIn, &r.TokensOut, &r.TokensCacheRead, &r.TokensCacheCreation); scanErr != nil {
			rows.Close()
			return UsageSummary{}, connector.NewError(connector.KindBadResponse, op, fmt.Errorf("解析 usage_events 行失败: %w", scanErr))
		}
		r.Provider, r.Model = provider.String, model.String
		usageRows = append(usageRows, r)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return UsageSummary{}, connector.NewError(connector.KindBadResponse, op, rowsErr)
	}

	prices, err := loadModelPrices(ctx, db)
	if err != nil {
		return UsageSummary{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	var totalRequests, unpricedRequests int64
	var totalCost *int64
	unpricedModelSet := map[string]struct{}{}
	for i := range usageRows {
		cost, costErr := rowCost(usageRows[i], prices[usageRows[i].Model])
		if costErr != nil {
			return UsageSummary{}, connector.NewError(connector.KindBadResponse, op, costErr)
		}
		usageRows[i].CostMicros = cost
		totalRequests += usageRows[i].RequestCount
		if cost == nil {
			unpricedRequests += usageRows[i].RequestCount
			unpricedModelSet[unpricedModelLabel(usageRows[i])] = struct{}{}
			continue
		}
		if totalCost == nil {
			zero := int64(0)
			totalCost = &zero
		}
		*totalCost += *cost
	}
	unpricedModels := make([]string, 0, len(unpricedModelSet))
	for k := range unpricedModelSet {
		unpricedModels = append(unpricedModels, k)
	}
	sort.Strings(unpricedModels)

	currency := ""
	if totalCost != nil {
		currency = Currency
	}
	return UsageSummary{
		Snapshot: Snapshot{
			ObservedAt: metadata.ObservedAt, Watermark: metadata.Generation,
			IsPartial: unpricedRequests > 0 || len(missing) > 0, Instance: FileInstance,
		},
		BusinessDay:          day,
		Rows:                 usageRows,
		TotalRequestCount:    totalRequests,
		TotalCostMicros:      totalCost,
		Currency:             currency,
		UnpricedRequestCount: unpricedRequests,
		UnpricedModels:       unpricedModels,
	}, nil
}

func unpricedModelLabel(r ProviderModelUsage) string {
	provider := r.Provider
	if provider == "" {
		provider = "(未知 provider)"
	}
	model := r.Model
	if model == "" {
		model = "(未知 model)"
	}
	return provider + "/" + model
}

// keyModelRow is one (api_key_hash, model) group before per-key roll-up.
type keyModelRow struct {
	hash, model string
	usage       ProviderModelUsage
	lastUsedMs  int64
}

// KeyUsage aggregates usage_events by api_key_hash for one UTC day.
//
// Pricing is computed at (key, model) grain and then rolled up per key —
// mixing a key's tokens across different-priced models before pricing would
// make correct cost math unrecoverable, the same reasoning UsageSummary
// applies at (provider, model) grain.
func (c *fileClient) KeyUsage(ctx context.Context, day string) (KeyUsagePage, error) {
	const op = "cpa.key_usage.read"
	if err := ctx.Err(); err != nil {
		return KeyUsagePage{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	start, end, err := dayBoundsUTC(day)
	if err != nil {
		return KeyUsagePage{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	db, err := c.open(ctx)
	if err != nil {
		return KeyUsagePage{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	defer db.Close()
	metadata, err := cpasnapshot.ReadMetadata(ctx, db)
	if err != nil {
		return KeyUsagePage{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	cols, missing, err := resolveTokenColumns(ctx, db)
	if err != nil {
		return KeyUsagePage{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	query := `SELECT api_key_hash, model, COUNT(*), ` +
		sumExpr(cols.input) + `, ` + sumExpr(cols.output) + `, ` +
		sumExpr(cols.cacheRead) + `, ` + sumExpr(cols.cacheCreation) + `, MAX(timestamp_ms)` +
		` FROM usage_events WHERE timestamp_ms >= ? AND timestamp_ms < ?` +
		` GROUP BY api_key_hash, model`
	rows, err := db.QueryContext(ctx, query, start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return KeyUsagePage{}, connector.NewError(connector.KindBadResponse, op, fmt.Errorf("查询 usage_events 失败: %w", err))
	}
	var raw []keyModelRow
	for rows.Next() {
		var hash, model sql.NullString
		var lastUsed sql.NullInt64
		var kr keyModelRow
		if scanErr := rows.Scan(&hash, &model, &kr.usage.RequestCount,
			&kr.usage.TokensIn, &kr.usage.TokensOut, &kr.usage.TokensCacheRead, &kr.usage.TokensCacheCreation,
			&lastUsed); scanErr != nil {
			rows.Close()
			return KeyUsagePage{}, connector.NewError(connector.KindBadResponse, op, fmt.Errorf("解析 usage_events 行失败: %w", scanErr))
		}
		kr.hash, kr.model = hash.String, model.String
		kr.usage.Model = model.String
		if lastUsed.Valid {
			kr.lastUsedMs = lastUsed.Int64
		}
		raw = append(raw, kr)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return KeyUsagePage{}, connector.NewError(connector.KindBadResponse, op, rowsErr)
	}

	prices, err := loadModelPrices(ctx, db)
	if err != nil {
		return KeyUsagePage{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	aliases, err := loadAPIKeyAliases(ctx, db)
	if err != nil {
		return KeyUsagePage{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	byKey := map[string]*KeyUsageRow{}
	lastUsedByKey := map[string]int64{}
	order := make([]string, 0)
	for _, kr := range raw {
		row, ok := byKey[kr.hash]
		if !ok {
			row = &KeyUsageRow{APIKeyHash: kr.hash, Alias: aliases[kr.hash]}
			byKey[kr.hash] = row
			order = append(order, kr.hash)
		}
		row.RequestCount += kr.usage.RequestCount
		row.TokensIn += kr.usage.TokensIn
		row.TokensOut += kr.usage.TokensOut
		row.TokensCacheRead += kr.usage.TokensCacheRead
		row.TokensCacheCreation += kr.usage.TokensCacheCreation
		if kr.lastUsedMs > lastUsedByKey[kr.hash] {
			lastUsedByKey[kr.hash] = kr.lastUsedMs
		}

		cost, costErr := rowCost(kr.usage, prices[kr.model])
		if costErr != nil {
			return KeyUsagePage{}, connector.NewError(connector.KindBadResponse, op, costErr)
		}
		if cost == nil {
			row.UnpricedRequestCount += kr.usage.RequestCount
			continue
		}
		if row.CostMicros == nil {
			zero := int64(0)
			row.CostMicros = &zero
		}
		*row.CostMicros += *cost
	}

	out := make([]KeyUsageRow, 0, len(order))
	var isPartial bool
	for _, hash := range order {
		row := *byKey[hash]
		if ms := lastUsedByKey[hash]; ms > 0 {
			t := time.UnixMilli(ms).UTC()
			row.LastUsedAt = &t
		}
		if row.UnpricedRequestCount > 0 {
			isPartial = true
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RequestCount != out[j].RequestCount {
			return out[i].RequestCount > out[j].RequestCount
		}
		return out[i].APIKeyHash < out[j].APIKeyHash
	})

	return KeyUsagePage{
		Snapshot: Snapshot{
			ObservedAt: metadata.ObservedAt, Watermark: metadata.Generation,
			IsPartial: isPartial || len(missing) > 0, Instance: FileInstance,
		},
		BusinessDay:   day,
		Rows:          out,
		TotalKeyCount: int64(len(out)),
		Truncated:     false,
	}, nil
}

// loadModelPrices reads every model_prices row, converting each REAL price
// column to fixed-point micro-USD at the SQL→Go boundary (see cost.go's
// priceMicroPer1M doc comment for why this is safe under constitution §13).
func loadModelPrices(ctx context.Context, db *sql.DB) (map[string]*modelPrice, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT model, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m FROM model_prices`)
	if err != nil {
		return nil, fmt.Errorf("cpa: 查询 model_prices 失败: %w", err)
	}
	defer rows.Close()

	out := map[string]*modelPrice{}
	for rows.Next() {
		var model string
		var prompt, completion, cache, cacheRead, cacheCreation sql.NullFloat64
		if err := rows.Scan(&model, &prompt, &completion, &cache, &cacheRead, &cacheCreation); err != nil {
			return nil, fmt.Errorf("cpa: 解析 model_prices 行失败: %w", err)
		}
		p := &modelPrice{}
		var convErr error
		if p.promptMicroPer1M, convErr = priceMicroPer1M(prompt); convErr != nil {
			return nil, convErr
		}
		if p.completionMicroPer1M, convErr = priceMicroPer1M(completion); convErr != nil {
			return nil, convErr
		}
		if p.cacheMicroPer1M, convErr = priceMicroPer1M(cache); convErr != nil {
			return nil, convErr
		}
		if p.cacheReadMicroPer1M, convErr = priceMicroPer1M(cacheRead); convErr != nil {
			return nil, convErr
		}
		if p.cacheCreationMicroPer1M, convErr = priceMicroPer1M(cacheCreation); convErr != nil {
			return nil, convErr
		}
		out[model] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cpa: 遍历 model_prices 失败: %w", err)
	}
	return out, nil
}

// loadAPIKeyAliases reads every api_key_hash → alias mapping. A hash absent
// from this map (or present with alias "") has no registered alias — a
// normal, expected state, not an error.
func loadAPIKeyAliases(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT api_key_hash, alias FROM api_key_aliases`)
	if err != nil {
		return nil, fmt.Errorf("cpa: 查询 api_key_aliases 失败: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var hash, alias sql.NullString
		if err := rows.Scan(&hash, &alias); err != nil {
			return nil, fmt.Errorf("cpa: 解析 api_key_aliases 行失败: %w", err)
		}
		out[hash.String] = alias.String
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cpa: 遍历 api_key_aliases 失败: %w", err)
	}
	return out, nil
}

// AccountHealth reads the latest codex_inspection run's results.
//
// "Latest" is the greatest started_at_ms, with integer id as the deterministic
// tie-break. Both columns were verified against the production CPA schema.
func (c *fileClient) AccountHealth(ctx context.Context) (AccountHealthSummary, error) {
	const op = "cpa.account_health.read"
	if err := ctx.Err(); err != nil {
		return AccountHealthSummary{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	db, err := c.open(ctx)
	if err != nil {
		return AccountHealthSummary{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	defer db.Close()

	metadata, err := cpasnapshot.ReadMetadata(ctx, db)
	if err != nil {
		return AccountHealthSummary{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	baseSnapshot := Snapshot{ObservedAt: metadata.ObservedAt, Watermark: metadata.Generation, Instance: FileInstance}

	var runID, startedAtMS int64
	err = db.QueryRowContext(ctx,
		`SELECT id, started_at_ms FROM codex_inspection_runs ORDER BY started_at_ms DESC, id DESC LIMIT 1`).Scan(&runID, &startedAtMS)
	switch {
	case err == sql.ErrNoRows:
		return AccountHealthSummary{Snapshot: baseSnapshot}, nil
	case err != nil:
		return AccountHealthSummary{}, connector.NewError(connector.KindBadResponse, op, fmt.Errorf("查询 codex_inspection_runs 失败: %w", err))
	}

	rows, err := db.QueryContext(ctx,
		`SELECT account_key, display_account, provider, disabled, status, state, action, action_reason
		 FROM codex_inspection_results WHERE run_id = ?`, runID)
	if err != nil {
		return AccountHealthSummary{}, connector.NewError(connector.KindBadResponse, op, fmt.Errorf("查询 codex_inspection_results 失败: %w", err))
	}
	var accountCount, disabledCount int64
	var allAnomalies []AccountAnomaly
	for rows.Next() {
		var accountKey, displayAccount, provider, status, state, action, actionReason sql.NullString
		var disabled sql.NullBool
		if scanErr := rows.Scan(&accountKey, &displayAccount, &provider, &disabled, &status, &state, &action, &actionReason); scanErr != nil {
			rows.Close()
			return AccountHealthSummary{}, connector.NewError(connector.KindBadResponse, op, fmt.Errorf("解析 codex_inspection_results 行失败: %w", scanErr))
		}
		accountCount++
		isDisabled := disabled.Valid && disabled.Bool
		if isDisabled {
			disabledCount++
		}
		trimmedAction := strings.TrimSpace(action.String)
		anomalous := isDisabled || (trimmedAction != "" && !strings.EqualFold(trimmedAction, "none"))
		if anomalous {
			allAnomalies = append(allAnomalies, AccountAnomaly{
				AccountKey: accountKey.String, DisplayAccount: displayAccount.String, Provider: provider.String,
				Disabled: isDisabled, Status: status.String, State: state.String,
				Action: action.String, ActionReason: actionReason.String,
			})
		}
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return AccountHealthSummary{}, connector.NewError(connector.KindBadResponse, op, rowsErr)
	}

	sort.SliceStable(allAnomalies, func(i, j int) bool {
		if allAnomalies[i].Disabled != allAnomalies[j].Disabled {
			return allAnomalies[i].Disabled // disabled accounts sort first
		}
		return allAnomalies[i].AccountKey < allAnomalies[j].AccountKey
	})
	truncated := len(allAnomalies) > MaxAnomalies
	shown := allAnomalies
	if truncated {
		shown = append([]AccountAnomaly(nil), allAnomalies[:MaxAnomalies]...)
	}

	baseSnapshot.IsPartial = truncated
	runAt := time.UnixMilli(startedAtMS).UTC()
	return AccountHealthSummary{
		Snapshot:      baseSnapshot,
		RunID:         strconv.FormatInt(runID, 10),
		RunAt:         &runAt,
		AccountCount:  accountCount,
		DisabledCount: disabledCount,
		Anomalies:     shown,
		AnomalyCount:  int64(len(allAnomalies)),
		Truncated:     truncated,
	}, nil
}
