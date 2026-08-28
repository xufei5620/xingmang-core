import { ChannelTable } from "./ChannelTable";

/** Sub2API · 渠道管理（原型 `V["s2/upstream"]`）。
 *
 *  页头归平台详情页所有，这里只画内容（XM-0034 起的约定）。
 *  结构与顶部四格见 `ChannelTable`——两个平台共用，差异用参数表达。 */
export function ChannelsPanel() {
  return (
    <ChannelTable
      platform="sub2api"
      lead="一行对应一个上游账号，并映射到分组倍率与 Key / 订阅账号；成本和利润按这一行单独核算。"
    />
  );
}
