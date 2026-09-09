// Command reqlog-recorder 是宝塔 nginx 与 NewAPI(:3000)/Sub2API(:8081) 之间的
// 透明记录代理：零改上游，把 /v1/* 的请求/响应明文（含完整 SSE 事件流）落盘，
// 按天分目录 + 索引行，供 connectors/reqlog 的文件后端只读连接器消费。
//
// XM-REQLOG-MERGE 从桌面端原型 `_import/reqlog/reqlogger.go`（在生产以
// systemd 服务运行，见 docs/runbooks/REQLOG-RECORDER.md 的切换步骤）收编
// 而来：代理转发、落盘格式、清理策略、令牌映射刷新逐一保留，硬编码值改成
// 可配置项（ParseConfig），文件权限收紧为可配置的属组只读（见 config.go）。
//
// 本二进制**不是**星芒平台控制面的一部分（ADR-011 不适用）：它是既有、
// 独立部署在宿主机的数据面组件，以 `User=root` 的 systemd 服务运行，
// 站在用户请求路径上做透明转发——这是收编前就存在的事实，不是本次任务
// 引入的架构决定。平台侧（cmd/platform-api）只以只读挂载消费它的输出文件，
// 不调用它、不管理它的生命周期。
package main

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/xufei5620/xingmang-platform/internal/platform/reqlogformat"
)

// Recorder 持有 reqlog-recorder 运行期共享的状态。
type Recorder struct {
	cfg    Config
	logger *slog.Logger

	// seq 跨 newapi/sub2api 两个代理共享同一个原子计数器——与桌面端原型
	// reqlogger.go 完全一致（那边的 seq 也是包级全局变量），保证同一天
	// 目录内产生的 id 不论来自哪个来源都不会重号。connectors/reqlog 的
	// 文件后端依赖这条不变量做跨来源隔离校验（见 file_client.go 里
	// RequestContent 对 full.Source != source 的检查）。
	seq    atomic.Int64
	writeQ chan *reqlogformat.FullRecord
}

// NewRecorder 构造 Recorder。不做任何 I/O——目录是否可写、上游是否可达都是
// Run() 之后才知道的事。
func NewRecorder(cfg Config, logger *slog.Logger) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Recorder{
		cfg:    cfg,
		logger: logger,
		writeQ: make(chan *reqlogformat.FullRecord, 2048),
	}
}

func (r *Recorder) nextID(startCST string) string {
	// 与原实现 `fmt.Sprintf("%s-%06d", start.In(cst).Format("150405"),
	// seq.Add(1)%1000000)` 一致：六位时间 + 六位序号回绕。
	n := r.seq.Add(1) % 1000000
	return startCST + "-" + padSeq(n)
}

func padSeq(n int64) string {
	// 等价于 fmt.Sprintf("%06d", n)，单独抽出来只是避免在热路径上反复
	// import fmt 的格式化开销——不是本次任务要求的优化，顺手做且无风险
	// （行为与 %06d 逐位一致，有单元测试钉住）。
	if n < 0 {
		n = -n
	}
	digits := [6]byte{'0', '0', '0', '0', '0', '0'}
	for i := 5; i >= 0 && n > 0; i-- {
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[:])
}

// Run 启动写入、清理、令牌映射刷新三个后台循环与两个反向代理监听器，
// 阻塞直到 ctx 被取消或某个监听器致命出错。
func (r *Recorder) Run(ctx context.Context) error {
	go r.writer(ctx)
	go r.cleanerLoop(ctx)
	go r.tokenMapLoop(ctx)

	errCh := make(chan error, 2)
	go func() { errCh <- r.serve(ctx, "newapi", r.cfg.ListenNewAPI, r.cfg.UpstreamNewAPI) }()
	go func() { errCh <- r.serve(ctx, "sub2api", r.cfg.ListenSub2API, r.cfg.UpstreamSub2API) }()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
