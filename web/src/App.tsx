import {
  BadgeCheck,
  AtSign,
  Bell,
  Building2,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  CircleDollarSign,
  Clock3,
  Download,
  FileCheck2,
  FileText,
  EyeOff,
  Inbox,
  Landmark,
  KeyRound,
  LayoutDashboard,
  Loader2,
  LogOut,
  Mail,
  Menu,
  Network,
  NotebookText,
  PanelLeftClose,
  Plus,
  ReceiptText,
  RefreshCcw,
  Search,
  Save,
  Server,
  Send,
  Settings2,
  ShieldCheck,
  Sparkles,
  UploadCloud,
  UserRound,
  UsersRound,
  WalletCards,
  X,
} from "lucide-react";
import {
  createContext,
  type FormEvent,
  type ReactNode,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  Link,
  Navigate,
  NavLink,
  Route,
  Routes,
  useLocation,
  useNavigate,
} from "react-router-dom";

import { dateTime, dateTimeShanghai, maskTaxId, money } from "./lib/format";
import { isAdminAreaPath, shouldShowAdminReturn } from "./lib/portal-navigation";
import { sourceName } from "./lib/source-labels";
import {
  eligibilityReasonLabel,
  eligibilityStatusLabel,
} from "./lib/eligibility-labels";
import { applyUserDataResults } from "./lib/user-data-load";
import {
  accountIdentityLabel,
  appendEmbeddedParams,
  parseEmbeddedPlatform,
  resolveEmbeddedPlatform,
  scopeBySource,
} from "./lib/embedded-scope";
import {
  appendEmbeddedAdminParams,
  buildXmEmbedHeightMessage,
  measureEmbeddedAdminHeight,
  shouldPostEmbeddedAdminHeight,
  isAdminNavItemVisible,
  parseEmbeddedAdminMode,
  parseEmbeddedAdminScope,
  resolvePlatformSourceInstanceId,
  shouldSyncEmbeddedAdminHeight,
  XM_EMBED_CONSOLE_ORIGIN,
  type AdminNavItemKey,
} from "./lib/embedded-admin-scope";
import { apiCapabilities, apiMode, invoiceApi } from "./lib/api";
import { InvoiceApiError } from "./lib/api-contract";
import {
  canUserCancelInvoice,
  currentLocalDateTimeValue,
  eligibilityStartLabel,
  invoicePDFSizeAllowed,
  isMechanicalReconciliationFreeze,
  normalizeIssuedAt,
  replaceDirectRequestAfterMutation,
  userCancellationLabel,
} from "./lib/workflow";
import { AuthProvider, useAuth } from "./AuthProvider";
import type {
  AccountBlockState,
  AccountLedgerDetail,
  AccountLedgerFilters,
  AccountLedgerListItem,
  InvoiceSystemSettings,
  DashboardSummary,
  EligibilityFreeze,
  EligibilityFreezeFilters,
  EligibilityFreezeReason,
  EligibilityProjectionHealth,
  FundingOrder,
  InvoiceProfile,
  InvoiceNoticeDelivery,
  InvoiceProfileType,
  InvoicePolicy,
  InvoiceRequest,
  InvoiceStatus,
  PaymentCandidate,
  RefundCase,
  RefundCaseStatus,
  SourceAccount,
  SourceHealthReport,
  SourceStreamHealth,
  SourceType,
  UserEligibilitySummary,
} from "./types";

const initialApplicationURL = new URL(window.location.href);
const embeddedUserMode =
  !initialApplicationURL.pathname.startsWith("/admin") &&
  initialApplicationURL.searchParams.get("ui_mode") === "embedded";
// XM-INV-EMBED-SCOPE: narrows the embedded view to one platform's data; see
// lib/embedded-scope.ts. Parsed once from the initial URL, same as
// embeddedUserMode above. XM-INV-PLATFORM-SCOPE (CR-0003) made the session's
// own platform authoritative over this URL param wherever data is actually
// scoped (useEmbeddedPlatform below) -- this raw URL-derived value survives
// only as that fallback and to build in-app navigation links (userRoute),
// which intentionally keep reflecting exactly what was in the original URL.
const urlEmbeddedPlatform = parseEmbeddedPlatform(
  initialApplicationURL.searchParams,
  embeddedUserMode,
);

function userRoute(path: string) {
  return appendEmbeddedParams(path, embeddedUserMode, urlEmbeddedPlatform);
}

// XM-INV-PLATFORM-SCOPE: the effective embedded platform scope, session-first
// (see resolveEmbeddedPlatform's own doc comment). Call this from any
// component that scopes data or UI to one platform; do not read
// urlEmbeddedPlatform directly for that purpose.
function useEmbeddedPlatform(): SourceType | null {
  const { user } = useAuth();
  return resolveEmbeddedPlatform(user?.platform, urlEmbeddedPlatform);
}

// XM-INV-ADMIN-EMBED: the xingmang platform console embeds the *admin*
// console (a separate embed from the user embed above) via
// `?ui_mode=embedded_admin`; see lib/embedded-admin-scope.ts for why this is
// a parallel module/constant pair rather than an extension of
// embeddedUserMode/embeddedPlatform above.
const embeddedAdminMode = parseEmbeddedAdminMode(
  initialApplicationURL.searchParams,
);
const embeddedAdminScope = parseEmbeddedAdminScope(
  initialApplicationURL.searchParams,
  embeddedAdminMode,
);

function adminRoute(path: string) {
  return appendEmbeddedAdminParams(path, embeddedAdminMode, embeddedAdminScope);
}

// The one platform a platform-scoped embedded admin session is allowed to
// see, or null for global/standalone (nothing to resolve) and while a
// platform scope's source_instance_id has not resolved yet.
function embeddedAdminPlatform(): SourceType | null {
  return embeddedAdminScope?.kind === "platform" ? embeddedAdminScope.platform : null;
}

// Resolves the current embedded-admin platform's source_instance_id from the
// admin source-health report -- see resolvePlatformSourceInstanceId's doc
// comment for why that endpoint and not a hardcoded UUID. null both before
// it has loaded and when this isn't platform-scoped; callers in platform
// scope must wait for a non-null value before issuing scoped list requests
// rather than briefly requesting an unscoped (all-platform) page.
function useEmbeddedAdminPlatformSourceInstanceId(): string | null {
  const platform = embeddedAdminPlatform();
  const [sourceInstanceId, setSourceInstanceId] = useState<string | null>(null);
  useEffect(() => {
    if (!platform) {
      setSourceInstanceId(null);
      return;
    }
    let active = true;
    void invoiceApi
      .getSourceHealth()
      .then((report) => {
        if (active) setSourceInstanceId(resolvePlatformSourceInstanceId(platform, report.items));
      })
      .catch(() => {
        if (active) setSourceInstanceId(null);
      });
    return () => {
      active = false;
    };
  }, [platform]);
  return sourceInstanceId;
}

// Posts this document's height to the hosting console (XM-INVCON0's
// EmbeddedConsoleFrame, merged on the platform side) so it can size the
// iframe: once right after mount, then again on every subsequent size
// change, debounced ~100ms so a burst of layout changes (a page swap, data
// loading in) collapses into one message instead of a flood. Gated by
// shouldSyncEmbeddedAdminHeight so it only ever runs for this admin embed
// while actually framed -- never standalone /admin, never the user embed,
// and never posted at all (not even a single call) when neither holds.
// Mounted once at the App root so it covers the whole embedded-admin
// session regardless of which admin sub-page is showing, not re-created on
// every route change.
function useEmbeddedAdminHeightSync() {
  useEffect(() => {
    if (!shouldSyncEmbeddedAdminHeight(embeddedAdminMode, window.parent !== window)) return;
    // XM-INV-EMBED-HEIGHT: only post when the value actually moved, and
    // XM-INV-EMBED-LOOP: only when it moved by more than a pixel or two. The
    // observers below fire for reasons that do not move the height, and a
    // difference smaller than the dead band is never worth a console
    // re-render -- which is also what stops a measurement that tracks the
    // frame from walking the two of us upward forever.
    let lastPosted = 0;
    const sendHeight = () => {
      const height = measureEmbeddedAdminHeight(document);
      if (!shouldPostEmbeddedAdminHeight(height, lastPosted)) return;
      lastPosted = height;
      window.parent.postMessage(
        buildXmEmbedHeightMessage(height),
        XM_EMBED_CONSOLE_ORIGIN,
      );
    };
    sendHeight();
    let debounceTimer: number | undefined;
    const schedule = () => {
      if (debounceTimer !== undefined) window.clearTimeout(debounceTimer);
      debounceTimer = window.setTimeout(sendHeight, 100);
    };
    const observer = new ResizeObserver(schedule);
    // Both boxes: the root element can stop growing once the console has
    // sized the frame, so observing it alone can go quiet exactly when a
    // table starts filling in.
    observer.observe(document.documentElement);
    if (document.body) observer.observe(document.body);
    // A slow safety net for growth neither observer reports -- an image
    // decoding late, a font swapping, a panel expanding inside an already
    // sized box. It posts only on change, so a stable page costs one
    // measurement per second and no messages at all.
    const poll = window.setInterval(sendHeight, 1000);
    return () => {
      observer.disconnect();
      window.clearInterval(poll);
      if (debounceTimer !== undefined) window.clearTimeout(debounceTimer);
    };
  }, []);
}

type AppData = {
  loading: boolean;
  orders: FundingOrder[];
  profiles: InvoiceProfile[];
  sourceAccounts: SourceAccount[];
  eligibilitySummaries: UserEligibilitySummary[];
  requests: InvoiceRequest[];
  summary: DashboardSummary | null;
  loadError: string | null;
  refresh: () => Promise<void>;
  requestsNextCursor?: string;
  loadingMoreRequests: boolean;
  loadMoreRequests: () => Promise<void>;
};

const DataContext = createContext<AppData | null>(null);

function useData() {
  const value = useContext(DataContext);
  if (!value) throw new Error("DataContext is missing");
  return value;
}

type ToastState = { message: string; tone: "success" | "error" } | null;
const ToastContext = createContext<
  (message: string, tone?: "success" | "error") => void
>(() => undefined);

function summarizeRequests(
  requestItems: InvoiceRequest[],
  totalAvailableMinor: number,
): DashboardSummary {
  return {
    totalAvailableMinor,
    reviewingMinor: requestItems
      .filter(
        (request) =>
          request.status === "submitted" || request.status === "reviewing",
      )
      .reduce((total, request) => total + request.amountMinor, 0),
    issuedThisYearMinor: requestItems
      .filter((request) => request.status === "issued")
      .reduce((total, request) => total + request.amountMinor, 0),
    pendingCount: requestItems.filter(
      (request) =>
        request.status === "submitted" || request.status === "reviewing",
    ).length,
  };
}

function DataProvider({ children }: { children: ReactNode }) {
  const { user } = useAuth();
  const location = useLocation();
  const adminRoute = location.pathname.startsWith("/admin");
  const [loading, setLoading] = useState(true);
  const [orders, setOrders] = useState<FundingOrder[]>([]);
  const [profiles, setProfiles] = useState<InvoiceProfile[]>([]);
  const [sourceAccounts, setSourceAccounts] = useState<SourceAccount[]>([]);
  const [eligibilitySummaries, setEligibilitySummaries] = useState<
    UserEligibilitySummary[]
  >([]);
  const [requests, setRequests] = useState<InvoiceRequest[]>([]);
  const [summary, setSummary] = useState<DashboardSummary | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [requestsNextCursor, setRequestsNextCursor] = useState<
    string | undefined
  >();
  const [loadingMoreRequests, setLoadingMoreRequests] = useState(false);
  const refreshVersion = useRef(0);
  const platformSourceInstanceId = useEmbeddedAdminPlatformSourceInstanceId();

  const refresh = async () => {
    const version = ++refreshVersion.current;
    setLoading(true);
    try {
      if (adminRoute) {
        if (user?.role !== "admin") {
          setOrders([]);
          setProfiles([]);
          setSourceAccounts([]);
          setEligibilitySummaries([]);
          setRequests([]);
          setSummary(null);
          setRequestsNextCursor(undefined);
          setLoadError(null);
          return;
        }
        if (location.pathname !== "/admin") {
          setOrders([]);
          setProfiles([]);
          setSourceAccounts([]);
          setEligibilitySummaries([]);
          setRequests([]);
          setSummary(null);
          setRequestsNextCursor(undefined);
          setLoadError(null);
          return;
        }
        // Platform-scoped embedded admin: wait for the platform's
        // source_instance_id to resolve rather than briefly requesting an
        // unscoped (all-platform) page and re-requesting a moment later.
        if (embeddedAdminPlatform() && !platformSourceInstanceId) return;
        const requestPage = await invoiceApi.getAdminRequestPage(
          undefined,
          platformSourceInstanceId ?? undefined,
        );
        const nextRequests = requestPage.items;
        if (version !== refreshVersion.current) return;
        setOrders([]);
        setProfiles([]);
        setSourceAccounts([]);
        setEligibilitySummaries([]);
        setRequests(nextRequests);
        setRequestsNextCursor(requestPage.nextCursor);
        setSummary(summarizeRequests(nextRequests, 0));
      } else {
        // Five independent reads, settled independently. Promise.all here was
        // the amplifier that turned one unrecognised enum value into a
        // whole-page outage: getOrders() rejecting meant setSourceAccounts()
        // below never ran, sourceAccounts stayed at its initial [], and the
        // "已关联的平台账号" panel re-rendered as the "关联平台账号" onboarding
        // wizard. Users with verified bindings were told to go and bind again
        // -- worse than showing them nothing, because it invites them to
        // "fix" an account that was never broken.
        //
        // Each request now keeps its own last-good value and reports its own
        // failure. A failing request must never blank a panel it does not own.
        const [orders, profiles, sourceAccounts, summaries, requests] =
          await Promise.allSettled([
            invoiceApi.getOrders(),
            invoiceApi.getProfiles(),
            invoiceApi.getSourceAccounts(),
            invoiceApi.getUserEligibilitySummary(),
            invoiceApi.getUserRequestPage(),
          ]);
        if (version !== refreshVersion.current) return;
        // Which panels survive a partial failure is decided (and tested) in
        // lib/user-data-load.ts, not here.
        applyUserDataResults(
          {
            orders,
            profiles,
            sourceAccounts,
            eligibilitySummaries: summaries,
            requests,
          },
          {
            setOrders,
            setProfiles,
            setSourceAccounts,
            setEligibilitySummaries,
            setRequests: (page) => {
              setRequests(page.items);
              setRequestsNextCursor(page.nextCursor);
            },
            setSummary: (page, availableMinor) =>
              setSummary(summarizeRequests(page.items, availableMinor)),
            setLoadError,
          },
        );
        return;
      }
      setLoadError(null);
    } catch (error) {
      if (version !== refreshVersion.current) return;
      setEligibilitySummaries([]);
      setLoadError(
        error instanceof Error
          ? error.message
          : "读取开票数据失败，请稍后重试。",
      );
    } finally {
      if (version === refreshVersion.current) setLoading(false);
    }
  };

  const loadMoreRequests = async () => {
    if (!requestsNextCursor || loadingMoreRequests) return;
    const version = refreshVersion.current;
    const cursor = requestsNextCursor;
    setLoadingMoreRequests(true);
    try {
      const page = adminRoute
        ? await invoiceApi.getAdminRequestPage(cursor, platformSourceInstanceId ?? undefined)
        : await invoiceApi.getUserRequestPage(cursor);
      if (version !== refreshVersion.current) return;
      const merged = [
        ...requests,
        ...page.items.filter(
          (item) => !requests.some((existing) => existing.id === item.id),
        ),
      ];
      setRequests(merged);
      setRequestsNextCursor(page.nextCursor);
      setSummary(
        summarizeRequests(
          merged,
          adminRoute
            ? 0
            : eligibilitySummaries.reduce(
                (total, item) => total + item.availableMinor,
                0,
              ),
        ),
      );
      setLoadError(null);
    } catch (error) {
      if (version !== refreshVersion.current) return;
      setLoadError(
        error instanceof Error ? error.message : "加载更多开票记录失败。",
      );
    } finally {
      if (version === refreshVersion.current) setLoadingMoreRequests(false);
    }
  };

  useEffect(() => {
    void refresh();
  }, [adminRoute, location.pathname, user?.id, user?.role, platformSourceInstanceId]);

  return (
    <DataContext.Provider
      value={{
        loading,
        orders,
        profiles,
        sourceAccounts,
        eligibilitySummaries,
        requests,
        summary,
        loadError,
        refresh,
        requestsNextCursor,
        loadingMoreRequests,
        loadMoreRequests,
      }}
    >
      {children}
    </DataContext.Provider>
  );
}

const userNav = [
  { to: "/orders", label: "申请开票", icon: ReceiptText },
  { to: "/profiles", label: "开票资料", icon: Building2 },
  { to: "/records", label: "开票记录", icon: FileText },
];

const adminNav = [
  { to: "/admin", label: "审核工作台", icon: LayoutDashboard, scopeKey: "review" },
  { to: "/admin/payment-candidates", label: "支付核验", icon: ShieldCheck, scopeKey: "payment-candidates" },
  { to: "/admin/eligibility-freezes", label: "资格冻结", icon: EyeOff, scopeKey: "eligibility-freezes" },
  { to: "/admin/accounts/ledger", label: "用户账本", icon: NotebookText, scopeKey: "account-ledger" },
  { to: "/admin/refund-cases", label: "退款与红冲", icon: CircleAlert, scopeKey: "refund-cases" },
  { to: "/admin/source-health", label: "同步状态", icon: Network, scopeKey: "source-health" },
  { to: "/admin?view=issued", label: "发票档案", icon: FileCheck2, scopeKey: "review" },
  { to: "/admin/settings", label: "系统设置", icon: Settings2, scopeKey: "settings" },
  { to: "/orders", label: "返回用户端", icon: UserRound, scopeKey: "return-to-user" },
] as const satisfies ReadonlyArray<{
  to: string;
  label: string;
  icon: typeof LayoutDashboard;
  scopeKey: AdminNavItemKey;
}>;

function PortalLayout({
  children,
  admin = false,
}: {
  children: ReactNode;
  admin?: boolean;
}) {
  const [mobileOpen, setMobileOpen] = useState(false);
  const { loadError, refresh, sourceAccounts } = useData();
  const { user, logout, stepUpRequired } = useAuth();
  const embeddedPlatform = useEmbeddedPlatform();
  const toast = useContext(ToastContext);
  const location = useLocation();
  const nav = admin
    ? adminNav.filter((item) => isAdminNavItemVisible(item.scopeKey, embeddedAdminScope))
    : userNav;
  const embedded = embeddedUserMode && !admin;
  // Separate from `embedded` above (which is the user embed, and explicitly
  // excludes admin): the admin console has its own embed, hosted by the
  // platform console rather than a per-account iframe, so it gets its own
  // chrome-hiding class (.portal-embedded-admin, styles.css) instead of
  // reusing .portal-embedded's.
  const embeddedAdmin = admin && embeddedAdminMode;
  // The regular sidebar/topbar chrome that would otherwise show who is
  // logged in is hidden in embedded mode (see .portal-embedded in
  // styles.css) -- without this, the embedded view has no account identity
  // at all.
  const accountLabel = user
    ? accountIdentityLabel({ user, embeddedPlatform, sourceAccounts })
    : null;

  useEffect(() => setMobileOpen(false), [location.pathname, location.search]);

  return (
    <div
      className={`portal ${admin ? "portal-admin" : ""} ${embedded ? "portal-embedded" : ""} ${embeddedAdmin ? "portal-embedded-admin" : ""}`}
    >
      <aside className={`sidebar ${mobileOpen ? "sidebar-open" : ""}`}>
        <div className="brand">
          <div className="brand-mark">
            <ReceiptText size={20} />
          </div>
          <div>
            <strong>SoloV</strong>
            <span>{admin ? "发票管理中心" : "开票中心"}</span>
          </div>
          <button
            className="icon-button sidebar-close"
            onClick={() => setMobileOpen(false)}
            aria-label="关闭菜单"
          >
            <PanelLeftClose size={19} />
          </button>
        </div>

        <div className="workspace-chip">
          <div className="avatar avatar-small">
            {(user?.displayName || user?.email || "用").slice(0, 1)}
          </div>
          <div>
            <strong>{user?.displayName || "已登录用户"}</strong>
            <span>{admin ? "管理员工作区" : "开票账号"}</span>
          </div>
          <ChevronDown size={16} />
        </div>

        <nav className="side-nav">
          <p>{admin ? "管理" : "开票服务"}</p>
          {nav.map((item) => {
            const Icon = item.icon;
            const hasMatchingView =
              new URLSearchParams(location.search).get("view") === "issued";
            return (
              <NavLink
                key={item.to}
                to={admin ? adminRoute(item.to) : userRoute(item.to)}
                end={item.to === "/admin"}
                className={({ isActive }) => {
                  const active = admin
                    ? item.to.includes("view=issued")
                      ? hasMatchingView
                      : item.to === "/admin"
                        ? isActive && !hasMatchingView
                        : isActive
                    : isActive;
                  return active ? "nav-item nav-item-active" : "nav-item";
                }}
              >
                <Icon size={18} />
                <span>{item.label}</span>
              </NavLink>
            );
          })}
          {shouldShowAdminReturn(admin, user?.role, stepUpRequired) && (
            <NavLink
              to="/admin"
              target={embedded ? "_top" : undefined}
              className={({ isActive }) =>
                isActive ? "nav-item nav-item-active" : "nav-item"
              }
            >
              <ShieldCheck size={18} />
              <span>返回管理端</span>
            </NavLink>
          )}
        </nav>

        <div className="sidebar-spacer" />
        <div className="sidebar-security">
          <ShieldCheck size={18} />
          <div>
            <strong>数据安全保护中</strong>
            <span>订单已独立核验</span>
          </div>
        </div>
        <button
          className="nav-item nav-item-muted"
          onClick={() =>
            void logout().catch((error) =>
              toast(
                error instanceof Error ? error.message : "退出登录失败。",
                "error",
              ),
            )
          }
        >
          <LogOut size={18} />
          <span>退出登录</span>
        </button>
        {embedded && accountLabel && (
          // Rightmost on purpose: the embedding platform pages float their
          // own "新窗口打开" overlay over the iframe's top-right corner, and
          // it was covering the logout button. The badge is display-only, so
          // it is the element allowed to sit under that overlay.
          <div className="embedded-account-badge" title="当前登录账号">
            <UserRound size={14} />
            <span>{accountLabel}</span>
          </div>
        )}
      </aside>
      {mobileOpen && (
        <button
          className="sidebar-backdrop"
          onClick={() => setMobileOpen(false)}
          aria-label="关闭菜单"
        />
      )}

      <div className="portal-main">
        <header className="topbar">
          <button
            className="icon-button mobile-menu"
            onClick={() => setMobileOpen(true)}
            aria-label="打开菜单"
          >
            <Menu size={21} />
          </button>
          <div className="environment">
            <span className="status-dot" /> 安全会话已建立
            <span className="runtime-mode">
              {apiMode === "http" ? "HTTP API" : "本地演示"}
            </span>
          </div>
          <div className="topbar-actions">
            <button className="icon-button" aria-label="通知">
              <Bell size={19} />
              <span className="notification-dot" />
            </button>
            <div className="top-user">
              <div className="avatar">
                {(user?.displayName || user?.email || "用").slice(0, 1)}
              </div>
              <div>
                <strong>{user?.displayName || "已登录用户"}</strong>
                <span>{user?.email || "—"}</span>
              </div>
            </div>
          </div>
        </header>
        <main className="content">
          {loadError && (
            <div className="api-error-banner" role="alert">
              <CircleAlert size={18} />
              <div>
                <strong>开票数据暂时无法读取</strong>
                <span>{loadError}</span>
              </div>
              <button
                className="button button-secondary"
                onClick={() => void refresh()}
              >
                <RefreshCcw size={15} />
                重试
              </button>
            </div>
          )}
          {children}
        </main>
      </div>
    </div>
  );
}

function PageHeader({
  eyebrow,
  title,
  description,
  action,
}: {
  eyebrow?: string;
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="page-header">
      <div>
        {eyebrow && <span className="eyebrow">{eyebrow}</span>}
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      {action}
    </div>
  );
}

function SummaryCards() {
  const { summary, sourceAccounts: allSourceAccounts } = useData();
  const embeddedPlatform = useEmbeddedPlatform();
  // Embedded views collapse the big "已关联的平台账号" panel into this
  // compact card once every (scoped) account is connected -- the panel's
  // remaining job there is binding guidance, which only matters while an
  // account is NOT connected yet (see SourceAccountStatus).
  const connectedAccounts = embeddedUserMode
    ? scopeBySource(allSourceAccounts, embeddedPlatform).filter(
        (account) => account.status === "verified",
      )
    : [];
  const cards = [
    {
      label: "当前可开票",
      value: money(summary?.totalAvailableMinor ?? 0),
      hint: "已通过支付核验",
      icon: CircleDollarSign,
      tone: "blue",
    },
    {
      label: "审核中金额",
      value: money(summary?.reviewingMinor ?? 0),
      hint: `${summary?.pendingCount ?? 0} 笔已加载申请处理中`,
      icon: Clock3,
      tone: "amber",
    },
    {
      label: "已加载的已开票",
      value: money(summary?.issuedThisYearMinor ?? 0),
      hint: "以当前已加载记录统计",
      icon: FileCheck2,
      tone: "green",
    },
  ];
  if (connectedAccounts.length === 1) {
    const account = connectedAccounts[0];
    cards.push({
      label: "关联账号",
      value: account.sourceLabel,
      hint: account.lastObservedAt
        ? `已连接 · 最近同步 ${dateTime(account.lastObservedAt)}`
        : "已连接 · 等待首次同步",
      icon: Network,
      tone: "blue",
    });
  } else if (connectedAccounts.length > 1) {
    cards.push({
      label: "关联账号",
      value: `${connectedAccounts.length} 个平台账号`,
      hint: connectedAccounts.map((account) => account.sourceLabel).join("、"),
      icon: Network,
      tone: "blue",
    });
  }
  return (
    <div className="summary-grid">
      {cards.map(({ icon: Icon, ...card }) => (
        <article className="summary-card" key={card.label}>
          <div className={`summary-icon ${card.tone}`}>
            <Icon size={20} />
          </div>
          <div>
            <span>{card.label}</span>
            <strong>{card.value}</strong>
            <small>{card.hint}</small>
          </div>
        </article>
      ))}
    </div>
  );
}

const statusMeta: Record<InvoiceStatus, { label: string; className: string }> =
  {
    submitted: { label: "已提交", className: "badge-blue" },
    reviewing: { label: "审核中", className: "badge-amber" },
    returned: { label: "已退回", className: "badge-red" },
    issued: { label: "已开具", className: "badge-green" },
  };

function displayStatus(request: InvoiceRequest) {
  if (request.workflowStatus === "refund_attention")
    return { label: "退款关注", className: "badge-red" };
  if (request.workflowStatus === "user_cancelled")
    return { label: "用户已取消", className: "badge-neutral" };
  if (request.workflowStatus === "rejected")
    return { label: "已拒绝", className: "badge-red" };
  return statusMeta[request.status];
}

function Badge({
  children,
  tone = "neutral",
}: {
  children: ReactNode;
  tone?: string;
}) {
  return <span className={`badge badge-${tone}`}>{children}</span>;
}

function SourceBadge({ source }: { source: SourceType }) {
  return (
    <Badge tone={source === "newapi" ? "violet" : "cyan"}>
      {sourceName[source]}
    </Badge>
  );
}

function SourceAccountStatus() {
  const { sourceAccounts: allSourceAccounts, loading, refresh } = useData();
  const embeddedPlatform = useEmbeddedPlatform();
  const sourceAccounts = scopeBySource(allSourceAccounts, embeddedPlatform);
  if (loading) return null;
  if (
    embeddedUserMode &&
    sourceAccounts.length > 0 &&
    sourceAccounts.every((account) => account.status === "verified")
  ) {
    // The connected state lives in SummaryCards' compact "关联账号" card;
    // this panel stays only while binding guidance is still needed.
    return null;
  }
  const showSub2APILink = !embeddedPlatform || embeddedPlatform === "sub2api";
  const showNewAPILink = !embeddedPlatform || embeddedPlatform === "newapi";
  return (
    <section className="card source-account-card" aria-label="源平台账号连接">
      <div className="card-heading compact">
        <div>
          <h2>{sourceAccounts.length ? "已关联的平台账号" : "关联平台账号"}</h2>
          <p>仅展示由统一登录主体明确绑定的账号，不会按邮箱自动匹配。</p>
        </div>
        <Network size={20} />
      </div>
      <div className="source-account-list">
        {sourceAccounts.map((account) => (
          <div className="source-account-row" key={account.id}>
            <SourceBadge source={account.source} />
            <div>
              <strong>{account.sourceLabel}</strong>
              <span>账号标识：{account.externalUserIdMasked}</span>
            </div>
            <Badge
              tone={
                account.status === "verified"
                  ? "green"
                  : account.status === "frozen" || account.status === "revoked"
                    ? "red"
                    : "amber"
              }
            >
              {account.status === "verified"
                ? "已连接"
                : account.status === "frozen"
                  ? "已冻结"
                  : account.status === "revoked"
                    ? "已撤销"
                    : "待确认"}
            </Badge>
            <small>
              {account.lastObservedAt
                ? `最近同步 ${dateTime(account.lastObservedAt)}`
                : "等待首次同步"}
            </small>
          </div>
        ))}
        {!sourceAccounts.length && (
          <div className="source-binding-empty">
            <div className="binding-step">
              <span>1</span>
              <div>
                <strong>打开原平台并使用原账号登录</strong>
                <p>
                  {embeddedPlatform
                    ? `进入你在${sourceName[embeddedPlatform]}实际使用的站点。`
                    : "分别进入你实际使用的 Sub2API 或 New API 站点。"}
                </p>
                <div className="binding-site-links">
                  {showSub2APILink && (
                    <a
                      href="https://api.solov.cc/"
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      打开 Sub2API
                    </a>
                  )}
                  {showNewAPILink && (
                    <a
                      href="https://xm.solov.cc/"
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      打开 New API
                    </a>
                  )}
                </div>
              </div>
            </div>
            <div className="binding-step">
              <span>2</span>
              <div>
                <strong>绑定“SoloV 统一登录”</strong>
                <p>在原平台账号设置中确认绑定；不要使用另一个邮箱账号代替。</p>
              </div>
            </div>
            <div className="binding-step">
              <span>3</span>
              <div>
                <strong>返回这里刷新</strong>
                <p>同步完成后，属于该原账号的充值记录才会安全显示。</p>
                <button
                  className="button button-secondary"
                  onClick={() => void refresh()}
                >
                  <RefreshCcw size={15} />
                  我已绑定，刷新状态
                </button>
              </div>
            </div>
          </div>
        )}
      </div>
    </section>
  );
}

// The eligibility label tables and their fallback accessors now live in
// lib/eligibility-labels.ts (XM-INV-LOT-REASON-CONTRACT) so the Chinese copy
// can be asserted directly by tests rather than only through rendering.

function formatServiceUnits(value: UserEligibilitySummary["noncash"]) {
  const grouped = value.serviceUnits.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return value.unitCode
    ? `${grouped} ${value.unitCode}`
    : `${grouped}（单位合同待建立）`;
}

function eligibilitySummaryReady(summary?: UserEligibilitySummary) {
  return (
    summary?.bindingStatus === "verified" &&
    summary.status === "active" &&
    summary.reasons.length === 1 &&
    summary.reasons[0] === "READY"
  );
}

function EligibilitySummaryPanel({
  items,
  loading,
}: {
  items: UserEligibilitySummary[];
  loading: boolean;
}) {
  return (
    <section className="card eligibility-summary-card" aria-label="开票资格摘要">
      <div className="card-heading">
        <div>
          <h2>按平台计算的开票资格</h2>
          <p>人民币只来自真实支付并已消费的现金；旧余额和非现金额度始终按源服务单位展示。</p>
        </div>
        <ShieldCheck size={20} />
      </div>
      {loading ? (
        <LoadingBlock />
      ) : items.length ? (
        <div className="eligibility-source-grid">
          {items.map((item) => (
            <article
              className={`eligibility-source-card eligibility-${item.status}`}
              key={item.sourceInstanceId}
            >
              <div className="eligibility-source-head">
                <div>
                  <SourceBadge source={item.source} />
                  <strong>{item.sourceLabel}</strong>
                </div>
                <Badge tone={eligibilitySummaryReady(item) ? "green" : "amber"}>
                  {eligibilityStatusLabel(item.status)}
                </Badge>
              </div>
              <div className="eligibility-money-grid">
                <div className="eligibility-money-primary">
                  <span>当前可开</span>
                  <strong>{money(item.availableMinor)}</strong>
                </div>
                <div>
                  <span>已消费现金</span>
                  <strong>{money(item.consumedMinor)}</strong>
                </div>
                <div>
                  <span>未消费现金</span>
                  <strong>{money(item.unconsumedMinor)}</strong>
                </div>
                <div>
                  <span>申请占用</span>
                  <strong>{money(item.reservedMinor)}</strong>
                </div>
                <div>
                  <span>已开票</span>
                  <strong>{money(item.issuedMinor)}</strong>
                </div>
              </div>
              <div className="eligibility-unit-grid">
                <div>
                  <span>切点前旧余额 · 不可开票</span>
                  <strong>{formatServiceUnits(item.legacyNoninvoiceable)}</strong>
                </div>
                <div>
                  <span>赠送 / 返利 / 管理员额度 · 不可开票</span>
                  <strong>{formatServiceUnits(item.noncash)}</strong>
                </div>
              </div>
              <div className="eligibility-reasons" aria-label="资格状态原因">
                {item.reasons.map((reason) => (
                  <span key={reason}>{eligibilityReasonLabel(reason)}</span>
                ))}
              </div>
            </article>
          ))}
        </div>
      ) : (
        <div className="eligibility-summary-empty" role="status">
          <CircleAlert size={18} />
          <span>尚未取得可验证的平台资格摘要，所有充值记录均保持不可选择。</span>
        </div>
      )}
    </section>
  );
}

function OrdersPage() {
  const { loading, orders, profiles, eligibilitySummaries, refresh } = useData();
  const toast = useContext(ToastContext);
  const navigate = useNavigate();
  const embeddedPlatform = useEmbeddedPlatform();
  const [source, setSource] = useState<"all" | SourceType>(
    embeddedPlatform ?? "all",
  );
  const [selected, setSelected] = useState<Record<string, number>>({});
  const [profileId, setProfileId] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [policy, setPolicy] = useState<InvoicePolicy | null>(null);
  const [policyError, setPolicyError] = useState<string | null>(null);

  const loadPolicy = async () => {
    setPolicyError(null);
    try {
      const loaded = await invoiceApi.getInvoicePolicy();
      // XM-INV-SETTABLE-INVOICE-MINIMUM: the threshold is an administrator
      // setting (default ¥200), so this guard checks it is a sane positive
      // amount rather than pinning the old ¥200 floor -- which would have
      // made the public page reject a policy the settings page can legally
      // save.
      if (
        !Number.isFinite(loaded.minimumRequestMinor) ||
        loaded.minimumRequestMinor <= 0 ||
        loaded.serviceItem !== "技术服务"
      ) {
        throw new Error("公开开票策略无效，已停止提交。 ");
      }
      setPolicy(loaded);
    } catch (error) {
      setPolicy(null);
      setPolicyError(
        error instanceof Error ? error.message : "开票策略加载失败。",
      );
    }
  };

  useEffect(() => {
    void loadPolicy();
  }, []);

  useEffect(() => {
    if (!profileId && profiles.length)
      setProfileId(
        profiles.find((profile) => profile.isDefault)?.id ?? profiles[0].id,
      );
  }, [profileId, profiles]);

  const activeSource = useMemo(() => {
    const firstId = Object.keys(selected)[0];
    return orders.find((order) => order.id === firstId)?.source;
  }, [orders, selected]);
  const activeSourceInstance = useMemo(() => {
    const firstId = Object.keys(selected)[0];
    return orders.find((order) => order.id === firstId)?.sourceInstanceId;
  }, [orders, selected]);
  const eligibilityBySource = useMemo(
    () =>
      new Map(
        eligibilitySummaries.map((item) => [item.sourceInstanceId, item]),
      ),
    [eligibilitySummaries],
  );
  const orderSourceReady = (order: FundingOrder) =>
    eligibilitySummaryReady(eligibilityBySource.get(order.sourceInstanceId));

  useEffect(() => {
    setSelected((current) => {
      const next = Object.fromEntries(
        Object.entries(current).filter(([orderId]) => {
          const order = orders.find((item) => item.id === orderId);
          return (
            order !== undefined &&
            order.availableMinor > 0 &&
            order.verification === "verified" &&
            !order.refundFrozen &&
            order.eligibilityStatus === "active" &&
            orderSourceReady(order)
          );
        }),
      );
      return Object.keys(next).length === Object.keys(current).length
        ? current
        : next;
    });
  }, [orders, eligibilitySummaries]);
  // scopeBySource is a no-op when the embedded view is unscoped; when scoped
  // it is the authority (the source tabs below are hidden in that case, but
  // this keeps filteredOrders correct even if `source` state ever drifted).
  const filteredOrders = scopeBySource(orders, embeddedPlatform).filter(
    (order) => source === "all" || order.source === source,
  );
  const selectedTotal = Object.values(selected).reduce(
    (total, value) => total + value,
    0,
  );
  const selectedCount = Object.keys(selected).length;

  const toggleOrder = (order: FundingOrder) => {
    if (
      order.availableMinor <= 0 ||
      order.verification !== "verified" ||
      order.refundFrozen ||
      order.eligibilityStatus !== "active" ||
      order.eligibilityKind === "legacy" ||
      order.eligibilityKind === "noncash" ||
      !orderSourceReady(order)
    )
      return;
    if (activeSource && activeSource !== order.source && !selected[order.id]) {
      toast("同一张发票不能跨平台合并，请先清空已选订单。", "error");
      return;
    }
    if (
      activeSourceInstance &&
      activeSourceInstance !== order.sourceInstanceId &&
      !selected[order.id]
    ) {
      toast("同一张发票不能跨平台实例合并，请先清空已选订单。", "error");
      return;
    }
    setSelected((current) => {
      const next = { ...current };
      if (next[order.id]) delete next[order.id];
      else next[order.id] = order.availableMinor;
      return next;
    });
  };

  const updateAmount = (order: FundingOrder, yuan: string) => {
    const next = Math.max(
      0,
      Math.min(order.availableMinor, Math.round((Number(yuan) || 0) * 100)),
    );
    setSelected((current) => ({ ...current, [order.id]: next }));
  };

  const submit = async () => {
    if (!selectedCount || !profileId || !confirmed || !activeSource || !policy)
      return;
    if (selectedTotal < policy.minimumRequestMinor) {
      toast(
        `单次开票金额最低为 ${money(policy.minimumRequestMinor)}，可继续合并同一平台的订单。`,
        "error",
      );
      return;
    }
    const selectedOrders = orders.filter(
      (order) => selected[order.id] !== undefined,
    );
    if (
      selectedOrders.length !== selectedCount ||
      selectedOrders.some((order) => !orderSourceReady(order)) ||
      new Set(selectedOrders.map((order) => order.sourceInstanceId)).size !== 1
    ) {
      toast("平台资格状态已经变化，请刷新后重新选择开票金额。", "error");
      return;
    }
    setSubmitting(true);
    try {
      await invoiceApi.submitInvoice({
        source: activeSource,
        sourceInstanceId: orders.find(
          (order) => selected[order.id] !== undefined,
        )?.sourceInstanceId,
        profileId,
        allocations: Object.entries(selected).map(
          ([orderId, allocatedMinor]) => ({
            orderId,
            tradeNo:
              orders.find((order) => order.id === orderId)?.tradeNo ?? "",
            allocatedMinor,
          }),
        ),
      });
      await refresh();
      toast("开票申请已提交，金额已进入审核占用。");
      navigate(userRoute("/records"));
    } catch (error) {
      toast(
        error instanceof Error ? error.message : "提交失败，请稍后重试",
        "error",
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <PortalLayout>
      <PageHeader
        eyebrow="INVOICE CENTER"
        title="申请电子普票"
        description="钱包充值仅按已经实际消费的现金部分开票；订阅套餐按核验后的实际支付金额开票。"
      />
      <SummaryCards />
      <SourceAccountStatus />
      <EligibilitySummaryPanel
        items={scopeBySource(eligibilitySummaries, embeddedPlatform)}
        loading={loading}
      />
      {policyError && (
        <div className="policy-error" role="alert">
          <CircleAlert size={17} />
          <span>开票策略暂不可用：{policyError} 当前已禁止提交。</span>
          <button onClick={() => void loadPolicy()}>重新加载</button>
        </div>
      )}
      <div className="split-layout invoice-layout">
        <section className="card orders-card">
          <div className="card-heading">
            <div>
              <h2>选择可开票资金</h2>
              <p>赠送、返利、管理员加款与切点前旧余额均不会进入可开金额</p>
            </div>
            <button
              className="button button-ghost"
              onClick={() => void refresh()}
            >
              <RefreshCcw size={16} />
              刷新
            </button>
          </div>
          {!embeddedPlatform && (
            <div className="segmented">
              {(
                [
                  ["all", "全部平台"],
                  ["sub2api", "SoloV API"],
                  ["newapi", "模型平台"],
                ] as const
              ).map(([value, label]) => (
                <button
                  key={value}
                  className={source === value ? "active" : ""}
                  onClick={() => setSource(value)}
                >
                  {label}
                </button>
              ))}
            </div>
          )}
          {loading ? (
            <LoadingBlock />
          ) : (
            <div className="order-list">
              {filteredOrders.map((order) => {
                const isSelected = selected[order.id] !== undefined;
                const disabled =
                  order.availableMinor <= 0 ||
                  order.verification !== "verified" ||
                  order.refundFrozen ||
                  order.eligibilityStatus !== "active" ||
                  order.eligibilityKind === "legacy" ||
                  order.eligibilityKind === "noncash" ||
                  !orderSourceReady(order);
                return (
                  <article
                    key={order.id}
                    className={`order-row ${isSelected ? "order-selected" : ""} ${disabled ? "order-disabled" : ""}`}
                  >
                    <button
                      className={`check-box ${isSelected ? "checked" : ""}`}
                      onClick={() => toggleOrder(order)}
                      disabled={disabled}
                      aria-label="选择订单"
                    >
                      {isSelected && <Check size={15} />}
                    </button>
                    <div className="order-main">
                      <div className="order-title">
                        <SourceBadge source={order.source} />
                        <strong>{order.description}</strong>
                        {order.eligibilityKind === "subscription" && (
                          <Badge tone="blue">订阅</Badge>
                        )}
                        {order.verification === "pending" && (
                          <Badge tone="amber">支付核验中</Badge>
                        )}
                        {order.refundFrozen && (
                          <Badge tone="amber">账本冻结</Badge>
                        )}
                        {order.eligibilityStatus === "syncing" && (
                          <Badge tone="amber">账本追平中</Badge>
                        )}
                        {order.eligibilityStatus === "frozen" &&
                          !order.refundFrozen && (
                            <Badge tone="amber">对账冻结</Badge>
                          )}
                        {order.eligibilityStatus === "source_unavailable" && (
                          <Badge tone="red">来源同步不可用</Badge>
                        )}
                        {order.eligibilityStatus ===
                          "not_invoiceable_pending_reconciliation" && (
                          // amber, not red: this is a wait, not an alarm. It
                          // clears itself, and the admin ledger view already
                          // tones the same state this way.
                          <Badge tone="amber">对账中暂不可开票</Badge>
                        )}
                        {order.eligibilityDegraded && (
                          <Badge tone="amber">账本状态待确认</Badge>
                        )}
                      </div>
                      <div className="order-meta">
                        <span>{order.tradeNo}</span>
                        <span>{dateTime(order.paidAt)}</span>
                        <span>{order.paymentMethod}</span>
                      </div>
                    </div>
                    <div className="order-money">
                      <span>本次可开</span>
                      <strong>{money(order.availableMinor)}</strong>
                      {order.eligibilityKind === "wallet" && (
                        <small>
                          已消费现金 {money(order.consumedCashMinor)} / 实付 {money(order.paidMinor)}
                        </small>
                      )}
                      {order.eligibilityKind === "subscription" && (
                        <small>订阅实付 {money(order.paidMinor)}</small>
                      )}
                      {order.availableMinor === 0 && (
                        <small>
                          {order.refundFrozen
                            ? "金额账本已冻结"
                            : order.eligibilityStatus === "syncing"
                              ? "历史基线与新事件正在追平"
                              : order.eligibilityStatus === "source_unavailable"
                                ? "来源五条同步流尚未全部就绪"
                              : order.eligibilityStatus ===
                                  "not_invoiceable_pending_reconciliation"
                                ? "来源账本正在自动对账，完成后会自动恢复"
                              : order.eligibilityStatus !== "active"
                                ? "来源账本正在对账处理"
                                : !orderSourceReady(order)
                                  ? "平台资格摘要尚未通过安全门禁"
                            : order.verification !== "verified"
                              ? "等待支付核验"
                              : order.eligibilityKind === "wallet" &&
                                  order.consumedCashMinor === 0
                                ? "尚无已消费现金"
                                : order.eligibilityKind === "legacy" ||
                                    order.eligibilityKind === "noncash"
                                  ? "此额度不可开票"
                                  : "已被申请占用或开票"}
                        </small>
                      )}
                    </div>
                    {isSelected && (
                      <label className="partial-amount">
                        <span>本次金额</span>
                        <div>
                          <span>¥</span>
                          <input
                            value={(selected[order.id] / 100).toFixed(2)}
                            onChange={(event) =>
                              updateAmount(order, event.target.value)
                            }
                            inputMode="decimal"
                          />
                        </div>
                      </label>
                    )}
                  </article>
                );
              })}
            </div>
          )}
          <div className="orders-footnote">
            <ShieldCheck size={16} />
            <span>
              可开金额由服务端资金账本计算；待审占用和已开金额均不能再次使用，浏览器不会自行推算额度。
              {policy && (
                <>
                  {" "}只有在 {eligibilityStartLabel(policy.eligibilityStartAt)}（北京时间）
                  及之后完成的真实充值，并由该时点及之后的实际消费核销，才能开票。
                </>
              )}
            </span>
          </div>
        </section>

        <aside className="card checkout-card">
          <div className="card-heading compact">
            <div>
              <h2>本次申请</h2>
              <p>
                {selectedCount
                  ? `已选择 ${selectedCount} 笔订单`
                  : "尚未选择订单"}
              </p>
            </div>
          </div>
          <div className="checkout-total">
            <span>开票金额</span>
            <strong>{money(selectedTotal)}</strong>
            <small>含税金额，币种 CNY</small>
            <small>开票项目：{policy?.serviceItem ?? "策略加载中"}</small>
            {policy &&
              selectedTotal > 0 &&
              selectedTotal < policy.minimumRequestMinor && (
                <small className="minimum-warning">
                  距离最低开票金额还差{" "}
                  {money(policy.minimumRequestMinor - selectedTotal)}
                </small>
              )}
          </div>
          <div className="divider" />
          <label className="field-label">开票抬头</label>
          {profiles.length ? (
            <div className="profile-options">
              {profiles.map((profile) => (
                <button
                  key={profile.id}
                  className={`profile-option ${profileId === profile.id ? "selected" : ""}`}
                  onClick={() => setProfileId(profile.id)}
                >
                  <span className="radio-dot">
                    {profileId === profile.id && <span />}
                  </span>
                  <div>
                    <strong>{profile.title}</strong>
                    <small>
                      {profile.type === "enterprise"
                        ? maskTaxId(profile.taxId)
                        : "个人电子普票"}
                    </small>
                  </div>
                  {profile.isDefault && <Badge tone="blue">默认</Badge>}
                </button>
              ))}
            </div>
          ) : (
            <div className="empty-inline">还没有开票资料</div>
          )}
          <Link to={userRoute("/profiles")} className="text-action">
            <Plus size={15} />
            新增或管理开票资料
          </Link>
          <label className="confirm-row">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            <span>我确认订单、金额与开票资料准确，提交后进入财务审核。</span>
          </label>
          <button
            className="button button-primary button-wide"
            disabled={
              !selectedCount ||
              !profileId ||
              !confirmed ||
              submitting ||
              !policy ||
              selectedTotal < policy.minimumRequestMinor
            }
            onClick={() => void submit()}
          >
            {submitting ? (
              <Loader2 className="spin" size={17} />
            ) : (
              <Send size={17} />
            )}
            {submitting ? "正在提交" : "提交开票申请"}
          </button>
          <p className="checkout-tip">
            {policy
              ? `单次最低 ${money(policy.minimumRequestMinor)}。`
              : "正在读取公开开票策略。"}
            开具后邮件只发送通知，请登录开票中心下载 PDF。
          </p>
        </aside>
      </div>
    </PortalLayout>
  );
}

function ProfilesPage() {
  const { profiles, refresh } = useData();
  const [editing, setEditing] = useState<InvoiceProfile | null>(null);
  const [creating, setCreating] = useState(false);
  return (
    <PortalLayout>
      <PageHeader
        title="开票资料"
        description="保存常用的个人或企业抬头，提交申请时会生成不可修改的历史快照。"
        action={
          <button
            className="button button-primary"
            onClick={() => setCreating(true)}
          >
            <Plus size={17} />
            新增资料
          </button>
        }
      />
      <div className="profile-grid">
        {profiles.map((profile) => (
          <article className="profile-card card" key={profile.id}>
            <div className={`profile-mark ${profile.type}`}>
              <span>{profile.type === "enterprise" ? "企业" : "个人"}</span>
              <Landmark size={25} />
            </div>
            <div className="profile-card-head">
              <div>
                <h2>{profile.title}</h2>
                <span>
                  {profile.type === "enterprise"
                    ? maskTaxId(profile.taxId)
                    : "个人电子普票"}
                </span>
              </div>
              {profile.isDefault && (
                <Badge tone="blue">
                  <CheckCircle2 size={13} />
                  默认抬头
                </Badge>
              )}
            </div>
            <dl className="profile-detail">
              <div>
                <dt>接收邮箱</dt>
                <dd>{profile.email}</dd>
              </div>
              {profile.phone && (
                <div>
                  <dt>联系电话</dt>
                  <dd>{profile.phone}</dd>
                </div>
              )}
              {profile.address && (
                <div>
                  <dt>注册地址</dt>
                  <dd>{profile.address}</dd>
                </div>
              )}
              {profile.bankName && (
                <div>
                  <dt>开户银行</dt>
                  <dd>{profile.bankName}</dd>
                </div>
              )}
            </dl>
            <button
              className="button button-secondary button-wide"
              onClick={() => setEditing(profile)}
            >
              <Settings2 size={16} />
              编辑资料
            </button>
          </article>
        ))}
        <button className="profile-add-card" onClick={() => setCreating(true)}>
          <span>
            <Plus size={22} />
          </span>
          <strong>新增开票资料</strong>
          <small>支持个人与企业增值税普通发票</small>
        </button>
      </div>
      {(editing || creating) && (
        <ProfileDialog
          initial={editing}
          onClose={() => {
            setEditing(null);
            setCreating(false);
          }}
          onSaved={refresh}
        />
      )}
    </PortalLayout>
  );
}

function ProfileDialog({
  initial,
  onClose,
  onSaved,
}: {
  initial: InvoiceProfile | null;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const toast = useContext(ToastContext);
  const { user } = useAuth();
  const [type, setType] = useState<InvoiceProfileType>(
    initial?.type ?? "enterprise",
  );
  const [saving, setSaving] = useState(false);
  const [form, setForm] = useState({
    title: initial?.title ?? "",
    taxId: initial?.taxId ?? "",
    email: initial?.email ?? (user?.emailVerified ? user.email : ""),
    phone: initial?.phone ?? "",
    address: initial?.address ?? "",
    bankName: initial?.bankName ?? "",
    bankAccount: initial?.bankAccount ?? "",
    isDefault: initial?.isDefault ?? false,
  });
  const update = (key: keyof typeof form, value: string | boolean) =>
    setForm((current) => ({ ...current, [key]: value }));
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (
      !form.title.trim() ||
      !form.email.trim() ||
      (type === "enterprise" && !form.taxId.trim())
    )
      return;
    setSaving(true);
    try {
      await invoiceApi.saveProfile({
        ...form,
        title: form.title.trim(),
        taxId: form.taxId.trim(),
        email: form.email.trim(),
        id: initial?.id,
        revision: initial?.revision ?? 0,
        type,
      });
      await onSaved();
      toast("开票资料已安全保存。");
      onClose();
    } catch (error) {
      toast(
        error instanceof Error ? error.message : "开票资料保存失败。",
        "error",
      );
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="modal-layer">
      <button className="modal-backdrop" onClick={onClose} aria-label="关闭" />
      <section className="modal-card profile-dialog">
        <div className="modal-header">
          <div>
            <h2>{initial ? "编辑开票资料" : "新增开票资料"}</h2>
            <p>请填写需要开具的个人或企业抬头信息</p>
          </div>
          <button className="icon-button" onClick={onClose}>
            <X size={20} />
          </button>
        </div>
        <form onSubmit={(event) => void submit(event)}>
          <div className="type-picker">
            <button
              type="button"
              className={type === "enterprise" ? "active" : ""}
              onClick={() => setType("enterprise")}
            >
              <Building2 size={18} />
              <span>
                <strong>企业抬头</strong>
                <small>需要纳税人识别号</small>
              </span>
            </button>
            <button
              type="button"
              className={type === "personal" ? "active" : ""}
              onClick={() => setType("personal")}
            >
              <UserRound size={18} />
              <span>
                <strong>个人抬头</strong>
                <small>用于个人电子普票</small>
              </span>
            </button>
          </div>
          <div className="form-grid">
            <Field
              label={type === "enterprise" ? "企业名称" : "个人姓名"}
              required
              value={form.title}
              onChange={(value) => update("title", value)}
              placeholder="请输入完整抬头"
            />
            {type === "enterprise" && (
              <Field
                label="纳税人识别号"
                required
                value={form.taxId}
                onChange={(value) => update("taxId", value.toUpperCase())}
                placeholder="请输入 15/18/20 位识别号"
              />
            )}
            <Field
              label="接收邮箱"
              required
              type="email"
              value={form.email}
              onChange={(value) => update("email", value)}
              placeholder="invoice@example.com"
              hint="V1 仅支持统一身份账号已经验证的邮箱；备用邮箱验证将在后续版本提供。"
            />
            <Field
              label="联系电话"
              value={form.phone}
              onChange={(value) => update("phone", value)}
              placeholder="选填"
            />
            {type === "enterprise" && (
              <>
                <Field
                  wide
                  label="注册地址"
                  value={form.address}
                  onChange={(value) => update("address", value)}
                  placeholder="电子普票业务需要时填写（选填）"
                />
                <Field
                  label="开户银行"
                  value={form.bankName}
                  onChange={(value) => update("bankName", value)}
                  placeholder="选填"
                />
                <Field
                  label="银行账号"
                  value={form.bankAccount}
                  onChange={(value) => update("bankAccount", value)}
                  placeholder="选填"
                />
              </>
            )}
          </div>
          <label className="switch-row">
            <input
              type="checkbox"
              checked={form.isDefault}
              onChange={(event) => update("isDefault", event.target.checked)}
            />
            <span>
              <strong>设为默认开票资料</strong>
              <small>提交申请时优先选中</small>
            </span>
          </label>
          <div className="modal-actions">
            <button
              type="button"
              className="button button-secondary"
              onClick={onClose}
            >
              取消
            </button>
            <button className="button button-primary" disabled={saving}>
              {saving && <Loader2 className="spin" size={16} />}保存资料
            </button>
          </div>
        </form>
      </section>
    </div>
  );
}

function Field({
  label,
  value,
  onChange,
  placeholder,
  required,
  type = "text",
  wide,
  disabled,
  maxLength,
  hint,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  required?: boolean;
  type?: string;
  wide?: boolean;
  disabled?: boolean;
  maxLength?: number;
  hint?: string;
}) {
  return (
    <label className={`form-field ${wide ? "field-wide" : ""}`}>
      <span>
        {label}
        {required && <em>*</em>}
      </span>
      <input
        type={type}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        required={required}
        disabled={disabled}
        maxLength={maxLength}
      />
      {hint && <small>{hint}</small>}
    </label>
  );
}

function RecordsPage() {
  const {
    requests,
    loading,
    requestsNextCursor,
    loadingMoreRequests,
    loadMoreRequests,
    refresh,
  } = useData();
  const toast = useContext(ToastContext);
  const location = useLocation();
  const [filter, setFilter] = useState<"all" | InvoiceStatus>("all");
  const [directRequest, setDirectRequest] = useState<InvoiceRequest | null>(null);
  const [directLoading, setDirectLoading] = useState(false);
  const [directError, setDirectError] = useState<string | null>(null);
  const [cancellingRequestID, setCancellingRequestID] = useState<string | null>(
    null,
  );
  const requestedID = new URLSearchParams(location.search).get("request_id")?.trim();

  useEffect(() => {
    setDirectRequest(null);
    setDirectError(null);
    if (!requestedID) return;
    if (requestedID.length > 64 || !/^[A-Za-z0-9_-]+$/.test(requestedID)) {
      setDirectError("邮件链接中的申请编号无效。");
      return;
    }
    let active = true;
    setDirectLoading(true);
    void invoiceApi
      .getInvoiceRequestDetail(requestedID, false)
      .then((request) => {
        if (active) setDirectRequest(request);
      })
      .catch((error) => {
        if (active)
          setDirectError(
            error instanceof Error ? error.message : "邮件中的开票申请读取失败。",
          );
      })
      .finally(() => {
        if (active) setDirectLoading(false);
      });
    return () => {
      active = false;
    };
  }, [requestedID]);

  const visibleRequests =
    directRequest && !requests.some((request) => request.id === directRequest.id)
      ? [directRequest, ...requests]
      : requests;
  const filtered = visibleRequests.filter(
    (request) => filter === "all" || request.status === filter,
  );
  const download = async (request: InvoiceRequest) => {
    try {
      await invoiceApi.downloadInvoiceDocument(request);
    } catch (error) {
      toast(
        error instanceof Error ? error.message : "发票文件暂时无法下载。",
        "error",
      );
    }
  };
  const cancel = async (request: InvoiceRequest) => {
    if (!request.version) {
      toast("申请版本缺失，请刷新后重试。", "error");
      return;
    }
    const message =
      request.workflowStatus === "needs_changes"
        ? "取消后会释放本申请占用的金额，你可以修改资料后重新申请。确定继续吗？"
        : "取消后会释放本申请占用的金额。确定取消吗？";
    if (!window.confirm(message)) return;
    setCancellingRequestID(request.id);
    try {
      const cancelled = await invoiceApi.cancelInvoice(request);
      setDirectRequest((current) =>
        replaceDirectRequestAfterMutation(current, cancelled),
      );
      await refresh();
      toast("申请已取消，占用金额已释放，可以重新申请。");
    } catch (error) {
      toast(error instanceof Error ? error.message : "取消申请失败。", "error");
    } finally {
      setCancellingRequestID(null);
    }
  };
  return (
    <PortalLayout>
      <PageHeader
        title="开票记录"
        description="查看申请进度、审核反馈以及已开具的电子发票。"
      />
      {requestedID && (
        <div
          className={`mail-request-banner ${directError ? "mail-request-error" : ""}`}
          role={directError ? "alert" : "status"}
        >
          {directLoading ? (
            <Loader2 className="spin" size={18} />
          ) : directError ? (
            <CircleAlert size={18} />
          ) : (
            <Mail size={18} />
          )}
          <div>
            <strong>
              {directLoading
                ? "正在打开邮件中的开票申请"
                : directError
                  ? "无法打开邮件中的申请"
                  : "已直接打开邮件中的开票申请"}
            </strong>
            <span>{directError ?? directRequest?.requestNo ?? requestedID}</span>
          </div>
        </div>
      )}
      <section className="card records-card">
        <div className="record-filters">
          {(
            [
              ["all", "全部"],
              ["submitted", "已提交"],
              ["reviewing", "审核中"],
              ["returned", "已退回"],
              ["issued", "已开具"],
            ] as const
          ).map(([value, label]) => (
            <button
              key={value}
              className={filter === value ? "active" : ""}
              onClick={() => setFilter(value)}
            >
              {label}
            </button>
          ))}
        </div>
        {loading ? (
          <LoadingBlock />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>申请编号</th>
                  <th>来源</th>
                  <th>开票抬头</th>
                  <th>金额</th>
                  <th>提交时间</th>
                  <th>状态</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {filtered.map((request) => (
                  <tr
                    key={request.id}
                    className={
                      directRequest?.id === request.id ? "direct-request-row" : ""
                    }
                  >
                    <td>
                      <strong>{request.requestNo}</strong>
                      <small>{request.allocations.length} 笔充值订单</small>
                    </td>
                    <td>
                      <SourceBadge source={request.source} />
                    </td>
                    <td>
                      <strong>{request.profileSnapshot.title}</strong>
                      <small>{request.profileSnapshot.email}</small>
                    </td>
                    <td className="money-cell">{money(request.amountMinor)}</td>
                    <td>{dateTime(request.submittedAt)}</td>
                    <td>
                      <span
                        className={`badge ${displayStatus(request).className}`}
                      >
                        {displayStatus(request).label}
                      </span>
                      {request.status === "returned" && (
                        <small className="error-text">
                          {request.reviewNote}
                        </small>
                      )}
                    </td>
                    <td className="action-cell">
                      {request.status === "issued" ? (
                        <button
                          className="button button-small button-secondary"
                          disabled={!apiCapabilities.documentDownload}
                          onClick={() => void download(request)}
                        >
                          <Download size={15} />
                          {apiCapabilities.documentDownload
                            ? "下载 PDF"
                            : "PDF 接口待接入"}
                        </button>
                      ) : canUserCancelInvoice(request) ? (
                        <button
                          className="button button-small button-secondary"
                          disabled={cancellingRequestID === request.id}
                          onClick={() => void cancel(request)}
                        >
                          {cancellingRequestID === request.id && (
                            <Loader2 className="spin" size={14} />
                          )}
                          {userCancellationLabel(request)}
                        </button>
                      ) : (
                        <button className="icon-button">
                          <ChevronRight size={18} />
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!filtered.length && (
              <EmptyState
                icon={<Inbox />}
                title="暂无相关记录"
                description="提交开票申请后，可以在这里查看进度。"
              />
            )}
          </div>
        )}
        {requestsNextCursor && (
          <button
            className="button button-secondary load-more"
            disabled={loadingMoreRequests}
            onClick={() => void loadMoreRequests()}
          >
            {loadingMoreRequests && <Loader2 className="spin" size={16} />}
            加载更多历史记录
          </button>
        )}
      </section>
    </PortalLayout>
  );
}

function AdminPage() {
  const {
    requests,
    summary,
    loading,
    refresh,
    requestsNextCursor,
    loadingMoreRequests,
    loadMoreRequests,
  } = useData();
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState<"all" | InvoiceStatus>("all");
  // Platform-scoped embedded admin: the list is already server-filtered
  // (DataProvider passes source_instance_id); this client-side filter is
  // initialized to match and its control is hidden below (CR-0005 (c)), same
  // defense-in-depth pattern as the user embed's OrdersPage source scoping.
  const [source, setSource] = useState<"all" | SourceType>(
    embeddedAdminPlatform() ?? "all",
  );
  const [selected, setSelected] = useState<InvoiceRequest | null>(null);
  const query = new URLSearchParams(useLocation().search);
  const viewIssued = query.get("view") === "issued";
  useEffect(() => {
    if (viewIssued) setStatus("issued");
  }, [viewIssued]);
  const filtered = requests.filter((request) => {
    const keyword = search.toLowerCase();
    return (
      (status === "all" || request.status === status) &&
      (source === "all" || request.source === source) &&
      (!keyword ||
        request.requestNo.toLowerCase().includes(keyword) ||
        request.userName.toLowerCase().includes(keyword) ||
        request.profileSnapshot.title.toLowerCase().includes(keyword))
    );
  });
  const pending = requests.filter(
    (request) =>
      request.status === "submitted" || request.status === "reviewing",
  ).length;
  return (
    <PortalLayout admin>
      <PageHeader
        eyebrow="FINANCE OPERATIONS"
        title={viewIssued ? "发票档案" : "审核工作台"}
        description={
          viewIssued
            ? "查询已开具的发票文件、邮件送达与历史快照。"
            : "核验充值证据、审核开票资料，并完成电子发票交付。"
        }
        action={
          <button
            className="button button-secondary"
            onClick={() => void refresh()}
          >
            <RefreshCcw size={16} />
            同步数据
          </button>
        }
      />
      <div className="admin-kpis">
        <Kpi
          label="已加载待处理"
          value={`${pending}`}
          unit="笔"
          icon={<Inbox />}
          hint="来自待处理申请"
        />
        <Kpi
          label="支付候选核验"
          value="独立队列"
          icon={<ShieldCheck />}
          hint="New API 付款证据"
        />
        <Kpi
          label="已加载已开具"
          value={money(summary?.issuedThisYearMinor ?? 0)}
          icon={<FileCheck2 />}
          hint="已完成电子归档"
        />
        <Kpi
          label="异常与退回"
          value={`${requests.filter((request) => request.status === "returned").length}`}
          unit="笔"
          icon={<CircleAlert />}
          hint="需要关注"
          danger
        />
      </div>
      <section className="card admin-table-card">
        <div className="toolbar">
          <div className="search-box">
            <Search size={17} />
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="搜索申请编号、用户或抬头"
            />
          </div>
          <select
            value={status}
            onChange={(event) => setStatus(event.target.value as typeof status)}
          >
            <option value="all">全部状态</option>
            <option value="submitted">已提交</option>
            <option value="reviewing">审核中</option>
            <option value="returned">已退回</option>
            <option value="issued">已开具</option>
          </select>
          {!embeddedAdminPlatform() && (
            <select
              value={source}
              onChange={(event) => setSource(event.target.value as typeof source)}
            >
              <option value="all">全部来源</option>
              <option value="sub2api">SoloV API</option>
              <option value="newapi">模型平台</option>
            </select>
          )}
        </div>
        {loading ? (
          <LoadingBlock />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>申请信息</th>
                  <th>用户</th>
                  <th>开票抬头</th>
                  <th>申请金额</th>
                  <th>来源与核验</th>
                  <th>状态</th>
                  <th>更新时间</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {filtered.map((request) => (
                  <tr
                    key={request.id}
                    onClick={() => setSelected(request)}
                    className="clickable-row"
                  >
                    <td>
                      <strong>{request.requestNo}</strong>
                      <small>{dateTime(request.submittedAt)}</small>
                    </td>
                    <td>
                      <strong>{request.userName}</strong>
                      <small>{request.userEmail}</small>
                    </td>
                    <td>
                      <strong>{request.profileSnapshot.title}</strong>
                      <small>
                        {request.profileSnapshot.type === "enterprise"
                          ? maskTaxId(request.profileSnapshot.taxId)
                          : "个人"}
                      </small>
                    </td>
                    <td className="money-cell">{money(request.amountMinor)}</td>
                    <td>
                      <SourceBadge source={request.source} />
                      {request.newApiVerification === "pending" && (
                        <small className="warning-text">待人工支付核验</small>
                      )}
                      {request.newApiVerification === "passed" && (
                        <small className="success-text">支付核验通过</small>
                      )}
                      {request.newApiVerification === "failed" && (
                        <small className="error-text">核验异常</small>
                      )}
                    </td>
                    <td>
                      <span
                        className={`badge ${displayStatus(request).className}`}
                      >
                        {displayStatus(request).label}
                      </span>
                    </td>
                    <td>{dateTime(request.updatedAt)}</td>
                    <td>
                      <button className="icon-button">
                        <ChevronRight size={18} />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!filtered.length && (
              <EmptyState
                icon={<Search />}
                title="没有匹配的申请"
                description="尝试调整状态、来源或搜索条件。"
              />
            )}
          </div>
        )}
      </section>
      {requestsNextCursor && (
        <button
          className="button button-secondary load-more"
          disabled={loadingMoreRequests}
          onClick={() => void loadMoreRequests()}
        >
          {loadingMoreRequests && <Loader2 className="spin" size={16} />}
          加载更多申请
        </button>
      )}
      {selected && (
        <AdminDrawer
          requestId={selected.id}
          onClose={() => setSelected(null)}
        />
      )}
    </PortalLayout>
  );
}

function Kpi({
  label,
  value,
  unit,
  icon,
  hint,
  danger,
}: {
  label: string;
  value: string;
  unit?: string;
  icon: ReactNode;
  hint: string;
  danger?: boolean;
}) {
  return (
    <article className="kpi-card">
      <div className={`kpi-icon ${danger ? "danger" : ""}`}>{icon}</div>
      <span>{label}</span>
      <strong>
        {value}
        <em>{unit}</em>
      </strong>
      <small>{hint}</small>
    </article>
  );
}

function AdminDrawer({
  requestId,
  onClose,
}: {
  requestId: string;
  onClose: () => void;
}) {
  const { requests, refresh } = useData();
  const toast = useContext(ToastContext);
  const request = requests.find((item) => item.id === requestId);
  const [working, setWorking] = useState(false);
  const [note, setNote] = useState("");
  const [invoiceNumber, setInvoiceNumber] = useState("");
  const [issuedAt, setIssuedAt] = useState(currentLocalDateTimeValue);
  const [file, setFile] = useState<File | null>(null);
  const [delivery, setDelivery] = useState<Awaited<
    ReturnType<typeof invoiceApi.getDeliveryState>
  > | null>(null);
  const [deliveryLoading, setDeliveryLoading] = useState(false);
  const [deliveryError, setDeliveryError] = useState<string | null>(null);
  // 企业微信通知（XM-INV-NOTICE-UI）。与交付状态分开：交付说的是"发票邮件
  // 寄了没有"（要先开票），通知说的是"提交那一刻推的那条群消息发了没有"，
  // 从提交起就存在。
  const [notices, setNotices] = useState<InvoiceNoticeDelivery[] | null>(null);
  const [noticesLoading, setNoticesLoading] = useState(false);
  const [noticesError, setNoticesError] = useState<string | null>(null);
  useEffect(() => {
    setDelivery(null);
    setDeliveryError(null);
    setDeliveryLoading(false);
    if (
      !request ||
      (request.status !== "issued" &&
        request.workflowStatus !== "issued_awaiting_document" &&
        request.workflowStatus !== "refund_attention")
    )
      return;
    let active = true;
    setDeliveryLoading(true);
    void invoiceApi
      .getDeliveryState(request, true)
      .then((value) => {
        if (active) setDelivery(value);
      })
      .catch((error) => {
        if (active)
          setDeliveryError(
            error instanceof Error ? error.message : "交付状态读取失败。",
          );
      })
      .finally(() => {
        if (active) setDeliveryLoading(false);
      });
    return () => {
      active = false;
    };
  }, [requestId, request?.status, request?.updatedAt, request?.workflowStatus]);
  // 通知不按状态设闸：一条 request.submitted 的通知在申请还是"待审核"时
  // 就存在了，而那正是最需要查"推出去了没有"的时候。
  useEffect(() => {
    setNotices(null);
    setNoticesError(null);
    setNoticesLoading(false);
    if (!request) return;
    let active = true;
    setNoticesLoading(true);
    void invoiceApi
      .listRequestNotices(request)
      .then((value) => {
        if (active) setNotices(value);
      })
      .catch((error) => {
        if (active)
          setNoticesError(
            error instanceof Error ? error.message : "通知状态读取失败。",
          );
      })
      .finally(() => {
        if (active) setNoticesLoading(false);
      });
    return () => {
      active = false;
    };
  }, [requestId, request?.status, request?.updatedAt]);
  useEffect(() => {
    setIssuedAt(currentLocalDateTimeValue());
  }, [requestId]);
  if (!request) return null;
  // View constraint, not a security boundary (CR-0005 (c)): the admin
  // request list is already server-filtered to this platform in embedded
  // platform mode, so this is unreachable through normal navigation -- kept
  // as defense in depth against any future path that could select an id
  // from outside the currently-loaded (and filtered) list.
  if (embeddedAdminPlatform() && request.source !== embeddedAdminPlatform()) {
    return (
      <div className="drawer-layer">
        <button className="drawer-backdrop" onClick={onClose} aria-label="关闭" />
        <aside className="drawer">
          <div className="drawer-head">
            <div>
              <span>开票申请详情</span>
              <h2>{request.requestNo}</h2>
            </div>
            <button className="icon-button" onClick={onClose}>
              <X size={20} />
            </button>
          </div>
          <div className="drawer-body">
            <EmptyState
              icon={<CircleAlert />}
              title="不在当前平台"
              description="该申请属于另一平台，当前嵌入视图仅展示所选平台的数据。"
            />
          </div>
        </aside>
      </div>
    );
  }
  const effectiveWorkflow =
    request.workflowStatus ??
    (request.status === "submitted"
      ? "pending_review"
      : request.status === "reviewing"
        ? "approved"
        : request.status === "issued"
          ? "issued"
          : "needs_changes");
  const canReview =
    effectiveWorkflow === "pending_review" ||
    effectiveWorkflow === "needs_changes";
  const canUpload =
    effectiveWorkflow === "approved" ||
    effectiveWorkflow === "manual_issuing" ||
    effectiveWorkflow === "issued_awaiting_document";
  const showDelivery =
    effectiveWorkflow === "issued" || effectiveWorkflow === "refund_attention";
  const run = async (action: () => Promise<void>, message: string) => {
    setWorking(true);
    try {
      await action();
      await refresh();
      toast(message);
    } catch (error) {
      toast(
        error instanceof Error ? error.message : "操作失败，请稍后重试",
        "error",
      );
    } finally {
      setWorking(false);
    }
  };
  const review = (action: "approve" | "return") => {
    if (action === "return" && !note.trim()) {
      toast("退回申请时必须填写需要用户修改的内容。", "error");
      return;
    }
    void run(
      () =>
        invoiceApi.adminReview({
          requestId: request.id,
          version: request.version,
          action,
          note,
        }),
      action === "approve" ? "资料审核已通过。" : "申请已退回用户修改。",
    );
  };
  const upload = () => {
    if (!file || !invoiceNumber) {
      toast("请填写发票号码并选择 PDF 文件。", "error");
      return;
    }
    if (
      file.type !== "application/pdf" &&
      !file.name.toLowerCase().endsWith(".pdf")
    ) {
      toast("仅允许上传 PDF 发票文件。", "error");
      return;
    }
    if (!invoicePDFSizeAllowed(file.size)) {
      toast("PDF 文件必须大于 0 且不能超过 20 MiB。", "error");
      return;
    }
    let normalizedIssuedAt: string;
    try {
      normalizedIssuedAt = normalizeIssuedAt(issuedAt);
    } catch (error) {
      toast(error instanceof Error ? error.message : "实际开票时间无效。", "error");
      return;
    }
    void run(
      () =>
        invoiceApi.adminUploadInvoice(
          request,
          file,
          invoiceNumber,
          normalizedIssuedAt,
        ),
      "电子发票已归档，邮件已进入发送队列。",
    );
  };
  return (
    <div className="drawer-layer">
      <button className="drawer-backdrop" onClick={onClose} aria-label="关闭" />
      <aside className="drawer">
        <div className="drawer-head">
          <div>
            <span>开票申请详情</span>
            <h2>{request.requestNo}</h2>
          </div>
          <button className="icon-button" onClick={onClose}>
            <X size={20} />
          </button>
        </div>
        <div className="drawer-status">
          <span className={`badge ${displayStatus(request).className}`}>
            {displayStatus(request).label}
          </span>
          <span>提交于 {dateTime(request.submittedAt)}</span>
        </div>
        <div className="drawer-body">
          <section className="detail-section">
            <h3>申请人与金额</h3>
            <div className="detail-summary">
              <div className="avatar">{request.userName.slice(0, 1)}</div>
              <div>
                <strong>{request.userName}</strong>
                <span>{request.userEmail}</span>
              </div>
              <strong>{money(request.amountMinor)}</strong>
            </div>
          </section>
          <section className="detail-section">
            <h3>开票资料快照</h3>
            <dl className="key-values">
              <div>
                <dt>发票抬头</dt>
                <dd>{request.profileSnapshot.title}</dd>
              </div>
              <div>
                <dt>抬头类型</dt>
                <dd>
                  {request.profileSnapshot.type === "enterprise"
                    ? "企业"
                    : "个人"}
                </dd>
              </div>
              <div>
                <dt>税号</dt>
                <dd>{request.profileSnapshot.taxId || "—"}</dd>
              </div>
              <div>
                <dt>接收邮箱</dt>
                <dd>{request.profileSnapshot.email}</dd>
              </div>
            </dl>
          </section>
          <section className="detail-section">
            <h3>占用订单</h3>
            {request.allocations.map((allocation) => (
              <div className="allocation-row" key={allocation.orderId}>
                <div>
                  <SourceBadge source={request.source} />
                  <strong>{allocation.tradeNo}</strong>
                </div>
                <span>{money(allocation.allocatedMinor)}</span>
              </div>
            ))}
          </section>
          {request.source === "newapi" && (
            <section
              className={`verification-box verification-${request.newApiVerification}`}
            >
              <div className="verification-title">
                <ShieldCheck size={19} />
                <div>
                  <strong>New API 支付人工核验</strong>
                  <span>候选订单需与支付通道凭证一致</span>
                </div>
                <VerificationBadge value={request.newApiVerification} />
              </div>
              <div className="verification-facts">
                <span>订单号已匹配</span>
                <span>用户归属已匹配</span>
                <span>金额 {money(request.amountMinor)}</span>
              </div>
              {request.newApiVerification === "pending" && (
                <div className="inline-actions">
                  <Link
                    className="button button-dark"
                    to="/admin/payment-candidates"
                    onClick={onClose}
                  >
                    <BadgeCheck size={16} />
                    前往支付核验队列
                  </Link>
                </div>
              )}
            </section>
          )}
          {request.workflowStatus === "refund_attention" && (
            <section className="detail-section refund-attention-box">
              <CircleAlert size={19} />
              <div>
                <strong>此申请涉及退款后的已开票暴露</strong>
                <span>请在退款与红冲队列核对并记录线下处理结果。</span>
              </div>
              <Link to="/admin/refund-cases" onClick={onClose}>
                前往处理
              </Link>
            </section>
          )}
          {canReview && (
            <section className="detail-section">
              <h3>审核处理</h3>
              <textarea
                value={note}
                onChange={(event) => setNote(event.target.value)}
                placeholder="填写审核备注；退回时请说明需要修改的内容"
                rows={3}
                maxLength={2000}
              />
              <div className="inline-actions">
                <button
                  className="button button-secondary"
                  disabled={working}
                  onClick={() => review("return")}
                >
                  退回修改
                </button>
                <button
                  className="button button-primary"
                  disabled={
                    working ||
                    (request.source === "newapi" &&
                      request.newApiVerification !== "passed")
                  }
                  onClick={() => review("approve")}
                >
                  <Check size={16} />
                  审核通过
                </button>
              </div>
            </section>
          )}
          {canUpload &&
            request.reviewer &&
            (request.source !== "newapi" ||
              request.newApiVerification === "passed") && (
              <section className="detail-section upload-section">
                <h3>上传电子发票</h3>
                {apiCapabilities.documentUpload ? (
                  <>
                    <Field
                      label="发票号码"
                      value={invoiceNumber}
                      onChange={setInvoiceNumber}
                      placeholder="请输入完整发票号码"
                      required
                      maxLength={128}
                    />
                    <Field
                      label="实际开票时间"
                      type="datetime-local"
                      value={issuedAt}
                      onChange={setIssuedAt}
                      required
                      hint="请填写税务平台实际开具时间；系统会按当前时区转换并保存。"
                    />
                    <label className={`file-drop ${file ? "has-file" : ""}`}>
                      <input
                        type="file"
                        accept="application/pdf,.pdf"
                        onChange={(event) =>
                          setFile(event.target.files?.[0] ?? null)
                        }
                      />
                      <UploadCloud size={23} />
                      <strong>{file?.name ?? "选择或拖入 PDF 文件"}</strong>
                      <span>
                        {file
                          ? `${(file.size / 1024).toFixed(1)} KB`
                          : "单个文件不超过 20 MiB"}
                      </span>
                    </label>
                    <button
                      className="button button-primary button-wide"
                      disabled={working}
                      onClick={upload}
                    >
                      <UploadCloud size={16} />
                      归档并进入邮件队列
                    </button>
                  </>
                ) : (
                  <div className="capability-notice">
                    <CircleAlert size={18} />
                    <div>
                      <strong>真实 PDF 上传尚未接通</strong>
                      <span>
                        当前 HTTP
                        后端仅接受文档元数据，尚无安全上传、隔离扫描和对象存储流程。
                      </span>
                    </div>
                  </div>
                )}
              </section>
            )}
          {showDelivery && (
            <section className="delivery-box">
              <div>
                <FileCheck2 size={21} />
                <div>
                  <strong>
                    {delivery?.documentAvailable
                      ? "电子发票已开具"
                      : request.workflowStatus === "refund_attention"
                        ? "退款案例交付核对"
                        : "正在读取电子发票"}
                  </strong>
                  <span>
                    {delivery?.invoiceNumber ??
                      request.invoiceNumber ??
                      (deliveryLoading ? "正在读取发票号码" : "发票文件已归档")}
                  </span>
                </div>
              </div>
              <button
                className="button button-secondary"
                disabled={
                  !apiCapabilities.documentDownload ||
                  deliveryLoading ||
                  !delivery?.documentAvailable
                }
                onClick={() =>
                  void run(
                    () => invoiceApi.downloadAdminInvoiceDocument(request),
                    "发票文件已开始下载。",
                  )
                }
              >
                <Download size={15} />
                {!apiCapabilities.documentDownload
                  ? "下载接口待接入"
                  : deliveryLoading
                    ? "读取中"
                    : delivery?.documentAvailable
                      ? "下载"
                      : "暂无文件"}
              </button>
              <div className="mail-row">
                <Mail size={17} />
                <span>
                  邮件状态：
                  {deliveryLoading
                    ? "读取中"
                    : deliveryError
                      ? "读取失败"
                      : delivery
                        ? mailStatusLabel(delivery.mailStatus)
                        : "尚无交付记录"}
                </span>
                {deliveryError && <small>{deliveryError}</small>}
                {delivery && delivery.mailAttempts > 0 && (
                  <small>已尝试 {delivery.mailAttempts} 次</small>
                )}
                {delivery?.nextMailAttemptAt && (
                  <small>下次尝试 {dateTime(delivery.nextMailAttemptAt)}</small>
                )}
                {delivery?.documentAvailable && delivery.mailStatus !== "queued" && (
                  <button
                    disabled={working || !apiCapabilities.mailResend}
                    onClick={() =>
                      void run(
                        async () => {
                          await invoiceApi.resendMail(request);
                          const next = await invoiceApi.getDeliveryState(
                            request,
                            true,
                          );
                          setDelivery(next);
                          setDeliveryError(null);
                        },
                        "发票邮件已重新进入发送队列。",
                      )
                    }
                  >
                    重新发送
                  </button>
                )}
              </div>
            </section>
          )}
          {/* 企业微信通知（XM-INV-NOTICE-UI）。**不设状态闸**：提交那一刻的
              通知在申请还是"待审核"时就存在，而那正是最需要查它的时候。 */}
          <section className="delivery-box">
            <div>
              <Send size={21} />
              <div>
                <strong>企业微信通知</strong>
                <span>
                  提交、审核这类事件推到群机器人的记录；与上面的发票邮件是两条
                  不同的通道。
                </span>
              </div>
            </div>
            <div className="mail-row">
              {noticesLoading ? (
                <span>正在读取通知状态</span>
              ) : noticesError ? (
                <>
                  <span>通知状态读取失败</span>
                  <small>{noticesError}</small>
                </>
              ) : notices && notices.length > 0 ? (
                notices.map((notice) => (
                  <span key={notice.id}>
                    {noticeKindLabel(notice.kind)}：
                    {noticeStatusLabel(notice.status)}
                    {notice.attemptCount > 0 && (
                      <small>已尝试 {notice.attemptCount} 次</small>
                    )}
                    {notice.deliveredAt && (
                      <small>送达 {dateTime(notice.deliveredAt)}</small>
                    )}
                    {!notice.deliveredAt && notice.nextAttemptAt && (
                      <small>下次尝试 {dateTime(notice.nextAttemptAt)}</small>
                    )}
                    {/* 失败短码是"为什么没发出去"的唯一线索；后端保证它不含
                        Webhook 地址与上游原文，所以可以直接显示。 */}
                    {notice.lastErrorCode && <small>{notice.lastErrorCode}</small>}
                  </span>
                ))
              ) : (
                <span>
                  没有通知记录
                  <small>
                    未配置群机器人地址时不会入队，这不是故障
                  </small>
                </span>
              )}
            </div>
          </section>
        </div>
        {working && (
          <div className="drawer-working">
            <Loader2 className="spin" size={17} />
            正在安全处理
          </div>
        )}
      </aside>
    </div>
  );
}

function VerificationBadge({
  value,
}: {
  value: InvoiceRequest["newApiVerification"];
}) {
  if (value === "passed") return <Badge tone="green">已通过</Badge>;
  if (value === "failed") return <Badge tone="red">异常</Badge>;
  return <Badge tone="amber">待核验</Badge>;
}

// 通知事件与状态的中文。**未知取值原样显示**，与卡片推送对未知交易类型的
// 处理同一条纪律：后端加了新事件/新状态时，显示原值仍然有用，藏起来不是。
function noticeKindLabel(value: string) {
  return { "request.submitted": "提交申请" }[value] ?? (value || "未知事件");
}

function noticeStatusLabel(value: string) {
  return (
    {
      queued: "排队中",
      sending: "投递中",
      sent: "已发送",
      // 到达重试上限后停住。行留着，看得见它失败了。
      failed: "已放弃",
    }[value] ?? (value || "未知状态")
  );
}

function mailStatusLabel(value: InvoiceRequest["mailStatus"]) {
  return {
    not_sent: "未发送",
    queued: "发送队列中",
    sent: "已发送",
    failed: "发送失败",
  }[value];
}

const eligibilityFreezeReasonLabels: Record<EligibilityFreezeReason, string> = {
  UNKNOWN_NEGATIVE_BALANCE: "来源账户出现未知负余额",
  LATE_FINALIZED_EVENT: "已最终化区间收到迟到事件",
  AMBIGUOUS_EVENT_ORDER: "同一时间事件缺少可证明顺序",
  EVENT_PAYLOAD_DRIFT: "已接收事实的内容发生漂移",
  UNIT_MISMATCH: "服务单位合同不一致",
  USAGE_EXCEEDS_LEDGER: "消费超过可解释资金账本",
  STREAM_WATERMARK_REGRESSION: "来源数据流水位回退",
  SOURCE_GAP: "来源事实或日志出现完整性缺口",
  SOURCE_REFUND: "来源退款需先完成红冲处置",
  EVENT_DEAD: "事件已失效（源事实死信）",
  POLICY_ANCHOR_BLOCKED: "策略锚定被阻断",
};

const eligibilityFreezeReasonOptions = Object.entries(
  eligibilityFreezeReasonLabels,
) as Array<[EligibilityFreezeReason, string]>;

// http-api.ts's mapEligibilityFreeze deliberately accepts any well-formed
// (not just catalogued) freeze_reason so one unrecognized row never fails
// the whole "资格冻结" list -- this is the display-side half of that same
// tolerance: an item.reason not in eligibilityFreezeReasonLabels (a future
// backend reason this frontend hasn't labeled yet) renders its raw code
// instead of `undefined`.
// FreezeTriggerLine names the specific evidence that opened one freeze
// (XM-INV-FREEZE-TRIGGER-VISIBLE).
//
// Several freezes on one account routinely share a reason, a scope and even
// the same second: production had four SOURCE_GAP rows on account 2092 whose
// only difference was which balance checkpoint tripped each one, and the
// queue rendered them as four identical rows. Renders nothing when the row
// carries no trigger, on the same "absent means absent" posture as the
// account email beside it.
function FreezeTriggerLine({
  objectType,
  objectId,
}: {
  objectType?: string;
  objectId?: string;
}) {
  if (!objectType && !objectId) return null;
  // The id is an upstream event/checkpoint key, long and opaque; the head of
  // it is enough to tell two rows apart and to search the audit trail with,
  // and the full value stays available on hover.
  const shortId = objectId ? objectId.slice(0, 12) : "";
  return (
    <small className="muted-text" title={objectId ?? undefined}>
      触发对象 {objectType || "未知类型"}
      {shortId ? `:${shortId}` : ""}
    </small>
  );
}

function freezeReasonLabel(reason: EligibilityFreeze["reason"]) {
  return (eligibilityFreezeReasonLabels as Record<string, string>)[reason] ?? reason;
}

function EligibilityFreezesPage() {
  const toast = useContext(ToastContext);
  const [filters, setFilters] = useState<EligibilityFreezeFilters>({
    status: "open",
  });
  // CR-0007 problem one: local, immediately-editable input state, committed
  // into filters.externalUserId (which actually triggers the server refetch
  // below) after a short pause in typing -- this codebase has no existing
  // free-text filter that hits the server, so this reuses the same debounce
  // idiom already established for height-sync above (useEmbeddedAdminHeightSync)
  // rather than refetching on every keystroke.
  const [externalUserIdInput, setExternalUserIdInput] = useState("");
  useEffect(() => {
    const trimmed = externalUserIdInput.trim();
    const timer = window.setTimeout(() => {
      setFilters((current) =>
        current.externalUserId === (trimmed || undefined)
          ? current
          : { ...current, externalUserId: trimmed || undefined },
      );
    }, 400);
    return () => window.clearTimeout(timer);
  }, [externalUserIdInput]);
  const [items, setItems] = useState<EligibilityFreeze[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [pageError, setPageError] = useState<string | null>(null);
  const [selected, setSelected] = useState<EligibilityFreeze | null>(null);
  const [knownSources, setKnownSources] = useState<
    Array<{ id: string; source: SourceType }>
  >([]);
  // CR-0009 "变更范围" item 5: default narrowed to accounts genuinely
  // needing manual review; a visible toggle reveals the mechanically
  // self-healing legacy rows too (see isMechanicalReconciliationFreeze).
  const [showAllReasons, setShowAllReasons] = useState(false);
  const visibleItems = useMemo(
    () =>
      showAllReasons
        ? items
        : items.filter((item) => !isMechanicalReconciliationFreeze(item)),
    [items, showAllReasons],
  );
  const hiddenCount = items.length - visibleItems.length;
  const loadVersion = useRef(0);
  const platformSourceInstanceId = useEmbeddedAdminPlatformSourceInstanceId();

  // Platform-scoped embedded admin forces the filter to the resolved
  // platform and hides the control below (CR-0005 (c)); it cannot be
  // overridden by a click on a control that no longer exists, only by a
  // filters.sourceInstanceId set from elsewhere -- there is none.
  useEffect(() => {
    if (!embeddedAdminPlatform() || !platformSourceInstanceId) return;
    setFilters((current) =>
      current.sourceInstanceId === platformSourceInstanceId
        ? current
        : { ...current, sourceInstanceId: platformSourceInstanceId },
    );
  }, [platformSourceInstanceId]);

  const load = async (cursor?: string) => {
    // Wait for the platform's source_instance_id rather than briefly
    // requesting an unscoped page.
    if (embeddedAdminPlatform() && !platformSourceInstanceId) return;
    const version = cursor ? loadVersion.current : ++loadVersion.current;
    cursor ? setLoadingMore(true) : setLoading(true);
    try {
      const page = await invoiceApi.getEligibilityFreezes(filters, cursor);
      if (version !== loadVersion.current) return;
      setItems((current) =>
        cursor
          ? [
              ...current,
              ...page.items.filter(
                (item) =>
                  !current.some((existing) => existing.id === item.id),
              ),
            ]
          : page.items,
      );
      setKnownSources((current) => {
        const merged = new Map(current.map((item) => [item.id, item.source]));
        page.items.forEach((item) =>
          merged.set(item.sourceInstanceId, item.source),
        );
        return Array.from(merged, ([id, source]) => ({ id, source }));
      });
      setNextCursor(page.nextCursor);
      setPageError(null);
    } catch (error) {
      if (version !== loadVersion.current) return;
      setPageError(
        error instanceof Error ? error.message : "资格冻结队列读取失败。",
      );
    } finally {
      if (version === loadVersion.current) {
        setLoading(false);
        setLoadingMore(false);
      }
    }
  };

  useEffect(() => {
    setItems([]);
    setNextCursor(undefined);
    setSelected(null);
    void load();
  }, [filters.status, filters.reason, filters.sourceInstanceId, filters.externalUserId]);

  const sourceOptionLabels = useMemo(() => {
    const counters: Partial<Record<SourceType, number>> = {};
    return knownSources.map((item) => {
      counters[item.source] = (counters[item.source] ?? 0) + 1;
      const total = knownSources.filter(
        (candidate) => candidate.source === item.source,
      ).length;
      return {
        ...item,
        label: `${sourceName[item.source]}${total > 1 ? ` · 实例 ${counters[item.source]}` : ""}`,
      };
    });
  }, [knownSources]);

  return (
    <PortalLayout admin>
      <PageHeader
        eyebrow="ELIGIBILITY SAFETY"
        title="开票资格冻结队列"
        description="只有最新最终化余额对账已证明安全、五条数据流就绪且不存在退款风险时，服务端才允许解除普通资格冻结。"
        action={
          <button
            className="button button-secondary"
            disabled={loading}
            onClick={() => void load()}
          >
            <RefreshCcw size={16} className={loading ? "spin" : ""} />
            刷新队列
          </button>
        }
      />
      <div className="freeze-guardrail" role="note">
        <ShieldCheck size={19} />
        <div>
          <strong>管理员不能人工覆盖资金事实</strong>
          <span>
            页面只提交加密保存的证据引用、说明和当前版本；是否安全解冻完全由服务端最新对账、退款与五流门禁判定。
          </span>
        </div>
      </div>
      <section className="card admin-table-card">
        <div className="toolbar freeze-toolbar">
          <div className="search-box">
            <Search size={17} />
            <input
              aria-label="来源用户 ID"
              value={externalUserIdInput}
              maxLength={512}
              autoComplete="off"
              placeholder="按来源用户 ID 精确过滤"
              onChange={(event) => setExternalUserIdInput(event.target.value)}
            />
          </div>
          <select
            aria-label="冻结状态"
            value={filters.status}
            onChange={(event) =>
              setFilters((current) => ({
                ...current,
                status: event.target.value as EligibilityFreezeFilters["status"],
              }))
            }
          >
            <option value="open">待处理</option>
            <option value="resolved">已安全解除</option>
            <option value="all">全部状态</option>
          </select>
          <select
            aria-label="冻结原因"
            value={filters.reason ?? ""}
            onChange={(event) =>
              setFilters((current) => ({
                ...current,
                reason: (event.target.value || undefined) as
                  | EligibilityFreezeReason
                  | undefined,
              }))
            }
          >
            <option value="">全部安全原因</option>
            {eligibilityFreezeReasonOptions.map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
          {!embeddedAdminPlatform() && (
            <select
              aria-label="来源平台实例"
              value={filters.sourceInstanceId ?? ""}
              onChange={(event) =>
                setFilters((current) => ({
                  ...current,
                  sourceInstanceId: event.target.value || undefined,
                }))
              }
            >
              <option value="">全部来源实例</option>
              {sourceOptionLabels.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.label}
                </option>
              ))}
            </select>
          )}
          <label className="toolbar-toggle">
            <input
              type="checkbox"
              checked={showAllReasons}
              onChange={(event) => setShowAllReasons(event.target.checked)}
            />
            <span>显示全部（含等对账自愈的记录）</span>
          </label>
          <Badge tone={filters.status === "open" ? "amber" : "blue"}>
            本页 {visibleItems.length} 条
            {!showAllReasons && hiddenCount > 0 ? `（已隐藏 ${hiddenCount} 条自愈中）` : ""}
          </Badge>
        </div>
        {pageError && (
          <div className="api-error-banner" role="alert">
            <CircleAlert size={18} />
            <span>{pageError}</span>
            <button className="button button-secondary" onClick={() => void load()}>
              重试
            </button>
          </div>
        )}
        {loading ? (
          <LoadingBlock />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>来源</th>
                  <th>来源用户 ID</th>
                  <th>冻结原因</th>
                  <th>范围</th>
                  <th>资格状态</th>
                  <th>处理状态</th>
                  <th>发现时间</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {visibleItems.map((item) => (
                  <tr key={item.id}>
                    <td>
                      <SourceBadge source={item.source} />
                      <small>{item.sourceLabel}</small>
                    </td>
                    <td>
                      {item.externalUserId}
                      <AccountEmailLine email={item.accountEmail} />
                    </td>
                    <td>
                      <strong>{freezeReasonLabel(item.reason)}</strong>
                      <FreezeTriggerLine
                        objectType={item.triggerObjectType}
                        objectId={item.triggerObjectId}
                      />
                      {item.reason === "SOURCE_REFUND" && (
                        <small className="error-text">只能从退款与红冲队列结案</small>
                      )}
                    </td>
                    <td>{item.scope === "account" ? "整个平台账号" : "单笔资金批次"}</td>
                    <td>
                      <Badge tone={item.eligibilityStatus === "active" ? "green" : "amber"}>
                        {eligibilityStatusLabel(item.eligibilityStatus)}
                      </Badge>
                    </td>
                    <td>
                      <Badge tone={item.status === "open" ? "amber" : "green"}>
                        {item.status === "open" ? "待安全处理" : "已安全解除"}
                      </Badge>
                    </td>
                    <td>{dateTime(item.openedAt)}</td>
                    <td className="action-cell">
                      <button
                        className="button button-secondary button-small"
                        onClick={() => setSelected(item)}
                      >
                        {item.status === "open" ? "处理" : "查看"}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!visibleItems.length && (
              <EmptyState
                icon={<ShieldCheck />}
                title={
                  !showAllReasons && hiddenCount > 0
                    ? "没有需要人工复核的冻结记录"
                    : "当前筛选条件下没有冻结记录"
                }
                description={
                  !showAllReasons && hiddenCount > 0
                    ? `本页 ${hiddenCount} 条记录都在等待下一次对账自动解除；勾选“显示全部”可查看。`
                    : "没有待处理记录时无需进行任何人工操作。"
                }
              />
            )}
          </div>
        )}
      </section>
      {nextCursor && (
        <button
          className="button button-secondary load-more"
          disabled={loadingMore}
          onClick={() => void load(nextCursor)}
        >
          {loadingMore && <Loader2 className="spin" size={16} />}
          加载更多冻结记录
        </button>
      )}
      {selected && (
        <EligibilityFreezeDrawer
          item={selected}
          onClose={() => setSelected(null)}
          onResolved={(resolved) => {
            setItems((current) =>
              filters.status === "open"
                ? current.filter((item) => item.id !== resolved.id)
                : current.map((item) =>
                    item.id === resolved.id ? resolved : item,
                  ),
            );
            // CR-0007 problem two: show the now-resolved item's own
            // "already resolved" branch (below) as the inline success
            // confirmation, in addition to this toast, instead of nulling
            // selected immediately. It still auto-closes shortly after (the
            // queue has dozens of records to work through, one at a time --
            // requiring a manual close on every success would slow that
            // down) but only if the operator has not already navigated
            // elsewhere in the meantime.
            setSelected(resolved);
            toast("普通资格冻结已通过服务端安全门禁解除。", "success");
            window.setTimeout(() => {
              setSelected((current) =>
                current?.id === resolved.id ? null : current,
              );
            }, 1600);
          }}
        />
      )}
    </PortalLayout>
  );
}

function EligibilityFreezeDrawer({
  item,
  onClose,
  onResolved,
}: {
  item: EligibilityFreeze;
  onClose: () => void;
  onResolved: (resolved: EligibilityFreeze) => void;
}) {
  const toast = useContext(ToastContext);
  const [evidenceReference, setEvidenceReference] = useState("");
  const [note, setNote] = useState("");
  const [working, setWorking] = useState(false);
  // CR-0007 problem two: the embedded console can stretch this page's iframe
  // to up to 4000px while the browser viewport stays much shorter, so a
  // toast anchored to the iframe's own document corner can land outside the
  // visible area -- an operator sees no feedback and assumes the click did
  // nothing. This mirrors the same failure into the drawer's own visible
  // area (in addition to, not instead of, the toast).
  const [resolveError, setResolveError] = useState<{
    message: string;
    code: string;
  } | null>(null);

  const resolve = async () => {
    if (working || item.status !== "open" || item.reason === "SOURCE_REFUND")
      return;
    const evidence = evidenceReference.trim();
    const resolutionNote = note.trim();
    if (!evidence || !resolutionNote) {
      const message = "必须填写可追溯证据引用和处理说明。";
      setResolveError({ message, code: "EVIDENCE_REQUIRED" });
      toast(message, "error");
      return;
    }
    if (
      evidence.length > 2048 ||
      resolutionNote.length > 2000 ||
      /[\r\n\0]/.test(evidence) ||
      /\0/.test(resolutionNote) ||
      /^manual-ui:/i.test(evidence)
    ) {
      const message = "证据引用或处理说明包含不允许的内容。";
      setResolveError({ message, code: "EVIDENCE_INVALID" });
      toast(message, "error");
      return;
    }
    setWorking(true);
    setResolveError(null);
    try {
      const resolved = await invoiceApi.resolveEligibilityFreeze(item.id, {
        version: item.version,
        evidenceReference: evidence,
        note: resolutionNote,
      });
      setEvidenceReference("");
      setNote("");
      onResolved(resolved);
    } catch (error) {
      const message =
        error instanceof Error ? error.message : "资格冻结处理失败。";
      const code = error instanceof InvoiceApiError ? error.code : "UNKNOWN";
      setResolveError({ message, code });
      toast(message, "error");
    } finally {
      setWorking(false);
    }
  };

  return (
    <div className="drawer-layer" role="dialog" aria-modal="true">
      <button className="drawer-backdrop" aria-label="关闭" onClick={onClose} />
      <aside className="drawer eligibility-freeze-drawer">
        <div className="drawer-head">
          <div>
            <span>开票资格安全处置</span>
            <h2>{freezeReasonLabel(item.reason)}</h2>
          </div>
          <button className="icon-button" onClick={onClose} disabled={working}>
            <X size={19} />
          </button>
        </div>
        <div className="drawer-status">
          <SourceBadge source={item.source} />
          <Badge tone={item.status === "open" ? "amber" : "green"}>
            {item.status === "open" ? "待处理" : "已解除"}
          </Badge>
          <span>{dateTime(item.openedAt)}</span>
        </div>
        <div className="drawer-body">
          <section className="detail-section">
            <h3>安全边界</h3>
            <p className="freeze-explanation">
              只有最新最终化余额检查为“完全匹配”或“正差额已归类为非现金”，且没有待处理事件、死信、投影任务或退款暴露时，服务端才会接受本次解冻。
            </p>
            <dl className="key-values">
              <div>
                <dt>来源</dt>
                <dd>{item.sourceLabel}</dd>
              </div>
              <div>
                <dt>来源用户 ID</dt>
                <dd>
                  {item.externalUserId}
                  <AccountEmailLine email={item.accountEmail} />
                </dd>
              </div>
              <div>
                <dt>冻结范围</dt>
                <dd>{item.scope === "account" ? "整个平台账号" : "单笔资金批次"}</dd>
              </div>
              <div>
                <dt>资格状态</dt>
                <dd>{eligibilityStatusLabel(item.eligibilityStatus)}</dd>
              </div>
            </dl>
          </section>
          {item.reason === "SOURCE_REFUND" ? (
            <section className="detail-section freeze-refund-block">
              <CircleAlert size={20} />
              <div>
                <strong>退款冻结禁止从这里解除</strong>
                <p>请先完成退款、线下红冲和新的余额对账，再由退款工作流结案。</p>
                <Link className="button button-dark" to="/admin/refund-cases" onClick={onClose}>
                  前往退款与红冲队列
                </Link>
              </div>
            </section>
          ) : item.status === "open" ? (
            <section className="detail-section freeze-resolution-form">
              <h3>提交审计证据</h3>
              <label className="form-field">
                <span>证据引用</span>
                <input
                  value={evidenceReference}
                  maxLength={2048}
                  autoComplete="off"
                  placeholder="对账报告、工单或只读证据位置"
                  onChange={(event) => setEvidenceReference(event.target.value)}
                />
              </label>
              <label className="form-field">
                <span>处理说明</span>
                <textarea
                  value={note}
                  maxLength={2000}
                  rows={6}
                  placeholder="说明异常原因、核对结论与为什么现在满足安全解冻条件"
                  onChange={(event) => setNote(event.target.value)}
                />
              </label>
              <p className="freeze-form-note">
                证据和说明会在服务端加密保存；本页面不会读取或回显已保存内容。
              </p>
              {resolveError && (
                <div className="freeze-resolution-result freeze-resolution-result-error" role="alert">
                  <CircleAlert size={16} />
                  <div>
                    <span>{resolveError.message}</span>
                    <small>错误代码：{resolveError.code}</small>
                  </div>
                </div>
              )}
              <div className="modal-actions">
                <button className="button button-secondary" disabled={working} onClick={onClose}>
                  取消
                </button>
                <button className="button button-dark" disabled={working} onClick={() => void resolve()}>
                  {working && <Loader2 className="spin" size={16} />}
                  由服务端验证并解除
                </button>
              </div>
            </section>
          ) : (
            <section className="detail-section freeze-resolved-note">
              <CheckCircle2 size={20} />
              <div>
                <strong>该冻结记录已安全解除</strong>
                <p>
                  {item.resolvedAt
                    ? `解除时间：${dateTime(item.resolvedAt)}`
                    : "服务端未返回解除时间。"}
                </p>
                <span>出于安全边界，页面不展示证据内容、哈希或内部游标。</span>
              </div>
            </section>
          )}
        </div>
      </aside>
    </div>
  );
}

// CR-0009 (XM-INV-CR0009-LEDGER-VIEW): the four account block_state values
// -- read-only display labels/tones only, the backend computes the state
// itself (see docs/ELIGIBILITY-OPERATIONS.md's "管理员账本视图" section).
const accountBlockStateLabels: Record<AccountBlockState, string> = {
  frozen_manual_review: "冻结待人工复核",
  not_invoiceable_pending_reconciliation: "对账中暂不可开票",
  settling: "结算中",
  below_threshold: "未达起票门槛",
  invoiceable: "可开票",
};

function accountBlockStateTone(state: AccountBlockState) {
  switch (state) {
    case "frozen_manual_review":
      return "red";
    case "not_invoiceable_pending_reconciliation":
      return "amber";
    // Settling is a wait, not a warning: neutral, like below_threshold.
    case "settling":
      return "neutral";
    case "below_threshold":
      return "neutral";
    case "invoiceable":
      return "green";
  }
}

function AccountLedgerPage() {
  const toast = useContext(ToastContext);
  const [filters, setFilters] = useState<AccountLedgerFilters>({});
  // Same debounce idiom as EligibilityFreezesPage's own externalUserIdInput
  // above (CR-0007 problem one's precedent) -- this codebase's established
  // pattern for a free-text filter that hits the server.
  const [externalUserIdInput, setExternalUserIdInput] = useState("");
  useEffect(() => {
    const trimmed = externalUserIdInput.trim();
    const timer = window.setTimeout(() => {
      setFilters((current) =>
        current.externalUserId === (trimmed || undefined)
          ? current
          : { ...current, externalUserId: trimmed || undefined },
      );
    }, 400);
    return () => window.clearTimeout(timer);
  }, [externalUserIdInput]);
  const [items, setItems] = useState<AccountLedgerListItem[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [pageError, setPageError] = useState<string | null>(null);
  const [selectedAccountId, setSelectedAccountId] = useState<string | null>(null);
  const loadVersion = useRef(0);
  const platformSourceInstanceId = useEmbeddedAdminPlatformSourceInstanceId();

  // Platform-scoped embedded admin forces the filter to the resolved
  // platform, same as EligibilityFreezesPage's own identical effect (CR-0005
  // (c)) -- there is no visible source-instance selector on this page to
  // override it with (task brief item 4 does not ask for one), so scoping
  // here is entirely invisible/automatic.
  useEffect(() => {
    if (!embeddedAdminPlatform() || !platformSourceInstanceId) return;
    setFilters((current) =>
      current.sourceInstanceId === platformSourceInstanceId
        ? current
        : { ...current, sourceInstanceId: platformSourceInstanceId },
    );
  }, [platformSourceInstanceId]);

  const load = async (cursor?: string) => {
    if (embeddedAdminPlatform() && !platformSourceInstanceId) return;
    const version = cursor ? loadVersion.current : ++loadVersion.current;
    cursor ? setLoadingMore(true) : setLoading(true);
    try {
      const page = await invoiceApi.getAccountLedger(filters, cursor);
      if (version !== loadVersion.current) return;
      setItems((current) =>
        cursor
          ? [
              ...current,
              ...page.items.filter(
                (item) =>
                  !current.some(
                    (existing) => existing.externalAccountId === item.externalAccountId,
                  ),
              ),
            ]
          : page.items,
      );
      setNextCursor(page.nextCursor);
      setPageError(null);
    } catch (error) {
      if (version !== loadVersion.current) return;
      setPageError(error instanceof Error ? error.message : "用户账本读取失败。");
    } finally {
      if (version === loadVersion.current) {
        setLoading(false);
        setLoadingMore(false);
      }
    }
  };

  useEffect(() => {
    setItems([]);
    setNextCursor(undefined);
    setSelectedAccountId(null);
    void load();
  }, [filters.externalUserId, filters.sourceInstanceId, filters.sort]);

  return (
    <PortalLayout admin>
      <PageHeader
        eyebrow="ACCOUNT LEDGER"
        title="用户账本"
        description="按用户账号聚合查看 2026-09-01 00:00（开票策略起点，对所有账号一致）之后的充值、消耗与可开票金额，替代逐条冻结记录排查（CR-0009）。"
        action={
          <button
            className="button button-secondary"
            disabled={loading}
            onClick={() => void load()}
          >
            <RefreshCcw size={16} className={loading ? "spin" : ""} />
            刷新账本
          </button>
        }
      />
      <section className="card admin-table-card">
        <div className="toolbar">
          <div className="search-box">
            <Search size={17} />
            <input
              aria-label="来源用户 ID"
              value={externalUserIdInput}
              maxLength={512}
              autoComplete="off"
              placeholder="按来源用户 ID 精确过滤"
              onChange={(event) => setExternalUserIdInput(event.target.value)}
            />
          </div>
          <select
            aria-label="排序方式"
            value={filters.sort ?? "invoiceable_now_minor"}
            onChange={(event) =>
              setFilters((current) => ({
                ...current,
                sort:
                  event.target.value === "block_state" ? "block_state" : undefined,
              }))
            }
          >
            <option value="invoiceable_now_minor">按可开票金额排序</option>
            <option value="block_state">按状态排序（待处理优先）</option>
          </select>
          <Badge tone="blue">本页 {items.length} 条</Badge>
        </div>
        {pageError && (
          <div className="api-error-banner" role="alert">
            <CircleAlert size={18} />
            <span>{pageError}</span>
            <button className="button button-secondary" onClick={() => void load()}>
              重试
            </button>
          </div>
        )}
        {loading ? (
          <LoadingBlock />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>来源</th>
                  <th>来源用户 ID</th>
                  <th>起点后充值</th>
                  <th>起点后消耗</th>
                  <th>可开票</th>
                  <th>已开票</th>
                  <th>状态</th>
                  <th>最近检查点</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {items.map((item) => (
                  <tr key={item.externalAccountId}>
                    <td>
                      <SourceBadge source={item.source} />
                    </td>
                    <td>
                      {item.externalUserId}
                      <AccountEmailLine email={item.accountEmail} />
                    </td>
                    <td>
                      {money(item.rechargesSinceStartMinor)}
                      <small>{item.rechargesSinceStartCount} 笔</small>
                    </td>
                    <td>{money(item.consumedSinceStartMinor)}</td>
                    <td>
                      {money(item.invoiceableNowMinor)}
                      {!item.thresholdReached && (
                        <small className="error-text">未达门槛</small>
                      )}
                    </td>
                    <td>{money(item.issuedMinor)}</td>
                    <td>
                      <Badge tone={accountBlockStateTone(item.blockState)}>
                        {accountBlockStateLabels[item.blockState]}
                      </Badge>
                    </td>
                    <td>
                      {item.lastCheckpointAt
                        ? dateTimeShanghai(item.lastCheckpointAt)
                        : "尚无记录"}
                    </td>
                    <td className="action-cell">
                      <button
                        className="button button-secondary button-small"
                        onClick={() => setSelectedAccountId(item.externalAccountId)}
                      >
                        查看
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!items.length && (
              <EmptyState
                icon={<NotebookText />}
                title="当前筛选条件下没有账号"
                description="按来源用户 ID 精确过滤，或调整排序方式后重试。"
              />
            )}
          </div>
        )}
      </section>
      {nextCursor && (
        <button
          className="button button-secondary load-more"
          disabled={loadingMore}
          onClick={() => void load(nextCursor)}
        >
          {loadingMore && <Loader2 className="spin" size={16} />}
          加载更多账号
        </button>
      )}
      {selectedAccountId && (
        <AccountLedgerDetailDrawer
          externalAccountId={selectedAccountId}
          onClose={() => setSelectedAccountId(null)}
        />
      )}
    </PortalLayout>
  );
}

function AccountLedgerDetailDrawer({
  externalAccountId,
  onClose,
}: {
  externalAccountId: string;
  onClose: () => void;
}) {
  const [detail, setDetail] = useState<AccountLedgerDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError(null);
    setDetail(null);
    invoiceApi
      .getAccountLedgerDetail(externalAccountId)
      .then((result) => {
        if (active) setDetail(result);
      })
      .catch((caught) => {
        if (active)
          setError(caught instanceof Error ? caught.message : "账本详情读取失败。");
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [externalAccountId]);

  return (
    <div className="drawer-layer" role="dialog" aria-modal="true">
      <button className="drawer-backdrop" aria-label="关闭" onClick={onClose} />
      <aside className="drawer account-ledger-drawer">
        <div className="drawer-head">
          <div>
            <span>用户账本详情</span>
            <h2>{detail?.externalUserId ?? "加载中"}</h2>
          </div>
          <button className="icon-button" onClick={onClose}>
            <X size={19} />
          </button>
        </div>
        {loading ? (
          <LoadingBlock />
        ) : error ? (
          <div className="api-error-banner" role="alert">
            <CircleAlert size={18} />
            <span>{error}</span>
          </div>
        ) : detail ? (
          <>
            <div className="drawer-status">
              <SourceBadge source={detail.source} />
              <Badge tone={accountBlockStateTone(detail.blockState)}>
                {accountBlockStateLabels[detail.blockState]}
              </Badge>
              <span>策略起点 {dateTimeShanghai(detail.policyStartAt)}</span>
            </div>
            <div className="drawer-body">
              <section className="detail-section">
                <h3>账本概览</h3>
                <dl className="key-values">
                  <div>
                    <dt>起点后充值</dt>
                    <dd>
                      {money(detail.rechargesSinceStartMinor)}（
                      {detail.rechargesSinceStartCount} 笔）
                    </dd>
                  </div>
                  <div>
                    <dt>起点后消耗</dt>
                    <dd>{money(detail.consumedSinceStartMinor)}</dd>
                  </div>
                  <div>
                    <dt>可开票金额</dt>
                    <dd>
                      {money(detail.invoiceableNowMinor)}
                      {!detail.thresholdReached && "（未达起票门槛）"}
                    </dd>
                  </div>
                  <div>
                    <dt>已开票金额</dt>
                    <dd>{money(detail.issuedMinor)}</dd>
                  </div>
                  <div>
                    <dt>期初余额（非现金）</dt>
                    <dd>
                      {detail.openingBalance.serviceUnits}{" "}
                      {detail.openingBalance.unitCode}
                    </dd>
                  </div>
                  {/* 与期初余额放在一起，因为它们讲的是同一件事：系统从哪一刻起
                      为这个账号的余额背书。**它不是开票起点**——开票起点是全局
                      的、对所有账号一致的策略起点。两者以前被「起点后充值」那一
                      列悄悄混在一起，见 XM-INV-LEDGER-RECHARGE-POLICY-START。 */}
                  <div>
                    <dt>纳入采集时刻</dt>
                    <dd>
                      {detail.cutoverAt
                        ? dateTimeShanghai(detail.cutoverAt)
                        : "尚无记录"}
                    </dd>
                  </div>
                  <div>
                    <dt>最近检查点</dt>
                    <dd>
                      {detail.lastCheckpointAt
                        ? dateTimeShanghai(detail.lastCheckpointAt)
                        : "尚无记录"}
                    </dd>
                  </div>
                  <div>
                    <dt>最近对账通过</dt>
                    <dd>
                      {detail.lastReconciledAt
                        ? dateTimeShanghai(detail.lastReconciledAt)
                        : "尚无记录"}
                    </dd>
                  </div>
                </dl>
              </section>
              {detail.blockReason && (
                <section className="detail-section">
                  <h3>阻断原因</h3>
                  <p>{detail.blockReason}</p>
                </section>
              )}
              <section className="detail-section">
                <h3>起点后充值明细</h3>
                {detail.recharges.length ? (
                  detail.recharges.map((recharge) => (
                    <div className="allocation-row" key={recharge.fundingLotId}>
                      <div>
                        <strong>{dateTimeShanghai(recharge.completedAt)}</strong>
                        <span>
                          {recharge.eligibilityKind === "WALLET_CASH"
                            ? "钱包充值"
                            : "订阅充值"}
                          {recharge.refundFrozen ? " · 退款冻结中" : ""}
                        </span>
                      </div>
                      <span>{money(recharge.amountMinor)}</span>
                    </div>
                  ))
                ) : (
                  <p>尚无符合条件的充值记录。</p>
                )}
              </section>
              <section className="detail-section">
                <h3>消耗时间线（按天，Asia/Shanghai）</h3>
                {detail.consumptionTimeline.length ? (
                  detail.consumptionTimeline.map((day) => (
                    <div className="allocation-row" key={day.date}>
                      <div>
                        <strong>{day.date}</strong>
                      </div>
                      <span>{money(day.consumedMinor)}</span>
                    </div>
                  ))
                ) : (
                  <p>尚无消耗记录。</p>
                )}
              </section>
            </div>
          </>
        ) : null}
      </aside>
    </div>
  );
}

function PaymentCandidatesPage() {
  const toast = useContext(ToastContext);
  const [items, setItems] = useState<PaymentCandidate[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [pageError, setPageError] = useState<string | null>(null);
  const [selected, setSelected] = useState<PaymentCandidate | null>(null);
	const [action, setAction] = useState<"verify" | "reject" | "adjust">("verify");
  const [paidYuan, setPaidYuan] = useState("");
  const [evidenceReference, setEvidenceReference] = useState("");
  const [reason, setReason] = useState("");
  const [working, setWorking] = useState(false);
  const platformSourceInstanceId = useEmbeddedAdminPlatformSourceInstanceId();

  const load = async (cursor?: string) => {
    // Wait for the platform's source_instance_id rather than briefly
    // requesting an unscoped page (CR-0005 (c)). This page only ever renders
    // in NewAPI platform mode (see isAdminNavItemVisible), but the guard is
    // unconditional so it also holds for a direct/hostile navigation to this
    // route under Sub2API scope -- that resolves to a real (sub2api)
    // source_instance_id, which the newapi-only queue then correctly (not
    // an error) returns zero rows for.
    if (embeddedAdminPlatform() && !platformSourceInstanceId) return;
    cursor ? setLoadingMore(true) : setLoading(true);
    try {
      const page = await invoiceApi.getPaymentCandidates(
        cursor,
        platformSourceInstanceId ?? undefined,
      );
      setItems((current) =>
        cursor
          ? [
              ...current,
              ...page.items.filter(
                (candidate) =>
                  !current.some((existing) => existing.id === candidate.id),
              ),
            ]
          : page.items,
      );
      setNextCursor(page.nextCursor);
      setPageError(null);
    } catch (error) {
      setPageError(
        error instanceof Error ? error.message : "支付候选读取失败。",
      );
    } finally {
      setLoading(false);
      setLoadingMore(false);
    }
  };

  useEffect(() => {
    void load();
  }, [platformSourceInstanceId]);

  const openCandidate = (
    candidate: PaymentCandidate,
	nextAction: "verify" | "reject" | "adjust",
  ) => {
    setSelected(candidate);
    setAction(nextAction);
	setPaidYuan(
		nextAction === "verify"
			? (candidate.quotedMinor / 100).toFixed(2)
			: nextAction === "adjust"
				? "0.00"
				: "",
	);
    setEvidenceReference("");
    setReason("");
  };

  const closeCandidate = () => {
    if (working) return;
    setSelected(null);
    setPaidYuan("");
    setEvidenceReference("");
    setReason("");
  };

  const submit = async () => {
    if (!selected) return;
    const evidence = evidenceReference.trim();
    if (!evidence) {
      toast("必须填写可追溯的支付通道证据编号或凭证地址。", "error");
      return;
    }
    if (/^manual-ui:/i.test(evidence)) {
      toast("申请编号或 manual-ui 标记不能作为支付证据。", "error");
      return;
    }
    setWorking(true);
    try {
      if (action === "verify") {
        const normalizedAmount = paidYuan.trim();
        if (!/^\d+(?:\.\d{1,2})?$/.test(normalizedAmount)) {
          throw new Error("请输入人工核对后的实际到账金额。");
        }
        const paidMinor = Math.round(Number(normalizedAmount) * 100);
        if (!Number.isSafeInteger(paidMinor) || paidMinor <= 0)
          throw new Error("实际到账金额超出允许范围。");
		if (paidMinor > selected.quotedMinor)
			throw new Error("实际到账金额不能超过同步候选金额上限。");
		const verificationResult = await invoiceApi.verifyPayment(selected.id, {
          paidMinor,
          currency: "CNY",
          evidenceReference: evidence,
        });
		if (verificationResult === "proposed") {
			toast("第一人复核已记录；须由另一名管理员输入相同金额和证据后才能生效。");
			await load();
		} else {
			toast("双人复核完成，实际到账金额已写入安全账本。");
			setItems((current) =>
				current.filter((candidate) => candidate.id !== selected.id),
			);
		}
	  } else if (action === "reject") {
        if (!reason.trim()) throw new Error("拒绝支付候选时必须填写原因。");
        await invoiceApi.rejectPayment(selected.id, {
          reason: reason.trim(),
          evidenceReference: evidence,
        });
        toast("支付候选已拒绝并记录审计证据。");
		setItems((current) =>
			current.filter((candidate) => candidate.id !== selected.id),
		);
	  } else {
		if (!reason.trim()) throw new Error("人工退款或冻结必须填写原因。");
		const normalizedAmount = paidYuan.trim();
		if (!/^\d+(?:\.\d{1,2})?$/.test(normalizedAmount))
			throw new Error("请输入退款后剩余可开票金额。");
		const newCapMinor = Math.round(Number(normalizedAmount) * 100);
		if (!Number.isSafeInteger(newCapMinor) || newCapMinor < 0 || newCapMinor >= selected.currentCapMinor)
			throw new Error("退款后剩余金额必须小于当前已授权金额，冻结请输入 0.00。");
		await invoiceApi.applyManualPaymentCap(selected.id, {
			newCapMinor,
			evidenceReference: evidence,
			reason: reason.trim(),
		});
		toast("人工退款/冻结已生效；关联申请已自动处理，恢复净额须重新双人复核。");
		await load();
      }
      setSelected(null);
      setPaidYuan("");
      setEvidenceReference("");
      setReason("");
    } catch (error) {
      toast(error instanceof Error ? error.message : "支付核验操作失败。", "error");
    } finally {
      setWorking(false);
    }
  };

  return (
    <PortalLayout admin>
      <PageHeader
        eyebrow="PAYMENT EVIDENCE"
		title="New API 支付核验与退款管理"
		description="候选须双人复核；已授权记录可凭真实退款证据降低净额或完全冻结。"
        action={
          <button className="button button-secondary" onClick={() => void load()}>
            <RefreshCcw size={16} />
            刷新队列
          </button>
        }
      />
      {pageError && (
        <div className="api-error-banner" role="alert">
          <CircleAlert size={18} />
          <span>{pageError}</span>
          <button className="button button-secondary" onClick={() => void load()}>
            重试
          </button>
        </div>
      )}
      <section className="card admin-table-card">
        <div className="card-heading">
          <div>
			<h2>New API 资金记录</h2>
			<p>页面不会自动生成证据；核验、人工退款和冻结都必须引用真实通道凭证。</p>
          </div>
		  <Badge tone="amber">{items.length} 笔记录</Badge>
        </div>
        {loading ? (
          <LoadingBlock />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>候选订单</th>
                  <th>归属用户</th>
                  <th>候选金额</th>
                  <th>源状态</th>
                  <th>同步时间</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {items.map((candidate) => (
                  <tr key={candidate.id}>
                    <td>
                      <strong>{candidate.tradeNo}</strong>
                      <small>{candidate.sourceInstanceId}</small>
                    </td>
                    <td>
                      <strong>
                        {candidate.principalDisplayName || candidate.principalId}
                      </strong>
                      <small>{candidate.principalId}</small>
                    </td>
                    <td className="money-cell candidate-money">
                      <strong>{money(candidate.quotedMinor)}</strong>
					  <small>
						{candidate.verification === "verified"
						  ? `已授权 ${money(candidate.currentCapMinor)}`
						  : candidate.reviewStage === "proposed"
						  ? "已完成第一人复核，等待第二人"
						  : "尚未授权为可开票金额"}
					  </small>
                    </td>
                    <td>{candidate.sourceStatus}</td>
                    <td>
                      {dateTime(candidate.observedAt)}
                    </td>
                    <td>
                      <div className="inline-actions compact-actions">
						{candidate.verification === "verified" ? (
						  <button
							className="button button-small button-dark"
							onClick={() => openCandidate(candidate, "adjust")}
						  >
							人工退款 / 冻结
						  </button>
						) : (
						  <>
							<button
							  className="button button-small button-secondary"
							  onClick={() => openCandidate(candidate, "reject")}
							>
							  拒绝
							</button>
							<button
							  className="button button-small button-dark"
							  onClick={() => openCandidate(candidate, "verify")}
							>
							  {candidate.reviewStage === "proposed" ? "第二人复核" : "第一人复核"}
							</button>
						  </>
						)}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!items.length && (
              <EmptyState
                icon={<ShieldCheck />}
				title="没有 New API 资金记录"
				description="同步候选和已授权记录会出现在这里。"
              />
            )}
          </div>
        )}
        {nextCursor && (
          <button
            className="button button-secondary load-more"
            disabled={loadingMore}
            onClick={() => void load(nextCursor)}
          >
            {loadingMore && <Loader2 className="spin" size={16} />}
            加载更多
          </button>
        )}
      </section>
      {selected && (
        <div className="modal-layer" role="dialog" aria-modal="true">
          <button
            className="modal-backdrop"
            aria-label="关闭"
            disabled={working}
            onClick={closeCandidate}
          />
          <section className="modal-card payment-review-dialog">
            <div className="modal-header">
              <div>
				<span>
				  {action === "verify"
					? "确认真实到账"
					: action === "adjust"
						? "人工退款 / 冻结"
						: "拒绝候选"}
				</span>
                <h2>{selected.tradeNo}</h2>
              </div>
              <button
                className="icon-button"
                disabled={working}
                onClick={closeCandidate}
              >
                <X size={20} />
              </button>
            </div>
            <div className="candidate-reference">
              <span>同步候选金额</span>
              <strong>{money(selected.quotedMinor)}</strong>
              {selected.currentCapMinor !== selected.quotedMinor && (
				<span>当前已授权金额 {money(selected.currentCapMinor)}</span>
              )}
			  <small>
				{action === "adjust"
				  ? "录入退款后仍可开票的净额；输入 0.00 表示完全冻结。证据生效后恢复任何净额都必须重新双人复核。"
				  : `核验金额最高为 ${money(selected.quotedMinor)}；${
					  selected.reviewStage === "proposed"
						? "本单已完成第一人复核，第二人必须输入完全相同的金额和证据。"
						: "此数值仅供对照，不能代替支付通道核验。"
					}`}
			  </small>
            </div>
            {action === "verify" && (
              <Field
                label="实际到账金额（CNY）"
                value={paidYuan}
                onChange={setPaidYuan}
                placeholder="请从真实支付记录录入，例如 399.00"
                required
                maxLength={20}
              />
            )}
			{action === "adjust" && (
			  <Field
				label="退款后剩余可开票金额（CNY）"
				value={paidYuan}
				onChange={setPaidYuan}
				placeholder="完全冻结请输入 0.00"
				required
				maxLength={20}
			  />
			)}
            <Field
              label="支付证据编号 / 凭证引用"
              value={evidenceReference}
              onChange={setEvidenceReference}
              placeholder="支付通道流水号、内部凭证 ID 或受控凭证地址"
              required
              maxLength={2048}
            />
			{(action === "reject" || action === "adjust") && (
              <label className="form-field">
				<span>{action === "adjust" ? "退款 / 冻结原因 *" : "拒绝原因 *"}</span>
                <textarea
                  value={reason}
                  onChange={(event) => setReason(event.target.value)}
                  rows={3}
                  maxLength={500}
				  placeholder={
					action === "adjust"
					  ? "说明人工退款、拒付或冻结的业务原因"
					  : "说明金额、归属、状态或凭证存在的问题"
				  }
                />
              </label>
            )}
            <div className="modal-actions">
              <button
                className="button button-secondary"
                disabled={working}
                onClick={closeCandidate}
              >
                取消
              </button>
              <button
                className={
                  action === "verify"
                    ? "button button-primary"
                    : "button button-dark"
                }
                disabled={working}
                onClick={() => void submit()}
              >
                {working && <Loader2 className="spin" size={16} />}
				{action === "verify"
				  ? "确认核验通过"
				  : action === "adjust"
					  ? "确认退款 / 冻结"
					  : "确认拒绝"}
              </button>
            </div>
          </section>
        </div>
      )}
    </PortalLayout>
  );
}

const refundStatusMeta: Record<
  RefundCaseStatus,
  { label: string; tone: "amber" | "green" | "blue" }
> = {
  open: { label: "待处理", tone: "amber" },
  resolved_red_letter: { label: "已手工红冲", tone: "green" },
  resolved_no_action: { label: "无需处理", tone: "blue" },
};

function RefundCasesPage() {
  const toast = useContext(ToastContext);
  const [status, setStatus] = useState<RefundCaseStatus>("open");
  const [items, setItems] = useState<RefundCase[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [pageError, setPageError] = useState<string | null>(null);
  const [selected, setSelected] = useState<RefundCase | null>(null);
  const [resolutionStatus, setResolutionStatus] = useState<
    "resolved_red_letter" | "resolved_no_action"
  >("resolved_red_letter");
  const [evidenceReference, setEvidenceReference] = useState("");
  const [note, setNote] = useState("");
  const [working, setWorking] = useState(false);
  const loadVersion = useRef(0);
  const platformSourceInstanceId = useEmbeddedAdminPlatformSourceInstanceId();

  const load = async (cursor?: string) => {
    // Wait for the platform's source_instance_id rather than briefly
    // requesting an unscoped page (CR-0005 (c)).
    if (embeddedAdminPlatform() && !platformSourceInstanceId) return;
    const version = cursor ? loadVersion.current : ++loadVersion.current;
    cursor ? setLoadingMore(true) : setLoading(true);
    try {
      const page = await invoiceApi.getRefundCases(
        status,
        cursor,
        platformSourceInstanceId ?? undefined,
      );
      if (version !== loadVersion.current) return;
      setItems((current) =>
        cursor
          ? [
              ...current,
              ...page.items.filter(
                (refundCase) =>
                  !current.some((existing) => existing.id === refundCase.id),
              ),
            ]
          : page.items,
      );
      setNextCursor(page.nextCursor);
      setPageError(null);
    } catch (error) {
      if (version !== loadVersion.current) return;
      setPageError(
        error instanceof Error ? error.message : "退款案例读取失败。",
      );
    } finally {
      if (version === loadVersion.current) {
        setLoading(false);
        setLoadingMore(false);
      }
    }
  };

  useEffect(() => {
    setItems([]);
    setNextCursor(undefined);
    void load();
  }, [status, platformSourceInstanceId]);

  const openResolution = (refundCase: RefundCase) => {
    setSelected(refundCase);
    setResolutionStatus("resolved_red_letter");
    setEvidenceReference("");
    setNote("");
  };

  const closeResolution = () => {
    if (working) return;
    setSelected(null);
    setEvidenceReference("");
    setNote("");
  };

  const resolve = async () => {
    if (!selected || working) return;
    const evidence = evidenceReference.trim();
    const resolutionNote = note.trim();
    if (!evidence || !resolutionNote) {
      toast("处理退款案例必须填写证据引用和处理说明。", "error");
      return;
    }
    if (/^manual-ui:/i.test(evidence) || /[\r\n\0]/.test(evidence)) {
      toast("页面标记或包含控制字符的内容不能作为退款处理证据。", "error");
      return;
    }
    setWorking(true);
    try {
      await invoiceApi.resolveRefundCase(selected.id, {
        resolutionStatus,
        evidenceReference: evidence,
        note: resolutionNote,
      });
      setItems((current) =>
        current.filter((refundCase) => refundCase.id !== selected.id),
      );
      setSelected(null);
      setEvidenceReference("");
      setNote("");
      toast(
        resolutionStatus === "resolved_red_letter"
          ? "已记录手工红冲证据；系统未自动修改任何已开票金额。"
          : "已记录无需处理的核实结论；系统未自动修改任何已开票金额。",
      );
    } catch (error) {
      toast(error instanceof Error ? error.message : "退款案例处理失败。", "error");
    } finally {
      setWorking(false);
    }
  };

  return (
    <PortalLayout admin>
      <PageHeader
        eyebrow="REFUND RECONCILIATION"
        title="退款与红冲处理"
        description="核对退款对已开具发票形成的暴露，记录线下红冲或无需处理的审计证据。"
        action={
          <button className="button button-secondary" onClick={() => void load()}>
            <RefreshCcw size={16} />
            刷新案例
          </button>
        }
      />
      <div className="refund-guardrail" role="note">
        <CircleAlert size={19} />
        <div>
          <strong>这里不会自动红冲，也不会回写或减少历史已开票金额</strong>
          <span>
            管理员须先在线下税务系统完成红冲或确认无需处理，再将真实凭证与说明记录到本系统。
          </span>
        </div>
      </div>
      <section className="card admin-table-card">
        <div className="toolbar refund-toolbar">
          <div className="segmented refund-segmented">
            {(
              [
                ["open", "待处理"],
                ["resolved_red_letter", "已手工红冲"],
                ["resolved_no_action", "无需处理"],
              ] as const
            ).map(([value, label]) => (
              <button
                key={value}
                className={status === value ? "active" : ""}
                onClick={() => setStatus(value)}
              >
                {label}
              </button>
            ))}
          </div>
          <Badge tone={refundStatusMeta[status].tone}>
            本页 {items.length} 条
          </Badge>
        </div>
        {pageError && (
          <div className="api-error-banner refund-page-error" role="alert">
            <CircleAlert size={18} />
            <span>{pageError}</span>
            <button className="button button-secondary" onClick={() => void load()}>
              重试
            </button>
          </div>
        )}
        {loading ? (
          <LoadingBlock />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>案例 / 源版本</th>
                  <th>申请与充值记录</th>
                  <th>观察退款</th>
                  <th>已开票暴露</th>
                  <th>退款后上限</th>
                  <th>状态</th>
                  <th>发现时间</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {items.map((refundCase) => (
                  <tr key={refundCase.id}>
                    <td>
                      <strong>{refundCase.id}</strong>
                      <small>{refundCase.sourceRevision || "未提供源版本"}</small>
                    </td>
                    <td>
                      <strong>申请 {refundCase.requestId}</strong>
                      <small>充值记录 {refundCase.fundingLotId}</small>
                    </td>
                    <td className="money-cell">
                      {money(refundCase.observedRefundMinor)}
                    </td>
                    <td className="money-cell refund-exposure">
                      {money(refundCase.issuedExposureMinor)}
                    </td>
                    <td className="money-cell">
                      {money(refundCase.observedCapMinor)}
                    </td>
                    <td>
                      <Badge tone={refundStatusMeta[refundCase.status].tone}>
                        {refundStatusMeta[refundCase.status].label}
                      </Badge>
                      {refundCase.resolvedBy && (
                        <small>处理人 {refundCase.resolvedBy}</small>
                      )}
                    </td>
                    <td>{dateTime(refundCase.openedAt)}</td>
                    <td>
                      {refundCase.status === "open" && (
                        <button
                          className="button button-small button-dark"
                          onClick={() => openResolution(refundCase)}
                        >
                          记录处理结果
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!items.length && !pageError && (
              <EmptyState
                icon={<CircleAlert />}
                title={`没有${refundStatusMeta[status].label}的退款案例`}
                description="源平台退款同步后，相关案例会按状态进入这里。"
              />
            )}
          </div>
        )}
        {nextCursor && (
          <button
            className="button button-secondary load-more"
            disabled={loadingMore}
            onClick={() => void load(nextCursor)}
          >
            {loadingMore && <Loader2 className="spin" size={16} />}
            加载更多
          </button>
        )}
      </section>
      {selected && (
        <div className="modal-layer" role="dialog" aria-modal="true">
          <button
            className="modal-backdrop"
            aria-label="关闭"
            disabled={working}
            onClick={closeResolution}
          />
          <section className="modal-card refund-resolution-dialog">
            <div className="modal-header">
              <div>
                <span>记录退款处理结果</span>
                <h2>{selected.id}</h2>
              </div>
              <button
                className="icon-button"
                disabled={working}
                onClick={closeResolution}
              >
                <X size={20} />
              </button>
            </div>
            <div className="refund-amount-summary">
              <div>
                <span>观察退款金额</span>
                <strong>{money(selected.observedRefundMinor)}</strong>
              </div>
              <div>
                <span>已开票暴露金额</span>
                <strong>{money(selected.issuedExposureMinor)}</strong>
              </div>
            </div>
            <div className="resolution-options">
              <button
                className={
                  resolutionStatus === "resolved_red_letter" ? "active" : ""
                }
                onClick={() => setResolutionStatus("resolved_red_letter")}
              >
                <FileCheck2 size={18} />
                <span>
                  <strong>已在税务系统手工红冲</strong>
                  <small>已经取得可追溯的红冲凭证</small>
                </span>
              </button>
              <button
                className={
                  resolutionStatus === "resolved_no_action" ? "active" : ""
                }
                onClick={() => setResolutionStatus("resolved_no_action")}
              >
                <ShieldCheck size={18} />
                <span>
                  <strong>经核实无需处理</strong>
                  <small>需记录完整判断依据与证据</small>
                </span>
              </button>
            </div>
            <Field
              label="红冲或核实证据引用"
              value={evidenceReference}
              onChange={setEvidenceReference}
              placeholder="红字发票号码、受控凭证 ID 或财务流水引用"
              required
              maxLength={2048}
            />
            <label className="form-field refund-note-field">
              <span>
                处理说明<em>*</em>
              </span>
              <textarea
                value={note}
                onChange={(event) => setNote(event.target.value)}
                rows={4}
                maxLength={2000}
                placeholder="说明退款核对、红冲范围或无需处理的判断依据"
              />
            </label>
            <div className="refund-resolution-warning">
              <CircleAlert size={17} />
              <span>提交只写入审计结论，不会自动修改已开票金额。</span>
            </div>
            <div className="modal-actions">
              <button
                className="button button-secondary"
                disabled={working}
                onClick={closeResolution}
              >
                取消
              </button>
              <button
                className="button button-primary"
                disabled={working}
                onClick={() => void resolve()}
              >
                {working && <Loader2 className="spin" size={16} />}
                {resolutionStatus === "resolved_red_letter"
                  ? "确认已手工红冲"
                  : "确认无需处理"}
              </button>
            </div>
          </section>
        </div>
      )}
    </PortalLayout>
  );
}

function validNetworkEntry(value: string) {
  const trimmed = value.trim();
  const [address, rawPrefix, extra] = trimmed.split("/");
  if (!address || extra !== undefined) return false;
  const ipv4 = address.split(".");
  const isIPv4 =
    ipv4.length === 4 &&
    ipv4.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255);
  const isIPv6 = address.includes(":") && /^[0-9a-f:]+$/i.test(address);
  if (!isIPv4 && !isIPv6) return false;
  if (rawPrefix === undefined) return true;
  if (!/^\d+$/.test(rawPrefix)) return false;
  const prefix = Number(rawPrefix);
  if (prefix === 0) return false;
  return isIPv4 ? prefix <= 32 : prefix <= 128;
}

function SystemSettingsPage() {
  const toast = useContext(ToastContext);
  const [settings, setSettings] = useState<InvoiceSystemSettings | null>(null);
  const [pageError, setPageError] = useState<string | null>(null);
  const [saving, setSaving] = useState<string | null>(null);
  const [minimumYuan, setMinimumYuan] = useState("200.00");
  const [issuerName, setIssuerName] = useState("");
  const [smtp, setSMTP] = useState({
    fromAddress: "",
    fromName: "",
    host: "smtp.qq.com",
    port: "587",
    startTLS: true,
    authorizationCode: "",
    // 测试收件人（XM-INV-SMTP-TEST-RECIPIENT-SETTING）。**只进不出**：服务端
    // 只回遮蔽值，所以这个输入框每次加载都是空的，填了才覆盖，空着就保持不变
    // ——与授权码、企业微信地址同一条纪律。
    testRecipient: "",
  });
  // 企业微信通知地址（XM-INV-NOTICE-WEBHOOK-SETTING）。**只进不出**：保存后
  // 清空输入框，页面从不回读地址——与 SMTP 授权码同一条纪律。
  const [noticeWebhookURL, setNoticeWebhookURL] = useState("");
  const [cidrs, setCIDRs] = useState<string[]>([]);
  const [networkEntry, setNetworkEntry] = useState("");

  const load = async () => {
    setPageError(null);
    try {
      const loaded = await invoiceApi.getAdminSettings();
      setSettings(loaded);
      setMinimumYuan((loaded.minimumRequestMinor / 100).toFixed(2));
      setIssuerName(loaded.issuerName);
      setSMTP({
        fromAddress: loaded.smtp.fromAddress,
        fromName: loaded.smtp.fromName,
        host: loaded.smtp.host,
        port: String(loaded.smtp.port),
        startTLS: loaded.smtp.startTLS,
        authorizationCode: "",
        testRecipient: "",
      });
      setCIDRs(loaded.adminAccess.cidrs);
    } catch (error) {
      setPageError(
        error instanceof Error ? error.message : "系统设置暂时无法读取。",
      );
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const run = async (
    key: string,
    action: () => Promise<void>,
    message: string,
  ) => {
    setSaving(key);
    try {
      await action();
      await load();
      toast(message);
    } catch (error) {
      if (error instanceof InvoiceApiError && error.status === 409) {
        toast(
          "设置已被其他管理员更新，已为你刷新，请重新确认后保存。",
          "error",
        );
        await load();
      } else {
        toast(
          error instanceof Error ? error.message : "保存失败，请稍后重试。",
          "error",
        );
      }
    } finally {
      setSaving(null);
    }
  };

  const saveRules = () => {
    const minimumMinor = Math.round(Number(minimumYuan) * 100);
    // XM-INV-SETTABLE-INVOICE-MINIMUM: ¥200 is the default this field is
    // seeded with, not a floor. The only rejected value is a non-positive
    // one, matching the backend's own rule.
    if (!Number.isFinite(minimumMinor) || minimumMinor <= 0) {
      toast("最低开票金额必须大于 ¥0.00。", "error");
      return;
    }
    if (!issuerName.trim()) {
      toast("开票主体名称不能为空。", "error");
      return;
    }
    void run(
      "rules",
      () =>
        invoiceApi.saveInvoiceRules({
          revision: settings?.revision ?? 0,
          issuerName: issuerName.trim(),
          minimumRequestMinor: minimumMinor,
        }),
      "开票规则已保存。",
    );
  };

  const saveSMTP = (event: FormEvent) => {
    event.preventDefault();
    const port = Number(smtp.port);
    if (!smtp.fromAddress.includes("@") || !smtp.host || port !== 587) {
      toast("请检查发件地址和 SMTP Host；当前仅支持 587 + STARTTLS。", "error");
      return;
    }
    const testRecipient = smtp.testRecipient.trim();
    if (testRecipient !== "" && !testRecipient.includes("@")) {
      toast("测试收件邮箱不是一个有效地址。", "error");
      return;
    }
    if (
      testRecipient !== "" &&
      testRecipient.toLowerCase() === smtp.fromAddress.trim().toLowerCase()
    ) {
      toast("测试收件邮箱不能与发件地址相同。", "error");
      return;
    }
    void run(
      "smtp",
      () =>
        invoiceApi.saveSMTPSettings({
          ...smtp,
          revision: settings?.revision ?? 0,
          port,
          authorizationCode: smtp.authorizationCode || undefined,
          // 空着=保持库里现值不变。要清空得走「清除」而不是留空，否则每次
          // 保存 SMTP 主机都会顺手把收件人抹掉。
          testRecipient: testRecipient === "" ? undefined : testRecipient,
        }),
      "SMTP 配置已保存；授权码与测试收件邮箱不会再次显示。",
    );
    setSMTP((current) => ({
      ...current,
      authorizationCode: "",
      testRecipient: "",
    }));
  };

  const addNetwork = (value = networkEntry) => {
    const normalized = value.trim();
    if (!validNetworkEntry(normalized)) {
      toast("请输入有效的 IPv4、IPv6 或 CIDR。", "error");
      return;
    }
    if (!cidrs.includes(normalized))
      setCIDRs((current) => [...current, normalized]);
    setNetworkEntry("");
  };

  const saveNetworks = () => {
    if (!cidrs.length) {
      toast("至少保留一条管理员 IP/CIDR，避免锁死管理后台。", "error");
      return;
    }
    void run(
      "network",
      () =>
        invoiceApi.saveAdminAccess({
          revision: settings?.revision ?? 0,
          cidrs,
        }),
      "管理员访问范围已保存。",
    );
  };

  return (
    <PortalLayout admin>
      <PageHeader
        eyebrow="SECURE CONFIGURATION"
        title="系统设置"
        description="管理开票规则、独立邮件通道和管理员访问范围。敏感值只允许写入，不会从服务端回显。"
      />
      {pageError && (
        <div className="settings-error card">
          <CircleAlert size={20} />
          <div>
            <strong>设置接口暂不可用</strong>
            <span>{pageError}</span>
          </div>
          <button
            className="button button-secondary"
            onClick={() => void load()}
          >
            <RefreshCcw size={15} />
            重试
          </button>
        </div>
      )}
      {!settings && !pageError ? (
        <LoadingBlock />
      ) : (
        settings && (
          <div className="settings-stack">
            <section className="settings-card card">
              <div className="settings-card-head">
                <div className="settings-section-icon">
                  <ReceiptText size={19} />
                </div>
                <div>
                  <h2>开票配置</h2>
                  <p>
                    主体仅管理员可维护且不会展示给用户；开票项目固定为“技术服务”。
                  </p>
                </div>
                <Badge tone="green">V1 普票</Badge>
              </div>
              <div className="settings-grid">
                <Field
                  label="开票主体名称"
                  required
                  value={issuerName}
                  onChange={setIssuerName}
                  placeholder="请输入实际开票主体全称"
                />
                <label className="form-field">
                  <span>开票项目</span>
                  <input value={settings.serviceItem} readOnly disabled />
                  <small>固定为“技术服务”</small>
                </label>
                <label className="form-field">
                  <span>单次最低开票金额</span>
                  <div className="money-input">
                    <span>¥</span>
                    <input
                      type="number"
                      min="0.01"
                      step="0.01"
                      value={minimumYuan}
                      onChange={(event) => setMinimumYuan(event.target.value)}
                    />
                  </div>
                  <small>默认 ¥200.00，可按需调高或调低（须大于 ¥0.00）</small>
                </label>
                <label className="form-field">
                  <span>开票资格生效时间</span>
                  <input
                    value={`${eligibilityStartLabel(settings.eligibilityStartAt)}（北京时间）`}
                    readOnly
                    disabled
                  />
                  <small>
                    财务政策 v{settings.eligibilityPolicyVersion}：充值完成时间和消费时间都必须不早于该时点；上线后不可回拨。
                  </small>
                </label>
              </div>
              <div className="settings-actions">
                <button
                  className="button button-primary"
                  disabled={saving === "rules"}
                  onClick={saveRules}
                >
                  {saving === "rules" ? (
                    <Loader2 className="spin" size={16} />
                  ) : (
                    <Save size={16} />
                  )}
                  保存开票规则
                </button>
              </div>
            </section>

            <section className="settings-card card">
              <div className="settings-card-head">
                <div className="settings-section-icon violet">
                  <Mail size={19} />
                </div>
                <div>
                  <h2>QQ / Gmail SMTP 邮件通道</h2>
                  <p>
                    独立于 New API 与 Sub2API，仅发送发票通知和短期下载入口。
                  </p>
                </div>
                <Badge
                  tone={settings.smtp.credentialConfigured ? "green" : "amber"}
                >
                  {settings.smtp.credentialConfigured
                    ? "SMTP 凭据已配置"
                    : "待录入 SMTP 凭据"}
                </Badge>
              </div>
              <form onSubmit={saveSMTP}>
                <div className="settings-form-grid">
                  <Field
                    label="发件邮箱地址"
                    type="email"
                    required
                    value={smtp.fromAddress}
                    onChange={(value) =>
                      setSMTP((current) => ({ ...current, fromAddress: value }))
                    }
                    placeholder="invoice@qq.com"
                  />
                  <Field
                    label="发件人名称"
                    required
                    value={smtp.fromName}
                    onChange={(value) =>
                      setSMTP((current) => ({ ...current, fromName: value }))
                    }
                    placeholder="SoloV 开票中心"
                  />
                  <Field
                    label="SMTP Host"
                    required
                    value={smtp.host}
                    onChange={(value) =>
                      setSMTP((current) => ({ ...current, host: value }))
                    }
                    placeholder="smtp.qq.com"
                  />
                  <Field
                    label="SMTP Port"
                    required
                    type="number"
                    value={smtp.port}
                    onChange={() => undefined}
                    placeholder="587"
                    disabled
                  />
                  <label className="form-field field-wide">
                    <span>QQ 授权码 / Google App Password（仅写入）</span>
                    <div className="secret-input">
                      <KeyRound size={16} />
                      <input
                        type="password"
                        autoComplete="new-password"
                        value={smtp.authorizationCode}
                        onChange={(event) =>
                          setSMTP((current) => ({
                            ...current,
                            authorizationCode: event.target.value,
                          }))
                        }
                        placeholder={
                          settings.smtp.credentialConfigured
                            ? "留空表示继续使用已保存授权码"
                            : "首次配置时必须录入"
                        }
                      />
                    </div>
                    <small>
                      保存后立即清空；服务端只返回“已配置”状态，不返回授权码内容。
                    </small>
                  </label>
                  {/* 测试收件邮箱（XM-INV-SMTP-TEST-RECIPIENT-SETTING）。
                      产品负责人：「为什么这个接收测试邮箱我不能自己在后台设置？」
                      之前钉在服务器环境变量里，换地址要 SSH 上去改再重建容器。 */}
                  <label className="form-field field-wide">
                    <span>测试邮件收件邮箱</span>
                    <div className="secret-input">
                      <AtSign size={16} />
                      <input
                        type="email"
                        autoComplete="off"
                        value={smtp.testRecipient}
                        onChange={(event) =>
                          setSMTP((current) => ({
                            ...current,
                            testRecipient: event.target.value,
                          }))
                        }
                        placeholder={
                          settings.smtp.testRecipientMasked
                            ? `留空表示继续使用 ${settings.smtp.testRecipientMasked}`
                            : "尚未配置，填一个能收信的邮箱"
                        }
                      />
                    </div>
                    <small>
                      「发送测试邮件」只会发到这一个地址，不接受在发信时临时指定
                      收件人。必须与发件地址不同。
                      {settings.smtp.testRecipientManaged
                        ? "保存后立即清空；服务端只返回遮蔽形式。"
                        : "当前用的是服务器上的旧配置，在这里存一次之后就以后台为准。"}
                    </small>
                  </label>
                </div>
                <label className="switch-row smtp-switch">
                  <input
                    type="checkbox"
                    checked={smtp.startTLS}
                    disabled
                    readOnly
                  />
                  <span>
                    <strong>强制 STARTTLS</strong>
                    <small>
                      QQ SMTP 的 587 端口必须升级为加密通道，后台不允许关闭
                    </small>
                  </span>
                </label>
                <div className="settings-actions">
                  <button
                    className="button button-primary"
                    disabled={saving === "smtp"}
                  >
                    {saving === "smtp" ? (
                      <Loader2 className="spin" size={16} />
                    ) : (
                      <Save size={16} />
                    )}
                    保存 SMTP 配置
                  </button>
                </div>
              </form>
              <div className="test-mail-row">
                <div className="verified-recipient-note">
                  <AtSign size={17} />
                  <span>
                    {settings.smtp.testRecipientMasked
                      ? `测试邮件固定发送至 ${settings.smtp.testRecipientMasked}，不接受在发信时指定收件人。`
                      : "尚未配置测试收件邮箱，请在上方填写并保存后再发送。"}
                  </span>
                </div>
                <button
                  className="button button-secondary"
                  disabled={
                    saving === "smtp-test" ||
                    !settings.smtp.credentialConfigured ||
                    !settings.smtp.testRecipientMasked
                  }
                  onClick={() =>
                    void run(
                      "smtp-test",
                      () => invoiceApi.sendSMTPTest(),
                      `测试邮件已发送至 ${settings.smtp.testRecipientMasked}。`,
                    )
                  }
                >
                  {saving === "smtp-test" ? (
                    <Loader2 className="spin" size={16} />
                  ) : (
                    <Send size={16} />
                  )}
                  发送测试邮件
                </button>
              </div>
            </section>

            {/* 企业微信通知（XM-INV-NOTICE-WEBHOOK-SETTING）。
                产品负责人：「这个地址我希望的是在前端可以设置配置。如果写入
                服务器中，那不是想更换很麻烦？」——原方案放宿主机文件，换群要
                SSH 上去改再重启。搬到这里之后，改完下一条通知就用新地址。 */}
            <section className="settings-card card">
              <div className="settings-card-head">
                <div className="settings-section-icon">
                  <Send size={19} />
                </div>
                <div>
                  <h2>企业微信通知</h2>
                  <p>
                    用户提交开票申请时推一条消息到群机器人。整个 Webhook
                    地址就是凭据，保存后不会再显示。
                  </p>
                </div>
                <Badge tone={settings.noticeWebhook.configured ? "green" : "amber"}>
                  {settings.noticeWebhook.configured ? "已配置" : "未配置"}
                </Badge>
              </div>
              {settings.noticeWebhook.configured && (
                <div className="current-ip">
                  <ShieldCheck size={17} />
                  <div>
                    <span>当前地址指纹</span>
                    <strong>{settings.noticeWebhook.fingerprint}</strong>
                  </div>
                  <small>
                    {settings.noticeWebhook.updatedAt
                      ? `${dateTime(settings.noticeWebhook.updatedAt)} 由 ${settings.noticeWebhook.updatedBy} 更新`
                      : ""}
                  </small>
                </div>
              )}
              <form
                className="settings-form"
                onSubmit={(event) => {
                  event.preventDefault();
                  const value = noticeWebhookURL.trim();
                  if (!value) {
                    toast("请粘贴群机器人的 Webhook 地址。", "error");
                    return;
                  }
                  void run(
                    "notice-webhook",
                    () => invoiceApi.saveNoticeWebhook(value),
                    "通知地址已保存；地址不会再次显示，请用「发送测试」确认。",
                  );
                  setNoticeWebhookURL("");
                }}
              >
                <label className="field">
                  <span>群机器人 Webhook 地址</span>
                  <input
                    type="password"
                    autoComplete="off"
                    value={noticeWebhookURL}
                    placeholder={
                      settings.noticeWebhook.configured
                        ? "已配置；粘贴新地址可覆盖"
                        : "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."
                    }
                    onChange={(event) => setNoticeWebhookURL(event.target.value)}
                  />
                  <small>
                    地址里的 key 就是鉴权凭据，所以这里按密码处理：保存后页面
                    永不回读，只显示指纹供核对。
                  </small>
                </label>
                <div className="settings-actions">
                  <button
                    className="button button-primary"
                    disabled={saving === "notice-webhook"}
                  >
                    {saving === "notice-webhook" ? (
                      <Loader2 className="spin" size={16} />
                    ) : (
                      <Save size={16} />
                    )}
                    保存通知地址
                  </button>
                </div>
              </form>
              <div className="test-mail-row">
                <div className="verified-recipient-note">
                  <Send size={17} />
                  <span>
                    消息到没到那个群，比看一段前缀可靠——换了地址之后用它确认。
                  </span>
                </div>
                <button
                  className="button button-secondary"
                  disabled={
                    saving === "notice-webhook-test" ||
                    !settings.noticeWebhook.configured
                  }
                  onClick={() =>
                    void run(
                      "notice-webhook-test",
                      () => invoiceApi.sendNoticeWebhookTest(),
                      "测试消息已发出，请到群里确认。",
                    )
                  }
                >
                  {saving === "notice-webhook-test" ? (
                    <Loader2 className="spin" size={16} />
                  ) : (
                    <Send size={16} />
                  )}
                  发送测试消息
                </button>
                <button
                  className="button button-secondary"
                  disabled={
                    saving === "notice-webhook-clear" ||
                    !settings.noticeWebhook.configured
                  }
                  onClick={() =>
                    void run(
                      "notice-webhook-clear",
                      () => invoiceApi.clearNoticeWebhook(),
                      "通知地址已清除；在重新配置之前不会再推送。",
                    )
                  }
                >
                  {saving === "notice-webhook-clear" ? (
                    <Loader2 className="spin" size={16} />
                  ) : (
                    <X size={16} />
                  )}
                  清除
                </button>
              </div>
            </section>

            <section className="settings-card card">
              <div className="settings-card-head">
                <div className="settings-section-icon amber">
                  <Network size={19} />
                </div>
                <div>
                  <h2>管理员固定 IP / CIDR</h2>
                  <p>
                    管理后台应使用顶层页面，并限制为可信办公网络或 VPN 出口。
                  </p>
                </div>
                <Badge tone="blue">{cidrs.length} 条规则</Badge>
              </div>
              <div className="current-ip">
                <Server size={17} />
                <div>
                  <span>当前请求 IP</span>
                  <strong>{settings.adminAccess.currentIP}</strong>
                </div>
                <button
                  className="button button-small button-secondary"
                  onClick={() =>
                    addNetwork(
                      settings.adminAccess.currentIP.includes(":")
                        ? `${settings.adminAccess.currentIP}/128`
                        : `${settings.adminAccess.currentIP}/32`,
                    )
                  }
                >
                  加入白名单
                </button>
              </div>
              <div className="network-editor">
                <div className="network-add">
                  <input
                    value={networkEntry}
                    onChange={(event) => setNetworkEntry(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === "Enter") {
                        event.preventDefault();
                        addNetwork();
                      }
                    }}
                    placeholder="例如 203.0.113.8/32 或 2001:db8::/64"
                  />
                  <button
                    className="button button-secondary"
                    onClick={() => addNetwork()}
                  >
                    <Plus size={16} />
                    添加
                  </button>
                </div>
                <div className="network-list">
                  {cidrs.map((cidr) => (
                    <div key={cidr}>
                      <Network size={15} />
                      <code>{cidr}</code>
                      <button
                        className="icon-button"
                        onClick={() => {
                          if (cidrs.length <= 1) {
                            toast("至少保留一条管理员 IP/CIDR。", "error");
                            return;
                          }
                          setCIDRs((current) =>
                            current.filter((item) => item !== cidr),
                          );
                        }}
                        aria-label={`删除 ${cidr}`}
                      >
                        <X size={16} />
                      </button>
                    </div>
                  ))}
                </div>
              </div>
              <div className="network-warning">
                <CircleAlert size={17} />
                <span>
                  保存错误的网段可能导致管理员无法访问。建议先加入当前出口
                  IP，再移除旧规则。
                </span>
              </div>
              <div className="settings-actions">
                <button
                  className="button button-primary"
                  disabled={saving === "network" || !cidrs.length}
                  onClick={saveNetworks}
                >
                  {saving === "network" ? (
                    <Loader2 className="spin" size={16} />
                  ) : (
                    <Save size={16} />
                  )}
                  保存访问范围
                </button>
              </div>
            </section>
          </div>
        )
      )}
    </PortalLayout>
  );
}

function LoadingBlock() {
  return (
    <div className="loading-block">
      <Loader2 className="spin" size={24} />
      <span>正在读取安全账本</span>
    </div>
  );
}
function EmptyState({
  icon,
  title,
  description,
}: {
  icon: ReactNode;
  title: string;
  description: string;
}) {
  return (
    <div className="empty-state">
      <span>{icon}</span>
      <strong>{title}</strong>
      <p>{description}</p>
    </div>
  );
}

function LoginPage() {
  const { login, error, refresh, oidcAdminLoginEnabled } = useAuth();
  const location = useLocation();
  // XM-INV-HIDE-ADMIN-LOGIN: only the admin area's own login screen (direct
  // /admin navigation, or the console embed, which always lands on /admin --
  // see lib/portal-navigation.ts) still offers the administrator-OIDC entry
  // point below. Every user-facing path (/, /orders, /profiles, /records,
  // and the user embed's /?ui_mode=embedded&source=...) never shows it.
  const showAdminLoginEntry = isAdminAreaPath(location.pathname);
  const [identifier, setIdentifier] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [tempToken, setTempToken] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [showAdminLogin, setShowAdminLogin] = useState(false);

  async function handleCredentialsSubmit(event: FormEvent) {
    event.preventDefault();
    if (submitting || !identifier.trim() || !password) return;
    setSubmitting(true);
    setFormError(null);
    try {
      const outcome = await invoiceApi.platformLogin({
        identifier: identifier.trim(),
        password,
      });
      if (outcome.requiresTwoFA) {
        setTempToken(outcome.tempToken);
        setPassword("");
      } else {
        setPassword("");
        await refresh();
      }
    } catch (cause) {
      setFormError(
        cause instanceof InvoiceApiError ? cause.message : "登录失败，请重试。",
      );
    } finally {
      setSubmitting(false);
    }
  }

  async function handleTwoFASubmit(event: FormEvent) {
    event.preventDefault();
    if (submitting || !tempToken || !code.trim()) return;
    setSubmitting(true);
    setFormError(null);
    try {
      await invoiceApi.verifyPlatformLoginTwoFA({
        tempToken,
        code: code.trim(),
      });
      setCode("");
      await refresh();
    } catch (cause) {
      setFormError(
        cause instanceof InvoiceApiError ? cause.message : "验证码校验失败，请重试。",
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <main className="auth-shell">
      <section className="auth-card">
        <div className="brand-mark auth-brand">
          <ReceiptText size={24} />
        </div>
        <span className="eyebrow">SOLOV INVOICE</span>
        <h1>登录开票中心</h1>
        <p>使用你已有的账号密码登录，系统自动识别所属平台。</p>
        {(formError || error) && (
          <div className="auth-error" role="alert">
            <CircleAlert size={17} />
            {formError || error}
          </div>
        )}
        {tempToken ? (
          <form onSubmit={handleTwoFASubmit}>
            <label className="form-field">
              <span>验证码</span>
              <input
                value={code}
                onChange={(event) => setCode(event.target.value)}
                inputMode="numeric"
                autoComplete="one-time-code"
                autoFocus
                maxLength={32}
                placeholder="请输入两步验证码"
              />
            </label>
            <button
              type="submit"
              className="button button-primary button-wide"
              disabled={submitting || !code.trim()}
            >
              {submitting ? (
                <Loader2 className="spin" size={17} />
              ) : (
                <KeyRound size={17} />
              )}
              验证并登录
            </button>
            <button
              type="button"
              className="button button-dark button-wide"
              disabled={submitting}
              onClick={() => {
                setTempToken(null);
                setCode("");
              }}
            >
              返回重新输入密码
            </button>
          </form>
        ) : (
          <form onSubmit={handleCredentialsSubmit}>
            <label className="form-field">
              <span>账号</span>
              <input
                value={identifier}
                onChange={(event) => setIdentifier(event.target.value)}
                type="text"
                autoComplete="username"
                maxLength={320}
                placeholder="邮箱或用户名"
              />
              <small>填写你在 SoloV API 的邮箱，或 SoloV 模型平台的用户名</small>
            </label>
            <label className="form-field">
              <span>密码</span>
              <input
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                type="password"
                autoComplete="current-password"
                maxLength={1024}
              />
            </label>
            <button
              type="submit"
              className="button button-primary button-wide"
              disabled={submitting || !identifier.trim() || !password}
            >
              {submitting ? (
                <Loader2 className="spin" size={17} />
              ) : (
                <KeyRound size={17} />
              )}
              登录
            </button>
          </form>
        )}
        {/* CR-0006 (XM-INV-CONSOLE-ASSERT): once OIDC_ADMIN_LOGIN_ENABLED is
            turned off, this OIDC entry point is hidden entirely -- it would
            only lead to a 404 (the route is no longer registered; see
            production_auth.go's Register). In embedded-admin mode there is
            nothing else to show here: the console posts a signed assertion
            in automatically (AuthProvider's listener) with no button needed.
            Standalone /admin gets an explicit pointer to the console instead
            of silently offering nothing. */}
        {showAdminLoginEntry && oidcAdminLoginEnabled &&
          (showAdminLogin ? (
            <button className="button button-dark button-wide" onClick={login}>
              <ShieldCheck size={17} />
              使用统一身份账号登录
            </button>
          ) : (
            <button
              type="button"
              className="button button-ghost button-wide"
              onClick={() => setShowAdminLogin(true)}
            >
              管理员登录
            </button>
          ))}
        {showAdminLoginEntry && !oidcAdminLoginEnabled && !embeddedAdminMode && (
          <p>请通过星芒控制台登录后使用开票管理功能。</p>
        )}
      </section>
    </main>
  );
}

function SessionLoadingPage() {
  return (
    <main className="auth-shell">
      <section className="auth-card auth-loading">
        <Loader2 className="spin" size={28} />
        <strong>正在验证安全会话</strong>
      </section>
    </main>
  );
}

function StepUpPage() {
  const { stepUp, oidcAdminLoginEnabled } = useAuth();
  // XM-INV-ASSERT-STEPUP: when the console assertion is the admin login (the
  // Keycloak step-up route is gone), renewal needs no popup and no gesture --
  // it is a postMessage to the framing console, and the automatic handshake
  // in AuthProvider is already asking. Say so, and keep the button as the
  // manual path rather than the only one.
  const assertionRenewal = embeddedAdminMode && oidcAdminLoginEnabled === false;
  useEffect(() => {
    // Embedded admin mode opens step-up in a popup (see AuthProvider),
    // which browsers block unless it is triggered by a direct user gesture
    // -- auto-firing here would silently fail. Standalone/non-embedded keeps
    // the existing auto-trigger: `_top` navigation is never popup-blocked.
    if (!embeddedAdminMode) stepUp();
  }, [stepUp]);
  return (
    <main className="auth-shell">
      <section className="auth-card">
        <ShieldCheck className="auth-shield" size={34} />
        <span className="eyebrow">ADMIN STEP-UP</span>
        <h1>需要管理员二次验证</h1>
        <p>
          {assertionRenewal
            ? "此操作涉及支付证据或开票设置，正在向控制台申请新的登录凭证，稍候即可自动恢复。"
            : embeddedAdminMode
              ? "此操作涉及支付证据或开票设置，请点击下方按钮在弹出窗口中完成强化认证。"
              : "此操作涉及支付证据或开票设置，请在顶层页面完成强化认证后返回。"}
        </p>
        <button className="button button-dark button-wide" onClick={stepUp}>
          <KeyRound size={17} />
          {assertionRenewal
            ? "立即重新验证"
            : embeddedAdminMode
              ? "重新验证"
              : "继续管理员验证"}
        </button>
      </section>
    </main>
  );
}

// AccountEmailLine renders the account's verified email under its upstream
// numeric ID, in the two operator lists that identify accounts by that ID
// alone (用户账本 and 资格冻结队列) and in the freeze detail.
//
// Renders nothing when the account has no verified address on file. That is a
// real state -- an account provisioned through a login path that never
// verified one -- and a placeholder like "未知" would read as a failed lookup
// rather than an absent address. The ID above stays the identifier: it is
// what the filters and cursors use, and it is the only value guaranteed to
// exist (XM-INV-LEDGER-ACCOUNT-EMAIL).
function AccountEmailLine({ email }: { email?: string }) {
  if (!email) return null;
  return <small title={email}>{email}</small>;
}

const sourceReasonLabels: Record<string, string> = {
  SOURCE_DISABLED: "来源已停用",
  STREAM_NEVER_ACCEPTED: "尚未收到签名批次",
  STREAM_STALE: "心跳超时",
  RUNTIME_VERSION_UNAPPROVED: "运行版本未批准或漂移",
  PROJECTION_BLOCKED: "上游投影已安全阻断",
  EVENTS_PENDING: "仍有待处理事件",
  EVENTS_DEAD: "存在死信事件",
  SCAN_CYCLE_INCOMPLETE: "完整扫描周期尚未完成",
  ECONOMIC_WATERMARK_NEVER_PUBLISHED: "经济账本尚未发布首个完整水位",
  ECONOMIC_WATERMARK_STALE: "经济账本水位尚未追平",
  CONFIGURATION_DRIFT: "上游资金配置已漂移",
};

const sourceStreamLabels: Record<SourceStreamHealth["streamId"], string> = {
  payments: "支付",
  identities: "身份",
  usage: "消费",
  credits: "非现金额度",
  balances: "余额对账",
};

// EligibilityProjectionCard surfaces the eligibility-projection worker's own
// queue grading (XM-INV-PROJECTION-FAILURE-GRADING) next to the per-stream
// rows. The counters are global -- one queue serves every account on both
// platforms -- so the card says so rather than sitting inside the
// platform-filtered stream table above, where an embedded admin would read
// them as this platform's numbers.
//
// Dead is the one counter that is never normal: a job stops retrying after
// eight graded failures and then only an operator repair
// (invoice-eligibility-repair --kind=projection-requeue-dead) moves it.
function EligibilityProjectionCard({
  health,
  loading,
  loaded,
}: {
  health?: EligibilityProjectionHealth;
  loading: boolean;
  loaded: boolean;
}) {
  const healthy = health ? health.dead === 0 : undefined;
  return (
    <section className="card admin-table-card">
      <div className="card-heading split-heading">
        <div>
          <p className="eyebrow">Eligibility projection</p>
          <h2>资格投影队列</h2>
          <small>
            全局队列，两个平台的全部账号共用；死信需运维修复后才会继续。
          </small>
        </div>
        {health ? (
          <span className={`badge ${healthy ? "badge-green" : "badge-red"}`}>
            {healthy ? "无死信" : `死信 ${health.dead}`}
          </span>
        ) : null}
      </div>
      {loading ? (
        <LoadingBlock />
      ) : !health ? (
        <EmptyState
          icon={<Network />}
          title="服务未返回资格投影明细"
          description={
            loaded
              ? "同步状态接口未包含该字段，通常表示服务端版本早于该能力；请核对开票服务版本，暂不能据此判断队列是否积压。"
              : "尚未读取到同步状态。"
          }
        />
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>排队 / 处理中</th>
                <th>重试中</th>
                <th>死信</th>
                <th>等待余额证明</th>
                <th>最早未完成</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>
                  <strong>
                    {health.queued} / {health.processing}
                  </strong>
                  <small>排队等待认领 / 正在处理</small>
                </td>
                <td>
                  <strong>{health.retrying}</strong>
                  <small>按 30s 起指数退避重试</small>
                </td>
                <td>
                  <strong>{health.dead}</strong>
                  <small>
                    {health.dead === 0
                      ? "无需人工介入"
                      : "已停止重试，需运维修复"}
                  </small>
                </td>
                <td>
                  <strong>{health.proofPending}</strong>
                  <small>
                    {health.oldestProofPendingAt
                      ? `最早 ${dateTime(health.oldestProofPendingAt)}`
                      : "无等待中的证明"}
                  </small>
                </td>
                <td>
                  <strong>
                    {health.oldestPendingAt
                      ? dateTime(health.oldestPendingAt)
                      : "队列已排空"}
                  </strong>
                  <small>超过 15 分钟未完成才计入就绪门禁</small>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function SourceHealthPage() {
  const [report, setReport] = useState<SourceHealthReport | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [autoRefresh, setAutoRefresh] = useState(false);
  const loadVersion = useRef(0);
  // Platform-scoped embedded admin: CR-0005 (b) shows only the current
  // platform's 5 streams, vs. the full 10-stream overview everywhere else
  // (standalone, and the global embedded-admin scope). This is a purely
  // client-side view filter -- the report already carries both platforms'
  // rows in one cheap, already-fetched response; there is no server-side
  // filter to add for this endpoint.
  const platform = embeddedAdminPlatform();
  const visibleItems = platform
    ? (report?.items ?? []).filter((item) => item.sourceType === platform)
    : (report?.items ?? []);
  const visibleReady = platform
    ? visibleItems.length > 0 && visibleItems.every((item) => item.ready)
    : report?.ready;

  const load = async () => {
    const version = ++loadVersion.current;
    setLoading(true);
    setError(null);
    try {
      const next = await invoiceApi.getSourceHealth();
      if (version === loadVersion.current) setReport(next);
    } catch (caught) {
      if (version === loadVersion.current)
        setError(
          caught instanceof Error ? caught.message : "无法读取来源同步状态。",
        );
    } finally {
      if (version === loadVersion.current) setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  useEffect(() => {
    if (!autoRefresh) return;
    const timer = window.setInterval(() => void load(), 30_000);
    return () => window.clearInterval(timer);
  }, [autoRefresh]);

  return (
    <PortalLayout admin>
      <PageHeader
        eyebrow="Source integrity"
        title="来源同步状态"
        description="两套来源的支付、身份、消费、非现金额度和余额对账流必须完整、版本一致且投影健康，才允许提交和最终确认开票。"
        action={
          <div className="source-health-actions">
            <button
              className={`button ${autoRefresh ? "button-primary" : "button-secondary"}`}
              aria-pressed={autoRefresh}
              onClick={() => setAutoRefresh((current) => !current)}
            >
              <Clock3 size={16} />
              自动刷新 {autoRefresh ? "已开启" : "已关闭"}
            </button>
            <button
              className="button button-secondary"
              onClick={() => void load()}
              disabled={loading}
            >
              <RefreshCcw size={16} className={loading ? "spin" : ""} />
              立即刷新
            </button>
          </div>
        }
      />
      {error ? (
        <div className="api-error-banner" role="alert">
          <CircleAlert size={20} />
          <div><strong>同步状态读取失败</strong><span>{error}</span></div>
        </div>
      ) : null}
      {report?.degradedHTTP && (
        <div className="source-health-degraded" role="status">
          <CircleAlert size={18} />
          <div>
            <strong>健康接口以 HTTP 503 安全降级返回</strong>
            <span>仍展示服务端提供的非敏感诊断明细，但整体状态强制视为不可用。</span>
          </div>
        </div>
      )}
      <section className="card admin-table-card">
        <div className="card-heading split-heading">
          <div>
            <p className="eyebrow">上线门禁</p>
            <h2>
              {!report
                ? platform
                  ? "正在核验该平台数据流"
                  : "正在核验十条数据流"
                : visibleReady
                  ? platform
                    ? "该平台数据流均可用"
                    : "十条数据流均可用"
                  : "开票已安全停止"}
            </h2>
            {report && (
              <small>
                {platform
                  ? `已返回 ${visibleItems.length} 条流；预期该平台 5 条`
                  : `已返回 ${report.items.length} 条流；预期为 Sub2API/New API 各 5 条，共 10 条`}
              </small>
            )}
          </div>
          <span className={`badge ${visibleReady ? "badge-green" : "badge-red"}`}>
            {visibleReady ? "READY" : "NOT READY"}
          </span>
        </div>
        {loading && !report ? (
          <LoadingBlock />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>来源 / 数据流</th>
                  <th>心跳</th>
                  <th>版本 / 投影</th>
                  <th>事件队列</th>
                  <th>诊断</th>
                </tr>
              </thead>
              <tbody>
                {visibleItems.map((item) => (
                  <tr key={`${item.sourceInstanceId}:${item.streamId}`}>
                    <td>
                      <strong>{item.sourceName}</strong>
                      <small>
                        {sourceStreamLabels[item.streamId]} · seq {item.sequence}
                      </small>
                    </td>
                    <td>
                      <strong>{item.lastAcceptedAt ? dateTime(item.lastAcceptedAt) : "从未接收"}</strong>
                      <small>最大 {item.maximumAgeSeconds}s</small>
                      {item.streamId !== "identities" && (
                        <small>
                          账本水位 {item.economicWatermarkAt ? dateTime(item.economicWatermarkAt) : "尚未发布"}
                          {item.economicWatermarkMaximumAgeSeconds
                            ? ` · 最大 ${item.economicWatermarkMaximumAgeSeconds}s`
                            : ""}
                        </small>
                      )}
                    </td>
                    <td>
                      <strong>代理声明 {item.observedRuntimeVersion || "未观测"}</strong>
                      <small>
                        契约审计 {item.approvedRuntimeVersion || "未配置"} · 切换时{" "}
                        {item.cutoverRuntimeVersion || "未登记"} · 投影
                        {item.projectionStatus === "healthy"
                          ? "健康"
                          : item.projectionStatus === "blocked"
                            ? "阻断"
                            : "未知"}
                      </small>
                      <small>Agent {item.observedAgentVersion || "未观测"}</small>
                    </td>
                    <td>
                      <strong>
                        待处理 {item.pendingEvents} / 死信 {item.deadEvents}
                      </strong>
                      <small>依赖等待 {item.waitingDependencies}（不阻断其他用户）</small>
                    </td>
                    <td>
                      <span className={`badge ${item.ready ? "badge-green" : "badge-red"}`}>
                        {item.ready ? "正常" : "已阻断"}
                      </span>
                      {!item.ready ? (
                        <small>
                          {item.reasons
                            .map(
                              (reason) =>
                                sourceReasonLabels[reason] ??
                                "未识别的安全阻断原因",
                            )
                            .join("；") || "接口降级或完整性门禁未通过"}
                        </small>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!visibleItems.length && !loading && (
              <EmptyState
                icon={<Network />}
                title="尚无可安全展示的同步流"
                description="预期应配置 Sub2API 与 New API 的支付、身份四条独立流。"
              />
            )}
          </div>
        )}
      </section>
      <EligibilityProjectionCard
        health={report?.eligibilityProjection}
        loading={loading && !report}
        loaded={Boolean(report)}
      />
    </PortalLayout>
  );
}

function RequireAdmin({ children }: { children: ReactNode }) {
  const { user, stepUpRequired } = useAuth();
  if (stepUpRequired) return <StepUpPage />;
  if (user?.role !== "admin")
    return <Navigate to={userRoute("/orders")} replace />;
  return children;
}

function AuthenticatedApplication() {
  const { loading, authenticated } = useAuth();
  if (loading) return <SessionLoadingPage />;
  if (!authenticated) return <LoginPage />;
  return (
    <DataProvider>
      <Routes>
        <Route path="/" element={<Navigate to={userRoute("/orders")} replace />} />
        <Route path="/orders" element={<OrdersPage />} />
        <Route path="/profiles" element={<ProfilesPage />} />
        <Route path="/records" element={<RecordsPage />} />
        <Route
          path="/admin"
          element={
            <RequireAdmin>
              <AdminPage />
            </RequireAdmin>
          }
        />
        <Route
          path="/admin/payment-candidates"
          element={
            <RequireAdmin>
              <PaymentCandidatesPage />
            </RequireAdmin>
          }
        />
        <Route
          path="/admin/eligibility-freezes"
          element={
            <RequireAdmin>
              <EligibilityFreezesPage />
            </RequireAdmin>
          }
        />
        <Route
          path="/admin/accounts/ledger"
          element={
            <RequireAdmin>
              <AccountLedgerPage />
            </RequireAdmin>
          }
        />
        <Route
          path="/admin/refund-cases"
          element={
            <RequireAdmin>
              <RefundCasesPage />
            </RequireAdmin>
          }
        />
        <Route
          path="/admin/source-health"
          element={
            <RequireAdmin>
              <SourceHealthPage />
            </RequireAdmin>
          }
        />
        <Route
          path="/admin/settings"
          element={
            <RequireAdmin>
              <SystemSettingsPage />
            </RequireAdmin>
          }
        />
        <Route path="*" element={<Navigate to={userRoute("/orders")} replace />} />
      </Routes>
    </DataProvider>
  );
}

function App() {
  const [toast, setToast] = useState<ToastState>(null);
  const showToast = (
    message: string,
    tone: "success" | "error" = "success",
  ) => {
    setToast({ message, tone });
    window.setTimeout(() => setToast(null), 3200);
  };
  // Mounted at the root (not inside AuthenticatedApplication) so the console
  // can size the iframe correctly even before login -- the hook itself is a
  // no-op unless embedded-admin mode is on and the page is actually framed.
  useEmbeddedAdminHeightSync();
  return (
    <AuthProvider>
      <ToastContext.Provider value={showToast}>
        <AuthenticatedApplication />
        {/* CR-0007 problem two: .toast is position:fixed against this
            page's OWN document, not the browser viewport. The embedded
            console can stretch this admin iframe up to 4000px tall while
            the real viewport stays much shorter, so a toast anchored to
            the document's bottom-right corner can land below the visible
            area. Anchoring to the top instead keeps it inside the part of
            the iframe that is always scrolled into view on load. */}
        {toast && (
          <div
            className={`toast toast-${toast.tone}${embeddedAdminMode ? " toast-anchor-top" : ""}`}
          >
            {toast.tone === "success" ? (
              <CheckCircle2 size={18} />
            ) : (
              <CircleAlert size={18} />
            )}
            <span>{toast.message}</span>
          </div>
        )}
      </ToastContext.Provider>
    </AuthProvider>
  );
}

export default App;
