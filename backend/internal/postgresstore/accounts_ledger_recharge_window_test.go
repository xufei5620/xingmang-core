package postgresstore

import (
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

// XM-INV-LEDGER-RECHARGE-POLICY-START
//
// 「起点后充值」曾经按**每个账号自己的** cutover_at 过滤，而同一行的已消耗/
// 可开票/已开票三列没有任何日期过滤——它们靠迁移 0016 的库级约束保证「现金批次
// 一定不早于全局策略起点」。于是同一笔钱进得了可开票的分母，却进不了充值那一列。
//
// 生产上真实撞到过：某账号的 cutover 落在 2026-09-01 20:14，而他当天下午 17:03
// 与 17:07 充的两笔已核验现金显示为「起点后充值 ¥0.00 / 0 笔」；一旦他开始消耗，
// 那一行就会变成「起点后充值 ¥0，可开票 ¥5」——凭空冒出来的可开票额。
//
// 夹具说明：批次的 completed_at 首次观测后不可改（enforce_funding_lot_invoice_
// policy 会拒绝 UPDATE），所以这里**直接以早于 cutover 的完成时刻插入**第二笔，
// 而不是事后挪动第一笔。两笔都仍然晚于全局策略起点，0016 的约束照样满足——
// 这正是要区分的两件事。
//
// 做过变异验证：把 completed_at>=cutover_at 加回汇总或逐笔清单任何一处，
// 对应断言立刻变红。
func TestRechargesCountLotsCompletedBeforeTheAccountCutover(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID := ledgerUUID('1', 760)
	if _, err := store.pool.Exec(ctx,
		`INSERT INTO source_instances(id,source_type,name) VALUES($1,'sub2api','ledger-recharge-window')`, sourceID); err != nil {
		t.Fatal(err)
	}
	const threshold = 20_000

	accountID := ledgerUUID('3', 761)
	lotAfterID := seedLedgerAccount(t, store, ctx, sourceID, accountID, "recharge-window-1", 110_000)

	var cutoverAt time.Time
	if err := store.pool.QueryRow(ctx,
		`SELECT cutover_at FROM source_account_eligibility_state WHERE external_account_id=$1`, accountID).Scan(&cutoverAt); err != nil {
		t.Fatal(err)
	}
	userID := ledgerUUID('2', accountSuffix(accountID))

	// 第二笔：完成于该账号 cutover **之前**三小时——正是生产上那两笔的形状。
	beforeCutover := cutoverAt.Add(-3 * time.Hour)
	lotBeforeID := ledgerUUID('6', accountSuffix(accountID))
	if err := store.UpsertFundingLot(ctx, domain.FundingLot{
		ID: lotBeforeID, PrincipalID: userID, SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
		ExternalOrderID: "order-before-" + accountID, Currency: domain.CurrencyCNY,
		OriginalMinor: 40_000, CurrentCapMinor: 40_000,
		Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
		SourceRevision: testHash("ledger-before-" + accountID),
		CompletedAt:    beforeCutover, ObservedAt: time.Now(),
	}); err != nil {
		t.Fatalf("插入完成于 cutover 之前的现金批次失败（若被 0016 拒绝，说明这个夹具本身站不住）：%v", err)
	}
	var stored time.Time
	if err := store.pool.QueryRow(ctx, `SELECT completed_at FROM funding_lots WHERE id=$1`, lotBeforeID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Before(cutoverAt) {
		t.Fatalf("夹具没有生效：completed_at=%s 并不早于 cutover_at=%s", stored, cutoverAt)
	}

	t.Run("汇总把它算进起点后充值", func(t *testing.T) {
		page, err := store.ListAccountLedgerPage(ctx, AccountLedgerPageQuery{Limit: 50, ThresholdMinor: threshold})
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, item := range page.Items {
			if item.ExternalAccountID != accountID {
				continue
			}
			found = true
			if item.RechargesSinceStartCount != 2 || item.RechargesSinceStartMinor != 150_000 {
				t.Fatalf("cutover 之前完成的现金批次被漏算了：count=%d minor=%d（期望 2 / 150000）",
					item.RechargesSinceStartCount, item.RechargesSinceStartMinor)
			}
		}
		if !found {
			t.Fatal("账本列表里找不到这个账号")
		}
	})

	t.Run("逐笔清单与汇总同口径", func(t *testing.T) {
		detail, err := store.GetAccountLedgerDetail(ctx, accountID, threshold)
		if err != nil {
			t.Fatal(err)
		}
		// 汇总说几笔，清单就得有几笔——这两处必须是同一个谓词。
		if int(detail.RechargesSinceStartCount) != len(detail.Recharges) {
			t.Fatalf("汇总 %d 笔，清单 %d 笔", detail.RechargesSinceStartCount, len(detail.Recharges))
		}
		seen := map[string]int64{}
		for _, r := range detail.Recharges {
			seen[r.FundingLotID] = r.AmountMinor
		}
		if seen[lotBeforeID] != 40_000 {
			t.Fatalf("清单里缺少 cutover 之前那一笔：%+v", detail.Recharges)
		}
		if seen[lotAfterID] != 110_000 {
			t.Fatalf("清单里缺少 cutover 之后那一笔：%+v", detail.Recharges)
		}
		// 升序：早的那笔必须排在前面。
		if detail.Recharges[0].FundingLotID != lotBeforeID {
			t.Fatalf("没有按 completed_at 升序：%+v", detail.Recharges)
		}
	})

	t.Run("详情单独报出账号的 cutover 时刻", func(t *testing.T) {
		detail, err := store.GetAccountLedgerDetail(ctx, accountID, threshold)
		if err != nil {
			t.Fatal(err)
		}
		if detail.CutoverAt.IsZero() {
			t.Fatal("cutover_at 没有被报出来——运营就无从区分「系统从哪天开始看这个账号」与「从哪天起的钱可以开票」")
		}
		if !detail.CutoverAt.UTC().Equal(cutoverAt.UTC()) {
			t.Fatalf("cutover_at=%s，库里是 %s", detail.CutoverAt, cutoverAt)
		}
	})
}
