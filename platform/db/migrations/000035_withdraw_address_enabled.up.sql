-- XM-CARD6：提现地址可以在管理后台下线。
--
-- 用**停用**而不是删除。删掉一行就再也回答不了「这条地址当初是谁登记的、
-- 什么时候下线的」，而那恰恰是出事之后第一个要问的问题；一条曾经被列入
-- 白名单的地址，它存在过这件事本身就是审计事实。
--
-- 已经发出去的提现不受影响：提现台账里存的是登记时的地址**快照**
-- （见迁移 000033 的 address 列），下线一条登记不会让历史记录变得查不到
-- 钱转去了哪儿。
--
-- 默认 TRUE：既有的行都是启用状态，这次迁移不改变任何一条地址的可用性。
-- 领域层结构体那一侧的默认是相反的（零值 = 不可用），理由写在
-- WithdrawAddress.Enabled 上——库里的行永远带着真实状态，会踩到零值的只有
-- 手写的构造点，而在资金路径上那正是最该被挡下的地方。
ALTER TABLE cards.withdraw_address
    ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT TRUE;

-- 谁改的状态。registered_by 记的是「谁登记的」，两者不是同一个人也不是
-- 同一件事——下线往往正是因为登记那次出了问题。
ALTER TABLE cards.withdraw_address
    ADD COLUMN IF NOT EXISTS status_changed_by TEXT NOT NULL DEFAULT '';
