import { EmptyState } from "@xingmang/ui-primitives";

export function DashboardPage() {
  return (
    <section>
      <h2 className="mb-4 text-base font-semibold">运营总览</h2>
      <EmptyState
        title="Sub2API 数据未接入"
        description="等待 XM-0014 数据新鲜度模型与 XM-0017 只读 Connector 交付后展示真实数据"
      />
    </section>
  );
}
