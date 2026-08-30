package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/reqlogformat"
)

// reModel / reRespID 从原始请求/响应正文里粗略抠出 model 名与响应体 ID
// （resp_/msg_/chatcmpl-/gen- 前缀），与桌面端原型 reqlogger.go 逐字符一致。
//
// 原型里还有一个 reDeltaTxt 正则，声明了但整份源码没有任何地方引用它——
// 收编时确认是死代码，不再移植（这是清理明确无用的声明，不是行为变更；
// 落盘字段与转发逻辑一个都没有少）。
var (
	reModel  = regexp.MustCompile(`"model"\s*:\s*"([^"]{1,100})"`)
	reRespID = regexp.MustCompile(`"id"\s*:\s*"((?:resp_|msg_|chatcmpl-|gen-)[^"]{1,80})"`)
)

// teeBody 包一层 io.ReadCloser：边把响应体转发给客户端，边把前 max 字节
// 抄一份到内存缓冲区，边测量首字节时间。逐字段照搬桌面端原型 reqlogger.go
// 的同名类型。
type teeBody struct {
	rc          io.ReadCloser
	buf         bytes.Buffer
	max         int64
	truncated   bool
	first       time.Time
	onDone      func(*teeBody, error)
	once        sync.Once
	lastErr     error
	status      int
	upReqID     string
	respHeaders map[string]string
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 {
		if t.first.IsZero() {
			t.first = time.Now()
		}
		if int64(t.buf.Len()) < t.max {
			room := t.max - int64(t.buf.Len())
			if int64(n) <= room {
				t.buf.Write(p[:n])
			} else {
				t.buf.Write(p[:room])
				t.truncated = true
			}
		} else {
			t.truncated = true
		}
	}
	if err != nil && err != io.EOF {
		t.lastErr = err
	}
	return n, err
}

func (t *teeBody) Close() error {
	err := t.rc.Close()
	t.once.Do(func() { t.onDone(t, t.lastErr) })
	return err
}

// headerMap 把 http.Header 拍平成 map[string]string；authorization/
// x-api-key/cookie 落盘前先截断打码（保留前 24 字符 + "...[masked]"）。
// 逐字段照搬原型 reqlogger.go 的 headerMap；连接器读侧不透出头部字段，
// 这一层截断已经是唯一且足够的一层（见 connectors/reqlog/file_client.go
// 的 RequestContent 注释）。
func headerMap(h http.Header) map[string]string {
	m := map[string]string{}
	for k, v := range h {
		lk := strings.ToLower(k)
		if lk == "authorization" || lk == "x-api-key" || lk == "cookie" {
			val := strings.Join(v, ",")
			if len(val) > 24 {
				val = val[:24] + "...[masked]"
			}
			m[k] = val
			continue
		}
		m[k] = strings.Join(v, ",")
	}
	return m
}

// tokenPrefix 取请求令牌的前 20 个字符——不是凭据，只是排查用的锚点
// （contracts/connectors/reqlog.read.v1.md §3.1 对 TokenPrefix 的说明），
// 与桌面端原型逐字段一致。
func tokenPrefix(r *http.Request) string {
	tk := r.Header.Get("Authorization")
	tk = strings.TrimPrefix(tk, "Bearer ")
	if tk == "" {
		tk = r.Header.Get("x-api-key")
	}
	if len(tk) > 20 {
		tk = tk[:20]
	}
	return tk
}

// makeProxy 构造一个来源专属（newapi 或 sub2api）的透明反向代理 Handler。
//
// 转发/抄录逻辑与桌面端原型 reqlogger.go 的 makeProxy 逐条一致：只抄
// /v1、/openai、/anthropic、/gemini、/antigravity、/pg 前缀的非 GET/OPTIONS
// 请求，其余原样透传、不落盘；请求体先整体读入内存（受 MaxReqBody 上限），
// 响应体经 teeBody 边转发边抄录（受 MaxRespBody 上限）。
//
// ⚠️ 已知缺口，本次收编未改动（与原型行为一致，写入 handoff 的风险清单）：
// 如果上游连接在拿到响应头**之前**就失败，ErrorHandler 直接给客户端写
// 502，不经过 ModifyResponse/teeBody，这一次请求**完全不落盘**——不是
// "记了一条 status=0 的记录"，而是压根没有记录。status=0 出现在磁盘上的
// 前提是响应头已经拿到、但读响应体过程中连接中断。
func (r *Recorder) makeProxy(source, target string) (http.Handler, error) {
	tu, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("解析上游地址 %q: %w", target, err)
	}
	rp := httputil.NewSingleHostReverseProxy(tu)
	rp.FlushInterval = -1
	origDirector := rp.Director
	rp.Director = func(req *http.Request) {
		origDirector(req)
		req.Host = req.Header.Get("Host")
		if req.Host == "" {
			req.Host = tu.Host
		}
	}
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		r.logger.Error("reqlog_recorder_upstream_error",
			slog.String("source", source), slog.String("error", err.Error()))
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream unreachable (reqlogger)"}}`))
	}

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		interesting := strings.HasPrefix(p, "/v1") || strings.HasPrefix(p, "/openai") ||
			strings.HasPrefix(p, "/anthropic") || strings.HasPrefix(p, "/gemini") ||
			strings.HasPrefix(p, "/antigravity") || strings.HasPrefix(p, "/pg")
		if !interesting || req.Method == http.MethodGet || req.Method == http.MethodOptions {
			rp.ServeHTTP(w, req)
			return
		}

		start := time.Now()
		reqBody, _ := io.ReadAll(io.LimitReader(req.Body, r.cfg.MaxReqBody+1))
		_ = req.Body.Close()
		reqTrunc := false
		if int64(len(reqBody)) > r.cfg.MaxReqBody {
			reqBody = reqBody[:r.cfg.MaxReqBody]
			reqTrunc = true
		}
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		req.ContentLength = int64(len(reqBody))

		reqHeaders := headerMap(req.Header)
		pfx := tokenPrefix(req)
		clientIP := req.Header.Get("X-Real-IP")
		if clientIP == "" {
			clientIP = req.RemoteAddr
		}
		model := ""
		if m := reModel.FindSubmatch(reqBody); m != nil {
			model = string(m[1])
		}
		isStream := bytes.Contains(reqBody, []byte(`"stream":true`)) ||
			bytes.Contains(reqBody, []byte(`"stream": true`))

		mrDone := func(t *teeBody, readErr error) {
			respBody := t.buf.Bytes()
			endNote := "eof"
			if readErr != nil {
				endNote = "read_err:" + readErr.Error()
				if len(endNote) > 120 {
					endNote = endNote[:120]
				}
			}
			id := r.nextID(start.In(reqlogformat.CST).Format("150405"))
			respID := ""
			if m := reRespID.FindSubmatch(respBody); m != nil {
				respID = string(m[1])
			}
			preview := reqlogformat.ExtractFinalText(string(respBody))
			if len(preview) > 150 {
				preview = preview[:150]
			}
			preview = strings.ReplaceAll(preview, "\n", " ")
			inTok, outTok, cacheTok := reqlogformat.ExtractUsage(string(respBody))

			fr := &reqlogformat.FullRecord{
				Record: reqlogformat.Record{
					ID: id, TsMs: start.UnixMilli(),
					Time:   start.In(reqlogformat.CST).Format("2006-01-02 15:04:05.000"),
					Source: source, Method: req.Method, Path: p,
					Status: t.status, DurMs: time.Since(start).Milliseconds(),
					ClientIP: clientIP, UA: req.Header.Get("User-Agent"),
					TokenPfx: pfx, UpReqID: t.upReqID, RespID: respID, Model: model,
					Stream: isStream, ReqSize: len(reqBody), RespSize: len(respBody),
					Truncated: reqTrunc || t.truncated, EndNote: endNote, Preview: preview,
					InTok: inTok, OutTok: outTok, CacheTok: cacheTok,
				},
				ReqHeaders: reqHeaders, ReqBody: string(reqBody),
				RespHeaders: t.respHeaders, RespBody: string(respBody),
			}
			if !t.first.IsZero() {
				fr.TtfbMs = t.first.Sub(start).Milliseconds()
			}
			select {
			case r.writeQ <- fr:
			default:
				r.logger.Warn("reqlog_recorder_write_queue_full")
			}
		}

		rp2 := *rp
		rp2.ModifyResponse = func(resp *http.Response) error {
			upID := resp.Header.Get("X-Oneapi-Request-Id")
			if upID == "" {
				upID = resp.Header.Get("X-Request-Id")
			}
			tb := &teeBody{
				rc: resp.Body, max: r.cfg.MaxRespBody, onDone: mrDone,
				status: resp.StatusCode, upReqID: upID, respHeaders: headerMap(resp.Header),
			}
			resp.Body = tb
			return nil
		}
		rp2.ServeHTTP(w, req)
	}), nil
}
