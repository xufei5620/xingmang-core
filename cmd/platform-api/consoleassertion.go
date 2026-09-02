package main

import (
	"context"
	"log/slog"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/consoleassertion"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/localauth"
)

// buildConsoleAssertionHandlers constructs the CR-0006/XM-INVCON1 signer +
// HTTP handlers when the feature is enabled. Returns (nil, nil) when
// disabled -- callers must not treat that as an error, it is the expected
// "capability not activated" state (cfg.ConsoleAssertion.Enabled defaults to
// false; see consoleAssertionConfigFromEnv).
//
// secretProvider is the same file-backed, audited provider TOTP secrets are
// resolved through (platformUsersSecretProvider) -- one XM_SECRET_ROOT
// directory serves both, matching the existing convention that any
// CredentialRef-addressable secret in this process comes from that one
// directory.
func buildConsoleAssertionHandlers(
	ctx context.Context, cfg config, localAuthStore *localauth.Store,
	secretProvider consoleassertion.SigningKeyProvider, auditStore *audit.Store, logger *slog.Logger,
) (*consoleassertion.Handlers, error) {
	if !cfg.ConsoleAssertion.Enabled {
		return nil, nil
	}
	signer, err := consoleassertion.NewSigner(ctx, secretProvider, cfg.ConsoleAssertion.KeyRef)
	if err != nil {
		return nil, err
	}
	// 三个值都是公开信息（公钥/指纹本身不是秘密），供运维核对已装配的
	// key_id 是否与两仓库 contracts/auth/console-assertion-keyring.v1.json
	// 里预期的记录一致——见 Signer 类型注释。
	logger.Info("console_assertion_signer_loaded",
		slog.String("module", "platform.api"),
		slog.String("key_id", signer.KeyID()),
		slog.String("public_key", signer.PublicKeyBase64()),
		slog.String("fingerprint", signer.Fingerprint()))

	return consoleassertion.NewHandlers(
		signer, localAuthStore, cfg.ConsoleAdminIPAllowlist,
		consoleassertion.Config{
			Issuer: cfg.ConsoleAssertion.Issuer, Audience: cfg.ConsoleAssertion.Audience,
			StepUpMaxAge: cfg.ConsoleAssertion.StepUpMaxAge,
		},
		cfg.Environment, auditStore, logger,
	), nil
}

// consoleAssertionHandlersOrNil converts a possibly-nil *consoleassertion.
// Handlers into httpapi.ConsoleAssertionHandlers without ever storing a
// typed-nil pointer inside a non-nil interface value (the classic Go
// footgun this repository guards against at every other optional-capability
// wiring point -- see platformUsersOrNil's own comment for the full
// explanation).
func consoleAssertionHandlersOrNil(h *consoleassertion.Handlers) httpapi.ConsoleAssertionHandlers {
	if h == nil {
		return nil
	}
	return h
}
