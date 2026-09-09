import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, PageState, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, Input } from "@xingmang/ui-primitives";
import { useId, useRef, useState } from "react";
import type { ApiClient } from "../api/client";
import {
  CREDENTIAL_MANAGE_PERMISSION,
  CREDENTIAL_QUERY_KEYS,
  listExpectedCredentials,
  rotateCredential,
  upsertCredential,
  type ExpectedCredential,
} from "../api/credentials";
import type { ActionRun } from "../api/platform";
import { redactActionError } from "../lib/credentialErrors";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

export interface ExpectedCredentialsPanelProps {
  /** 可选注入点：单测给一个脱离真实后端的 client。 */
  client?: ApiClient;
}

type WriteMode = "upsert" | "rotate";

interface PasteVariables {
  mode: WriteMode;
  /** 只在本次请求内存在；成功或失败后由行表单清空。 */
  secretValue: string;
}

/** 平台需要的凭据清单 + 逐行「粘贴并保存」。
 *
 *  哪几条是「需要的」由后端说（GET /credentials/expected），页面不自带清单。
 *  缺失的走 credential.secret.upsert；已配置的再粘贴一次等于轮换
 *  （credential.secret.rotate）。两者都只把值放进本次请求，之后输入框清空。 */
export function ExpectedCredentialsPanel({ client }: ExpectedCredentialsPanelProps) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: CREDENTIAL_QUERY_KEYS.expected,
    queryFn: ({ signal }) => listExpectedCredentials({ signal }, client),
  });
  const [notice, setNotice] = useState<ActionResult | null>(null);
  const titleId = useId();

  const onSaved = (run: ActionRun, item: ExpectedCredential, mode: WriteMode) => {
    setNotice({
      title: `${mode === "rotate" ? "已轮换" : "已保存"} ${item.credential_ref}`,
      runId: run.runId,
    });
    // 「缺失」徽章与下方元数据表要一起变：一次作废整个 ["credentials"] 前缀
    void queryClient.invalidateQueries({ queryKey: CREDENTIAL_QUERY_KEYS.all });
  };

  const columns: DataTableColumn<ExpectedCredential>[] = [
    {
      id: "credential_ref",
      header: "CredentialRef",
      primary: true,
      value: (row) => row.credential_ref,
      cell: (row) => <span className="font-mono text-xs break-all text-fg">{row.credential_ref}</span>,
    },
    {
      id: "platform",
      header: "平台",
      value: (row) => row.platform,
      cell: (row) => <span className="font-mono text-xs text-fg">{row.platform || "—"}</span>,
    },
    {
      id: "purpose",
      header: "用途",
      value: (row) => row.purpose,
      cell: (row) => <span className="text-xs text-fg">{row.purpose || "—"}</span>,
    },
    {
      id: "configured",
      header: "状态",
      value: (row) => (row.configured ? "已配置" : "缺失"),
      cell: (row) =>
        row.configured ? (
          <Badge tone="success" title="已保存且未撤销；再粘贴一次即轮换">
            已配置
          </Badge>
        ) : (
          <Badge tone="warning" title="尚未保存；对应平台切到 real 之前必须补齐">
            缺失
          </Badge>
        ),
    },
    {
      id: "paste",
      header: "粘贴并保存",
      cell: (row) => <PasteSecretForm item={row} client={client} onSaved={onSaved} />,
    },
  ];

  const rows = query.data ?? [];

  return (
    <section className="flex min-w-0 flex-col gap-3" aria-labelledby={titleId}>
      <div className="rounded-lg border border-edge bg-surface px-4 py-3 shadow-sm">
        <h2 id={titleId} className="text-base font-semibold text-fg">
          平台需要的凭据
        </h2>
        <p className="mt-1 max-w-4xl text-xs leading-5 text-fg-muted">
          清单由后端声明。缺失的粘贴一次即保存（credential.upsert）；已配置的再粘贴等于轮换（credential.rotate）。
          值只在本次请求内存在，保存后不会再显示。
        </p>
      </div>

      {notice ? <ActionResultNote result={notice} onDismiss={() => setNotice(null)} /> : null}

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {rows.length === 0 ? (
          <PageState
            kind="empty"
            title="后端没有声明需要的凭据"
            description="GET /api/v1/credentials/expected 返回了空清单；这不代表凭据都已配置。"
          />
        ) : (
          <DataTableV2
            caption="平台需要的凭据：CredentialRef、平台、用途、配置状态与粘贴保存表单"
            columns={columns}
            rows={rows}
            rowKey={(row) => row.credential_ref}
            emptyState={<PageState kind="empty" title="没有匹配的预期凭据" />}
          />
        )}
      </ApiStateView>
    </section>
  );
}

/** 单行的粘贴表单。每行自己持有输入值与 mutation，行之间互不影响。 */
function PasteSecretForm({
  item,
  client,
  onSaved,
}: {
  item: ExpectedCredential;
  client?: ApiClient;
  onSaved: (run: ActionRun, item: ExpectedCredential, mode: WriteMode) => void;
}) {
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [actionError, setActionError] = useState<unknown>(null);
  const lastSubmittedSecret = useRef("");
  const inputId = useId();
  const mode: WriteMode = item.configured ? "rotate" : "upsert";

  const mutation = useMutation<ActionRun, unknown, PasteVariables>({
    mutationFn: ({ mode: nextMode, secretValue }) => {
      lastSubmittedSecret.current = secretValue;
      const input = { credentialRef: item.credential_ref, secretValue };
      return nextMode === "rotate"
        ? rotateCredential(input, {}, client)
        : upsertCredential(input, {}, client);
    },
    onSuccess: (run, variables) => {
      lastSubmittedSecret.current = "";
      setValue("");
      setActionError(null);
      // TanStack Query 会暂存 mutation variables；清掉，别让 secret_value
      // 在前端状态里比请求生命周期更久
      mutation.reset();
      onSaved(run, item, variables.mode);
    },
    onError: (err) => {
      const secret = lastSubmittedSecret.current;
      lastSubmittedSecret.current = "";
      setValue("");
      setActionError(redactActionError(err, secret));
      mutation.reset();
    },
  });

  return (
    <form
      className="flex min-w-[16rem] flex-col gap-1"
      onSubmit={(event) => {
        event.preventDefault();
        if (!value.trim()) {
          setError("请粘贴凭据值；保存后不会再次显示");
          return;
        }
        setError(undefined);
        setActionError(null);
        mutation.mutate({ mode, secretValue: value });
      }}
    >
      <div className="flex items-center gap-2">
        <Input
          id={inputId}
          type="password"
          aria-label={`${item.credential_ref} 的凭据值`}
          value={value}
          invalid={Boolean(error)}
          disabled={mutation.isPending}
          autoComplete="new-password"
          spellCheck={false}
          placeholder={mode === "rotate" ? "粘贴新值即轮换" : "粘贴值即保存"}
          onChange={(event) => {
            setValue(event.target.value);
            setError(undefined);
            setActionError(null);
          }}
        />
        <Button
          type="submit"
          size="sm"
          loading={mutation.isPending}
          aria-label={`粘贴并保存 ${item.credential_ref}`}
        >
          粘贴并保存
        </Button>
      </div>
      {error ? (
        <p role="alert" className="text-xs text-danger">
          {error}
        </p>
      ) : null}
      <ActionErrorNote error={actionError} permission={CREDENTIAL_MANAGE_PERMISSION} />
    </form>
  );
}
