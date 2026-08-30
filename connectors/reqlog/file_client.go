package reqlog

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/reqlogformat"
)

// FileInstance 是文件后端的 Snapshot.Instance 取值。
//
// 与 FakeInstance 分得清清楚楚是本字段存在的唯一理由（见 contract.go 里
// Snapshot.Instance 的说明）：前端按整串相等匹配 DEFAULT_DEMO_SOURCES
// （web/apps/admin-web/src/lib/demoData.ts）来挂"演示数据"横幅，这里读的是
// 记录代理落盘的真实用户对话，绝不能与 "reqlog-fake" 撞上——那会让真实数据
// 被当成演示数据（危害较小的一种误判），但反过来给 Fake 挂一个像真的
// Instance 才是真正危险的方向，所以两个常量必须显式分开、谁都不得复用谁。
const FileInstance = "reqlog-file"

// fileBackendVersion 是文件后端自称的版本标识。
//
// 不是 reqlog 的版本——磁盘记录代理没有版本号概念（Fake 的 fakeDefaultVersion
// 注释已经说明这一点）。这里标的是"文件后端理解的磁盘格式版本"，我们自己
// 定义、自己实现、自己兼容，因此 Supported 恒为 true：不存在"探测到一个我们
// 不认识的磁盘格式版本"这种情况（尚未发生过格式变更）。
const fileBackendVersion = "file/1"

// FileConfig 配置 NewFileClient。
//
// 字段都是**路径**而不是凭据：DataDir/TokenMapPath 不经 CredentialRef
// （ADR-014 管的是"用来向第三方系统认证的凭据"，本后端不向任何系统认证，
// 只读一份由平台容器只读挂载进来的本地文件——权限边界由挂载本身
// （deploy/compose/server-prod.yaml 的 `:ro` 绑定挂载 + 宿主一次性
// chgrp/chmod）承担，与 XM_SECRET_ROOT 之类的基础设施路径是同一类配置，
// 不是"连接第三方系统的凭据"）。
type FileConfig struct {
	// DataDir 是记录代理落盘数据的根目录（按天分子目录），必填。
	DataDir string
	// TokenMapPath 是 tokenmap.json 的路径，可留空——留空时 Username 恒为
	// 空串（"映射不到"是契约允许的合法状态，不是错误）。
	TokenMapPath string
	// RetentionDays 覆盖每页返回的保留期天数；<=0 时用契约常量 RetentionDays。
	//
	// 之所以可覆盖而不是死读常量：记录代理自己的保留天数已经做成可配置
	// （cmd/reqlog-recorder 的 --retention-days），两边如果各写一份容易漂——
	// 但连接器和记录代理是两个独立部署的进程，没有共享配置的通道，所以只能
	// 靠运维在 cmd/platform-api 的环境变量里保持两边一致（见
	// docs/runbooks/REQLOG-RECORDER.md），不是自动同步。
	RetentionDays int
	// Logger 用于记录跳过的坏数据行、读不到的 tokenmap 等非致命问题。
	// 为空时退回 slog.Default()。
	Logger *slog.Logger
	// Now 覆盖时钟（测试用）；默认 time.Now。
	Now func() time.Time
}

type fileClient struct {
	dataDir       string
	tokenMapPath  string
	retentionDays int
	logger        *slog.Logger
	now           func() time.Time
}

// NewFileClient 构造只读文件后端客户端（XM-REQLOG-MERGE）。
//
// 构造期只做字符串校验，不碰文件系统——磁盘是否真的可读由 Health() 回答，
// 不是构造函数该做的事（与 NewClient「构造期不做任何 I/O」是同一条纪律）。
func NewFileClient(cfg FileConfig) (ReadClient, error) {
	dir := strings.TrimSpace(cfg.DataDir)
	if dir == "" {
		return nil, fmt.Errorf("reqlog: FileConfig.DataDir 不能为空")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	retention := cfg.RetentionDays
	if retention <= 0 {
		retention = RetentionDays
	}
	return &fileClient{
		dataDir:       dir,
		tokenMapPath:  strings.TrimSpace(cfg.TokenMapPath),
		retentionDays: retention,
		logger:        logger,
		now:           now,
	}, nil
}

func (c *fileClient) clock() time.Time { return c.now().UTC() }

func (c *fileClient) snapshot() Snapshot {
	observed := c.clock()
	return Snapshot{
		ObservedAt: observed,
		Watermark:  fmt.Sprintf("file-%d", observed.UnixMilli()),
		IsPartial:  false,
		Instance:   FileInstance,
	}
}

// Version 报告文件后端自己的格式版本（见 fileBackendVersion 的说明）。
func (c *fileClient) Version(ctx context.Context) (connector.VersionInfo, error) {
	if err := ctx.Err(); err != nil {
		return connector.VersionInfo{}, connector.NewError(connector.KindUnavailable, "reqlog.service.version_read", err)
	}
	return connector.VersionInfo{
		Detected:    fileBackendVersion,
		Fingerprint: fileBackendVersion,
		Supported:   true,
		DetectedAt:  c.clock(),
	}, nil
}

// Health 检查数据目录可读、且（如果已有落盘数据）最新一天的索引可以打开。
//
// 空目录（记录代理刚起、还没写过任何一天）判为健康：没有数据不等于坏了，
// 与 RetentionIsReported 那条"没查到"要能分清"那天没有请求"和"过了保留期"
// 是同一条论证——这里对应的是"还没来得及有数据"和"目录读不出来"。
func (c *fileClient) Health(ctx context.Context) (connector.HealthResult, error) {
	if err := ctx.Err(); err != nil {
		return connector.HealthResult{}, connector.NewError(connector.KindUnavailable, "reqlog.health.read", err)
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
	days, err := c.dayDirs()
	if err != nil {
		return unhealthy("reqlog 数据目录不可读"), nil
	}
	if len(days) > 0 {
		idxPath := filepath.Join(c.dataDir, days[0], "index.jsonl")
		f, err := os.Open(idxPath)
		if err != nil {
			return unhealthy("最新一天的索引文件不可打开"), nil
		}
		_ = f.Close()
	}
	return connector.HealthResult{
		Healthy:   true,
		CheckedAt: c.clock(),
		LatencyMS: c.clock().Sub(start).Milliseconds(),
	}, nil
}

// Capabilities 返回全部四项能力——文件后端对四者都有真实实现，
// 不是 HTTP 骨架（client.go）那种"形状未核实、只能给空清单"的状态。
func (c *fileClient) Capabilities(ctx context.Context) ([]registry.Capability, error) {
	if err := ctx.Err(); err != nil {
		return nil, connector.NewError(connector.KindUnavailable, "reqlog.capabilities", err)
	}
	return append([]registry.Capability(nil), ReadCapabilities...), nil
}

// ListRequests 扫描 DataDir 下全部按天目录、按契约过滤条件收窄、按发生时刻
// 倒序，再套用游标分页。
//
// 统计（Stats）覆盖分页前的完整过滤集（契约要求），因此每次调用都要扫完
// 命中 Source 的全部数据——与桌面端原型 viewer.go 的 `loadRange("all")`
// 是同一条路径（那边也是全量载入再排序），不是本次新增的性能问题。
// 是否按 Since/Until 提前跳过目录是一个可以做但本次没做的优化：磁盘目录名
// 按 CST 分天，而过滤条件是 UTC，跨零点时"提前跳过"很容易因为时区换算错
// 一格而悄悄漏数据——宪法 1 条"准确…优先于速度"，本次选择先保证不丢数据，
// 把按目录名剪枝列进交接文档的 follow-up。
func (c *fileClient) ListRequests(ctx context.Context, filter ListFilter) (RequestLogPage, error) {
	const op = "reqlog.requests.read"
	if err := ctx.Err(); err != nil {
		return RequestLogPage{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	if err := ValidateFilter(filter); err != nil {
		return RequestLogPage{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	matched, err := c.matchedSummaries(ctx, filter)
	if err != nil {
		return RequestLogPage{}, err
	}
	stats := statsFor(matched)

	offset, err := decodeCursor(filter.Cursor)
	if err != nil {
		return RequestLogPage{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	if offset > len(matched) {
		offset = len(matched)
	}
	limit := NormalizeLimit(filter.Limit)
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	page := RequestLogPage{
		Snapshot:      c.snapshot(),
		Items:         append([]RequestLogSummary(nil), matched[offset:end]...),
		Stats:         stats,
		RetentionDays: c.retentionDays,
	}
	if end < len(matched) {
		page.NextCursor = encodeCursor(end)
	}
	return page, nil
}

// matchedSummaries 扫描全部按天目录，解析索引行，套用过滤条件，
// 按发生时刻倒序返回。
func (c *fileClient) matchedSummaries(ctx context.Context, filter ListFilter) ([]RequestLogSummary, error) {
	const op = "reqlog.requests.read"
	days, err := c.dayDirs()
	if err != nil {
		return nil, connector.NewError(connector.KindUnavailable, op, err)
	}
	tokenMap := c.loadTokenMap()

	var out []RequestLogSummary
	for _, day := range days {
		if err := ctx.Err(); err != nil {
			return nil, connector.NewError(connector.KindUnavailable, op, err)
		}
		for _, r := range c.loadDayIndex(day) {
			if r.Source != filter.Source {
				continue
			}
			s := c.summaryFromRecord(day, r, tokenMap)
			if !matchesFilter(s, filter) {
				continue
			}
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OccurredAt.After(out[j].OccurredAt) })
	return out, nil
}

// summaryFromRecord 把一条磁盘 Record 翻译成契约的 RequestLogSummary。
//
// ListRequests（从 index.jsonl 逐行读到的 Record）与 RequestContent
// （从 <id>.json.gz 里 FullRecord.Record 读到的同一形状）共用这一个函数，
// 保证两条路径对同一条记录给出完全一致的摘要（contracttest 的
// testResultsCarrySnapshot 断言了这一点）。
func (c *fileClient) summaryFromRecord(day string, r reqlogformat.Record, tokenMap map[string]string) RequestLogSummary {
	var ttfb *int64
	if r.MeasuredTTFB() {
		v := r.TtfbMs
		ttfb = &v
	}
	return RequestLogSummary{
		ID:          compositeID(day, r.ID),
		Source:      r.Source,
		OccurredAt:  time.UnixMilli(r.TsMs).UTC(),
		Username:    resolveUsername(tokenMap, r.TokenPfx, r.Source),
		TokenPrefix: r.TokenPfx,
		Model:       r.Model,
		// reqlog 磁盘记录不含渠道/上游字段——确认自桌面端原型 reqlogger.go
		// 的 Record 结构体（没有 channel/upstream 字段），不是"暂时没接"。
		// 见 contracts/connectors/reqlog.read.v1.md §8 第 21 项的核对结论。
		Channel:  "",
		Upstream: "",
		Status:   r.Status,
		// reqlog 磁盘记录同样不含计费字段，理由同上（§8 第 22 项）。
		BilledAmount:      nil,
		DurationMS:        r.DurMs,
		TTFBMS:            ttfb,
		TokensIn:          int64(r.InTok),
		TokensOut:         int64(r.OutTok),
		TokensCache:       int64(r.CacheTok),
		Stream:            r.Stream,
		UpstreamRequestID: r.UpReqID,
		ClientIP:          MaskIP(r.ClientIP),
	}
}

// RequestContent 读取单条记录的完整内容。
func (c *fileClient) RequestContent(ctx context.Context, source, id string) (RequestLogContent, error) {
	const op = "reqlog.request.content_read"
	if err := ctx.Err(); err != nil {
		return RequestLogContent{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	source, err := ParseSource(source)
	if err != nil {
		return RequestLogContent{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	day, recordID, ok := splitCompositeID(id)
	if !ok {
		// 形状就不对：不可能是本后端发出的 ID，直接判未命中，
		// 不去猜一个"最接近"的记录
		return RequestLogContent{}, connector.NewError(connector.KindNotSupported, op, ErrNotFound)
	}

	full, err := c.loadFullRecord(day, recordID)
	if err != nil {
		if os.IsNotExist(err) {
			return RequestLogContent{}, connector.NewError(connector.KindNotSupported, op, ErrNotFound)
		}
		return RequestLogContent{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	if full.Source != source {
		// 记录存在，但来源对不上——必须是一次明确的未命中（ReadClient 的
		// 接口注释、contracttest 的 testSourceIsolation 都要求这一条）
		return RequestLogContent{}, connector.NewError(connector.KindNotSupported, op, ErrNotFound)
	}

	tokenMap := c.loadTokenMap()
	summary := c.summaryFromRecord(day, full.Record, tokenMap)

	turns := reqlogformat.ParseTurns(full.ReqBody)
	messages := make([]Message, 0, len(turns))
	for i, t := range turns {
		if i >= MaxMessages {
			break
		}
		body, truncated, original := TruncateUTF8(t.Text, MaxMessageBytes)
		messages = append(messages, Message{
			Role: t.Role, Content: body, Truncated: truncated, OriginalBytes: original,
		})
	}
	// "解析出了多少轮"直接决定 MessagesParsed：请求体没有可识别的对话结构
	// （比如 embedding 请求）时 ParseTurns 返回空切片，这里就如实报告
	// "没解析出消息"，而不是与"请求体里压根没有消息"混为一谈的另一套判据。
	messagesParsed := len(turns) > 0

	finalRaw := reqlogformat.ExtractFinalText(full.RespBody)
	finalReply, finalTruncated, finalBytes := TruncateUTF8(finalRaw, MaxFinalReplyBytes)
	reqBody, reqTruncated, reqBytes := TruncateUTF8(full.ReqBody, MaxRawPayloadBytes)
	respBody, respTruncated, respBytes := TruncateUTF8(full.RespBody, MaxRawPayloadBytes)

	// req_headers/resp_headers 落盘时已对 authorization/x-api-key/cookie
	// 做过截断脱敏（cmd/reqlog-recorder 的 headerMap），但契约的 RawPayload
	// 本就不含头部字段——本函数从不把 ReqHeaders/RespHeaders 整图暴露出去，
	// 只取 Content-Type 这一个非敏感字段，所以这里没有"再脱敏一次"的对象：
	// 不透出头部本身就是比再脱敏更强的一层。
	return RequestLogContent{
		Snapshot:            c.snapshot(),
		Summary:             summary,
		Messages:            messages,
		MessagesParsed:      messagesParsed,
		FinalReply:          finalReply,
		FinalReplyTruncated: finalTruncated,
		FinalReplyBytes:     finalBytes,
		RawRequest: RawPayload{
			Body: reqBody, Truncated: reqTruncated, OriginalBytes: reqBytes,
			ContentType: headerLookup(full.ReqHeaders, "Content-Type"),
		},
		RawResponse: RawPayload{
			Body: respBody, Truncated: respTruncated, OriginalBytes: respBytes,
			ContentType: headerLookup(full.RespHeaders, "Content-Type"),
		},
	}, nil
}

// --- 磁盘扫描 ---

func (c *fileClient) dayDirs() ([]string, error) {
	entries, err := os.ReadDir(c.dataDir)
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

// loadDayIndex 读一天的 index.jsonl。坏行（不是合法 JSON）被跳过并记警告，
// 不让一行坏数据拖垮整页——与桌面端原型 viewer.go 的 loadIndex 同一条
// 容错策略（那边用 `if json.Unmarshal(l,&r)==nil { append }` 静默跳过）。
func (c *fileClient) loadDayIndex(day string) []reqlogformat.Record {
	path := filepath.Join(c.dataDir, day, "index.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			c.logger.Warn("reqlog_file_client_index_unreadable",
				slog.String("day", day), slog.String("error", err.Error()))
		}
		return nil
	}
	defer f.Close()

	var out []reqlogformat.Record
	sc := bufio.NewScanner(f)
	// index 行只含元数据（不含正文），但放宽默认的 64KiB/行上限到 1MiB，
	// 防止一行异常长的 UA/preview 把整天的索引读取整体判成扫描失败。
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
			c.logger.Warn("reqlog_file_client_bad_index_line",
				slog.String("day", day), slog.Int("line", lineNo))
			continue
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		c.logger.Warn("reqlog_file_client_index_scan_error",
			slog.String("day", day), slog.String("error", err.Error()))
	}
	return out
}

func (c *fileClient) loadFullRecord(day, recordID string) (*reqlogformat.FullRecord, error) {
	// 组合前先分别校验过 day/recordID 的形状（splitCompositeID 用严格的
	// 纯数字+短横线正则），这里再用 filepath.Join 落地路径不存在遍历风险——
	// 双保险：即便正则以后被改松，越界路径也过不了这一条前缀检查。
	path := filepath.Join(c.dataDir, day, recordID+".json.gz")
	if !strings.HasPrefix(path, filepath.Clean(c.dataDir)+string(filepath.Separator)) {
		return nil, fmt.Errorf("reqlog: 拒绝越出数据目录的路径 %q", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("reqlog: 解压 %s 失败: %w", path, err)
	}
	defer gz.Close()
	var fr reqlogformat.FullRecord
	if err := json.NewDecoder(gz).Decode(&fr); err != nil {
		return nil, fmt.Errorf("reqlog: 解析 %s 失败: %w", path, err)
	}
	return &fr, nil
}

// --- 复合 ID：day + 磁盘原生 id（HHMMSS-NNNNNN） ---

// compositeIDPattern 严格限定形状为 YYYYMMDD-HHMMSS-NNNNNN（三段纯数字，
// 短横线分隔），比契约的 IsPathSafeID（放行 RFC 3986 unreserved 字符集）
// 严得多：这条正则本身就是路径拼接前唯一需要的输入校验，`.`/`~` 等
// IsPathSafeID 放行、但本格式里永远不会出现的字符在这里会被直接拒绝。
var compositeIDPattern = regexp.MustCompile(`^([0-9]{8})-([0-9]{6}-[0-9]{6})$`)

func compositeID(day, recordID string) string {
	return day + "-" + recordID
}

func splitCompositeID(id string) (day, recordID string, ok bool) {
	m := compositeIDPattern.FindStringSubmatch(strings.TrimSpace(id))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// --- 令牌前缀 → 用户名 ---

func (c *fileClient) loadTokenMap() map[string]string {
	if c.tokenMapPath == "" {
		return nil
	}
	b, err := os.ReadFile(c.tokenMapPath)
	if err != nil {
		if !os.IsNotExist(err) {
			c.logger.Warn("reqlog_file_client_tokenmap_unreadable", slog.String("error", err.Error()))
		}
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		c.logger.Warn("reqlog_file_client_tokenmap_malformed", slog.String("error", err.Error()))
		return nil
	}
	return m
}

// resolveUsername 把 token_prefix 解到用户名，邮箱经 MaskEmail 打码后才
// 离开本函数（PROJECT-CONSTITUTION 宪法 7 条；tokenmap.json 由记录代理
// 从上游库导出，含 PII，见 docs/handoffs/slices/XM-REQLOG-MERGE.md 的风险
// 说明）。
//
// tokenmap 里的值形如 "<用户名或邮箱>@newapi" / "...@sub2api"
// （cmd/reqlog-recorder 的 refreshTokenMap 写入时拼的后缀）：
//   - 剥掉 "@<source>" 后缀，剩下的是真正的标识；
//   - 若那部分本身像邮箱（含 '@'），交给 platformusers.MaskEmail 打码；
//   - 否则（纯用户名）原样返回——用户名不是本次风险清单里的 PII 项。
//
// 后缀剥不掉（形状与假设不符）时**不**把原始值透出去，判成"映射不到"：
// 契约允许 Username 为空（"映射不到"是合法状态），但不允许把一个形状陌生、
// 未经审视的字符串原样交给前端。
func resolveUsername(tokenMap map[string]string, prefix, source string) string {
	if len(tokenMap) == 0 || prefix == "" {
		return ""
	}
	raw, ok := tokenMap[prefix]
	if !ok {
		return ""
	}
	suffix := "@" + source
	ident := strings.TrimSuffix(raw, suffix)
	if ident == raw || ident == "" {
		return ""
	}
	if strings.Contains(ident, "@") {
		return platformusers.MaskEmail(ident)
	}
	return ident
}

// --- 头部查找 ---

// headerLookup 从落盘的头部图里取一个字段值（多值头部落盘时已用逗号拼接，
// 这里只取第一段）。大小写严格匹配优先，找不到再做一次不区分大小写的兜底
// ——写侧用 Go 的 http.Header 落盘，键名应恒为规范形态（"Content-Type"），
// 兜底只是防御性的，不依赖它。
func headerLookup(headers map[string]string, key string) string {
	if v, ok := headers[key]; ok {
		return firstCSVField(v)
	}
	lower := strings.ToLower(key)
	for k, v := range headers {
		if strings.ToLower(k) == lower {
			return firstCSVField(v)
		}
	}
	return ""
}

func firstCSVField(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		return strings.TrimSpace(v[:i])
	}
	return v
}
