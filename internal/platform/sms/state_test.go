package sms

import (
	"context"
	"testing"
	"time"
)

// 统一号码状态（ADR-022 决策 4，迁移 000042）。
//
// 上游原话（Hero 的 1/2/3/4/6/7/8/10，62 的「正常」）在页面上没有统一含义，
// 这里钉住两件事：官方状态码怎么归到五态，以及我们自己的事实（取到码、
// 取消成功、完成成功）什么时候改写它。

func TestMapHeroStatusFollowsOfficialCodes(t *testing.T) {
	cases := map[string]NumberState{
		"1":  StateWaitingCode,
		"2":  StateWaitingCode,
		"3":  StateWaitingCode,
		"4":  StateCodeReceived,
		"6":  StateFinished,
		"7":  StateExpired,
		"8":  StateCancelled,
		"10": StateCancelled,
		" 6": StateFinished, // 上游偶尔带空白
		"":   StateWaitingCode,
		"99": StateWaitingCode, // 没见过的值：更可能还在进行中
	}
	for in, want := range cases {
		if got := MapHeroStatus(in); got != want {
			t.Errorf("MapHeroStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

// 过期只由本地时钟判，且只对「待收码」生效。
func TestEffectiveStateOnlyExpiresWaiting(t *testing.T) {
	past := testNow.Add(-time.Minute)
	future := testNow.Add(time.Minute)
	cases := []struct {
		name string
		r    Resource
		want NumberState
	}{
		{"待收码且已过期限", Resource{State: StateWaitingCode, ExpiresAt: past}, StateExpired},
		{"待收码未到期限", Resource{State: StateWaitingCode, ExpiresAt: future}, StateWaitingCode},
		{"待收码没有期限", Resource{State: StateWaitingCode}, StateWaitingCode},
		{"已收码不会因时间变化", Resource{State: StateCodeReceived, ExpiresAt: past}, StateCodeReceived},
		{"已取消不会因时间变化", Resource{State: StateCancelled, ExpiresAt: past}, StateCancelled},
		{"旧数据未映射保持空", Resource{ExpiresAt: past}, ""},
	}
	for _, c := range cases {
		if got := c.r.EffectiveState(testNow); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// 取到码就是「已收码」——这是我们自己的事实，不等上游改状态（62 根本没有）。
func TestFetchCodeMarksCodeReceived(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{code: Code{Code: "123456", Sender: "OPENAI"}}
	svc := newService(t, adapter, store)
	id, _ := store.UpsertResource(context.Background(), Resource{
		Provider: ProviderSMS62, ExternalID: "fp-1", ProviderToken: "tok", State: StateWaitingCode,
	})

	if _, err := svc.FetchCode(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetResource(context.Background(), id)
	if got.State != StateCodeReceived {
		t.Fatalf("取到码后应为 code_received, got %q", got.State)
	}
}

// 还没有码不改状态：待收码就是待收码。
func TestFetchCodeNotAvailableKeepsWaiting(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{codeErr: ErrCodeNotAvailable}
	svc := newService(t, adapter, store)
	id, _ := store.UpsertResource(context.Background(), Resource{
		Provider: ProviderHero, ExternalID: "a1", State: StateWaitingCode,
	})

	_, _ = svc.FetchCode(context.Background(), id)
	got, _ := store.GetResource(context.Background(), id)
	if got.State != StateWaitingCode {
		t.Fatalf("没取到码不该改状态, got %q", got.State)
	}
}

// lifecycleFake 让取消 / 完成成功，并像真实 Hero 适配器那样回读一份带上游
// 状态的资源——这正是要钉住的坑：回读的 waiting_code 不能把我们刚写的
// cancelled 冲掉。
type lifecycleFake struct {
	fakeAdapter
	echoState NumberState
}

func (f *lifecycleFake) ExecuteAction(ctx context.Context, kind string, r Resource, opt ActionOptions) (Resource, error) {
	r.State = f.echoState
	return r, nil
}

func TestCancelAndFinishWriteUnifiedState(t *testing.T) {
	for kind, want := range map[string]NumberState{KindCancel: StateCancelled, KindFinish: StateFinished} {
		store := newMemStore()
		svc := newService(t, &lifecycleFake{echoState: StateWaitingCode}, store)
		id, _ := store.UpsertResource(context.Background(), Resource{
			Provider: ProviderHero, ExternalID: "a1", Status: "1", State: StateWaitingCode,
		})

		if _, err := svc.ExecuteAction(context.Background(), "op-"+kind, kind, id, ActionOptions{}); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		got, _ := store.GetResource(context.Background(), id)
		if got.State != want {
			t.Errorf("%s 成功后应为 %q, got %q", kind, want, got.State)
		}
		// 上游原话不动：那一列等下一次同步读回真值。
		if got.Status != "1" {
			t.Errorf("%s 不该改写上游原话 status, got %q", kind, got.Status)
		}
	}
}

// 取消失败不改状态。
func TestCancelFailureKeepsState(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &fakeAdapter{}, store) // ExecuteAction 一律 ErrActionNotSupported
	id, _ := store.UpsertResource(context.Background(), Resource{
		Provider: ProviderHero, ExternalID: "a1", State: StateWaitingCode,
	})

	_, _ = svc.ExecuteAction(context.Background(), "op-1", KindCancel, id, ActionOptions{})
	got, _ := store.GetResource(context.Background(), id)
	if got.State != StateWaitingCode {
		t.Fatalf("失败不该改状态, got %q", got.State)
	}
}
