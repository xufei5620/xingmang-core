import { Link } from "react-router";

/** 审批队列所在的地址（router.tsx 的 `/actions` + navigation.ts 的子页签 id
 *  `pending`）。写成常量是为了让回执里那句「去哪里看」和实际路由一起改。 */
export const APPROVAL_QUEUE_PATH = "/actions?sub=pending";

/** 执行完了：Handler 真的跑过，有 action_run_id。 */
export interface ActionExecutedResult {
  /** 一句话说清「刚才发生了什么」，用动词过去式，别用「操作成功」。 */
  title: string;
  /** 规格 §5.8：所有写接口返回 action_run_id，界面必须把它显示出来。 */
  runId: string;
}

/** 已受理为审批单：**动作还没发生**，等人批准后才由人在审批队列里触发。 */
export interface ActionApprovalPendingResult {
  /** 一句话说清「刚才提交了什么」。**不能写成「已开卡」「已提现」那种完成态**
   *  ——那件事还没发生，写成完成态就是在骗人。 */
  title: string;
  /** 审批单号（后端 202 体的 `approval_request_id`）。 */
  approvalRequestId: string;
  /** 内核给的那句人话，原样带上；没有就不显示，不自己编一句。 */
  message?: string;
}

/** 一次写操作的回执。
 *
 *  **两种结局是两个类型，不是一个类型的两种取值**：L2 及以上的调用不会同步
 *  执行，而是落成一张审批单（HTTP 202）。曾经这里只有 `{title, runId}`，
 *  于是「已受理为审批单 X」被显示成一次 run_id 为空的成功——这条回执正是那个
 *  谎话说出口的地方，所以修在这里。 */
export type ActionResult = ActionExecutedResult | ActionApprovalPendingResult;

function isApprovalPending(result: ActionResult): result is ActionApprovalPendingResult {
  return "approvalRequestId" in result;
}

/** 写操作后的回执条。
 *
 *  用 `role="status"` 而不是 `alert`：这是一条回执，不该抢走屏幕阅读器的当前
 *  焦点（失败才用 alert，见 ActionErrorNote）。
 *
 *  执行成功那一支刻意**不说**「已记入审计」：业务写、ActionRun、审计追加三段
 *  目前不是原子的，审计还是 fail-open（#38/#40）。拿到 run_id 只证明动作执行了,
 *  不证明审计里一定有这条事件——所以那里给的是入口和检索方法，不是保证。
 *
 *  审批那一支用 warning 而不是 success 的配色：颜色也是文案的一部分，绿色会
 *  在人读到字之前就先告诉他「成了」。 */
export function ActionResultNote({
  result,
  onDismiss,
}: {
  result: ActionResult;
  onDismiss?: () => void;
}) {
  if (isApprovalPending(result)) {
    return (
      <div
        role="status"
        className="flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
      >
        <span className="font-medium">{result.title}</span>
        {result.approvalRequestId ? (
          <span className="font-mono text-fg-muted" title="approval_request_id">
            审批单号 {result.approvalRequestId}
          </span>
        ) : (
          // 拿到 202 却没有单号：说出来而不是留一个空位置。
          // 「没落单」与「落了单但号没带回来」在排障时是两条不同的线索
          <span className="text-fg-muted">这次响应里没有 approval_request_id</span>
        )}
        <Link to={APPROVAL_QUEUE_PATH} className="underline underline-offset-2">
          去「操作与审批 · 待审批」
        </Link>
        <span className="text-fg-muted">
          批准之后要在那一页由人点「执行」，动作才真的发生
        </span>
        {result.message ? (
          <span className="w-full text-fg-muted">服务端原话：{result.message}</span>
        ) : null}
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

/** 把一次 submitAction 的结局折成回执。
 *
 *  两个标题必须分开给，而且**都由调用点写**：执行完了说「已开卡」，落单了只能
 *  说「已提交开卡审批」。让这一层按模板拼一句，就等于把「事情有没有发生」交给
 *  一个不知道业务的函数去措辞。 */
export function actionResultOf(
  outcome:
    | { kind: "executed"; runId: string }
    | { kind: "approval_pending"; approvalRequestId: string; message: string },
  titles: { executed: string; approvalPending: string },
): ActionResult {
  if (outcome.kind === "approval_pending") {
    return {
      title: titles.approvalPending,
      approvalRequestId: outcome.approvalRequestId,
      ...(outcome.message ? { message: outcome.message } : {}),
    };
  }
  return { title: titles.executed, runId: outcome.runId };
}
