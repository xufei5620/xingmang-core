package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
	"github.com/xufei5620/xingmang-platform/internal/platform/sms"
)

// buildSMSProber 按环境变量组装接码巡检器（XM-SMS2 #7）。
//
// 返回 nil 表示未启用（XM_SMS_MODE 未设或为 off），调用方据此不打开
// SMSProbeEnabled。**默认关闭**与 API 侧同一条纪律：这一轮会给每家打两个
// 上游请求，一个没配好凭据的环境只会每轮产生两次注定失败的调用。
//
// 供应商清单由 sms.BuildProviders 装配——与 platform-api 同一个函数，两个进程
// 必须认识**同一组供应商**：各写一份解析迟早会分叉成「API 能买号、worker 不
// 认识这家」，而那种分叉不报错，只是那家的余额永远没有数据。
//
// notifier 传 nil：巡检只做连接测试与读余额，不取码，没有要推送的东西。
func buildSMSProber(
	pool *pgxpool.Pool,
	provider secrets.SecretProvider,
	environment string,
	getenv func(string) string,
) (jobs.SMSProber, error) {
	mode, err := sms.ParseMode(getenv("XM_SMS_MODE"))
	if err != nil {
		return nil, err
	}
	if mode == sms.ModeOff {
		return nil, nil
	}
	if pool == nil {
		return nil, fmt.Errorf("接码巡检需要数据库连接")
	}
	providers, err := sms.BuildProviders(mode, provider, time.Now)
	if err != nil {
		return nil, err
	}
	store := sms.NewPgStore(pool, environment, time.Now)
	return sms.NewService(providers, store, nil, time.Now), nil
}

// smsSecretProvider 与卡片那侧同样的构造（审计包裹的文件 Provider）。
// 单独一个函数是为了让「谁在读哪一类密钥」在审计日志里分得开。
func smsSecretProvider(secretRoot, environment string, logger *slog.Logger) secrets.SecretProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return secrets.NewAudited(secrets.NewFileProvider(secretRoot), secrets.NewSlogRecorder(logger), environment)
}
