import { useCallback, useEffect, useRef, useState } from "react";
import { cx } from "@xingmang/ui-primitives";
import { PageState } from "./PageState";

/** 三个已批准的嵌入入口（CR-0005 平台线 h）。字面量联合而不是 `string`：
 *  一个拼错的路径不该被 TypeScript 放行，那正是 postMessage 白名单之外
 *  唯一还需要人读代码才能发现的错误来源。 */
export type EmbeddedConsolePath =
  | "/embed/admin/sub2api"
  | "/embed/admin/newapi"
  | "/embed/admin/global";

export interface EmbeddedConsoleFrameProps {
  /** 嵌入来源，不含路径（如 `https://invoice.solov.cc`）。调用方负责校验
   *  这是一个干净的 https 来源——本组件只按字符串拼接，不再校验一遍。 */
  origin: string;
  path: EmbeddedConsolePath;
  /** iframe 的可访问标题，也是加载失败卡片的标题。 */
  title: string;
  /** CR-0006 XM-INVCON1：待投递的断言载荷。每次拿到新断言就传一个新对象
   *  （对象恒等性变化即触发重新投递，调用方不需要自己判断"是不是同一个
   *  值"）；为 `null`/`undefined` 时不投递任何消息。iframe 尚未 `load` 完成
   *  时先记住这份负载，`onLoad` 触发的那一刻立即补发一次——跨源 iframe 在
   *  自己的脚本跑起来之前收不到任何 postMessage，早发的一次很可能丢在
   *  导航完成之前，这里用"记住 + onLoad 补发"兜底，不依赖时序恰好对齐。 */
  assertion?: { assertion: string } | null;
  /** 收到 iframe 侧 `kind:"admin-assertion-needed"` 消息时回调——开票前端
   *  判断自己手头的断言已经用不了（未曾收到过、已过期、或兑换失败）时会
   *  主动发这条消息；调用方应据此重新签发一份新断言并再次传入 `assertion`
   *  prop。仅在传了这个回调时才认这个 kind，省得每个不参与断言登录的嵌入
   *  场景都要处理一条永远不会来的消息。 */
  onAssertionNeeded?: () => void;
}

/** postMessage 高度同步的版本化消息（CR-0005 平台线 h：「仅接受
 *  invoiceConsoleOrigin 的 postMessage」+「沿用用户嵌入的版本化消息规范」）。
 *  `version` 恒为 1：以后要改形状就发 version:2，这里继续认 1，不做隐式升级——
 *  隐式兼容旧版消息形状，是这类跨来源契约最容易悄悄漂移的地方。 */
interface EmbedHeightMessage {
  type: "xm-embed";
  version: 1;
  kind: "height";
  height: number;
}

function isEmbedHeightMessage(data: unknown): data is EmbedHeightMessage {
  if (typeof data !== "object" || data === null) return false;
  const value = data as Record<string, unknown>;
  return (
    value.type === "xm-embed" &&
    value.version === 1 &&
    value.kind === "height" &&
    typeof value.height === "number" &&
    Number.isFinite(value.height)
  );
}

/** 断言过期/兑换失败时，iframe 主动要求重新签发（CR-0006 XM-INVCON1，与
 *  开票线在同一份技术规格下并行实现）。与高度消息同一个信封形状，只是
 *  `kind` 不同——沿用 XM-INVCON0 已建立的版本化信封，不需要升版本。 */
interface EmbedAdminAssertionNeededMessage {
  type: "xm-embed";
  version: 1;
  kind: "admin-assertion-needed";
}

function isAdminAssertionNeededMessage(data: unknown): data is EmbedAdminAssertionNeededMessage {
  if (typeof data !== "object" || data === null) return false;
  const value = data as Record<string, unknown>;
  return value.type === "xm-embed" && value.version === 1 && value.kind === "admin-assertion-needed";
}

/** 控制台 → iframe 的断言投递消息（CR-0006 正文 d 条："沿用 XM-INVCON0
 *  已建立的 xm-embed 版本化信封，新增一个 kind 值即可"）。方向与高度同步/
 *  重新签发请求相反：这是唯一一条由控制台主动发起的消息。 */
interface EmbedAdminAssertionMessage {
  type: "xm-embed";
  version: 1;
  kind: "admin-assertion";
  assertion: string;
}

/** 高度钳制范围。下限保证一屏管理界面不会被压成一条缝；上限防止一条形状对、
 *  数值离谱的消息（无论是嵌入应用的 bug 还是别的什么）把页面撑成没法用的长条。
 *  管理端页面本身不算矮，480 是留够列表 + 操作条的下限，不是随手挑的整数。 */
const MIN_HEIGHT_PX = 480;
const MAX_HEIGHT_PX = 4000;

/** 还没收到任何高度消息之前的兜底：按视口高度给一个够用的初始尺寸，而不是
 *  一个固定像素数——固定值在小屏与大屏上一个太空、一个太挤。真实高度以
 *  postMessage 为准，这只是「消息到达前」的过渡态。 */
const VIEWPORT_FALLBACK_CLASS = "h-[70vh]";

/** 判定「加载失败」的超时时长。跨来源 iframe 的 `onload` 在内容确实起不来时
 *  (证书错误、`frame-ancestors` 拒绝承载等) 不一定会触发 `onerror`——很多这类
 *  失败在浏览器里就是永远不触发 load 也不触发 error。超时是唯一兜底。 */
const LOAD_TIMEOUT_MS = 15_000;

function clampHeight(height: number): number {
  return Math.min(MAX_HEIGHT_PX, Math.max(MIN_HEIGHT_PX, Math.round(height)));
}

/** 嵌入开票控制台管理端的 iframe 壳（CR-0005 平台线 h）。
 *
 *  这里只做承载与消息过滤，不认识开票、不认识金额（k：平台侧不展示任何开票
 *  数字，这个组件从不读取也不显示 iframe 内部的任何内容）。鉴权、审批、
 *  双人复核与审计全部在 iframe 里的开票系统里发生（CR-0005「明确不变」）。
 *
 *  不加 `sandbox`：开票系统的登录走弹出的顶层窗口 + iframe 内的同站
 *  Cookie（CR-0005 开票线 d），`sandbox` 会同时挡掉弹窗与 Cookie，
 *  这个功能就直接不能用了——这不是遗漏，是这次嵌入唯一不能加它的理由。 */
export function EmbeddedConsoleFrame({
  origin,
  path,
  title,
  assertion = null,
  onAssertionNeeded,
}: EmbeddedConsoleFrameProps) {
  const [height, setHeight] = useState<number | undefined>(undefined);
  const [failed, setFailed] = useState(false);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const src = `${origin}${path}`;

  const markFailed = useCallback(() => {
    clearTimeout(timeoutRef.current);
    setFailed(true);
  }, []);

  // 投递断言：iframe 已挂载且拿到了非空断言就发；同一个 useEffect 兜住两条
  // 触发路径（assertion 变化、iframe 重新 onLoad），不必分别在两处各写一份
  // postMessage 调用。targetOrigin 精确等于 origin——与高度消息的入站校验
  // 对称，跨源投递永远不放宽成通配符。
  const deliverAssertion = useCallback(() => {
    const node = frameRef.current;
    if (!node?.contentWindow || !assertion) return;
    const message: EmbedAdminAssertionMessage = {
      type: "xm-embed",
      version: 1,
      kind: "admin-assertion",
      assertion: assertion.assertion,
    };
    node.contentWindow.postMessage(message, origin);
  }, [assertion, origin]);

  useEffect(() => {
    deliverAssertion();
  }, [deliverAssertion]);

  // React 的合成事件系统不给 <iframe> 接 "error"：react-dom 按标签分发原生
  // 监听时只对 iframe/object/embed 接了 "load"，"error" 只接给
  // img/link/source/embed（标签白名单，iframe 缺席是上游没做，不是漏配——
  // 传 onError prop 不会报错，但永远不会被调用，在任何浏览器里都一样)。
  // 用回调 ref 绕开合成事件系统，直接在真实 DOM 节点上挂原生监听：回调 ref
  // 由 React 在 commit 阶段随节点挂载/卸载调用，不依赖某个 useEffect 恰好在
  // 同一轮里重新执行，天然跟手这个节点自己的挂载/卸载时机（含 failed 状态
  // 翻转导致的重新挂载）。 React 19 的 ref 回调支持返回清理函数，语义与
  // useEffect 的清理一致。
  const attachErrorListener = useCallback(
    (node: HTMLIFrameElement | null) => {
      frameRef.current = node;
      if (!node) return;
      node.addEventListener("error", markFailed);
      return () => node.removeEventListener("error", markFailed);
    },
    [markFailed],
  );

  // 只信配置里的来源：其余一律忽略，不做「看起来像」的宽松匹配
  useEffect(() => {
    function handleMessage(event: MessageEvent) {
      if (event.origin !== origin) return;
      if (isEmbedHeightMessage(event.data)) {
        setHeight(clampHeight(event.data.height));
        return;
      }
      if (onAssertionNeeded && isAdminAssertionNeededMessage(event.data)) {
        onAssertionNeeded();
      }
    }
    window.addEventListener("message", handleMessage);
    return () => window.removeEventListener("message", handleMessage);
  }, [origin, onAssertionNeeded]);

  // src 变了（切平台页签、换配置）就重新走一遍加载判定；上一轮的定时器不能
  // 带到这一轮，否则旧请求的超时会把新请求判成失败
  useEffect(() => {
    setFailed(false);
    setHeight(undefined);
    timeoutRef.current = setTimeout(markFailed, LOAD_TIMEOUT_MS);
    return () => clearTimeout(timeoutRef.current);
  }, [src, markFailed]);

  function handleLoad() {
    clearTimeout(timeoutRef.current);
    // iframe 刚完成一次（新）导航：这一刻它自己的脚本才第一次有机会接收
    // postMessage，之前若已经拿到断言但那次投递发生在导航完成之前，iframe
    // 永远不会收到——这里补发一次，弥补"src 没变但内容重新加载"（如
    // iframe 内部自己刷新）与"首次加载"两种场景。
    deliverAssertion();
  }

  if (failed) {
    return (
      <PageState
        kind="error"
        title={title}
        message="嵌入的控制台加载失败，可能是网络问题或来源暂不可达。"
        action={
          <a
            href={src}
            target="_blank"
            rel="noopener noreferrer"
            className="text-xs font-medium text-accent hover:underline"
          >
            在新窗口打开
          </a>
        }
      />
    );
  }

  return (
    <iframe
      ref={attachErrorListener}
      title={title}
      src={src}
      allow="clipboard-write"
      referrerPolicy="strict-origin"
      onLoad={handleLoad}
      className={cx(
        "w-full rounded-lg border border-edge bg-surface",
        height === undefined && VIEWPORT_FALLBACK_CLASS,
      )}
      style={height === undefined ? undefined : { height: `${height}px` }}
    />
  );
}
