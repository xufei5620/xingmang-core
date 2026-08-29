package main

import (
	"fmt"
	"strings"

	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
)

// usersMode 决定「用户管理」页签用哪个 ReadClient 实现。
type usersMode string

const (
	// usersModeFake 用样本数据。上游响应形状核实之前唯一走得通的模式。
	usersModeFake usersMode = "fake"
	// usersModeReal 走真实只读客户端骨架;数据方法目前一律 not_supported。
	usersModeReal usersMode = "real"
	// usersModeOff 完全不挂载用户端点。
	//
	// 比 fake 多出来的这一档是必要的:一个没接上游用户清单的环境,
	// 端点**不存在**(404)比端点存在却只回演示数据诚实——
	// 后者会让前端把编出来的客户名与余额渲染成真实经营数据。
	usersModeOff usersMode = "off"
)

// parseUsersMode 解析 XM_PLATFORM_USERS_MODE,空串按 fake 处理。
//
// 默认 fake 而不是 off(与 reqlog 的选择相反),理由是这两条通道的**代价不同**:
// reqlog 默认 off,因为它 fake 出来的是「用户与模型的完整对话」,有人会以为
// 自己在看真实问答;而用户清单 fake 出来的是一批一眼可辨的样本客户
// (「试用账号 07」),且 `data_source` 里带 `-fake`、前端据此挂演示横幅。
//
// 代价小的一侧选可用性:默认 off 会让每个新拉起的开发环境都看到一个 404,
// 然后有人去配环境变量——而那个变量的值十有八九就是 fake。
func parseUsersMode(s string) (usersMode, error) {
	switch mode := usersMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return usersModeFake, nil
	case usersModeFake, usersModeReal, usersModeOff:
		return mode, nil
	default:
		return "", fmt.Errorf("XM_PLATFORM_USERS_MODE %q: 只接受 off / fake / real", s)
	}
}

// buildPlatformUsers 按模式构造用户清单查询入口。
//
// 返回 nil 表示不挂载端点(off 模式)。
func buildPlatformUsers(mode usersMode) (*platformusers.Service, error) {
	if mode == usersModeOff {
		return nil, nil
	}
	clients := make(map[string]platformusers.Client, len(connusers.Sources))
	for _, source := range connusers.Sources {
		switch mode {
		case usersModeFake:
			clients[source] = connusers.NewFakeClient(source, nil)
		case usersModeReal:
			// real 骨架的构造期护栏要求 endpoint / 白名单 / CredentialRef 齐全。
			// 这些**逐平台不同**,应当来自成本登记簿那样的配置源,而不是
			// 一组进程级环境变量——所以这条路今天不通,显式说出来而不是
			// 悄悄回落到 fake:回落之后没有人会发现自己配的 real 没生效。
			return nil, fmt.Errorf(
				"XM_PLATFORM_USERS_MODE=real 尚不可用:上游响应形状待核对,且逐平台的端点与凭据引用还没有配置来源(见 connectors/platformusers 的接入清单)")
		}
	}
	return platformusers.NewService(clients)
}

// platformUsersOrNil 把具体类型转成接口,nil 保持 nil。
//
// 直接把 *platformusers.Service 赋给接口字段会得到一个「非 nil 接口包着 nil
// 指针」的值,于是 Deps 里那句 `if d.PlatformUsers != nil` 永远为真,
// 端点照挂、一调就 panic。这是 Go 里最常见的一个坑,单独一个函数把它挡住。
func platformUsersOrNil(s *platformusers.Service) httpapi.PlatformUsersQuerier {
	if s == nil {
		return nil
	}
	return s
}
