-- XM-SMS2（ADR-022 决策 3）：路由规则。
--
-- 「要号」默认由系统按规则选供应商，人只在想指定时才选。规则按「服务 × 国家」
-- 配：供应商的优先级列表（第一家失败回落下一家）与单价上限。
--
-- 放数据库不放环境变量，与供应商开关同一条纪律（000038）：换供应商、某家挂了
-- 先停掉，都是运营随时会改的决定；走 Action 才有「谁在什么时候改了路由」。
CREATE TABLE IF NOT EXISTS sms.routing_rule (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment    text        NOT NULL,
    -- service / country 用 '*' 表示任意。命中顺序：精确 > 服务通配国家 >
    -- 国家通配服务 > 全通配（见代码 ResolveRoute）。
    service        text        NOT NULL,
    country        text        NOT NULL,
    -- providers 是优先级顺序；供应商名字由注册表校验（000041 起不再用 CHECK）。
    providers      text[]      NOT NULL,
    -- 单价上限，按各家自己的币种比较、不折算；NULL = 不限。numeric 不是 float
    -- （宪法 13），对外一律文本。
    max_unit_price numeric,
    enabled        boolean     NOT NULL DEFAULT TRUE,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    CONSTRAINT sms_routing_rule_key UNIQUE (environment, service, country),
    CONSTRAINT sms_routing_rule_providers_nonempty CHECK (cardinality(providers) > 0),
    CONSTRAINT sms_routing_rule_price_positive CHECK (max_unit_price IS NULL OR max_unit_price > 0)
);
