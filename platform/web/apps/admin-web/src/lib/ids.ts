/** 请求 ID 生成。
 *
 *  写路径每次执行都要带一个 request_id：报障时它是把界面上的一次点击、
 *  服务端日志和审计事件串起来的唯一线索（规格 §5.8）。 */

/** 后端 `safeRequestID` 接受的字符集（httpapi/middleware.go）：
 *  `^[A-Za-z0-9._:-]{1,128}$`。不匹配的值会被服务端**静默换掉**，
 *  于是界面上显示的 ID 和日志里的对不上——所以兜底方案也必须落在这个集合里。 */
const SAFE_REQUEST_ID = /^[A-Za-z0-9._:-]{1,128}$/;

function randomHex(bytes: number): string | null {
  const c: Crypto | undefined = globalThis.crypto;
  if (!c?.getRandomValues) return null;
  const buf = new Uint8Array(bytes);
  c.getRandomValues(buf);
  return [...buf].map((b) => b.toString(16).padStart(2, "0")).join("");
}

/** 生成一个请求 ID。
 *
 *  首选 crypto.randomUUID；老浏览器（或没实现它的测试环境）退到
 *  getRandomValues 拼十六进制。两条路都不通时才用时间戳兜底——那已经不是
 *  「唯一」而只是「大概率不撞」，但比不带 ID 强：至少还能定位到分钟。 */
export function newRequestId(): string {
  const c: Crypto | undefined = globalThis.crypto;
  if (typeof c?.randomUUID === "function") return c.randomUUID();
  const hex = randomHex(16);
  if (hex) return `req-${hex}`;
  return `req-${Date.now().toString(36)}`;
}

/** 该请求 ID 能否被后端原样接受。给测试与调试用。 */
export function isSafeRequestId(id: string): boolean {
  return SAFE_REQUEST_ID.test(id);
}
