package secrets

import (
	"context"
	"errors"
	"fmt"
)

// Chain 按顺序尝试多个 Provider，返回第一个成功解析的结果（XM-CRED0）。
//
// 它存在的理由是「文件优先、env 兜底」：管理后台写进 XM_SECRET_ROOT 的凭据
// 文件一出现就该生效，而还没在后台填过的引用仍能落到旧的环境变量登记表上。
// 两个来源的**优先级由装配顺序显式给定**，不是按 scope 猜——这与 Router
// 「未登记 scope 直接报错」的纪律不冲突：Chain 只在「上一个来源明确说没有」
// 时才往下走，任何别的失败（IO 错误、找到了但为空）都原地停下，
// 绝不拿下一个来源的值去掩盖上一个来源的故障（fail closed）。
//
// Chain 本身不记审计：把每个成员各自包上 Audited，审计里才看得出
// 「这次是文件命中的还是 env 兜底的」。
type Chain struct {
	providers []SecretProvider
}

// NewChain 构造链式 Provider；nil 成员会被跳过。
func NewChain(providers ...SecretProvider) SecretProvider {
	chain := &Chain{}
	for _, p := range providers {
		if p != nil {
			chain.providers = append(chain.providers, p)
		}
	}
	return chain
}

func (c *Chain) ID() string { return "chain" }

// chainMiss 判断一个错误是否只是「这个来源没有这条引用」：
// 不存在、scope 未登记、env 登记表里没映射——这三种都允许往下一个来源走。
// 其余（IO 错误、内容为空）是故障，必须停下。
func chainMiss(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnknownScope) || errors.Is(err, ErrUnmappedRef)
}

func (c *Chain) Resolve(ctx context.Context, ref CredentialRef, purpose string) (SecretValue, error) {
	if len(c.providers) == 0 {
		return SecretValue{}, fmt.Errorf("chain: 没有可用的 provider: %s: %w", ref, ErrNotFound)
	}
	var last error
	for _, p := range c.providers {
		v, err := p.Resolve(ctx, ref, purpose)
		if err == nil {
			return v, nil
		}
		if !chainMiss(err) {
			// 错误信息来自成员 Provider，按各自纪律不含明文（文件 Provider 只带路径）。
			return SecretValue{}, fmt.Errorf("chain: %w", err)
		}
		last = err
	}
	// 全部未命中：对外归 ErrNotFound（审计 error_code=not_found），
	// 同时保留最后一个成员的原因供排查。
	return SecretValue{}, fmt.Errorf("chain: %s 在所有 provider 均未命中: %w (%w)", ref, ErrNotFound, last)
}

// Metadata 返回第一个报告 Available=true 的成员结果；都不可用时返回最后一个
// 成员的结果（含它的错误），让调用方看得出「最终落到哪儿、为什么没有」。
func (c *Chain) Metadata(ctx context.Context, ref CredentialRef) (SecretMetadata, error) {
	if len(c.providers) == 0 {
		return SecretMetadata{Ref: ref, Provider: c.ID()}, nil
	}
	var (
		last    SecretMetadata
		lastErr error
	)
	for _, p := range c.providers {
		m, err := p.Metadata(ctx, ref)
		if err == nil && m.Available {
			return m, nil
		}
		last, lastErr = m, err
	}
	return last, lastErr
}
