package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/internal/platform/sms"
)

type stubSMSProber struct {
	results []sms.ProbeResult
	err     error
	calls   int
}

func (s *stubSMSProber) ProbeOnce(ctx context.Context) ([]sms.ProbeResult, error) {
	s.calls++
	return s.results, s.err
}

func TestSMSProbeWorkerRunsOneRound(t *testing.T) {
	prober := &stubSMSProber{results: []sms.ProbeResult{
		{Provider: "hero_sms", Verified: true, AmountText: "12.34"},
		{Provider: "sms62", Skipped: true, Reason: "未启用"},
	}}
	worker := NewSMSProbeWorker(nil, prober)

	if err := worker.Work(context.Background(), &river.Job[SMSProbeArgs]{Args: SMSProbeArgs{}}); err != nil {
		t.Fatalf("一轮巡检应成功: %v", err)
	}
	if prober.calls != 1 {
		t.Fatalf("应恰好跑一轮, got %d", prober.calls)
	}
}

// 上游失败已经写进 provider_status.last_error，页面上看得见；把它变成任务失败
// 只会让 River 重试同一个坏掉的凭据。所以领域层不抛，Worker 也不造错误。
func TestSMSProbeWorkerDoesNotFailOnProviderErrors(t *testing.T) {
	prober := &stubSMSProber{results: []sms.ProbeResult{
		{Provider: "hero_sms", Reason: "连接测试失败：401 unauthorized"},
	}}
	if err := NewSMSProbeWorker(nil, prober).Work(context.Background(), &river.Job[SMSProbeArgs]{Args: SMSProbeArgs{}}); err != nil {
		t.Fatalf("单家上游失败不该让任务失败: %v", err)
	}
}

// 落库失败（领域层抛出来的那种）要让任务失败，River 才会重试。
func TestSMSProbeWorkerFailsOnInfrastructureError(t *testing.T) {
	prober := &stubSMSProber{err: errors.New("落余额快照: 库满了")}
	if err := NewSMSProbeWorker(nil, prober).Work(context.Background(), &river.Job[SMSProbeArgs]{Args: SMSProbeArgs{}}); err == nil {
		t.Fatal("基础设施故障必须让任务失败")
	}
}

// 没绑定 prober 是装配漏了：要一个刺眼的失败，不是空指针崩溃。
func TestSMSProbeWorkerWithoutProberFails(t *testing.T) {
	if err := NewSMSProbeWorker(nil, nil).Work(context.Background(), &river.Job[SMSProbeArgs]{Args: SMSProbeArgs{}}); err == nil {
		t.Fatal("未绑定 prober 必须失败")
	}
}

func TestSMSProbeArgsUseMaintenanceQueueAndPeriodUniqueness(t *testing.T) {
	args := SMSProbeArgs{}
	if args.Kind() != SMSProbeJobKind {
		t.Fatalf("kind = %q, want %q", args.Kind(), SMSProbeJobKind)
	}
	opts := args.InsertOpts()
	if opts.Queue != QueueMaintenance {
		t.Errorf("queue = %q, want %q", opts.Queue, QueueMaintenance)
	}
	if !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue || opts.UniqueOpts.ByPeriod != DefaultSMSProbeInterval {
		t.Errorf("unique opts 应按 args+queue+period: %+v", opts.UniqueOpts)
	}
	if len(opts.UniqueOpts.ByState) != len(rivertype.UniqueOptsByStateDefault()) {
		t.Errorf("unique states 应与默认集一致: %+v", opts.UniqueOpts.ByState)
	}
	if DefaultSMSProbeInterval < time.Minute {
		t.Errorf("巡检周期不该短于一分钟: %s", DefaultSMSProbeInterval)
	}
}

// 部署态时刻表要认识这个任务：它的开关判据必须与 worker 里的判据同源
// （XM_SMS_MODE），否则那张表会显示一个与实际不符的状态。
func TestDeployedSchedulesCoverSMSProbe(t *testing.T) {
	env := map[string]string{"XM_SMS_MODE": "real", "XM_SMS_PROBE_INTERVAL": "15m"}
	got, err := DeployedSchedulesFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	row, ok := got[SMSProbeJobKind]
	if !ok {
		t.Fatalf("时刻表漏了 %s", SMSProbeJobKind)
	}
	if !row.Enabled || row.IntervalSeconds != 900 || row.EnabledSource != "XM_SMS_MODE=fake|real" {
		t.Fatalf("SMS 巡检的部署态不对: %+v", row)
	}

	off := map[string]string{"XM_SMS_MODE": "off"}
	got, err = DeployedSchedulesFromEnv(func(k string) string { return off[k] })
	if err != nil {
		t.Fatal(err)
	}
	if got[SMSProbeJobKind].Enabled {
		t.Fatalf("off 时不该启用: %+v", got[SMSProbeJobKind])
	}
}
