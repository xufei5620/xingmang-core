package publishing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 本文件钉住本片对使用者最重要的一句交代：**平台没有出站投递器，内容发不出去。**
//
// 这是一条缺席型主张，所以每一条用例都写清了「变异什么会让它变红」，并且那次
// 变异真的跑过（见文件末尾的 TestAbsenceAssertionsAreMutationChecked）。
// 不做变异验证的缺席断言等于恒真——它只是把「我没写那段代码」重复了一遍。

func testClock() func() time.Time {
	at := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	return func() time.Time { return at }
}

// stubDeliverer 只在**变异验证**里被注册。生产装配传 nil 表；本仓库没有任何
// 真实 Deliverer 实现。
type stubDeliverer struct {
	platform Platform
	calls    int
}

func (s *stubDeliverer) Platform() Platform { return s.platform }

func (s *stubDeliverer) Deliver(context.Context, Delivery) (Receipt, error) {
	s.calls++
	return Receipt{ExternalRef: "stub-1", DeliveredAt: time.Now().UTC()}, nil
}

// seedPublishable 建一条草稿 + 一个 ACTIVE 渠道，返回两者的 id。
func seedPublishable(t *testing.T, store *memStore, platform Platform) (uuid.UUID, uuid.UUID) {
	t.Helper()
	now := testClock()()
	draft, err := store.SaveDraft(context.Background(), DraftInput{
		Title: "十月产品更新", Body: "本月上线了三项能力。", Actor: "ops-1", Now: now,
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	channel, err := store.UpsertChannel(context.Background(), Channel{
		Platform: platform, Handle: "@xingmang", Status: ChannelActive,
		CreatedBy: "ops-1", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	return draft.ID, channel.ID
}

// 每一个已登记的平台都没有出站投递器。
//
// 遍历 AllPlatforms 而不是只点名 X：新增一个平台时这条用例自动覆盖到它，
// 不靠作者记得来补一行。
//
// **变异验证**：往 NewService 的 deliverers 传 {PlatformX: &stubDeliverer{}}，
// 本用例在 x 这一档 Fatal「登记了出站投递器」。已实测。
func TestNoOutboundDelivererIsRegistered(t *testing.T) {
	svc := NewService(newMemStore(), nil, testClock())

	for _, p := range AllPlatforms {
		if d := svc.DelivererFor(p); d != nil {
			t.Fatalf("平台 %s 登记了出站投递器 %T：本仓库不该有任何 Deliverer 实现", p, d)
		}
	}
	if got := svc.PlatformsWithDeliverer(); len(got) != 0 {
		t.Fatalf("PlatformsWithDeliverer 应为空，got %v", got)
	}
}

// 一次发布落下的记录，结果是「未投递」，且**逐字**带着那句解释。
//
// 先 await 正向锚点（记录确实落了、id 非空）再断言「没投递」，不是反过来：
// 一条根本没落库的记录当然也「没投递」，那种绿是假的。
//
// 只断枚举值不够，文案也逐字断——把「没有这个能力」写软成「暂未投递」
// 「投递中」是最容易发生的一次退化，而枚举值不会跟着变。
//
// **变异验证**：把 Service.Publish 里 record.Detail 改成 "投递中"，本用例在
// 「detail 应逐字等于」那一行变红（不是在锚点上）。已实测。
func TestPublishRecordsNotDeliveredOutcome(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil, testClock())
	draftID, channelID := seedPublishable(t, store, PlatformX)

	outcome, err := svc.Publish(context.Background(), PublishRequest{
		DraftID: draftID, ChannelID: channelID, RequestedBy: "ops-1",
	})
	// 正向锚点：这次调用**成功了**，并且真的落了一条记录。
	if err != nil {
		t.Fatalf("Publish 应当成功受理（未投递不是失败）: %v", err)
	}
	if outcome.Record.ID == uuid.Nil {
		t.Fatal("发布记录没有 id：这次调用没有真的落库")
	}
	saved, err := store.ListPublishRecords(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListPublishRecords: %v", err)
	}
	if len(saved) != 1 || saved[0].ID != outcome.Record.ID {
		t.Fatalf("落库的记录与返回的对不上: %+v", saved)
	}

	// 锚点站住了，下面才是这条用例真正要证明的事。
	if outcome.Delivered {
		t.Fatal("Delivered 为真：平台没有出站投递器，不可能发出去")
	}
	if saved[0].Result != ResultNotDelivered {
		t.Fatalf("result 应为 %s，got %s", ResultNotDelivered, saved[0].Result)
	}
	if saved[0].Detail != NotDeliveredReason {
		t.Fatalf("detail 应逐字等于\n  %q\ngot\n  %q", NotDeliveredReason, saved[0].Detail)
	}
	if outcome.Reason != NotDeliveredReason {
		t.Fatalf("返回体的 reason 应逐字等于\n  %q\ngot\n  %q", NotDeliveredReason, outcome.Reason)
	}
	// 平台返回编号与投递时刻必须为空：一条带着编号的「未投递」记录会让人
	// 以为发出去了。库层 CHECK 表达同一条规则（迁移 000052）。
	if saved[0].ExternalRef != "" || saved[0].DeliveredAt != nil {
		t.Fatalf("未投递的记录不该带平台返回编号或投递时刻: %+v", saved[0])
	}
	if saved[0].Delivered() {
		t.Fatal("PublishRecord.Delivered() 为真")
	}
}

// 那句话不能被改软。
//
// 这条用例专门盯着**措辞**而不是行为：「暂未」「投递中」「稍后」都是把
// 「平台没有这个能力」说成「这一次没成功」，而后者会让运营去重试、去查网络、
// 去问是不是凭据配错了。
//
// **变异验证**：把 NotDeliveredReason 改成「投递中，请稍后查看」，本用例在
// 「不该出现软化措辞」那一行变红。已实测。
func TestNotDeliveredReasonSaysTheCapabilityIsMissing(t *testing.T) {
	for _, must := range []string{"未投递", "尚无出站投递器", "没有发送到任何外部平台"} {
		if !strings.Contains(NotDeliveredReason, must) {
			t.Fatalf("那句解释里应当出现 %q，实际是\n  %q", must, NotDeliveredReason)
		}
	}
	for _, forbidden := range []string{"暂未", "投递中", "稍后", "重试"} {
		if strings.Contains(NotDeliveredReason, forbidden) {
			t.Fatalf("那句解释里不该出现软化措辞 %q（会让人以为是这一次失败），实际是\n  %q",
				forbidden, NotDeliveredReason)
		}
	}
}

// 变异验证的对照组 + 靶心：注册一个假投递器之后，缺席那两条**必须**变红。
//
// 这条用例把变异做成常驻代码而不是一次性手工操作，理由是本仓库栽过的那条：
// 「缺席型断言一律变异验证，不红就是恒真」——手工验过一次，下一个人重写实现
// 时不会再验一次。这里让它每次跑测都验一遍。
//
// 三段：
//  1. 靶心：注册假投递器后 DelivererFor 不再是 nil（缺席断言的前提被推翻），
//     且 PlatformsWithDeliverer 报出它——那正是 TestNoOutboundDelivererIsRegistered
//     会 Fatal 的两处；
//  2. 靶心：Publish 走进了投递分支并**拒绝**落一条假装发出去的记录——那正是
//     TestPublishRecordsNotDeliveredOutcome 的锚点会 Fatal 的地方；
//  3. **对照组**：同一份代码、不注册投递器时，两处都回到缺席态。没有这一段，
//     上面两段可能只是因为「这个用例怎么写都红/都绿」。
func TestAbsenceAssertionsAreMutationChecked(t *testing.T) {
	t.Run("注册投递器之后缺席断言不再成立", func(t *testing.T) {
		store := newMemStore()
		stub := &stubDeliverer{platform: PlatformX}
		svc := NewService(store, map[Platform]Deliverer{PlatformX: stub}, testClock())
		draftID, channelID := seedPublishable(t, store, PlatformX)

		if svc.DelivererFor(PlatformX) == nil {
			t.Fatal("变异没生效：注册了投递器却查不到，那么缺席用例的绿说明不了任何事")
		}
		if got := svc.PlatformsWithDeliverer(); len(got) != 1 || got[0] != PlatformX {
			t.Fatalf("变异没生效：PlatformsWithDeliverer = %v", got)
		}

		_, err := svc.Publish(context.Background(), PublishRequest{
			DraftID: draftID, ChannelID: channelID, RequestedBy: "ops-1",
		})
		if err == nil {
			t.Fatal("登记了投递器却仍然落了一条「未投递」记录：投递分支是死代码，缺席用例证明不了控制流")
		}
		if !strings.Contains(err.Error(), "尚未接通") {
			t.Fatalf("错误应说清缺的是投递链路（幂等/重试/库层枚举），got %v", err)
		}
		// 半接上的投递不许留下任何记录——否则运营会在发布记录里看到一条
		// 结果不明的行。
		if recs, _ := store.ListPublishRecords(context.Background(), 10); len(recs) != 0 {
			t.Fatalf("投递链路没接通时不该落记录，got %d 条", len(recs))
		}
		if stub.calls != 0 {
			t.Fatalf("投递链路没接通就不该真的调投递器，calls=%d", stub.calls)
		}
	})

	t.Run("对照组：不注册投递器时一切照旧", func(t *testing.T) {
		store := newMemStore()
		svc := NewService(store, nil, testClock())
		draftID, channelID := seedPublishable(t, store, PlatformX)

		if svc.DelivererFor(PlatformX) != nil {
			t.Fatal("对照组里不该有投递器")
		}
		outcome, err := svc.Publish(context.Background(), PublishRequest{
			DraftID: draftID, ChannelID: channelID, RequestedBy: "ops-1",
		})
		if err != nil {
			t.Fatalf("对照组的 Publish 不该失败: %v", err)
		}
		if outcome.Delivered || outcome.Record.Result != ResultNotDelivered {
			t.Fatalf("对照组应当落一条未投递记录, got %+v", outcome)
		}
	})
}

// 暂停/退役的渠道不接受发布。
//
// 这条不是缺席断言，是正向的：先证明 ACTIVE 时**发得成**（否则「暂停时发不成」
// 可能只是因为这条路谁都走不通），再证明另外两态各自被拒。
func TestPausedAndRetiredChannelsRejectPublish(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	svc := NewService(store, nil, testClock())
	draftID, channelID := seedPublishable(t, store, PlatformX)

	if _, err := svc.Publish(ctx, PublishRequest{
		DraftID: draftID, ChannelID: channelID, RequestedBy: "ops-1",
	}); err != nil {
		t.Fatalf("ACTIVE 渠道应当发得成（否则下面两段的红说明不了什么）: %v", err)
	}

	for _, status := range []ChannelStatus{ChannelPaused, ChannelRetired} {
		if _, err := store.SetChannelStatus(ctx, channelID, status, testClock()()); err != nil {
			t.Fatalf("SetChannelStatus(%s): %v", status, err)
		}
		_, err := svc.Publish(ctx, PublishRequest{
			DraftID: draftID, ChannelID: channelID, RequestedBy: "ops-1",
		})
		if err == nil {
			t.Fatalf("%s 的渠道不该接受发布", status)
		}
		if !strings.Contains(err.Error(), "channel is not accepting content") {
			t.Fatalf("%s 应当报渠道不可发布，got %v", status, err)
		}
	}
}

// 归档的草稿不能发布。
func TestArchivedDraftCannotBePublished(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	svc := NewService(store, nil, testClock())
	draftID, channelID := seedPublishable(t, store, PlatformX)

	if _, err := store.ArchiveDraft(ctx, draftID, testClock()()); err != nil {
		t.Fatalf("ArchiveDraft: %v", err)
	}
	_, err := svc.Publish(ctx, PublishRequest{
		DraftID: draftID, ChannelID: channelID, RequestedBy: "ops-1",
	})
	if err == nil || !strings.Contains(err.Error(), "草稿已归档") {
		t.Fatalf("归档的草稿不该能发布，got %v", err)
	}
}
