import { Link } from "react-router";

/** 一次 Action 执行的回执。 */
export interface ActionResult {
  /** 一句话说清「刚才发生了什么」，用动词过去式，别用「操作成功」。 */
  title: string;
  /** 规格 §5.8：所有写接口返回 action_run_id，界面必须把它显示出来。 */
  runId: string;
}

/** 写操作成功后的回执条：说清做了什么 + run_id + 去审计页的入口。
 *
 *  用 `role="status"` 而不是 `alert`：这是一条成功回执，不该抢走屏幕阅读器的
 *  当前焦点（失败才用 alert，见 ActionErrorNote）。
 *
 *  文案上刻意**不说**「已记入审计」：业务写、ActionRun、审计追加三段目前不是
 *  原子的，审计还是 fail-open（#38/#40）。拿到 run_id 只证明动作执行了,
 *  不证明审计里一定有这条事件——所以这里给的是入口和检索方法，不是保证。 */
export function ActionResultNote({
  result,
  onDismiss,
}: {
  result: ActionResult;
  onDismiss?: () => void;
}) {
  return (
    <div
      role="status"
      className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-success bg-success/10 px-3 py-2 text-xs text-success"
    >
      <span className="font-medium">{result.title}</span>
      {result.runId ? (
        <span className="font-mono text-fg-muted" title="action_run_id">
          run_id {result.runId}
        </span>
      ) : (
        // 后端没回 run_id 时说出来，而不是显示一个空的 run_id 位置:
        // 「没拿到」与「拿到了但是空的」在排障时是两条不同的线索
        <span className="text-fg-muted">这次响应里没有 action_run_id</span>
      )}
      <Link to="/audit" className="underline underline-offset-2">
        去审计记录页
      </Link>
      <span className="text-fg-muted">
        {result.runId ? "在那一页搜索上面的 run_id 可定位到事件" : null}
      </span>
      {onDismiss ? (
        <button
          type="button"
          onClick={onDismiss}
          className="ml-auto rounded-md px-1 text-fg-muted underline underline-offset-2 outline-none focus-visible:outline-2 focus-visible:outline-accent"
        >
          知道了
        </button>
      ) : null}
    </div>
  );
}
