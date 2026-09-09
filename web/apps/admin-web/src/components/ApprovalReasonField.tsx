import { FormField, Input } from "@xingmang/ui-primitives";

/** 理由的长度下限。
 *
 *  3 个字是**下限，不是标准**：它只挡住空串和「1」「aa」这种明显的敷衍，
 *  真正的把关在审批人那里——一句话够不够说明问题，只有看单的人判断得了。
 *  把这个数字调大不会让理由变好，只会让人学会凑字数。 */
export const MIN_APPROVAL_REASON_LENGTH = 3;

/** 这条理由能不能提交。 */
export function isApprovalReasonUsable(reason: string): boolean {
  return reason.trim().length >= MIN_APPROVAL_REASON_LENGTH;
}

/** 理由太短时给人看的那句话。 */
export const APPROVAL_REASON_TOO_SHORT = `请写清为什么要做这件事（至少 ${MIN_APPROVAL_REASON_LENGTH} 个字），审批人只能看到这句话。`;

export interface ApprovalReasonFieldProps {
  /** 表单内唯一的 input id。 */
  id: string;
  value: string;
  onChange: (next: string) => void;
  /** 这个动作在审批单上是什么，用来把提示写具体（如「这笔提现」「这次开卡」）。 */
  subject: string;
  /** 已经动过手了没有。没动过不显示红字——刚打开的表单不该先骂人一句。 */
  touched?: boolean;
}

/** L2 及以上动作的「理由」输入框。
 *
 *  这句话会原样写进审批单，是审批人唯一能据以判断的东西（审批库层 reason
 *  NOT NULL；内核对空 reason 直接回 INVALID_PARAMS，见 action/kernel.go）。
 *  因此这一格有两条硬规矩：
 *
 *  1. **不预填、不给候选、不自动生成。** 一句拼出来的套话会让审批退化成盖章
 *     ——每张单上的理由都长得一样时，那一栏就不再携带任何信息。所以这里
 *     没有 defaultValue，也不接受「留空就替你写一句」的兜底。
 *  2. **空着时在前端就拦住**，并说明为什么。让人点完提交再吃一个
 *     INVALID_PARAMS，等于用后端的错误码替代界面该说的话。前端拦截**不是**
 *     安全控制，服务端仍会再判一次——这一层只是为了不浪费人一次点击。 */
export function ApprovalReasonField({
  id,
  value,
  onChange,
  subject,
  touched = false,
}: ApprovalReasonFieldProps) {
  const tooShort = touched && !isApprovalReasonUsable(value);
  return (
    <FormField
      label="理由（提交审批用）"
      htmlFor={id}
      required
      hint={`${subject}会先落成一张审批单，这句话原样给审批人看。写清为什么现在要做、依据是什么。`}
      {...(tooShort ? { error: APPROVAL_REASON_TOO_SHORT } : {})}
    >
      <Input
        id={id}
        aria-label="理由"
        value={value}
        invalid={tooShort}
        onChange={(e) => onChange(e.target.value)}
      />
    </FormField>
  );
}
