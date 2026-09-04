-- XM-CARD6：资金提现。
--
-- **这是平台风险最高的动作**：把钱转到平台之外，不可逆、不可追回。
-- 三道闸依次是地址白名单、金额上限（不许 unlimited）、幂等台账。
--
-- 一、提现地址白名单。
--
-- 存在的理由：提现表单不允许手打地址，只能从这张表里选。白名单挡不住
-- 有人拿密钥直接打 Infini（那条路平台管不了），但能挡住误操作与通过后台
-- 的滥用——而这两样才是日常真正会发生的。一次粘贴错误就是钱没了。
--
-- 地址与链一起登记：同一串地址在不同链上可能都「看起来合法」，
-- 而转错链的钱找不回来。
CREATE TABLE IF NOT EXISTS cards.withdraw_address (
    id           TEXT        PRIMARY KEY,
    environment  TEXT        NOT NULL,
    account      TEXT        NOT NULL,
    chain        TEXT        NOT NULL,
    address      TEXT        NOT NULL,
    label        TEXT        NOT NULL DEFAULT '',
    -- 谁登记的、什么时候登记的。地址登记本身也是敏感动作，要能追溯。
    registered_by TEXT       NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL
);

-- 同一账号同一条链上的同一个地址只登记一次。
CREATE UNIQUE INDEX IF NOT EXISTS withdraw_address_identity_idx
    ON cards.withdraw_address (environment, account, chain, address);

-- 二、提现台账。
--
-- 与卡片的 card_operation 分开：提现有**真正的幂等键**（上游的 request_id），
-- 不需要 card_operation 那套「派生 alias + 宽限期对账」的机制——超时后按
-- request_id 查一次状态就有答案。硬塞进同一张表会让两套完全不同的收敛逻辑
-- 共用一组列，读代码的人分不清哪几列对哪一种。
--
-- 金额存文本：单位是代币，标度未验证；与操作台账同一条纪律。
CREATE TABLE IF NOT EXISTS cards.withdraw_request (
    request_id      TEXT        PRIMARY KEY,
    environment     TEXT        NOT NULL,
    account         TEXT        NOT NULL,
    chain           TEXT        NOT NULL,
    token_type      TEXT        NOT NULL,
    amount_text     TEXT        NOT NULL,
    address_id      TEXT        NOT NULL,
    -- 地址在这里再存一份快照：登记条目以后可能被改名或删除，
    -- 而「这笔钱当时转到哪儿」必须永远查得到。
    address         TEXT        NOT NULL,
    status          TEXT        NOT NULL,
    tx_hash         TEXT        NOT NULL DEFAULT '',
    actual_amount   TEXT        NOT NULL DEFAULT '',
    gas_fee         TEXT        NOT NULL DEFAULT '',
    gas_fee_currency TEXT       NOT NULL DEFAULT '',
    fx_fee          TEXT        NOT NULL DEFAULT '',
    fx_fee_currency TEXT        NOT NULL DEFAULT '',
    note            TEXT        NOT NULL DEFAULT '',
    started_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL
);

-- 今日累计要按账号与日期算，且**把未收敛的也算进去**：
-- 那些可能真的转出去了，当没转过会让单日上限在最需要生效时失效。
CREATE INDEX IF NOT EXISTS withdraw_request_daily_idx
    ON cards.withdraw_request (environment, account, started_at);

-- 未收敛的提现：pending/processing 要被反复查状态直到终态。
CREATE INDEX IF NOT EXISTS withdraw_request_open_idx
    ON cards.withdraw_request (environment, status)
    WHERE status IN ('pending', 'processing');
