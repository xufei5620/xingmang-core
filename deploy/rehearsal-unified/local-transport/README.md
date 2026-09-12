# N-2：管理员列表 30 秒超时的根因与修复

失败来自本地 Docker Desktop 演练的嵌套 `nc -lk ... -e nc ...` 转发。业务 web 在 2026-09-12 04:59:09 UTC 已返回 HTTP 200、2812 字节；HTTP/1.0 无 Content-Length 的响应以连接结束界定正文，旧 nc 链收到全部字节后未及时传播远端 EOF，主机 nginx 等待结束，外部冒烟最终触发原有 30 秒截止。不能据此推断数据库查询花了 30 秒。

R2 报告以操作器源码/配置 SHA 未变推断“只是重跑”，遗漏了仓库外本地转发器的实际修复。本次将已验证的 `main.go`、网络回归及诊断证据正式纳入版本控制和原 operator 测试入口。应用源码没有查询性能修补；单次 HTTP 超时仍为 30 秒，没有重试。

原始证据保留在 [EOF 报告](G:/xingmang/logs/unified-review-fixes-r1-20260912/rehearsal-nginx/eof-fix-01/REPORT.md)：

- [实际 RED](G:/xingmang/logs/unified-review-fixes-r1-20260912/rehearsal-nginx/eof-fix-01/01-actual-red/RESULT.json)：相同 fixture 二进制、相同 2812 字节及 SHA；直连与带长度的 nc 响应成功，无长度 nc 响应完整字节已到但等 EOF 超时。
- [实际 GREEN](G:/xingmang/logs/unified-review-fixes-r1-20260912/rehearsal-nginx/eof-fix-01/04-actual-green/RESULT.json)：新 relay 的无长度响应保持 contentLength=-1，正常 EOF 到达；未改 fixture、HTTP 版本或增加长度头。
- [完整链](G:/xingmang/logs/unified-review-fixes-r1-20260912/rehearsal-nginx/eof-fix-01/06-tls-full-chain/RESULT.json)：原 Windows 转发、PROXY、真实 nginx/CA/SNI、HTTP 1.0/1.1 与两种响应定界，四组成功。

修复是纯 TCP 双向 `io.Copy`：一个方向读到 EOF 后，对目标调用 `CloseWrite`；保留反方向直至其完成，最后关闭连接。它不解析或改写 HTTP、TLS、Cookie 或 PROXY；只有原 30 秒连接截止，没有业务读取截止或重试。仅绑定 127.0.0.1，目标限制为私有或环回字面量 IPv4。

`main_test.go` 用真实套接字验证服务端 EOF 先于客户端写关闭，以及客户端半关闭后仍收到完整二进制响应。删除任一方向的 CloseWrite 都必须使对应断言失败；原 operator Python suite 会实际执行这些 Go 测试（禁下载依赖）。

这是本地合成环境的适配器，生产 D 优先使用真实主机环回端口，不安装旧嵌套 nc 方案。部署工具必须核对自有监听器与目标，不能替换任意已有进程。构建不增加依赖：

```powershell
$env:CGO_ENABLED='0'; $env:GOOS='linux'; $env:GOARCH='amd64'
go build -trimpath -o <本次独占输出路径> ./deploy/rehearsal-unified/local-transport/main.go
```

`smoke.Client` 现另记 headers_received_utc、response_bytes 与 transport_phase，区分响应头未收到和已收到状态码后等待正文/EOF；不记录正文、Cookie、凭据或原始异常文本。实际生产规模查询耗时仍应由服务器 D 的只读请求记录证明，不把本地传输修复写成生产性能已验证。
