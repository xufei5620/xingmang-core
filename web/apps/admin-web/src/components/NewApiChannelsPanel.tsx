import { ChannelTable } from "./ChannelTable";

/** NewAPI · 渠道管理（原型 `V["newapi/upstream"]`）。
 *
 *  与 Sub2API 共用同一张表：原型给两个平台画的表几乎相同，
 *  差别只有主列的名字、顶部第一格和 Sub2API 多一列「成功率」。
 *
 *  ⚠️ 本片把行粒度改成了上游账号之后，NewAPI 原来那三列
 *  （启停 / 错误率 / 延迟，来自 `newapi.channels.status` 指标）**不在这张表上了**：
 *  那条指标一行 = NewAPI 自己的一条渠道，与上游账号对不上号
 *  （见 `ChannelTable` 顶部的说明）。原型的 NewAPI 渠道表也没有这三列，
 *  它们的去处是渠道保障（M1.5）。 */
export function NewApiChannelsPanel({ serviceId, serviceStatus }: { serviceId?: string; serviceStatus?: string } = {}) {
  return (
    <ChannelTable
      platform="newapi"
      serviceId={serviceId}
      serviceStatus={serviceStatus}
      lead="一行对应一个上游账号，并映射到分组倍率与 Key；成本与利润按这一行独立核算。"
    />
  );
}
