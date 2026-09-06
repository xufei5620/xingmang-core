-- XM-INV-SMTP-TEST-RECIPIENT-SETTING：测试邮件的收件邮箱改为在管理端配置。
--
-- 产品负责人 2026-09-06：「为什么这个接收测试邮箱我不能自己在后台设置？」
-- 原方案把它钉在宿主机环境变量 SMTP_TEST_RECIPIENT 里，换一个地址要 SSH 上去
-- 改 .env 再重建容器。按本仓库既有的归属纪律，运营会调的东西进后台。
--
-- **为什么加一列而不是像 0028 那样新建一张表**：0028 的通知地址与 SMTP 口令
-- 生命周期无关（清 SMTP 口令走 DELETE，会连带抹掉通知地址），所以必须分开。
-- 这里正相反——测试收件人**必须与 smtp_from 不同**，两者要在同一次保存里
-- 一起校验、一起提交、共用 admin_settings 那把 revision 乐观锁。拆成两张表
-- 就没法用一个 CHECK 表达「不得与发件人相同」，也会出现「发件人改成了 A、
-- 收件人还停在 A」的中间状态。
--
-- **不是凭据，所以存明文**：它只是一个内部邮箱地址，管理员本来就能在页面上
-- 看到 smtp_from。与 notice_webhook_setting 不同——那个整串 URL 里带着鉴权
-- key，才必须只存密文与指纹。
--
-- 空串 = 未配置。生产上这一列迁移后必然是空的（迁移读不到环境变量），由
-- 运行时回退到 SMTP_TEST_RECIPIENT 兜住，直到管理员在页面上存一次为止。
-- 这是**过渡期**的回退，不是「默认值」：一旦这一列非空，环境变量就不再参与。

ALTER TABLE admin_settings
    ADD COLUMN smtp_test_recipient TEXT NOT NULL DEFAULT ''
        CONSTRAINT admin_settings_test_recipient_shape CHECK (
            smtp_test_recipient = ''
            OR (
                char_length(smtp_test_recipient) <= 320
                -- 单个裸地址：不接受 "显示名 <a@b>"、逗号分号分隔的多个收件人，
                -- 也不接受任何空白或控制字符——后者是邮件头注入的入口。
                AND smtp_test_recipient ~ '^[^[:space:][:cntrl:]<>,;:"]+@[^[:space:][:cntrl:]<>,;:"]+$'
            )
        );

-- 「不得与发件人相同」原本只活在 HTTP 处理器里（SMTP_TEST_RECIPIENT_CONFLICT）。
-- 收件人进了库，这条规则就该由库来兜底：绕过服务层的任何写入都不该产生
-- 「自己发给自己」的配置——那样一封测试邮件既证明不了投递，也可能被当成回环。
ALTER TABLE admin_settings
    ADD CONSTRAINT admin_settings_test_recipient_differs_from_sender CHECK (
        smtp_test_recipient = '' OR lower(smtp_test_recipient) <> lower(smtp_from)
    );

COMMENT ON COLUMN admin_settings.smtp_test_recipient IS
    '「发送测试邮件」的固定收件人；空串=未配置（回退到环境变量 SMTP_TEST_RECIPIENT）。必须与 smtp_from 不同。';
