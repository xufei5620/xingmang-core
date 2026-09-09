package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// XM-ASSURE1-core：渠道主动探测（检测任务）的进程装配。

// assuranceProbeGlobalKillSwitchFromEnv 解析 XM_ASSURE_PROBE_ENABLED（ADR-019
// 决策·四·#5 的全局 Kill Switch，默认 false）。与 cmd/platform-worker 对同一个
// 变量的解析各自独立（两个进程互不可见对方内存，XM_CONNECTOR_PROBE_ENABLED
// 的既有先例同理）：run@1 Action 接受时读到的是这里解析的值，Job 执行时刻
// 复检读到的是 worker 侧独立解析的同一个值，两侧必须配同一个环境变量值。
func assuranceProbeGlobalKillSwitchFromEnv(getenv func(string) string) (bool, error) {
	v := strings.TrimSpace(getenv("XM_ASSURE_PROBE_ENABLED"))
	if v == "" {
		return false, nil
	}
	enabled, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("XM_ASSURE_PROBE_ENABLED: %w", err)
	}
	return enabled, nil
}

// assuranceProbeDailyBudgetFromEnv 解析 XM_ASSURE_PROBE_DAILY_BUDGET（每平台
// 每日探测预算，env-overridable；0 表示未配置，交给
// assurance.DefaultDailyBudgetPerPlatform 补默认值 50）。
func assuranceProbeDailyBudgetFromEnv(getenv func(string) string) (int, error) {
	v := strings.TrimSpace(getenv("XM_ASSURE_PROBE_DAILY_BUDGET"))
	if v == "" {
		return 0, nil
	}
	budget, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("XM_ASSURE_PROBE_DAILY_BUDGET: %w", err)
	}
	if budget <= 0 {
		return 0, fmt.Errorf(
			"XM_ASSURE_PROBE_DAILY_BUDGET 必须为正，got %s（预算为 0 应使用 XM_ASSURE_PROBE_ENABLED=false 关闭探测）", v)
	}
	return budget, nil
}

// newAssuranceProbeInsertClient 构造一个**只插入、不处理**的 River 客户端。
//
// cmd/platform-api 从不运行任何 River Worker（那是 cmd/platform-worker 的
// 职责），它只需要在 assurance.probe.run@1 的 Handler 里，与 probe_run 行
// 插入同一个事务地把 assurance_probe Job 塞进队列
// （assurance.Store.EvaluateAndCreateRun → insertPendingRunAndEnqueue →
// river.Client.InsertTx）。river 的 Config 文档明确支持这种用法：
// "an insert-only client can be initialized by omitting Queues, and not
// calling Start"——Workers 同样可以省略（省略只是放弃了"插入时校验这个
// job kind 有没有对应 Worker"这一层健全性检查，本进程反正不运行 Worker，
// 这层检查在这里没有意义）。
func newAssuranceProbeInsertClient(pool *pgxpool.Pool, logger *slog.Logger) (*river.Client[pgx.Tx], error) {
	return river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: logger})
}
