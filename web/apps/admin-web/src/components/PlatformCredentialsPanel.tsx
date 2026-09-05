import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  FreshnessBadge,
  FreshnessNote,
  PageState,
  ServiceStatusBadge,
  StatTile,
  describeServiceStatus,
  formatUtcTimestamp,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import {
  CREDENTIAL_QUERY_KEYS,
  fingerprintPrefix,
  listCredentials,
  listExpectedCredentials,
  type CredentialMetadata,
  type ExpectedCredential,
} from "../api/credentials";
import {
  listServices,
  serviceFreshness,
  type ServiceItem,
} from "../api/platform";
import { ApiStateView } from "./ApiStateView";

/** 一条凭据引用在本页要显示的全部事实：来自「平台声明需要什么」的清单，
 *  外加（有权限时）元数据表里的指纹与版本。
 *
 *  **永远只有引用与证据，没有值**：指纹是可核对的安全证据，页面只显示前八位；
 *  凭据正文不经过前端，也不在任何 API 响应里。 */
interface CredentialRefRow {
  credentialRef: string;
  purpose: string;
  configured: boolean;
  metadata: CredentialMetadata | undefined;
}

const CREDENTIAL_REF_COLUMNS: DataTableColumn<CredentialRefRow>[] = [
  {
    id: "credential_ref",
    header: "引用名",
    primary: true,
    value: (row) => row.credentialRef,
    cell: (row) => <span className="font-mono text-xs break-all text-fg">{row.credentialRef}</span>,
  },
  {
    id: "purpose",
    header: "用途",
    value: (row) => row.purpose,
    cell: (row) => <span className="text-xs text-fg-muted">{row.purpose || "—"}</span>,
  },
  {
    id: "configured",
    header: "状态",
    value: (row) => (row.configured ? "已配置" : "未配置"),
    cell: (row) =>
      row.configured ? (
        <Badge tone="success" title="平台已保存这条引用的值，且未撤销">
          已配置
        </Badge>
      ) : (
        <Badge tone="warning" title="平台声明需要这条引用，但还没有保存过值">
          未配置
        </Badge>
      ),
  },
  {
    id: "fingerprint",
    header: "指纹",
    headerTitle: "值的 sha256 前缀，用来核对两处填的是不是同一个；不是值本身",
    value: (row) => row.metadata?.fingerprint ?? "",
    cell: (row) => (
      <span className="font-mono text-xs text-fg-muted">
        {row.metadata ? fingerprintPrefix(row.metadata.fingerprint) : "—"}
      </span>
    ),
  },
  {
    id: "version",
    header: "版本",
    numeric: true,
    value: (row) => row.metadata?.version ?? null,
    cell: (row) => (
      <span className="tabular-nums text-xs text-fg-muted">
        {row.metadata && row.metadata.version > 0 ? String(row.metadata.version) : "—"}
      </span>
    ),
  },
  {
    id: "updated_at",
    header: "最近更新",
    // 刻意**不叫「最近轮换」**：这一列在保存与轮换时都会动，把它写成「轮换」
    // 会让一次首填看起来像一次轮换。轮换周期与「最后使用」上游还没有，
    // 不猜（见本片 handoff 的 follow_ups）。
    headerTitle: "保存或轮换都会更新这个时间；不是「最后使用」时间",
    value: (row) => row.metadata?.updated_at ?? "",
    cell: (row) => (
      <span className="tabular-nums text-xs text-fg-muted">
        {row.metadata ? formatUtcTimestamp(row.metadata.updated_at) : "—"}
      </span>
    ),
  },
];

const PLATFORM_LABELS: Readonly<Record<string, string>> = {
  sub2api: "Sub2API",
  newapi: "NewAPI",
};

function platformLabel(platform: string): string {
  return PLATFORM_LABELS[platform] ?? platform;
}

const COLUMNS: DataTableColumn<ServiceItem>[] = [
  {
    id: "instance",
    header: "实例",
    primary: true,
    value: (service) => `${service.instance_id} ${service.id}`,
    cell: (service) => (
      <div className="min-w-36">
        <strong className="font-medium text-fg">{service.instance_id}</strong>
        <p className="font-mono text-xs text-fg-muted">{service.id}</p>
      </div>
    ),
  },
  {
    id: "environment",
    header: "环境",
    value: (service) => service.environment,
    cell: (service) => <span className="text-xs text-fg">{service.environment || "—"}</span>,
  },
  {
    id: "status",
    header: "服务状态",
    value: (service) => describeServiceStatus(service.status).label,
    cell: (service) => <ServiceStatusBadge status={service.status} />,
  },
  {
    id: "endpoint",
    header: "接入地址",
    value: (service) => service.endpoint,
    cell: (service) => (
      <span className="block max-w-72 font-mono text-xs break-all text-fg-muted">
        {service.endpoint || "—"}
      </span>
    ),
  },
  {
    id: "owner",
    header: "负责人",
    value: (service) => service.owner,
    cell: (service) => <span className="text-sm text-fg">{service.owner || "—"}</span>,
  },
  {
    id: "freshness",
    header: "最近观测",
    value: (service) => service.observed_at,
    cell: (service) => {
      const freshness = serviceFreshness(service);
      return (
        <div className="flex min-w-44 flex-col items-start gap-1">
          <FreshnessBadge freshness={freshness} />
          <FreshnessNote freshness={freshness} />
        </div>
      );
    },
  },
];

/** 平台内「连接与凭据」。
 *
 * 当前平台只有 Service Query，所以本页刻意把三类事实分开：
 * - 已登记实例、地址、负责人、状态与最近观测：真实可读；
 * - CredentialRef / 连接登记簿：尚无 Query，不能从别的业务字段猜；
 * - 独立健康探测历史：尚无 Query，Service 的 observed_at 只代表最近一次观测。
 *
 * 这样既把目标 UI 画完整，也不会拿一行样例轮换日期冒充已接入能力。 */
export function PlatformCredentialsPanel({ platform }: { platform: string }) {
  const label = platformLabel(platform);
  const query = useQuery({
    queryKey: ["services"],
    queryFn: ({ signal }) => listServices({ signal }),
  });

  // React 指南：这是 Query 结果的纯派生，不用 Effect 复制一份容易失真的 state。
  const services = (query.data ?? []).filter((service) => service.service_type === platform);
  const freshCount = services.filter((service) => serviceFreshness(service).state === "fresh").length;
  const environments = [...new Set(services.map((service) => service.environment).filter(Boolean))];

  // 凭据引用（XM-CREDS-TAB-REFS）。两条既有 Query：清单说「平台声明需要什么」，
  // 元数据表给指纹与版本。清单不在前端硬编码——硬编码的清单会在后端多一条或
  // 改名时继续显示「全部已配置」，而那正是上线前最不能说谎的一屏。
  const expectedQuery = useQuery({
    queryKey: CREDENTIAL_QUERY_KEYS.expected,
    queryFn: ({ signal }) => listExpectedCredentials({ signal }),
  });
  // 元数据要 credential.manage 权限，清单不要。分成两条 Query 而不是一条：
  // 只读操作员看得到「这条配了没有」，只是看不到指纹与版本，而不是整块变成
  // 「无权访问」。
  const metadataQuery = useQuery({
    queryKey: CREDENTIAL_QUERY_KEYS.metadata(undefined),
    queryFn: ({ signal }) => listCredentials({ signal }),
    retry: false,
  });
  const metadataByRef = new Map<string, CredentialMetadata>(
    (metadataQuery.data ?? []).map((item) => [item.credential_ref, item]),
  );
  const credentialRefs: CredentialRefRow[] = (expectedQuery.data ?? [])
    .filter((item: ExpectedCredential) => item.platform === platform)
    .map((item) => ({
      credentialRef: item.credential_ref,
      purpose: item.purpose,
      configured: item.configured,
      metadata: metadataByRef.get(item.credential_ref),
    }));
  const configuredCount = credentialRefs.filter((row) => row.configured).length;
  const credentialsKnown = expectedQuery.isSuccess;

  return (
    <section className="flex flex-col gap-4">
      <div className="rounded-lg border border-edge bg-surface px-4 py-3 shadow-sm">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-fg">连接事实与凭据边界</h2>
            <p className="mt-1 max-w-4xl text-xs leading-5 text-fg-muted">
              本页只显示平台服务注册表已经提供的事实。凭据正文永不展示；连接登记簿接入后也只显示 CredentialRef 与轮换状态。
            </p>
          </div>
          <Badge tone="info">只读</Badge>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          label="已登记实例"
          value={query.isPending ? "—" : String(services.length)}
          unavailable={query.isPending}
          note={`service_type 精确匹配 ${platform}`}
        />
        <StatTile
          label="观测新鲜"
          value={query.isPending ? "—" : `${freshCount} / ${services.length}`}
          unavailable={query.isPending || services.length === 0}
          note="按服务注册表最近一次观测与当前统一阈值计算"
        />
        <StatTile
          label="CredentialRef 状态"
          value={credentialsKnown ? `${configuredCount} / ${credentialRefs.length}` : "—"}
          unavailable={!credentialsKnown}
          status={
            credentialsKnown && configuredCount < credentialRefs.length ? (
              <Badge tone="warning">有未配置</Badge>
            ) : undefined
          }
          note={
            credentialsKnown
              ? "平台声明本平台需要的引用中，已保存且未撤销的条数"
              : "凭据清单尚未读到；不会从财务字段或地址猜 CredentialRef"
          }
        />
        <StatTile
          label="探测数据状态"
          value="—"
          unavailable
          status={<Badge tone="neutral">未接入</Badge>}
          note="独立探测历史尚未接入；当前只能显示服务最近一次观测"
        />
      </div>

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <DataTableV2
          caption={`${label} 已登记连接实例、状态、地址、负责人及最近观测`}
          columns={COLUMNS}
          rows={services}
          rowKey={(service) => service.id}
          searchable
          filters={
            environments.length > 1
              ? [{ columnId: "environment", label: "环境", options: environments }]
              : []
          }
          emptyState={
            <PageState
              kind="empty"
              title={`${label} 还没有登记实例`}
              description={`前往「平台治理 → 服务注册表」登记 ${label} 实例后，这里会显示接入地址、负责人、服务状态与最近一次观测。`}
            />
          }
        />
      </ApiStateView>

      {/* 凭据引用（XM-CREDS-TAB-REFS）。原型这一格要的是「引用名 / 状态 /
          轮换周期 / 最近轮换 / 最后使用」——后三项里只有「最近更新」有真实
          数据，另两项上游还没有，宁可不列也不猜（宪法 12 条）。 */}
      <section className="flex flex-col gap-2">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <h3 className="text-sm font-semibold text-fg">凭据引用</h3>
          <p className="text-xs text-fg-muted">密钥不落库，只存引用与指纹；正文永不展示，也不经过前端。</p>
        </div>
        <ApiStateView
          isPending={expectedQuery.isPending}
          error={expectedQuery.error}
          onRetry={() => void expectedQuery.refetch()}
          compact
        >
          <DataTableV2
            caption={`${label} 需要的 CredentialRef、是否已配置及其指纹与版本`}
            columns={CREDENTIAL_REF_COLUMNS}
            rows={credentialRefs}
            rowKey={(row) => row.credentialRef}
            emptyState={
              <PageState
                kind="empty"
                title={`${label} 没有声明需要的凭据`}
                description="平台的凭据清单里没有属于这个平台的引用。清单由后端给出，前端不硬编码。"
                compact
              />
            }
            footerExtra={
              metadataQuery.error ? (
                <p className="text-xs text-fg-muted">
                  指纹与版本需要 credential.manage 权限，当前账号看不到；「是否已配置」不受影响。
                </p>
              ) : null
            }
          />
        </ApiStateView>
      </section>


      <div className="grid grid-cols-1 gap-4">

        <article className="rounded-lg border border-edge bg-surface p-4 shadow-sm">
          <div className="flex items-start justify-between gap-3">
            <div>
              <h3 className="text-sm font-semibold text-fg">健康探测历史</h3>
              <p className="mt-1 text-xs leading-5 text-fg-muted">
                目标字段包括探测对象、检查项、结果、延迟、错误码与执行时间，用于区分服务观测和连接可用性。
              </p>
            </div>
            <Badge tone="neutral">未接入</Badge>
          </div>
          <p className="mt-4 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
            独立探测历史 Query 尚未提供；上表只能显示服务最近一次观测，不能证明凭据或上游模型当前可用。
          </p>
        </article>
      </div>
    </section>
  );
}
