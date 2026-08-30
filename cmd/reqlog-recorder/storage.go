package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/reqlogformat"
)

// writer 从 writeQ 消费完整记录，写 <id>.json.gz 明细与 index.jsonl 索引行。
//
// 落盘格式与桌面端原型 reqlogger.go 的 writer() 逐字段一致；改动只有权限：
// 目录/文件从硬编码 0700/0600 改成可配置的 DirPerm/FilePerm（默认收紧后的
// 新默认是 0750/0640，见 config.go），新建后按 GroupID 尽力 chgrp
// （失败不致命，见 perm.go 的说明）。
func (r *Recorder) writer(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case fr, ok := <-r.writeQ:
			if !ok {
				return
			}
			r.writeOne(fr)
		}
	}
}

func (r *Recorder) writeOne(fr *reqlogformat.FullRecord) {
	day := reqlogformat.DayDir(time.UnixMilli(fr.TsMs))
	dir := filepath.Join(r.cfg.DataDir, day)
	if err := os.MkdirAll(dir, r.cfg.DirPerm); err != nil {
		r.logger.Error("reqlog_recorder_mkdir_failed",
			slog.String("dir", dir), slog.String("error", err.Error()))
		return
	}
	chownGroup(r.logger, dir, r.cfg.GroupID)

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		r.logger.Error("reqlog_recorder_gzip_init_failed", slog.String("error", err.Error()))
		return
	}
	if err := json.NewEncoder(gz).Encode(fr); err != nil {
		r.logger.Error("reqlog_recorder_encode_failed",
			slog.String("id", fr.ID), slog.String("error", err.Error()))
		return
	}
	if err := gz.Close(); err != nil {
		r.logger.Error("reqlog_recorder_gzip_close_failed",
			slog.String("id", fr.ID), slog.String("error", err.Error()))
		return
	}
	gzPath := filepath.Join(dir, fr.ID+".json.gz")
	if err := os.WriteFile(gzPath, buf.Bytes(), r.cfg.FilePerm); err != nil {
		r.logger.Error("reqlog_recorder_write_detail_failed",
			slog.String("path", gzPath), slog.String("error", err.Error()))
		return
	}
	chownGroup(r.logger, gzPath, r.cfg.GroupID)

	line, err := json.Marshal(fr.Record)
	if err != nil {
		r.logger.Error("reqlog_recorder_marshal_index_failed",
			slog.String("id", fr.ID), slog.String("error", err.Error()))
		return
	}
	idxPath := filepath.Join(dir, "index.jsonl")
	f, err := os.OpenFile(idxPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, r.cfg.FilePerm)
	if err != nil {
		r.logger.Error("reqlog_recorder_open_index_failed",
			slog.String("path", idxPath), slog.String("error", err.Error()))
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		r.logger.Error("reqlog_recorder_write_index_failed",
			slog.String("path", idxPath), slog.String("error", err.Error()))
	}
	// 索引文件跨多条记录复用同一个 inode：每次追加都尝试 chown 一次，
	// 代价是一次系统调用，换来的是"不用判断这次 OpenFile 是不是刚创建
	// 的那次"——同组重复 chown 是幂等操作。
	chownGroup(r.logger, idxPath, r.cfg.GroupID)
}

// cleanerLoop 每小时清理一次超出保留期的按天目录，启动时先清一次。
func (r *Recorder) cleanerLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	r.cleanOnce(time.Now)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.cleanOnce(time.Now)
		}
	}
}

// cleanOnce 删除按 CST 日历日早于「保留期天数之前」的整个目录——与桌面端
// 原型 reqlogger.go 的 cleaner() 同一条边界：cut 当天本身保留，只删严格
// 早于 cut 的目录。
//
// 用 now 参数而不是内部直接调 time.Now，是为了让清理边界能在不等真实一小时
// 的前提下被单元测试覆盖（recorder_test.go 的 TestCleanOnceRetentionBoundary）。
func (r *Recorder) cleanOnce(now func() time.Time) {
	cut := reqlogformat.DayDir(now().AddDate(0, 0, -r.cfg.RetentionDays))
	ents, err := os.ReadDir(r.cfg.DataDir)
	if err != nil {
		r.logger.Warn("reqlog_recorder_cleaner_readdir_failed",
			slog.String("dir", r.cfg.DataDir), slog.String("error", err.Error()))
		return
	}
	for _, e := range ents {
		if !e.IsDir() || !reqlogformat.DayDirPattern.MatchString(e.Name()) {
			continue
		}
		if e.Name() < cut {
			path := filepath.Join(r.cfg.DataDir, e.Name())
			if err := os.RemoveAll(path); err != nil {
				r.logger.Error("reqlog_recorder_cleaner_remove_failed",
					slog.String("dir", path), slog.String("error", err.Error()))
				continue
			}
			r.logger.Info("reqlog_recorder_cleaner_removed", slog.String("dir", e.Name()))
		}
	}
}
