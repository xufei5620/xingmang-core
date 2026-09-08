package publishing_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/publishing"
)

const testEnv = "staging"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newStore 给每个用例一个仓储，并在结束时清掉自己写的行。
//
// 测试库是 worktree 独享的，但同一次跑里的用例之间仍然共享——不清会互相看到
// 对方的草稿与渠道（approval 包的集成用例踩过同一个坑）。
func newStore(t *testing.T, pool *pgxpool.Pool) *publishing.PgStore {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM publishing.publish_record WHERE environment=$1`,
			`DELETE FROM publishing.draft_asset WHERE draft_id IN
				(SELECT id FROM publishing.draft WHERE environment=$1)`,
			`DELETE FROM publishing.draft_revision WHERE draft_id IN
				(SELECT id FROM publishing.draft WHERE environment=$1)`,
			`DELETE FROM publishing.draft WHERE environment=$1`,
			`DELETE FROM publishing.asset WHERE environment=$1`,
			`DELETE FROM publishing.channel WHERE environment=$1`,
		} {
			if _, err := pool.Exec(ctx, stmt, testEnv); err != nil {
				t.Logf("清理失败（不影响本用例结论）: %v", err)
			}
		}
	})
	return publishing.NewPgStore(pool, testEnv)
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("解析时刻 %q: %v", s, err)
	}
	return ts.UTC()
}

// 草稿的每一次保存都落一版不可变修订，版本号单调递增。
func TestDraftRevisionsAreImmutableAndVersioned(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, testPool(t))
	now := at(t, "2026-09-08T03:00:00Z")

	v1, err := store.SaveDraft(ctx, publishing.DraftInput{
		Title: "第一版标题", Body: "第一版正文", Note: "初稿",
		Actor: "editor-1", Now: now,
	})
	if err != nil {
		t.Fatalf("SaveDraft v1: %v", err)
	}
	if v1.CurrentVersion != 1 || v1.Status != publishing.DraftDraft {
		t.Fatalf("第一版应当是 version=1 / DRAFT，got %+v", v1)
	}

	scheduled := at(t, "2026-10-01T02:00:00Z")
	v2, err := store.SaveDraft(ctx, publishing.DraftInput{
		ID: &v1.ID, Title: "第二版标题", Body: "第二版正文",
		ScheduledAt: &scheduled, Note: "改了标题并排期",
		Actor: "editor-2", Now: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("SaveDraft v2: %v", err)
	}
	if v2.CurrentVersion != 2 {
		t.Fatalf("第二版应当是 version=2，got %d", v2.CurrentVersion)
	}
	if v2.Status != publishing.DraftScheduled {
		t.Fatalf("给了排期时间就该是 SCHEDULED，got %s", v2.Status)
	}

	revs, err := store.ListRevisions(ctx, v1.ID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("应当有两版修订，got %d", len(revs))
	}
	// 要害：**第一版没有被第二次保存改写**。发布记录钉的是 (draft_id, version)，
	// 改了草稿不该让已经提交审批的那一版变样。
	if revs[0].Version != 2 || revs[1].Version != 1 {
		t.Fatalf("修订应当新的在前，got %d / %d", revs[0].Version, revs[1].Version)
	}
	if revs[1].Title != "第一版标题" || revs[1].Body != "第一版正文" {
		t.Fatalf("第一版修订被改写了: %+v", revs[1])
	}
	if revs[1].CreatedBy != "editor-1" || revs[0].CreatedBy != "editor-2" {
		t.Fatalf("修订没有记住各自是谁写的: %q / %q", revs[1].CreatedBy, revs[0].CreatedBy)
	}
}

// 归档的草稿不再接受新版本，且从日历上撤下来。
func TestArchivedDraftRejectsFurtherEdits(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, testPool(t))
	now := at(t, "2026-09-08T03:00:00Z")
	scheduled := at(t, "2026-10-01T02:00:00Z")

	d, err := store.SaveDraft(ctx, publishing.DraftInput{
		Title: "要归档的稿", ScheduledAt: &scheduled, Actor: "editor-1", Now: now,
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	archived, err := store.ArchiveDraft(ctx, d.ID, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("ArchiveDraft: %v", err)
	}
	if archived.Status != publishing.DraftArchived {
		t.Fatalf("状态应当是 ARCHIVED，got %s", archived.Status)
	}
	// 归档同时撤下排期：一条已归档却还占着日历格子的草稿会让人以为它还要发。
	if archived.ScheduledAt != nil {
		t.Fatalf("归档应当清掉排期，got %v", archived.ScheduledAt)
	}

	_, err = store.SaveDraft(ctx, publishing.DraftInput{
		ID: &d.ID, Title: "偷偷改一版", Actor: "editor-1", Now: now.Add(2 * time.Hour),
	})
	if !errors.Is(err, publishing.ErrNotFound) {
		t.Fatalf("归档的草稿不该接受新版本，got %v", err)
	}
	// 反面确认：**没有**因为这次被拒而多出一版修订。
	revs, err := store.ListRevisions(ctx, d.ID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("被拒的保存不该留下修订，got %d 版", len(revs))
	}
}

// 内容日历按排期区间过滤（左闭右开），过滤在 SQL 里做。
func TestListDraftsFiltersByScheduleWindow(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, testPool(t))
	now := at(t, "2026-09-08T03:00:00Z")

	mk := func(title, when string) {
		t.Helper()
		var sched *time.Time
		if when != "" {
			ts := at(t, when)
			sched = &ts
		}
		if _, err := store.SaveDraft(ctx, publishing.DraftInput{
			Title: title, ScheduledAt: sched, Actor: "editor-1", Now: now,
		}); err != nil {
			t.Fatalf("SaveDraft %s: %v", title, err)
		}
	}
	mk("九月末", "2026-09-30T10:00:00Z")
	mk("十月初", "2026-10-01T10:00:00Z")
	mk("十月末", "2026-10-31T10:00:00Z")
	mk("十一月", "2026-11-01T10:00:00Z")
	mk("没排期", "")

	got, err := store.ListDrafts(ctx, publishing.DraftFilter{
		ScheduledFrom: at(t, "2026-10-01T00:00:00Z"),
		ScheduledTo:   at(t, "2026-11-01T00:00:00Z"),
	})
	if err != nil {
		t.Fatalf("ListDrafts: %v", err)
	}
	titles := make([]string, 0, len(got))
	for _, d := range got {
		titles = append(titles, d.Title)
	}
	want := "十月初,十月末"
	if strings.Join(sorted(titles), ",") != want {
		t.Fatalf("十月这一屏应当只有 %q，got %q", want, strings.Join(sorted(titles), ","))
	}
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// 仍被草稿引用的素材删不掉（库层 RESTRICT），移除引用之后删得掉。
//
// 后半段是正向锚点：没有它，「删不掉」可能只是因为删除这条路根本不通。
func TestAssetStillReferencedCannotBeRemoved(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, testPool(t))
	now := at(t, "2026-09-08T03:00:00Z")

	asset, err := store.UpsertAsset(ctx, publishing.Asset{
		Name: "配图", Kind: publishing.AssetImage,
		URI: "https://cdn.example.com/a.png", CreatedBy: "editor-1", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("UpsertAsset: %v", err)
	}
	draft, err := store.SaveDraft(ctx, publishing.DraftInput{
		Title: "带图的稿", AssetIDs: []uuid.UUID{asset.ID}, Actor: "editor-1", Now: now,
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	err = store.RemoveAsset(ctx, asset.ID)
	if !errors.Is(err, publishing.ErrConflict) {
		t.Fatalf("仍被引用的素材应当删不掉（ErrConflict），got %v", err)
	}

	// 把引用去掉（保存一版不带素材的），再删——这一段证明上面那条红不是因为
	// 删除本身走不通。
	if _, err = store.SaveDraft(ctx, publishing.DraftInput{
		ID: &draft.ID, Title: "带图的稿", Actor: "editor-1", Now: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("SaveDraft（去掉素材）: %v", err)
	}
	if err = store.RemoveAsset(ctx, asset.ID); err != nil {
		t.Fatalf("去掉引用之后应当删得掉: %v", err)
	}
	if _, err = store.GetAsset(ctx, asset.ID); !errors.Is(err, publishing.ErrNotFound) {
		t.Fatalf("素材应当已经不在，got %v", err)
	}
}

// 库层拦住直接粘进来的明文凭据。
//
// 应用层已经有一道（NormalizeCredentialRef），这条测的是**库层那一道**——
// 绕开 Action 的任何路径（修数据脚本、将来的批量导入）同样拦得住。
func TestChannelCredentialRefRejectsPlaintextAtDatabaseLayer(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, testPool(t))
	now := at(t, "2026-09-08T03:00:00Z")

	// 正向锚点：合法引用**存得进去**。
	ok, err := store.UpsertChannel(ctx, publishing.Channel{
		Platform: publishing.PlatformX, Handle: "@good", Status: publishing.ChannelActive,
		CredentialRef: "secret://publishing-x/mkt-token", CreatedBy: "editor-1", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("合法引用应当存得进去: %v", err)
	}
	if ok.CredentialRef != "secret://publishing-x/mkt-token" {
		t.Fatalf("引用被改写了: %q", ok.CredentialRef)
	}

	// 形状像 token 的假串（不是任何真实凭据）。绕过应用层校验直接写仓储。
	_, err = store.UpsertChannel(ctx, publishing.Channel{
		Platform: publishing.PlatformX, Handle: "@bad", Status: publishing.ChannelActive,
		CredentialRef: "AAAAAAAAAAAAAAAAAAAAA-not-a-real-token",
		CreatedBy:     "editor-1", CreatedAt: now,
	})
	if !errors.Is(err, publishing.ErrInvalidInput) {
		t.Fatalf("库层应当拒绝非引用形状的凭据，got %v", err)
	}
}

// 库层拦住一条「假装发出去了」的发布记录。
//
// 这是「平台没有出站投递器」在**数据层**的那一道保证：即便有人绕开 Service
// 直接写记录，带着平台返回编号或投递时刻的行也进不去（迁移 000052 的
// publish_record_not_delivered_shape 与 result 闭集）。
func TestPublishRecordRejectsDeliveredShape(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, testPool(t))
	now := at(t, "2026-09-08T03:00:00Z")

	channel, err := store.UpsertChannel(ctx, publishing.Channel{
		Platform: publishing.PlatformX, Handle: "@xingmang",
		Status: publishing.ChannelActive, CreatedBy: "editor-1", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	draft, err := store.SaveDraft(ctx, publishing.DraftInput{
		Title: "要发的稿", Actor: "editor-1", Now: now,
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	base := publishing.PublishRecord{
		DraftID: draft.ID, DraftVersion: draft.CurrentVersion, ChannelID: channel.ID,
		RequestedBy: "publisher-1", Result: publishing.ResultNotDelivered,
		Detail: publishing.NotDeliveredReason, CreatedAt: now,
	}

	// 正向锚点：一条老实的「未投递」记录**存得进去**。没有它，下面两段的红
	// 可能只是因为这张表根本写不进。
	saved, err := store.CreatePublishRecord(ctx, base)
	if err != nil {
		t.Fatalf("未投递的记录应当存得进去: %v", err)
	}
	if saved.Result != publishing.ResultNotDelivered || saved.Detail != publishing.NotDeliveredReason {
		t.Fatalf("落库的记录不对: %+v", saved)
	}

	// 带平台返回编号的「未投递」——库层 CHECK 该拒。
	withRef := base
	withRef.ID = uuid.Nil
	withRef.ExternalRef = "1234567890"
	if _, err = store.CreatePublishRecord(ctx, withRef); !errors.Is(err, publishing.ErrInvalidInput) {
		t.Fatalf("带平台返回编号的未投递记录应当被库层拒绝，got %v", err)
	}

	// 带投递时刻的「未投递」——同上。
	withTime := base
	withTime.ID = uuid.Nil
	delivered := now.Add(time.Minute)
	withTime.DeliveredAt = &delivered
	if _, err = store.CreatePublishRecord(ctx, withTime); !errors.Is(err, publishing.ErrInvalidInput) {
		t.Fatalf("带投递时刻的未投递记录应当被库层拒绝，got %v", err)
	}

	// 一个不在闭集里的结果——result 的 CHECK 该拒。这一条钉住「接上投递器
	// 必须经过一次显式迁移」，而不是往枚举里悄悄补两个词。
	fake := base
	fake.ID = uuid.Nil
	fake.Result = "DELIVERED"
	if _, err = store.CreatePublishRecord(ctx, fake); !errors.Is(err, publishing.ErrInvalidInput) {
		t.Fatalf("result 只该认 NOT_DELIVERED，got %v", err)
	}

	// 结尾再确认一次：这张表里只有那一条老实记录。
	all, err := store.ListPublishRecords(ctx, 10)
	if err != nil {
		t.Fatalf("ListPublishRecords: %v", err)
	}
	if len(all) != 1 || all[0].ID != saved.ID {
		t.Fatalf("被拒的三条里有落库的: %+v", all)
	}
}

// 渠道按 (environment, platform, handle) 去重；重复登记是更新而不是新增一行。
func TestChannelUpsertIsIdempotentOnIdentity(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, testPool(t))
	now := at(t, "2026-09-08T03:00:00Z")

	first, err := store.UpsertChannel(ctx, publishing.Channel{
		Platform: publishing.PlatformX, Handle: "@xingmang", Purpose: "产品公告",
		Status: publishing.ChannelActive, CreatedBy: "editor-1", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}
	// 先暂停它，再用同样的身份重新登记一次。
	if _, err = store.SetChannelStatus(ctx, first.ID, publishing.ChannelPaused, now); err != nil {
		t.Fatalf("SetChannelStatus: %v", err)
	}
	second, err := store.UpsertChannel(ctx, publishing.Channel{
		Platform: publishing.PlatformX, Handle: "@xingmang", Purpose: "改成市场活动",
		Status: publishing.ChannelActive, CreatedBy: "editor-2", CreatedAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("UpsertChannel（第二次）: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("同一个账号应当更新原行而不是新增，%s vs %s", second.ID, first.ID)
	}
	if second.Purpose != "改成市场活动" {
		t.Fatalf("用途没更新: %q", second.Purpose)
	}
	// 要害：一次「改用途」的保存**没有**把被暂停的渠道悄悄恢复成 ACTIVE。
	// 暂停通常正是因为出了事，启停只走 SetChannelStatus。
	if second.Status != publishing.ChannelPaused {
		t.Fatalf("重新登记不该改动状态，got %s", second.Status)
	}
	all, err := store.ListChannels(ctx, 10)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("应当只有一行渠道，got %d", len(all))
	}
}
