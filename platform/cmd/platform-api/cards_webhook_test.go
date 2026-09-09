package main

import (
	"context"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 取密钥必须用 Reveal()，不能用 String()。
//
// SecretValue.String() 恒定返回遮蔽标记——那是它的**设计目的**，防止密钥被
// 误打进日志。拿它去算 HMAC，得到的是一个对所有密钥都相同的常量，
// 症状是「签名永远不匹配」，而且那个遮蔽串不是 base64，会把诊断引向
// 「密钥值不对」——2026-09-05 就为这个让产品负责人反复重填了三次密钥。
//
// 这条测试不检查具体实现，只检查**行为**：取出来的必须是真值。
func TestWebhookSecretReturnsRealValueNotRedactedPlaceholder(t *testing.T) {
	const real = "0bIWuELQI-fvighSNR-k1E5MdHzd7VjnXQAYuX84-EA="

	p := &cardWebhookProcessor{provider: fixedSecretProvider{value: real}}

	got, err := p.WebhookSecret(context.Background(), "LINFENG")
	if err != nil {
		t.Fatal(err)
	}
	if got != real {
		t.Fatalf("取到的密钥 = %q, want 真值", got)
	}
	// 遮蔽标记的典型形态：星号或方括号。真值里不该出现它们。
	if strings.ContainsAny(got, "*[") {
		t.Fatalf("取到的像是遮蔽占位符而不是真值: %q", got)
	}
}

// 密钥为空时必须报错，而不是拿空串去算 HMAC。
func TestWebhookSecretFailsOnEmptyValue(t *testing.T) {
	p := &cardWebhookProcessor{provider: fixedSecretProvider{value: "   "}}

	if _, err := p.WebhookSecret(context.Background(), "LINFENG"); err == nil {
		t.Fatal("空密钥必须报错")
	}
}

type fixedSecretProvider struct{ value string }

func (f fixedSecretProvider) ID() string { return "fixed" }

func (f fixedSecretProvider) Resolve(
	context.Context, secrets.CredentialRef, string,
) (secrets.SecretValue, error) {
	return secrets.NewSecretValue([]byte(f.value)), nil
}

func (f fixedSecretProvider) Metadata(
	context.Context, secrets.CredentialRef,
) (secrets.SecretMetadata, error) {
	return secrets.SecretMetadata{}, nil
}
