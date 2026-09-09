package publishing

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 本文件断言的是**行为**而不是字面等级：写死 `RiskLevel == "L3"` 的用例只是把
// 常量抄了一遍，等级表演进时还会挡路。这里问的是同一次调用到底被执行了，
// 还是被受理成一张审批单。

type nopRunStore struct{}

func (nopRunStore) InsertRun(context.Context, action.Run) error { return nil }

// fakeApprovals 是内核这一侧的审批网关替身，形状照 cards/audit_test.go。
type fakeApprovals struct {
	submitted []action.ApprovalSubmission
	claims    map[string]action.ApprovalClaim
	claimed   []string
}

func newFakeApprovals() *fakeApprovals {
	return &fakeApprovals{claims: map[string]action.ApprovalClaim{}}
}

func (g *fakeApprovals) Submit(_ context.Context, in action.ApprovalSubmission) (string, error) {
	id := fmt.Sprintf("appr-%d", len(g.submitted)+1)
	g.submitted = append(g.submitted, in)
	g.claims[id] = action.ApprovalClaim{
		ActionID: in.ActionID, ActionVersion: in.ActionVersion, RiskLevel: in.RiskLevel,
		Params: in.Params, Reason: in.Reason, RequesterID: in.Requester.ID,
	}
	return id, nil
}

func (g *fakeApprovals) Peek(_ context.Context, id string) (action.ApprovalClaim, error) {
	c, ok := g.claims[id]
	if !ok {
		return action.ApprovalClaim{}, fmt.Errorf("审批单 %s 不存在", id)
	}
	return c, nil
}

func (g *fakeApprovals) Claim(_ context.Context, id string, _ uuid.UUID, _ map[string]any) error {
	g.claimed = append(g.claimed, id)
	return nil
}

func ctxAs(p principal.Principal) context.Context {
	return principal.WithPrincipal(context.Background(), p)
}

// editor 持编辑权，**不持**发布权。
func editor() principal.Principal {
	return principal.Principal{
		ID: "editor-1", Type: principal.TypeHuman, Issuer: "test",
		Environment: "development", Scopes: []string{ScopeManage},
	}
}

// publisher 持发布权（content-publisher 角色），不持编辑权。
func publisher() principal.Principal {
	return principal.Principal{
		ID: "publisher-1", Type: principal.TypeHuman, Issuer: "test",
		Environment: "development", Scopes: []string{ScopePublish},
	}
}

func kernelWith(t *testing.T, svc *Service) (*action.Kernel, *fakeApprovals) {
	t.Helper()
	reg := action.NewRegistry()
	if err := RegisterActions(reg, svc); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	gw := newFakeApprovals()
	return action.NewKernel(reg, nopRunStore{}, action.WithApprovalGateway(gw)), gw
}

// 对外发布经过接了审批中心的内核时被受理成审批单，而不是当场执行。
//
// 断言不止「拿到了 APPROVAL_REQUIRED」，还要求**单上冻结的是这一次调用**——
// 否则把 action_id 换成任何别的 Action，这条用例照样绿。
func TestPublishSubmitLandsAsApprovalRequest(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil, testClock())
	draftID, channelID := seedPublishable(t, store, PlatformX)
	k, gw := kernelWith(t, svc)

	reason := "十月产品更新，市场部已确认文案"
	_, err := k.Execute(ctxAs(publisher()), action.Request{
		ActionID: ActionPublishSubmit, ActionVersion: actionVersion,
		RequestID: "req-publish", Reason: reason,
		Params: map[string]any{
			"draft_id": draftID.String(), "channel_id": channelID.String(),
		},
	})
	if code := action.ErrorCode(err); code != action.CodeApprovalRequired {
		t.Fatalf("对外发布应当被受理成审批单，got %q（%v）", code, err)
	}
	if len(gw.submitted) != 1 {
		t.Fatalf("没有真的落单：submitted=%d", len(gw.submitted))
	}
	sub := gw.submitted[0]
	if sub.ActionID != ActionPublishSubmit || sub.Reason != reason {
		t.Fatalf("单上冻结的不是这一次调用: %+v", sub)
	}
	if sub.Params["draft_id"] != draftID.String() {
		t.Fatalf("单上冻结的参数不对: %+v", sub.Params)
	}
	// 最要紧的一条：**没有执行**。落单不等于做了。
	if recs, _ := store.ListPublishRecords(context.Background(), 10); len(recs) != 0 {
		t.Fatalf("落单阶段就写了发布记录 %d 条：那意味着动作已经发生了", len(recs))
	}
}

// L3 缺 reason 时当场被拒，且**不落单**。
//
// 这一条钉的是内核的闸在这个 Action 上确实生效（kernel.go 对 L2+ 要求非空
// reason）。没有它，前端漏发 reason 的症状会是「点了没反应」。
func TestPublishSubmitRequiresReason(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil, testClock())
	draftID, channelID := seedPublishable(t, store, PlatformX)
	k, gw := kernelWith(t, svc)

	_, err := k.Execute(ctxAs(publisher()), action.Request{
		ActionID: ActionPublishSubmit, ActionVersion: actionVersion,
		RequestID: "req-noreason",
		Params: map[string]any{
			"draft_id": draftID.String(), "channel_id": channelID.String(),
		},
	})
	if code := action.ErrorCode(err); code != action.CodeInvalidParams {
		t.Fatalf("缺 reason 应当拿 INVALID_PARAMS，got %q（%v）", code, err)
	}
	if len(gw.submitted) != 0 {
		t.Fatalf("缺 reason 不该落单，got %d 张", len(gw.submitted))
	}
}

// 编辑类动作**不**走审批，一步跑完并真的改了数据。
//
// 反面用例，与上面两条配对：没有它，「发布落单」可能只是因为这个包里所有
// Action 都落单。断言到「真的改了数据」而不只是「没落单」——后者在 Handler
// 整个失效时也会绿。
func TestDraftAndAssetActionsExecuteWithoutApproval(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil, testClock())
	k, gw := kernelWith(t, svc)
	ctx := ctxAs(editor())

	assetRes, err := k.Execute(ctx, action.Request{
		ActionID: ActionAssetSet, ActionVersion: actionVersion, RequestID: "req-asset",
		Params: map[string]any{
			"name": "十月更新配图", "kind": "image",
			"uri": "https://cdn.example.com/oct.png",
		},
	})
	if err != nil {
		t.Fatalf("登记素材应当一步跑完: %v", err)
	}
	asset, ok := assetRes.Value.(assetWire)
	if !ok || asset.ID == "" {
		t.Fatalf("素材返回体不对: %#v", assetRes.Value)
	}

	draftRes, err := k.Execute(ctx, action.Request{
		ActionID: ActionDraftSet, ActionVersion: actionVersion, RequestID: "req-draft",
		Params: map[string]any{
			"title": "十月产品更新", "body": "本月上线了三项能力。",
			"scheduled_at": "2026-10-01T10:00:00+08:00",
			"asset_ids":    []string{asset.ID},
		},
	})
	if err != nil {
		t.Fatalf("保存草稿应当一步跑完: %v", err)
	}
	draft, ok := draftRes.Value.(draftWire)
	if !ok {
		t.Fatalf("草稿返回体不对: %#v", draftRes.Value)
	}
	// 正事：真的存下来了，状态与排期按输入算好了，素材引用也接上了。
	if draft.Status != string(DraftScheduled) {
		t.Fatalf("给了排期时间就该是 SCHEDULED，got %s", draft.Status)
	}
	if draft.ScheduledAt != "2026-10-01T02:00:00Z" {
		t.Fatalf("排期时刻应归一成 UTC，got %q", draft.ScheduledAt)
	}
	if len(draft.AssetIDs) != 1 || draft.AssetIDs[0] != asset.ID {
		t.Fatalf("素材引用没接上: %+v", draft.AssetIDs)
	}
	saved, err := store.GetDraft(context.Background(), uuid.MustParse(draft.ID))
	if err != nil {
		t.Fatalf("草稿没有真的落库: %v", err)
	}
	if saved.Title != "十月产品更新" {
		t.Fatalf("落库的标题不对: %q", saved.Title)
	}
	if len(gw.submitted) != 0 {
		t.Fatalf("L1 动作不该落审批单，got %d 张", len(gw.submitted))
	}
}

// 渠道登记与启停都一步跑完，并且真的改了数据。
//
// 这两个都是 L1（见 channelSetDefinition 的注释：定成 L2 会让粘进来的明文
// 先被冻进审批单）。断言到「真的改了数据」而不只是「没落单」——后者在
// Handler 整个失效时也会绿。
func TestChannelRegistrationAndPausingExecuteDirectly(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	svc := NewService(store, nil, testClock())
	_, channelID := seedPublishable(t, store, PlatformX)
	k, gw := kernelWith(t, svc)

	res, err := k.Execute(ctxAs(editor()), action.Request{
		ActionID: ActionChannelSet, ActionVersion: actionVersion,
		RequestID: "req-channel",
		Params: map[string]any{
			"platform": "x", "handle": "@xingmang-mkt",
			"credential_ref": "secret://publishing-x/mkt-token",
		},
	})
	if err != nil {
		t.Fatalf("渠道登记应当一步跑完: %v", err)
	}
	wire, ok := res.Value.(channelWire)
	if !ok {
		t.Fatalf("渠道返回体不对: %#v", res.Value)
	}
	// 引用**原样回显**（它不是秘密，运营要看得出绑的是哪一条）。
	if wire.CredentialRef != "secret://publishing-x/mkt-token" {
		t.Fatalf("凭据引用应当原样回显，got %q", wire.CredentialRef)
	}
	saved, err := store.GetChannel(ctx, uuid.MustParse(wire.ID))
	if err != nil {
		t.Fatalf("渠道没有真的落库: %v", err)
	}
	if saved.Handle != "@xingmang-mkt" || saved.Status != ChannelActive {
		t.Fatalf("落库的渠道不对: %+v", saved)
	}

	// 启停：一步跑完，并且真的改了状态。
	if _, err = k.Execute(ctxAs(editor()), action.Request{
		ActionID: ActionChannelSetStatus, ActionVersion: actionVersion,
		RequestID: "req-pause",
		Params:    map[string]any{"channel_id": channelID.String(), "status": "PAUSED"},
	}); err != nil {
		t.Fatalf("暂停渠道应当一步跑完（出事时不能排队等审批）: %v", err)
	}
	after, err := store.GetChannel(ctx, channelID)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if after.Status != ChannelPaused {
		t.Fatalf("渠道没有真的被暂停，status=%s", after.Status)
	}
	if len(gw.submitted) != 0 {
		t.Fatalf("这两个动作都不该落审批单，got %d 张", len(gw.submitted))
	}
}

// 编辑权拿不到发布权，发布权拿不到编辑权。
//
// 这是 permissions.go 那次拆分（ScopeManage / ScopePublish）的行为面：
// 拆成两个串如果实际上谁都能干另一件事，那次拆分就只是文档。
func TestManageAndPublishScopesDoNotSubstituteForEachOther(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil, testClock())
	draftID, channelID := seedPublishable(t, store, PlatformX)
	k, _ := kernelWith(t, svc)

	// 编辑去发布 → 权限被拒（连审批单都不该落，见 kernel.go 把风险闸挪到
	// 权限之后的理由：没有权限的人不该能刷审批单）。
	_, err := k.Execute(ctxAs(editor()), action.Request{
		ActionID: ActionPublishSubmit, ActionVersion: actionVersion,
		RequestID: "req-x1", Reason: "试着发一下",
		Params: map[string]any{
			"draft_id": draftID.String(), "channel_id": channelID.String(),
		},
	})
	if code := action.ErrorCode(err); code != action.CodePermissionDenied {
		t.Fatalf("只有编辑权的人不该发得了，got %q（%v）", code, err)
	}

	// 发布人去改稿 → 同样被拒。
	_, err = k.Execute(ctxAs(publisher()), action.Request{
		ActionID: ActionDraftSet, ActionVersion: actionVersion, RequestID: "req-x2",
		Params: map[string]any{"title": "偷偷改一版"},
	})
	if code := action.ErrorCode(err); code != action.CodePermissionDenied {
		t.Fatalf("只有发布权的人不该改得了稿，got %q（%v）", code, err)
	}
}

// 渠道登记只收 CredentialRef，粘明文当场被拒，且**错误里不回显原串**。
//
// 后半句是这条用例存在的主要理由：一条「credential_ref 非法：xoxb-真token」
// 的错误会把刚被拒绝的明文写进 ActionRun、审计事件和访问日志——比放行还糟。
func TestChannelRejectsPastedPlaintextCredential(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil, testClock())
	k, gw := kernelWith(t, svc)

	// 形状像一条真 token 的假串。**本身不是任何真实凭据**，只用来证明它不会
	// 被回显；用例里出现真凭据是红线（宪法 7 条）。
	const pastedLooksLikeToken = "AAAAAAAAAAAAAAAAAAAAA-not-a-real-token"

	// **带上 reason 是刻意的**：不带的话，把等级改成 L2 的变异会先撞上内核
	// 「L2+ 必须给 reason」那道闸，于是这次调用根本走不到冻结参数那一步——
	// 变异会红在别的地方，什么也证明不了。带上 reason，L2 就会真的落一张单，
	// 下面第一条断言才是这次变异的靶心。
	_, err := k.Execute(ctxAs(editor()), action.Request{
		ActionID: ActionChannelSet, ActionVersion: actionVersion,
		RequestID: "req-plain", Reason: "登记市场部账号",
		Params: map[string]any{
			"platform": "x", "handle": "@leaky",
			"credential_ref": pastedLooksLikeToken,
		},
	})

	// **这一条是渠道登记定成 L1 的理由**（见 channelSetDefinition），所以它排在
	// 最前面：L2+ 会在 Handler 之前把参数冻进 core.approval_request.params_json，
	// 于是这个串会永久落库、还会显示给审批人——比放行更糟。
	//
	// 变异验证：把 channelSetDefinition 的 RiskLevel 改成 action.L2，本用例红在
	// **这一行**（submitted=1）。已实测。
	if len(gw.submitted) != 0 {
		t.Fatalf("被拒绝的明文串被冻进了 %d 张审批单的 params：那等于永久落库",
			len(gw.submitted))
	}
	if code := action.ErrorCode(err); code != action.CodeInvalidParams {
		t.Fatalf("粘明文应当拿 INVALID_PARAMS，got %q（%v）", code, err)
	}
	if strings.Contains(err.Error(), pastedLooksLikeToken) {
		t.Fatalf("错误信息回显了被拒绝的串——那会把它写进审计与日志: %v", err)
	}
	if !strings.Contains(err.Error(), "不要粘贴凭据本身") {
		t.Fatalf("错误应当说清该填什么，got %v", err)
	}
	// 渠道也不该被建出来（这个用例没有种子渠道，所以应当是 0 条）。
	if chans, _ := store.ListChannels(context.Background(), 10); len(chans) != 0 {
		t.Fatalf("被拒绝的登记不该留下渠道行，got %d 条", len(chans))
	}
}

// 素材地址必须是 https。
//
// 正向锚点在前：https 的地址**存得进去**，否则「http 被拒」可能只是因为这条路
// 谁都走不通。
func TestAssetURIMustBeHTTPS(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil, testClock())
	k, _ := kernelWith(t, svc)
	ctx := ctxAs(editor())

	if _, err := k.Execute(ctx, action.Request{
		ActionID: ActionAssetSet, ActionVersion: actionVersion, RequestID: "req-ok",
		Params: map[string]any{
			"name": "合法素材", "kind": "image", "uri": "https://cdn.example.com/a.png",
		},
	}); err != nil {
		t.Fatalf("https 的素材地址应当存得进去: %v", err)
	}

	_, err := k.Execute(ctx, action.Request{
		ActionID: ActionAssetSet, ActionVersion: actionVersion, RequestID: "req-bad",
		Params: map[string]any{
			"name": "明文素材", "kind": "image", "uri": "http://cdn.example.com/a.png",
		},
	})
	if code := action.ErrorCode(err); code != action.CodeInvalidParams {
		t.Fatalf("http 的素材地址应当被拒，got %q（%v）", code, err)
	}
}

// 七个 Action 都只给人，一个都不给机器身份。
//
// 遍历注册表而不是逐个点名：新增一个 Action 忘了限制身份类型时，这条会红。
func TestAllPublishingActionsAreHumanOnly(t *testing.T) {
	reg := action.NewRegistry()
	if err := RegisterActions(reg, NewService(newMemStore(), nil, testClock())); err != nil {
		t.Fatalf("RegisterActions: %v", err)
	}
	defs := reg.List()
	if len(defs) != 7 {
		t.Fatalf("内容发布应当注册 7 个 Action，got %d", len(defs))
	}
	for _, def := range defs {
		if !strings.HasPrefix(def.ID, "publishing.") {
			t.Fatalf("注册表里混进了非本域的 Action: %s", def.ID)
		}
		if len(def.PrincipalTypes) != 1 || def.PrincipalTypes[0] != principal.TypeHuman {
			t.Fatalf("%s 的 PrincipalTypes 应当只有 HUMAN，got %v", def.ID, def.PrincipalTypes)
		}
	}
}
