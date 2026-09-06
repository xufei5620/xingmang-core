-- XM-0030a Action Advanced Controls：审批单与审批记录。
--
-- 设计稿 docs/superpowers/plans/2026-08-27-xm-0030-action-advanced-controls-design.md。
-- Foundation-A 的内核对 L2+ 一律返回 ADVANCED_CONTROLS_REQUIRED（fail closed），
-- 挡住了 Connector/Connection 登记、Kill Switch 拉闸、以及 Foundation-B 全部写
-- 场景。本迁移给「先批准、后执行」备好落库面，**不给任何绕过 Action 的口子**：
-- 审批单只是执行前多了一道可追溯的前置事件，执行仍然走内核原有全链。

CREATE TABLE core.approval_request (
    id                uuid PRIMARY KEY,
    action_id         text NOT NULL CHECK (btrim(action_id) <> ''),
    action_version    integer NOT NULL CHECK (action_version > 0),
    -- 全量参数在提交时冻结。params_hash 是 SHA256(canonical(params))，执行时
    -- 内核重算并比对：**审批过的是这份参数，不是这个意图的任意版本**。
    params_json       jsonb NOT NULL,
    params_hash       text NOT NULL CHECK (params_hash ~ '^[0-9a-f]{64}$'),
    -- 冗余记录提交时的等级：等级表会演进，而一张单批的是它提交那一刻的风险。
    risk_level        text NOT NULL CHECK (risk_level IN ('L2', 'L3', 'L4')),
    environment       text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    requester_id      text NOT NULL CHECK (btrim(requester_id) <> ''),
    -- 提交人可以是任何身份，含 AI（宪法 24 只禁止 AI **投票**，不禁止 AI 提交）。
    -- 词表与 internal/platform/principal 的 Type 常量一致。
    requester_type    text NOT NULL CHECK (requester_type IN ('HUMAN', 'SERVICE', 'AI', 'SERVER_AGENT')),
    reason            text NOT NULL CHECK (btrim(reason) <> ''),
    status            text NOT NULL DEFAULT 'PENDING'
                      CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED', 'EXECUTED', 'EXPIRED', 'CANCELLED')),
    -- 防陈年审批单复活：过期后不可再投票、不可再执行。
    expires_at        timestamptz NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    decided_at        timestamptz NULL,
    executed_at       timestamptz NULL,
    -- 一单最多一跑。UNIQUE 让「重复触发执行」在库层就撞上，而不是靠应用层自觉。
    execution_run_id  uuid NULL UNIQUE,

    -- 状态与时间戳必须自洽，否则「已执行但没有执行时刻」这种行会让审计无从解释。
    CONSTRAINT approval_request_decided_when_terminal CHECK (
        (status IN ('PENDING', 'CANCELLED', 'EXPIRED')) = (decided_at IS NULL)
    ),
    CONSTRAINT approval_request_executed_shape CHECK (
        (status = 'EXECUTED') = (executed_at IS NOT NULL)
        AND (status = 'EXECUTED') = (execution_run_id IS NOT NULL)
    ),
    CONSTRAINT approval_request_expiry_after_creation CHECK (expires_at > created_at)
);

COMMENT ON TABLE core.approval_request IS
    'Action Advanced Controls 审批单（XM-0030）。L2+ 的执行意图在这里冻结参数、等待人工票数。';
COMMENT ON COLUMN core.approval_request.params_hash IS
    'SHA256(canonical(params))；执行时内核重算比对，不一致即拒。';

-- 队列页按状态 + 等级取待办，按创建时间排队。
CREATE INDEX approval_request_status_created_idx
    ON core.approval_request (status, created_at DESC);
-- 过期扫描只关心还在 PENDING 的单。
CREATE INDEX approval_request_pending_expiry_idx
    ON core.approval_request (expires_at)
    WHERE status = 'PENDING';

CREATE TABLE core.approval_decision (
    id            uuid PRIMARY KEY,
    request_id    uuid NOT NULL REFERENCES core.approval_request (id) ON DELETE CASCADE,
    approver_id   text NOT NULL CHECK (btrim(approver_id) <> ''),
    -- **宪法 24 条的库层红线**：AI 不作为 L3/L4 第二审批人。这里比条文更严，
    -- 一律只收 HUMAN——把「AI 能不能投票」变成一个不需要读代码就能回答的问题。
    -- 内核侧另有一道同样的校验（双重），因为库层 CHECK 只在写到这张表时生效，
    -- 而内核要在更早的地方给出可读的错误。
    approver_type text NOT NULL CHECK (approver_type = 'HUMAN'),
    verdict       text NOT NULL CHECK (verdict IN ('APPROVE', 'REJECT')),
    comment       text NOT NULL DEFAULT '',
    -- 投票那一刻此人是否持 approval.l4。冻结成事实而不是执行时再查：权限会
    -- 变动，而「这一票当时算不算特权票」是审计事实，不该被日后的授权变更
    -- 改写——一个人今天被收回特权，不该让他昨天投出的那票追溯失效。
    privileged    boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),

    -- 一人一单只能投一票。改主意要走驳回后重提，不是改票——票是审计事实。
    CONSTRAINT approval_decision_one_vote_per_approver UNIQUE (request_id, approver_id)
);

COMMENT ON TABLE core.approval_decision IS
    '审批记录；approver_type 只收 HUMAN（宪法 24：AI 不作为审批人）。一人一单一票。';

CREATE INDEX approval_decision_request_idx
    ON core.approval_decision (request_id, created_at);
