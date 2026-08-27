import { useMutation } from "@tanstack/react-query";
import { PageState } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import {
  describeCredential,
  removeTokenMapping,
  setTokenMapping,
  TOKEN_MAP_MANAGE_PERMISSION,
  type TokenMappingItem,
  type UpstreamAccountItem,
} from "../api/finance";
import {
  buildTokenMapParams,
  EMPTY_TOKEN_MAP_FORM,
  validateTokenMapForm,
  type TokenMapFormErrors,
  type TokenMapFormValues,
} from "../lib/upstreamForm";
import { ActionErrorNote } from "./ActionErrorNote";
import type { ActionResult } from "./ActionResultNote";

/** 令牌映射（成本侧键 ↔ 收入侧键）的维护区。
 *
 *  手工维护是 §11.11 与 §12 的拍板结果：自动发现只产出**候选**，落地仍要人确认。
 *  一条写错的映射会把成本记到别的渠道上——两条渠道的毛利一个虚高一个虚低,
 *  合计却完全正确，是最难从总数上看出来的一类错误。 */
export function TokenMappingEditor({
  account,
  onDone,
}: {
  account: UpstreamAccountItem;
  onDone: (result: ActionResult) => void;
}) {
  // sub2api 的计量型渠道：每条映射都必须有每令牌凭据，否则成本侧的
  // /v1/usage 根本打不出去（§3.1）。newapi 走账号级会话，没有是正常的
  const credentialRequired =
    account.system_type === "sub2api" && account.access_method === "upstream_key";

  return (
    <section className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-xs font-medium text-fg">
          令牌映射
          <span className="ml-2 font-normal text-fg-muted">
            成本侧的上游令牌对到我方哪个账号，决定这笔成本记在谁头上
          </span>
        </h4>
        <TokenMappingDialog
          account={account}
          credentialRequired={credentialRequired}
          onDone={onDone}
        />
      </div>

      {account.token_mappings.length === 0 ? (
        <PageState
          kind="empty"
          compact
          title="这个账号还没有令牌映射"
          description={
            credentialRequired
              ? "sub2api 计量型渠道没有映射就取不到成本：采集器不知道该用哪把令牌去问 /v1/usage，这个账号的成本会一直是空的。"
              : "没有映射时，这个账号的上游消耗归不到具体的我方账号上。"
          }
        />
      ) : (
        <MappingTable
          account={account}
          credentialRequired={credentialRequired}
          onDone={onDone}
        />
      )}
    </section>
  );
}

const TH = "px-2 py-1 text-left text-xs font-medium text-fg-muted whitespace-nowrap";
const TD = "px-2 py-1 align-top text-xs text-fg";

function MappingTable({
  account,
  credentialRequired,
  onDone,
}: {
  account: UpstreamAccountItem;
  credentialRequired: boolean;
  onDone: (result: ActionResult) => void;
}) {
  return (
    <div className="relative max-w-full overflow-x-auto rounded-md border border-edge">
      <table className="w-full border-collapse">
        <caption className="sr-only">令牌映射：上游令牌、我方账号与每令牌凭据</caption>
        <thead className="border-b border-edge bg-surface-muted">
          <tr>
            {["上游令牌", "我方账号", "凭据", "更新时间", "操作"].map((h) => (
              <th key={h} scope="col" className={TH}>
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {account.token_mappings.map((m) => {
            const cred = describeCredential(m.credential_ref);
            return (
              <tr key={m.upstream_token_id} className="border-b border-edge last:border-b-0">
                <td className={`${TD} font-mono break-all`}>{m.upstream_token_id}</td>
                <td className={`${TD} font-mono break-all`}>{m.own_account_id}</td>
                <td className={TD}>
                  {/* 缺凭据在 sub2api 计量型上是**阻断性**的，不是一句提示 */}
                  <Badge
                    tone={
                      cred.configured ? "success" : credentialRequired ? "danger" : "neutral"
                    }
                    title={
                      cred.configured
                        ? cred.hint
                        : credentialRequired
                          ? "缺每令牌凭据，这条映射取不到成本"
                          : cred.hint
                    }
                  >
                    {cred.label}
                  </Badge>
                </td>
                <td className={`${TD} tabular-nums whitespace-nowrap`}>{m.updated_at}</td>
                <td className={TD}>
                  <div className="flex gap-1">
                    <TokenMappingDialog
                      account={account}
                      credentialRequired={credentialRequired}
                      mapping={m}
                      onDone={onDone}
                    />
                    <RemoveTokenMappingDialog account={account} mapping={m} onDone={onDone} />
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/** 登记 / 改写一条映射(`finance.token_map.set@1`)。 */
function TokenMappingDialog({
  account,
  credentialRequired,
  mapping,
  onDone,
}: {
  account: UpstreamAccountItem;
  credentialRequired: boolean;
  mapping?: TokenMappingItem;
  onDone: (result: ActionResult) => void;
}) {
  const editing = Boolean(mapping);
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<TokenMapFormValues>(() => initialMapping(mapping));
  const [errors, setErrors] = useState<TokenMapFormErrors>({});
  const [attempts, setAttempts] = useState(0);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (form: TokenMapFormValues) =>
      setTokenMapping(buildTokenMapParams(account.id, form)),
    onSuccess: (run) => {
      setOpen(false);
      setErrors({});
      setAttempts(0);
      onDone({
        title: editing ? "映射已改写" : "映射已登记",
        runId: run.runId,
      });
    },
  });

  const summaryRef = useRef<HTMLDivElement>(null);
  const showSummary = attempts > 0 && Boolean(mutation.error);
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [attempts, mutation.error, showSummary]);

  const set = (field: keyof TokenMapFormValues) => (value: string) => {
    setValues((prev) => ({ ...prev, [field]: value }));
    setErrors((prev) => {
      if (!(field in prev)) return prev;
      const next = { ...prev };
      delete next[field];
      return next;
    });
  };

  const submit = () => {
    setAttempts((n) => n + 1);
    const found = validateTokenMapForm(values, credentialRequired);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    mutation.mutate(values);
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) setValues(initialMapping(mapping));
        else {
          setErrors({});
          setAttempts(0);
          mutation.reset();
        }
      }}
      trigger={
        <Button size="sm" variant={editing ? "ghost" : "secondary"}>
          {editing ? "改写" : "新增映射"}
        </Button>
      }
      title={editing ? "改写令牌映射" : "登记令牌映射"}
      description="通过 finance.token_map.set@1 写入。写错会把成本记到别的渠道上，提交前请核对两侧标识。"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <FormField
          label="上游令牌标识"
          htmlFor={`${fieldPrefix}-token`}
          required
          {...(errors.upstream_token_id ? { error: errors.upstream_token_id } : {})}
          hint={
            editing
              ? "改写模式下这一项就是被改写的那条的键，不可更改"
              : "成本侧的键：上游系统里那把令牌的 ID"
          }
        >
          <Input
            value={values.upstream_token_id}
            readOnly={editing}
            invalid={Boolean(errors.upstream_token_id)}
            onChange={(e) => set("upstream_token_id")(e.target.value)}
          />
        </FormField>

        <FormField
          label="我方账号标识"
          htmlFor={`${fieldPrefix}-own`}
          required
          {...(errors.own_account_id ? { error: errors.own_account_id } : {})}
          hint="收入侧的键：这笔成本要记到我方哪个账号头上"
        >
          <Input
            value={values.own_account_id}
            invalid={Boolean(errors.own_account_id)}
            onChange={(e) => set("own_account_id")(e.target.value)}
          />
        </FormField>

        <FormField
          label="每令牌凭据引用"
          htmlFor={`${fieldPrefix}-cred`}
          required={credentialRequired}
          {...(errors.credential_ref ? { error: errors.credential_ref } : {})}
          hint={
            credentialRequired
              ? "sub2api 计量型必填：成本侧要用这把令牌的凭据打 /v1/usage。填引用，不是密钥"
              : "可留空：newapi 侧走账号级会话，没有每令牌凭据是正常状态"
          }
        >
          <Input
            value={values.credential_ref}
            invalid={Boolean(errors.credential_ref)}
            onChange={(e) => set("credential_ref")(e.target.value)}
          />
        </FormField>

        <div
          ref={summaryRef}
          tabIndex={-1}
          className="outline-none focus-visible:ring-2 focus-visible:ring-accent"
        >
          <ActionErrorNote error={mutation.error} permission={TOKEN_MAP_MANAGE_PERMISSION} />
        </div>

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending}>
            {editing ? "保存" : "登记"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** 移除一条映射(`finance.token_map.remove@1`,reason 必填)。
 *
 *  删除路径必须存在：一条写错的映射会持续把成本记到别的渠道上,
 *  没有删除入口时唯一的补救是直接改库——那正是宪法 2 条要挡的绕过。 */
function RemoveTokenMappingDialog({
  account,
  mapping,
  onDone,
}: {
  account: UpstreamAccountItem;
  mapping: TokenMappingItem;
  onDone: (result: ActionResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | undefined>(undefined);
  const [attempts, setAttempts] = useState(0);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (text: string) =>
      removeTokenMapping({
        upstream_account_id: account.id,
        upstream_token_id: mapping.upstream_token_id,
        reason: text.trim(),
      }),
    onSuccess: (run) => {
      setOpen(false);
      setReason("");
      setAttempts(0);
      onDone({ title: "映射已移除", runId: run.runId });
    },
  });

  const summaryRef = useRef<HTMLDivElement>(null);
  const showSummary = attempts > 0 && Boolean(mutation.error);
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [attempts, mutation.error, showSummary]);

  const submit = () => {
    setAttempts((n) => n + 1);
    if (!reason.trim()) {
      setError("必须写明为什么移除：这条映射消失之后，它原先承担的成本会变成无归属");
      return;
    }
    setError(undefined);
    mutation.mutate(reason);
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setReason("");
          setError(undefined);
          setAttempts(0);
          mutation.reset();
        }
      }}
      trigger={
        <Button size="sm" variant="ghost">
          移除
        </Button>
      }
      title="移除令牌映射"
      description="通过 finance.token_map.remove@1 删除，理由必填并进审计链。"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <dl className="grid grid-cols-1 gap-1 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs">
          <div>
            <dt className="text-fg-muted">上游令牌</dt>
            <dd className="font-mono break-all text-fg">{mapping.upstream_token_id}</dd>
          </div>
          <div>
            <dt className="text-fg-muted">当前记到</dt>
            <dd className="font-mono break-all text-fg">{mapping.own_account_id}</dd>
          </div>
        </dl>

        <FormField
          label="移除理由"
          htmlFor={`${fieldPrefix}-reason`}
          required
          {...(error ? { error } : {})}
          hint="会进审计链。移除之后这条令牌的成本不再归到上面那个账号"
        >
          <Input
            value={reason}
            invalid={Boolean(error)}
            onChange={(e) => {
              setReason(e.target.value);
              setError(undefined);
            }}
          />
        </FormField>

        <div
          ref={summaryRef}
          tabIndex={-1}
          className="outline-none focus-visible:ring-2 focus-visible:ring-accent"
        >
          <ActionErrorNote error={mutation.error} permission={TOKEN_MAP_MANAGE_PERMISSION} />
        </div>

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" variant="danger" loading={mutation.isPending}>
            移除
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function initialMapping(mapping?: TokenMappingItem): TokenMapFormValues {
  if (!mapping) return { ...EMPTY_TOKEN_MAP_FORM };
  return {
    upstream_token_id: mapping.upstream_token_id,
    own_account_id: mapping.own_account_id,
    credential_ref: mapping.credential_ref,
  };
}
