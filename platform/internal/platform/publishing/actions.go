package publishing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// Action ID 与版本。
const (
	ActionDraftSet         = "publishing.draft.set"
	ActionDraftArchive     = "publishing.draft.archive"
	ActionAssetSet         = "publishing.asset.set"
	ActionAssetRemove      = "publishing.asset.remove"
	ActionChannelSet       = "publishing.channel.set"
	ActionChannelSetStatus = "publishing.channel.set_status"
	ActionPublishSubmit    = "publishing.publish.submit"
	actionVersion          = "1"

	resourceDraft   = "publishing.draft"
	resourceAsset   = "publishing.asset"
	resourceChannel = "publishing.channel"
	resourceRecord  = "publishing.publish_record"
)

var (
	allEnvironments = []string{"development", "staging", "production"}
	// 七个 Action **全部只给人**。
	//
	// 机器身份刻意不在列：对外发布是「以谁的名义说话」，没有无人值守的场景值得
	// 承担「一个跑飞的任务把稿子发出去」的风险。这与 sms.number.request 的取舍
	// 相反，理由也相反——那条链路的价值就在无人值守，这条链路的风险就在无人看着。
	humanOnly = []principal.Type{principal.TypeHuman}
)

// RegisterActions 把内容发布的七个 Action 登记进内核。
func RegisterActions(registry *action.Registry, svc *Service) error {
	items := []struct {
		def     action.Definition
		handler action.Handler
	}{
		{draftSetDefinition(), draftSetHandler(svc)},
		{draftArchiveDefinition(), draftArchiveHandler(svc)},
		{assetSetDefinition(), assetSetHandler(svc)},
		{assetRemoveDefinition(), assetRemoveHandler(svc)},
		{channelSetDefinition(), channelSetHandler(svc)},
		{channelSetStatusDefinition(), channelSetStatusHandler(svc)},
		{publishSubmitDefinition(), publishSubmitHandler(svc)},
	}
	for _, item := range items {
		if err := registry.Register(item.def, item.handler); err != nil {
			return fmt.Errorf("注册 %s: %w", item.def.ID, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 声明
// ---------------------------------------------------------------------------

// draftSetDefinition：新建或修改草稿。
//
// **L1**（ADR-003：修改低风险平台配置）。草稿完全在平台内部，改错了改回来即可，
// 而且每一版都留了不可变修订——与 server.supplier.set 这类登记簿写同一档。
func draftSetDefinition() action.Definition {
	return action.Definition{
		ID: ActionDraftSet, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			// 空串表示新建。做成「空串 = 新建」而不是「字段缺省 = 新建」，
			// 是因为 Schema 的 Required 只区分有无，区分不出「显式给了空」。
			{Name: "draft_id", Type: action.FieldString},
			{Name: "title", Type: action.FieldString, Required: true},
			{Name: "body", Type: action.FieldString},
			// RFC3339；空串表示不排期（草稿态）。
			{Name: "scheduled_at", Type: action.FieldString},
			{Name: "note", Type: action.FieldString},
			{Name: "asset_ids", Type: action.FieldStringSlice},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// draftArchiveDefinition：归档草稿。L1，同上；归档不是删除，可逆性由「再建一条」承担。
func draftArchiveDefinition() action.Definition {
	return action.Definition{
		ID: ActionDraftArchive, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "draft_id", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// assetSetDefinition：登记素材引用。L1，纯登记。
func assetSetDefinition() action.Definition {
	return action.Definition{
		ID: ActionAssetSet, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "asset_id", Type: action.FieldString},
			{Name: "name", Type: action.FieldString, Required: true},
			{Name: "kind", Type: action.FieldString, Required: true, Enum: []string{"image", "video", "link"}},
			{Name: "uri", Type: action.FieldString, Required: true},
			{Name: "note", Type: action.FieldString},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// assetRemoveDefinition：删除素材引用。L1；仍被草稿引用时库层 RESTRICT 挡住。
func assetRemoveDefinition() action.Definition {
	return action.Definition{
		ID: ActionAssetRemove, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "asset_id", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// channelSetDefinition：登记渠道账号。
//
// # 为什么是 L1 而不是 L2
//
// 这一条**曾经写成 L2**（理由是「它决定以后以谁的名义、用哪把钥匙对外说话」，
// 与 `registry.connector.create` 对齐）。写完之后
// `TestChannelRejectsPastedPlaintextCredential` 立刻红了，红出来的是一个真问题：
//
//	内核对 L2+ 的顺序是 …→ 权限 → Schema → **风险闸（冻结参数、落审批单）**，
//	Handler 在这之后才跑（kernel.go）。而 `action.Schema` 的字段只支持
//	Required 与 Enum，**表达不了 CredentialRef 的形状**——那道校验只能写在
//	Handler 里。于是运营一旦把 token 本身粘进 credential_ref，L2 会先把这个串
//	**原样冻进 `core.approval_request.params_json`**（永久落库、还会显示给审批人
//	看），然后才轮到 Handler 说「这不是引用」。
//
// 一次已实测：探针用例里那个串确实出现在 ApprovalSubmission.Params 里。
// 也就是说，把这个动作定成 L2 会**亲手造出一条明文凭据泄漏路径**（宪法 7 条），
// 而它换来的只是「一票可自批」的一张单。这笔交易不划算。
//
// L1 的正面依据也站得住，不是只为了绕开上面那条：
//
//   - ADR-003 的 L1 是「修改低风险平台配置」。这条记录**自己不会让任何内容发
//     出去**——真正不可逆的那一下是 `publishing.publish.submit`（L3，两票且
//     不许自批），而它每次都会重新读渠道。
//   - 同类先例都是 L1：`finance.platform_channel_binding.set`（决定哪个上游账号
//     在给哪个平台供给，绑错会让成本记到错误渠道）、`server.supplier.set`、
//     `finance.upstream_account.set`。渠道登记比第一条还轻。
//   - 护栏一道没减：只收 CredentialRef（领域层 + 库层两道正则）、
//     `publishing.manage` 权限、全量审计，以及发布本身的 L3。
//
// **留给后来人的口子**：等 `action.Schema` 支持字段形状校验（正则/格式），
// 这个动作可以安全地回到 L2——那时明文会在冻结之前就被拒。已登记为
// follow_up，见 docs/handoffs/slices/XM-EXT-PUBLISHING.md。
func channelSetDefinition() action.Definition {
	return action.Definition{
		ID: ActionChannelSet, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "platform", Type: action.FieldString, Required: true, Enum: []string{"x", "telegram", "other"}},
			{Name: "handle", Type: action.FieldString, Required: true},
			{Name: "display_name", Type: action.FieldString},
			{Name: "purpose", Type: action.FieldString},
			// **只收引用**，形如 secret://<scope>/<name>。领域层与库层各有一道
			// 校验挡住直接粘进来的明文（宪法 7 条）。
			{Name: "credential_ref", Type: action.FieldString},
			{Name: "note", Type: action.FieldString},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// channelSetStatusDefinition：启停/退役渠道。
//
// **L1**，与 channel.set 同档。即便日后 channel.set 回到 L2（见那里的注释），
// 这一个也该留在 L1：它的危险方向是单向的——只能让一个渠道**更不容易**被发出去
// （PAUSED/RETIRED 都会让 Publish 直接拒）。把「暂停一个出事的渠道」压进审批
// 队列，等于在最需要立刻停手的时候加一道等。反方向（改回 ACTIVE）不绕过任何
// 闸：发布本身仍是 L3。
func channelSetStatusDefinition() action.Definition {
	return action.Definition{
		ID: ActionChannelSetStatus, Version: actionVersion, RiskLevel: action.L1, Permission: ScopeManage,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "channel_id", Type: action.FieldString, Required: true},
			{Name: "status", Type: action.FieldString, Required: true,
				Enum: []string{"ACTIVE", "PAUSED", "RETIRED"}},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// publishSubmitDefinition：提起一次对外发布。
//
// # 为什么是 L3
//
// 判据只用仓库里写下来的话（XM-RISK-RESTORE 的纪律）：
//
//  1. ADR-003 的等级表把 L4 给了「退款、生产基础设施高影响动作、开票关键动作」，
//     L3 给了「服务切换、账号批量导入、敏感配置」。对外发布不属于花钱那一类，
//     但它与 `cards.withdraw.execute` 共享同一条最要紧的性质：**把东西送到平台
//     之外，不可逆、不可追回**。发出去的内容即便删除也已经被抓取、被截图。
//  2. 原型自己写的话（`blueprints/ext.ts` 的审批队列格，逐字）：「对外发布属于
//     有外部影响的操作，必须人工审核，不会有自动放行。」L2 允许提交人自批
//     （approval.DefaultPolicy），自批不是审核；L3 是「两票且审批人≠提交人」，
//     那才是这句话说的东西。
//  3. 宪法 9 条：「L3/L4 必须审批」。审批中心已启用，L3 会正常落单。
//
// # 现在定成 L3 而不是等以后再抬，是刻意的
//
// 今天这个动作**发不出去任何东西**（没有出站投递器），所以 L3 的摩擦此刻是零
// 成本的。等第二层接上再抬级，那是一次行为变更，而且要在「已经有人依赖这条路
// 很顺畅」之后做——XM-RISK-RESTORE 整片就是在还这笔账。
//
// # 留给产品负责人的两件事
//
//   - **L3 要两票且不许自批。** 今天如果只有一个人持有 approval.decide，
//     发布单会一直停在 PENDING 直到过期。这不是缺陷，是 L3 的定义；但它是不是
//     此刻想要的，得负责人拍。退回 L2 只需改这一处等级 + 契约那一处，
//     用例钉的是行为不是字面等级。
//   - **publishing.publish 该发给谁。** 它刻意不进 admin（见 permissions.go），
//     专门角色是 content-publisher。
func publishSubmitDefinition() action.Definition {
	return action.Definition{
		ID: ActionPublishSubmit, Version: actionVersion, RiskLevel: action.L3, Permission: ScopePublish,
		Schema: action.Schema{Fields: []action.Field{
			{Name: "draft_id", Type: action.FieldString, Required: true},
			{Name: "channel_id", Type: action.FieldString, Required: true},
		}},
		Environments: allEnvironments, PrincipalTypes: humanOnly,
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// domainError 把领域错误翻成 Action 错误码。
//
// 内核在 Handler 失败时会 errors.As 出 *action.Error 并原样保留 Code 与 Message
// （kernel.go），所以这里给的码与文案就是调用方看到的那一份。
func domainError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidInput):
		return action.NewError(action.CodeInvalidParams, err.Error(), err)
	case errors.Is(err, ErrNotFound):
		// **不用 CodeNotRegistered**：端点存在、Action 也注册着，不存在的只是
		// 参数里指名的那个对象（见 action/errors.go 对该码的注释）。
		return action.NewError(action.CodePreconditionFailed, err.Error(), err)
	case errors.Is(err, ErrConflict):
		return action.NewError(action.CodeConflict, err.Error(), err)
	case errors.Is(err, ErrChannelNotPublishable):
		return action.NewError(action.CodePreconditionFailed, err.Error(), err)
	default:
		return err
	}
}

func actorFrom(ctx context.Context) (string, error) {
	p, ok := principal.FromContext(ctx)
	if !ok || strings.TrimSpace(p.ID) == "" {
		return "", action.NewError(action.CodePermissionDenied, "缺少身份", nil)
	}
	return p.ID, nil
}

func requireService(svc *Service) error {
	if svc == nil || svc.store == nil {
		return action.NewError(action.CodePreconditionFailed, "内容发布未装配存储", nil)
	}
	return nil
}

// parseOptionalUUID 解析「空串表示没有」的 id 参数。
func parseOptionalUUID(raw string) (*uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: id %q 不是合法 UUID", ErrInvalidInput, raw)
	}
	return &id, nil
}

func parseRequiredUUID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: id %q 不是合法 UUID", ErrInvalidInput, raw)
	}
	return id, nil
}

// parseOptionalTime 解析 RFC3339 时刻；空串表示没有。
//
// 一律转 UTC（宪法 14 条：时间库内 UTC）。带偏移量的输入被接受并归一化，
// 不带偏移量的被拒——「2026-09-08 10:00」在哪个时区是运营的判断，不是解析器的。
func parseOptionalTime(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: 时刻 %q 必须是带时区的 RFC3339（例：2026-09-08T10:00:00+08:00）", ErrInvalidInput, raw)
	}
	utc := t.UTC()
	return &utc, nil
}

func draftSetHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireService(svc); err != nil {
			return nil, err
		}
		actor, err := actorFrom(ctx)
		if err != nil {
			return nil, err
		}
		draftID, err := parseOptionalUUID(action.StringParam(params, "draft_id"))
		if err != nil {
			return nil, domainError(err)
		}
		title, err := NormalizeTitle(action.StringParam(params, "title"))
		if err != nil {
			return nil, domainError(err)
		}
		body, err := NormalizeBody(action.StringParam(params, "body"))
		if err != nil {
			return nil, domainError(err)
		}
		note, err := NormalizeNote(action.StringParam(params, "note"))
		if err != nil {
			return nil, domainError(err)
		}
		scheduledAt, err := parseOptionalTime(action.StringParam(params, "scheduled_at"))
		if err != nil {
			return nil, domainError(err)
		}
		var assetIDs []uuid.UUID
		for _, raw := range action.StringSliceParam(params, "asset_ids") {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			id, parseErr := parseRequiredUUID(raw)
			if parseErr != nil {
				return nil, domainError(parseErr)
			}
			assetIDs = append(assetIDs, id)
		}

		draft, err := svc.store.SaveDraft(ctx, DraftInput{
			ID: draftID, Title: title, Body: body, ScheduledAt: scheduledAt,
			Note: note, AssetIDs: assetIDs, Actor: actor, Now: svc.now().UTC(),
		})
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceDraft, draft.ID.String())
		// 审计摘要**只放形态，不放正文**：草稿正文是运营还没想好要不要说的话，
		// 把它抄进 audit_event 等于让每一版草稿都进一条永不删除的审计链。
		// 与 savedviews 只记 state_hash 同一条纪律。
		action.RecordAfter(ctx, map[string]any{
			"draft_id": draft.ID.String(), "version": draft.CurrentVersion,
			"status": string(draft.Status), "asset_count": len(draft.AssetIDs),
			"scheduled": draft.ScheduledAt != nil,
		})
		return draftResponse(draft), nil
	}
}

func draftArchiveHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireService(svc); err != nil {
			return nil, err
		}
		if _, err := actorFrom(ctx); err != nil {
			return nil, err
		}
		id, err := parseRequiredUUID(action.StringParam(params, "draft_id"))
		if err != nil {
			return nil, domainError(err)
		}
		draft, err := svc.store.ArchiveDraft(ctx, id, svc.now().UTC())
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceDraft, draft.ID.String())
		action.RecordAfter(ctx, map[string]any{"status": string(draft.Status)})
		return draftResponse(draft), nil
	}
}

func assetSetHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireService(svc); err != nil {
			return nil, err
		}
		actor, err := actorFrom(ctx)
		if err != nil {
			return nil, err
		}
		assetID, err := parseOptionalUUID(action.StringParam(params, "asset_id"))
		if err != nil {
			return nil, domainError(err)
		}
		name, err := NormalizeTitle(action.StringParam(params, "name"))
		if err != nil {
			return nil, domainError(err)
		}
		kind, err := ParseAssetKind(action.StringParam(params, "kind"))
		if err != nil {
			return nil, domainError(err)
		}
		uri, err := NormalizeAssetURI(action.StringParam(params, "uri"))
		if err != nil {
			return nil, domainError(err)
		}
		note, err := NormalizeNote(action.StringParam(params, "note"))
		if err != nil {
			return nil, domainError(err)
		}
		asset := Asset{Name: name, Kind: kind, URI: uri, Note: note,
			CreatedBy: actor, CreatedAt: svc.now().UTC()}
		if assetID != nil {
			asset.ID = *assetID
		}
		saved, err := svc.store.UpsertAsset(ctx, asset)
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceAsset, saved.ID.String())
		action.RecordAfter(ctx, map[string]any{
			"asset_id": saved.ID.String(), "name": saved.Name, "kind": string(saved.Kind),
		})
		return assetResponse(saved), nil
	}
}

func assetRemoveHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireService(svc); err != nil {
			return nil, err
		}
		if _, err := actorFrom(ctx); err != nil {
			return nil, err
		}
		id, err := parseRequiredUUID(action.StringParam(params, "asset_id"))
		if err != nil {
			return nil, domainError(err)
		}
		if err := svc.store.RemoveAsset(ctx, id); err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceAsset, id.String())
		return map[string]string{"asset_id": id.String()}, nil
	}
}

func channelSetHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireService(svc); err != nil {
			return nil, err
		}
		actor, err := actorFrom(ctx)
		if err != nil {
			return nil, err
		}
		platform, err := ParsePlatform(action.StringParam(params, "platform"))
		if err != nil {
			return nil, domainError(err)
		}
		handle, err := NormalizeHandle(action.StringParam(params, "handle"))
		if err != nil {
			return nil, domainError(err)
		}
		credentialRef, err := NormalizeCredentialRef(action.StringParam(params, "credential_ref"))
		if err != nil {
			return nil, domainError(err)
		}
		purpose, err := NormalizeNote(action.StringParam(params, "purpose"))
		if err != nil {
			return nil, domainError(err)
		}
		note, err := NormalizeNote(action.StringParam(params, "note"))
		if err != nil {
			return nil, domainError(err)
		}
		displayName, err := NormalizeNote(action.StringParam(params, "display_name"))
		if err != nil {
			return nil, domainError(err)
		}
		saved, err := svc.store.UpsertChannel(ctx, Channel{
			Platform: platform, Handle: handle, DisplayName: displayName,
			Purpose: purpose, CredentialRef: credentialRef, Status: ChannelActive,
			Note: note, CreatedBy: actor, CreatedAt: svc.now().UTC(),
		})
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceChannel, saved.ID.String())
		// 审计里只记「有没有登记引用」而不记引用本身。Ref 不是秘密（secrets/ref.go
		// 说得很清楚），但它是「哪把钥匙」——审计摘要不需要它，少写一处少一处可漏。
		action.RecordAfter(ctx, map[string]any{
			"channel_id": saved.ID.String(), "platform": string(saved.Platform),
			"handle": saved.Handle, "credential_ref_present": saved.CredentialRef != "",
		})
		return channelResponse(saved), nil
	}
}

func channelSetStatusHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireService(svc); err != nil {
			return nil, err
		}
		if _, err := actorFrom(ctx); err != nil {
			return nil, err
		}
		id, err := parseRequiredUUID(action.StringParam(params, "channel_id"))
		if err != nil {
			return nil, domainError(err)
		}
		status, err := ParseChannelStatus(action.StringParam(params, "status"))
		if err != nil {
			return nil, domainError(err)
		}
		before, err := svc.store.GetChannel(ctx, id)
		if err != nil {
			return nil, domainError(err)
		}
		saved, err := svc.store.SetChannelStatus(ctx, id, status, svc.now().UTC())
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceChannel, saved.ID.String())
		action.RecordBefore(ctx, map[string]any{"status": string(before.Status)})
		action.RecordAfter(ctx, map[string]any{"status": string(saved.Status)})
		return channelResponse(saved), nil
	}
}

// publishSubmitHandler 是「发布」的执行体。
//
// 它跑到的时候，这次调用**已经过了审批**（L3 走 ExecuteApproved）。它做的事是
// 落一条发布记录——而记录的结果一定是「未投递」，因为平台没有出站投递器。
//
// 返回体把 `delivered: false` 与那句话原样交给前端：页面据此把结果显示成
// 「已受理，未投递」而不是绿色的「已发布」。
func publishSubmitHandler(svc *Service) action.Handler {
	return func(ctx context.Context, params map[string]any) (any, error) {
		if err := requireService(svc); err != nil {
			return nil, err
		}
		actor, err := actorFrom(ctx)
		if err != nil {
			return nil, err
		}
		draftID, err := parseRequiredUUID(action.StringParam(params, "draft_id"))
		if err != nil {
			return nil, domainError(err)
		}
		channelID, err := parseRequiredUUID(action.StringParam(params, "channel_id"))
		if err != nil {
			return nil, domainError(err)
		}
		outcome, err := svc.Publish(ctx, PublishRequest{
			DraftID: draftID, ChannelID: channelID, RequestedBy: actor,
		})
		if err != nil {
			return nil, domainError(err)
		}
		action.RecordResource(ctx, resourceRecord, outcome.Record.ID.String())
		action.RecordAfter(ctx, map[string]any{
			"record_id": outcome.Record.ID.String(),
			"draft_id":  outcome.Record.DraftID.String(),
			"version":   outcome.Record.DraftVersion,
			"result":    string(outcome.Record.Result),
			"delivered": outcome.Delivered,
		})
		return publishResponse(outcome), nil
	}
}

// ---------------------------------------------------------------------------
// 返回体
// ---------------------------------------------------------------------------

type draftWire struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	Status         string   `json:"status"`
	CurrentVersion int      `json:"current_version"`
	ScheduledAt    string   `json:"scheduled_at"`
	AssetIDs       []string `json:"asset_ids"`
	UpdatedAt      string   `json:"updated_at"`
}

func rfc3339(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func draftResponse(d Draft) draftWire {
	ids := make([]string, 0, len(d.AssetIDs))
	for _, id := range d.AssetIDs {
		ids = append(ids, id.String())
	}
	return draftWire{
		ID: d.ID.String(), Title: d.Title, Body: d.Body, Status: string(d.Status),
		CurrentVersion: d.CurrentVersion, ScheduledAt: rfc3339(d.ScheduledAt),
		AssetIDs: ids, UpdatedAt: d.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type assetWire struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	URI       string `json:"uri"`
	Note      string `json:"note"`
	UpdatedAt string `json:"updated_at"`
}

func assetResponse(a Asset) assetWire {
	return assetWire{
		ID: a.ID.String(), Name: a.Name, Kind: string(a.Kind), URI: a.URI,
		Note: a.Note, UpdatedAt: a.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type channelWire struct {
	ID          string `json:"id"`
	Platform    string `json:"platform"`
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name"`
	Purpose     string `json:"purpose"`
	// CredentialRef 是**引用**（secret://…），不是凭据。回显它是为了让运营
	// 看得出这个渠道绑的是哪一条；明文从来不经过这里（宪法 7 条）。
	CredentialRef string `json:"credential_ref"`
	Status        string `json:"status"`
	Note          string `json:"note"`
	UpdatedAt     string `json:"updated_at"`
}

func channelResponse(c Channel) channelWire {
	return channelWire{
		ID: c.ID.String(), Platform: string(c.Platform), Handle: c.Handle,
		DisplayName: c.DisplayName, Purpose: c.Purpose, CredentialRef: c.CredentialRef,
		Status: string(c.Status), Note: c.Note,
		UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type publishWire struct {
	RecordID     string `json:"record_id"`
	DraftID      string `json:"draft_id"`
	DraftVersion int    `json:"draft_version"`
	ChannelID    string `json:"channel_id"`
	Result       string `json:"result"`
	// Delivered 是这段返回体里最要紧的一个字段：它回答「发出去了没有」。
	// 今天恒为 false。
	Delivered bool   `json:"delivered"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at"`
}

func publishResponse(o PublishOutcome) publishWire {
	return publishWire{
		RecordID: o.Record.ID.String(), DraftID: o.Record.DraftID.String(),
		DraftVersion: o.Record.DraftVersion, ChannelID: o.Record.ChannelID.String(),
		Result: string(o.Record.Result), Delivered: o.Delivered, Reason: o.Reason,
		CreatedAt: o.Record.CreatedAt.UTC().Format(time.RFC3339),
	}
}
