package action

import (
	"context"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 这个文件补齐 `reason` 那道闸的**另一半**：L0/L1 缺 reason 仍然照常执行。
//
// 已有的 `TestApprovalRequiresReason`（kernel_approval_test.go）盖住了前一半
// ——L2 缺 reason 拿 INVALID_PARAMS 且不落单。但**没有任何用例说过低等级不受
// 这道闸约束**，于是「把 reason 校验误加到所有等级上」这个改动可以一路绿灯
// 通过，而平台上绝大多数写操作是 L0/L1、它们的调用方从来不发 reason。
//
// 这条契约值得补票的由来：XM-RISK-RESTORE 把开卡、关停、提现与三个采购动作
// 恢复成 L2/L3 之后，普查发现管理端的统一执行函数从来不发 reason
// （`web/apps/admin-web/src/api/platform.ts`，全文件 grep 零命中），那六个
// 动作在界面上点了就是 INVALID_PARAMS。**后端这道闸本身是有测试的**——
// 漏掉的是「两侧」与跨前后端那一段，前者补在这里，后者属 XM-ACTION-REASON。

// reasonlessRequest 是一次**除了没有 reason 之外完全合法**的调用。
//
// 参数要真的过 Schema：否则「被拒」可能是因为参数不合法，那样用例会以错误的
// 理由绿，也以错误的理由红。okRequest() 本身就不带 Reason——它恰好就是前端
// 今天发出来的那个形状。
func reasonlessRequest() Request { return okRequest() }

func reasonCtx() context.Context {
	return principal.WithPrincipal(context.Background(), testPrincipal("registry.service.manage"))
}

// **L0/L1 缺 reason 仍然正常执行。**
//
// 这是本文件存在的理由。绝大多数 Action 是 L0/L1，把 reason 变成所有等级的
// 必填等于让平台上大部分写操作当场停摆——而在这条用例之前，那样改是绿的。
func TestLowRiskLevelsDoNotRequireReason(t *testing.T) {
	for _, lvl := range []RiskLevel{L0, L1} {
		t.Run(string(lvl), func(t *testing.T) {
			def := demoDefinition()
			def.RiskLevel = lvl
			called := false
			k, store := newTestKernel(t, def, func(context.Context, map[string]any) (any, error) {
				called = true
				return "done", nil
			})
			// 网关接着——证明「不要 reason」不是因为没接审批中心，
			// 而是因为这个等级本来就不过风险闸。
			k.approvals = &recordingGateway{id: "approval-1"}

			res, err := k.Execute(reasonCtx(), reasonlessRequest())
			if err != nil {
				t.Fatalf("%s 缺 reason 应当照常执行，got %v", lvl, err)
			}
			if !called {
				t.Fatalf("%s 的 Handler 应当被调用", lvl)
			}
			if res.Value != "done" {
				t.Fatalf("%s 应当返回 Handler 的结果，got %+v", lvl, res.Value)
			}
			if len(store.runs) != 1 || store.runs[0].Status != RunSucceeded {
				t.Fatalf("%s 应当记一条成功：%+v", lvl, store.runs)
			}
		})
	}
}

// L3/L4 与 L2 一样要 reason，且被拒时要留痕、要点名缺的是 reason。
//
// 与 `TestApprovalRequiresReason` 不重复的部分有三处：那条只跑 L2；
// 它不看错误话里说了什么；它不看被拒有没有进审计。
//
// 「点名 reason」不是措辞洁癖：缺的是一个**不在 Schema 里**的字段，
// 调用方拿到笼统的「参数不符合 Action Schema」会去翻 Schema，而翻不到。
func TestApprovalLevelsRejectMissingReason(t *testing.T) {
	for _, lvl := range []RiskLevel{L3, L4} {
		t.Run(string(lvl), func(t *testing.T) {
			def := demoDefinition()
			def.RiskLevel = lvl
			k, store := newTestKernel(t, def, func(context.Context, map[string]any) (any, error) {
				t.Fatal("Handler 不该被调用——这次调用连审批单都不该落")
				return nil, nil
			})
			gateway := &recordingGateway{id: "approval-1"}
			k.approvals = gateway

			_, err := k.Execute(reasonCtx(), reasonlessRequest())

			if code := ErrorCode(err); code != CodeInvalidParams {
				t.Fatalf("%s 缺 reason 应当拿 INVALID_PARAMS，got %q（%v）", lvl, code, err)
			}
			if !strings.Contains(err.Error(), "reason") {
				t.Fatalf("错误信息该点名 reason，got %q", err.Error())
			}
			if len(gateway.submissions) != 0 {
				t.Fatalf("缺 reason 的调用不该落单，got %+v", gateway.submissions)
			}
			// 被拒也要留痕：谁在什么时候试过做什么。
			if len(store.runs) != 1 || store.runs[0].ErrorCode != CodeInvalidParams {
				t.Fatalf("被拒也要留痕：%+v", store.runs)
			}
		})
	}
}
