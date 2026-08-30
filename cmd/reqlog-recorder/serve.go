package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// serve 起一个 http.Server（而不是原型直接调包级 http.ListenAndServe），
// 挂上 ctx 取消时的优雅关闭。这一处比原型多做了一点：原型是
// `go func(){ log.Fatal(http.ListenAndServe(...)) }()`，监听失败直接砍
// 整个进程；这里改成可关闭、可在测试里绑 127.0.0.1:0 起停的 http.Server，
// 两种收尾都不阻塞：ctx 先取消就优雅关闭并等 ListenAndServe 真正退出，
// ListenAndServe 自己先失败（比如端口被占用）就立刻把错误报回去，不会
// 卡在等一个可能永远不会来的 ctx.Done()。转发/抄录逻辑（makeProxy）
// 一行未改，这一层只影响"进程怎么启动、怎么停"，不影响任何落盘数据。
func (r *Recorder) serve(ctx context.Context, source, listen, upstream string) error {
	handler, err := r.makeProxy(source, upstream)
	if err != nil {
		return fmt.Errorf("%s: 构造反向代理失败: %w", source, err)
	}
	srv := &http.Server{Addr: listen, Handler: handler}

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- srv.ListenAndServe()
	}()
	r.logger.Info("reqlog_recorder_listening",
		slog.String("source", source), slog.String("listen", listen), slog.String("upstream", upstream))

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		<-serveErrCh // 等 ListenAndServe 真正返回，避免连接还在收尾时进程已经退出
		return ctx.Err()
	case err := <-serveErrCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("%s: %w", source, err)
		}
		return nil
	}
}
