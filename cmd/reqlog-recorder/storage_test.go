package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/reqlogformat"
)

func testRecorder(t *testing.T, dataDir string) *Recorder {
	t.Helper()
	cfg := Config{
		DataDir:       dataDir,
		RetentionDays: DefaultRetentionDays,
		MaxReqBody:    DefaultMaxReqBody,
		MaxRespBody:   DefaultMaxRespBody,
		DirPerm:       DefaultDirPerm,
		FilePerm:      DefaultFilePerm,
		GroupID:       0, // 测试环境不假设某个 GID 存在
	}
	return NewRecorder(cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

func sampleFullRecord(id string, tsMs int64, source string) *reqlogformat.FullRecord {
	return &reqlogformat.FullRecord{
		Record: reqlogformat.Record{
			ID: id, TsMs: tsMs, Source: source, Method: "POST", Path: "/v1/chat/completions",
			Status: 200, DurMs: 10, TtfbMs: 5, ClientIP: "203.0.113.42",
			TokenPfx: "sk-test0001invalid00", Model: "gpt-4o", ReqSize: 3, RespSize: 3,
		},
		ReqHeaders:  map[string]string{"Content-Type": "application/json"},
		ReqBody:     `{"a":1}`,
		RespHeaders: map[string]string{"Content-Type": "application/json"},
		RespBody:    `{"b":2}`,
	}
}

func TestWriteOneAppendsIndexLinesInOrder(t *testing.T) {
	dataDir := t.TempDir()
	r := testRecorder(t, dataDir)

	day := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC) // 2026-08-29 11:00 CST
	base := day.UnixMilli()
	r.writeOne(sampleFullRecord("110000-000001", base, "sub2api"))
	r.writeOne(sampleFullRecord("110001-000002", base+1000, "newapi"))

	idxPath := filepath.Join(dataDir, reqlogformat.DayDir(day), "index.jsonl")
	b, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatalf("读取 index.jsonl: %v", err)
	}
	var lines []reqlogformat.Record
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var rec reqlogformat.Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("解析索引行: %v", err)
		}
		lines = append(lines, rec)
	}
	if len(lines) != 2 {
		t.Fatalf("index.jsonl 应有 2 行（追加而不是覆盖），got %d", len(lines))
	}
	if lines[0].ID != "110000-000001" || lines[1].ID != "110001-000002" {
		t.Fatalf("索引行顺序不对: %+v", lines)
	}
}

func TestWriteOneProducesDecodableDetailFile(t *testing.T) {
	dataDir := t.TempDir()
	r := testRecorder(t, dataDir)

	day := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	fr := sampleFullRecord("110000-000001", day.UnixMilli(), "sub2api")
	r.writeOne(fr)

	gzPath := filepath.Join(dataDir, reqlogformat.DayDir(day), "110000-000001.json.gz")
	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("打开明细文件: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	var got reqlogformat.FullRecord
	if err := json.NewDecoder(gz).Decode(&got); err != nil {
		t.Fatalf("解码明细: %v", err)
	}
	if got.ID != fr.ID || got.ReqBody != fr.ReqBody || got.RespBody != fr.RespBody {
		t.Fatalf("解码结果与写入不一致: got %+v", got)
	}
}

func TestWriteOnePermissionBits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的权限模型不映射 Unix rwx 位，生产部署目标是 Linux systemd 服务，这条断言只在类 Unix 平台上有意义")
	}
	dataDir := t.TempDir()
	cfg := Config{
		DataDir: dataDir, RetentionDays: 30,
		MaxReqBody: DefaultMaxReqBody, MaxRespBody: DefaultMaxRespBody,
		DirPerm: 0o750, FilePerm: 0o640, GroupID: 0,
	}
	r := NewRecorder(cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	day := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	r.writeOne(sampleFullRecord("110000-000001", day.UnixMilli(), "sub2api"))

	dayDir := filepath.Join(dataDir, reqlogformat.DayDir(day))
	dirInfo, err := os.Stat(dayDir)
	if err != nil {
		t.Fatalf("stat 目录: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o750 {
		t.Errorf("目录权限 = %o, want 0750", got)
	}
	for _, name := range []string{"index.jsonl", "110000-000001.json.gz"} {
		info, err := os.Stat(filepath.Join(dayDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != 0o640 {
			t.Errorf("%s 权限 = %o, want 0640", name, got)
		}
	}
}

func TestCleanOnceRetentionBoundary(t *testing.T) {
	dataDir := t.TempDir()
	r := testRecorder(t, dataDir)
	r.cfg.RetentionDays = 30

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, reqlogformat.CST)
	fixedNow := func() time.Time { return now }

	// cut = now - 30 天 = 2026-07-30；cut 当天本身保留，早于 cut 的删除，
	// 晚于/等于 cut 的保留——这是原型 reqlogger.go cleaner() 的既有边界，
	// 不是本次新定的口径。
	dirs := map[string]bool{
		"20260730":      true,  // 恰好等于 cut：保留
		"20260729":      false, // 早于 cut 一天：删除
		"20260801":      true,  // 晚于 cut：保留
		"20260601":      false, // 远早于 cut：删除
		"not-a-day-dir": true,  // 不是 8 位数字目录名：忽略，不动它
	}
	for name := range dirs {
		if err := os.MkdirAll(filepath.Join(dataDir, name), 0o750); err != nil {
			t.Fatalf("创建固件目录 %s: %v", name, err)
		}
	}

	r.cleanOnce(fixedNow)

	for name, wantKept := range dirs {
		_, err := os.Stat(filepath.Join(dataDir, name))
		exists := err == nil
		if exists != wantKept {
			t.Errorf("目录 %s：存在=%v, want 存在=%v", name, exists, wantKept)
		}
	}
}

func TestCleanOnceIgnoresUnreadableDataDir(t *testing.T) {
	// 数据目录本身读不出来（比如还没建）：cleanOnce 不该 panic，
	// 只是没有目录可清，静默返回。
	r := testRecorder(t, filepath.Join(t.TempDir(), "does-not-exist"))
	r.cleanOnce(time.Now)
}
