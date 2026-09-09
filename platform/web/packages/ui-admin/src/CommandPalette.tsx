import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";
import { cx } from "@xingmang/ui-primitives";

/** 一条可跳转的搜索结果。
 *
 *  只描述**去哪**，不描述**做什么**——ADMIN-IA §七 红线：全局搜索只导航,
 *  不执行写命令。禁止在这里放退款、审批、重试、禁用、发布之类的条目。 */
export interface CommandItem {
  /** 稳定 id。option 的 DOM id 由它拼出来，`aria-activedescendant` 要指向它,
   *  所以不能用数组下标——过滤一次下标就全变了，读屏会念错当前项。 */
  id: string;
  /** 左列的类别标签(原型 `xm-search-type`)：平台 / 页面 / 页签…… */
  type: string;
  title: string;
  /** 副标题：这一条在哪、属于谁。 */
  meta: string;
  /** 额外的匹配词（英文别名、路径），不显示，只参与匹配。 */
  keywords?: string;
}

/** 匹配规则，与原型 `xmRunGlobalSearch` 逐字一致:
 *  `id + title + meta + keywords` 拼起来做子串匹配，大小写不敏感。
 *
 *  抽成纯函数是为了能直接断言规则本身。做成模糊匹配或分词很诱人，但那会让
 *  「为什么这条没出来」变得没法解释——一个搜不到的导航比没有搜索更让人不信任。 */
export function filterCommandItems<T extends CommandItem>(
  items: readonly T[],
  query: string,
): T[] {
  const q = query.trim().toLowerCase();
  if (!q) return [...items];
  return items.filter((item) =>
    `${item.id} ${item.title} ${item.meta} ${item.keywords ?? ""}`.toLowerCase().includes(q),
  );
}

/** 这次按键是不是「在输入框里打字」。
 *
 *  Ctrl/Cmd+K 是全局快捷键，但人在搜索框、表单、可编辑区里打字时不能被劫持——
 *  原型的全局监听里就带着这条判断。 */
function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return target.matches("input, textarea, select, [contenteditable=true]");
}

/** 全局 Ctrl/Cmd + K。
 *
 *  监听挂在 document 上而不是某个组件上：面板关着的时候它自己不在 DOM 里,
 *  没法接管快捷键。`enabled` 为 false（面板已打开）时不再响应,
 *  否则在面板里按 Ctrl+K 会「再打开一次」并把输入清空。 */
export function useCommandPaletteHotkey(onOpen: () => void, enabled = true): void {
  useEffect(() => {
    if (!enabled) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== "k") return;
      if (isEditableTarget(event.target)) return;
      event.preventDefault();
      onOpen();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [onOpen, enabled]);
}

export interface CommandPaletteProps<T extends CommandItem = CommandItem> {
  open: boolean;
  onClose: () => void;
  items: readonly T[];
  /** 选中一条。调用方负责跳转——包本身不依赖 router。
   *  泛型是为了让调用方把自己的字段（例如目标路由）带在条目上原样收回来，
   *  不必再按 id 反查一遍。 */
  onSelect: (item: T) => void;
  /** 输入框的提示语。**必须诚实**：这一版只搜得到导航，就不能写「搜索用户、订单」。 */
  placeholder: string;
  /** 结果区之上的一句话范围说明（能搜什么、不能搜什么）。 */
  scopeNote?: ReactNode;
  emptyLabel?: string;
}

/** 全局搜索面板（Command Palette）。
 *
 *  形态与键盘契约照原型的 `xmGlobalSearchShell`:
 *  ↑↓ 移动、Enter 打开、Esc 关闭、Tab 在面板内循环、点遮罩关闭、
 *  关闭后焦点回到触发它的那个按钮。
 *
 *  用 `aria-activedescendant` 而不是把焦点真的移到选项上：焦点留在输入框里,
 *  人才能一边看结果一边继续打字。这也是原型的做法。
 *
 *  包里不依赖 router：壳与面板都只管「选了哪一条」，跳转是应用的事。 */
export function CommandPalette<T extends CommandItem>({
  open,
  onClose,
  items,
  onSelect,
  placeholder,
  scopeNote,
  emptyLabel = "没有匹配结果",
}: CommandPaletteProps<T>) {
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const optionPrefix = useId();

  const matches = filterCommandItems(items, query);
  // 过滤之后结果可能变少，活动项要跟着收回来，否则 aria-activedescendant
  // 会指向一个已经不存在的 option id
  const active = Math.min(activeIndex, Math.max(matches.length - 1, 0));
  const activeItem = matches[active];

  // 每次打开都从干净状态开始：上一次搜的词留在框里，人会以为这就是全部结果
  useEffect(() => {
    if (!open) return;
    setQuery("");
    setActiveIndex(0);
    inputRef.current?.focus();
  }, [open]);

  const select = useCallback(
    (item: T) => {
      onSelect(item);
      onClose();
    },
    [onSelect, onClose],
  );

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      onClose();
      return;
    }
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const delta = event.key === "ArrowDown" ? 1 : -1;
      setActiveIndex(Math.max(0, Math.min(matches.length - 1, active + delta)));
      return;
    }
    if (event.key === "Enter" && activeItem) {
      event.preventDefault();
      select(activeItem);
      return;
    }
    if (event.key === "Tab") {
      // 焦点困在面板里：模态对话框开着的时候 Tab 跑到背后的页面上,
      // 键盘用户会「掉出去」且没有任何提示
      const focusable = dialogRef.current?.querySelectorAll<HTMLElement>(
        "input, button:not([disabled])",
      );
      if (!focusable || focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last?.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first?.focus();
      }
    }
  };

  if (!open) return null;

  return (
    // 遮罩：点面板之外关闭（原型行为）。它不是可聚焦元素，键盘用户走 Esc
    <div
      className="fixed inset-0 z-50 grid justify-items-center bg-overlay p-4 pt-[10vh]"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={onKeyDown}
        className="flex max-h-[76vh] w-full max-w-2xl flex-col overflow-hidden rounded-lg border border-edge bg-surface shadow-lg"
      >
        <div className="flex items-center gap-2 border-b border-edge px-4 py-3">
          <h2 id={titleId} className="text-sm font-semibold text-fg">
            全局搜索
          </h2>
          <span className="text-xs text-fg-muted">导航 · Ctrl/Cmd + K</span>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭全局搜索"
            className="ml-auto rounded-md border border-edge px-2 py-1 text-xs text-fg-muted hover:bg-surface-muted"
          >
            关闭
          </button>
        </div>

        <input
          ref={inputRef}
          type="text"
          value={query}
          aria-label="搜索全平台"
          aria-controls={`${optionPrefix}-results`}
          aria-activedescendant={activeItem ? `${optionPrefix}-${activeItem.id}` : undefined}
          placeholder={placeholder}
          onChange={(event) => {
            setQuery(event.target.value);
            setActiveIndex(0);
          }}
          className="min-h-12 shrink-0 border-b border-edge bg-transparent px-4 text-sm text-fg outline-none placeholder:text-fg-muted focus:border-accent"
        />

        {scopeNote ? (
          <p className="shrink-0 border-b border-edge bg-surface-muted px-4 py-2 text-xs text-fg-muted">
            {scopeNote}
          </p>
        ) : null}

        <div id={`${optionPrefix}-results`} role="listbox" className="min-h-0 flex-1 overflow-y-auto p-2">
          {matches.length === 0 ? (
            <p className="p-4 text-center text-xs text-fg-muted">{emptyLabel}</p>
          ) : (
            matches.map((item, index) => (
              <button
                key={item.id}
                type="button"
                id={`${optionPrefix}-${item.id}`}
                role="option"
                aria-selected={index === active}
                // 鼠标移过去就跟着选中：否则鼠标指着 A、Enter 却打开 B
                onMouseEnter={() => setActiveIndex(index)}
                onClick={() => select(item)}
                className={cx(
                  "grid w-full grid-cols-[6rem_minmax(0,1fr)_minmax(0,0.8fr)] items-center gap-3 rounded-md px-3 py-2 text-left",
                  index === active ? "bg-surface-muted text-fg" : "text-fg-muted hover:bg-surface-muted",
                )}
              >
                <span className="truncate text-xs text-fg-muted">{item.type}</span>
                <span className="truncate text-sm font-medium text-fg">{item.title}</span>
                <span className="truncate text-xs text-fg-muted">{item.meta}</span>
              </button>
            ))
          )}
        </div>

        <p className="shrink-0 border-t border-edge px-4 py-2 text-xs text-fg-muted">
          ↑↓ 选择　Enter 打开　Esc 关闭
        </p>
      </div>
    </div>
  );
}

export interface CommandPaletteTriggerProps {
  onOpen: () => void;
  label?: string;
}

/** 顶栏里的搜索入口。
 *
 *  在 XM-0043 之前这里是一个 `aria-disabled` 的占位（阶段 2 才实装）,
 *  现在换成真按钮。`aria-keyshortcuts` 让读屏用户也知道有快捷键——
 *  只在视觉上摆一个 `⌘K` 的话，这条信息对他们等于不存在。 */
export function CommandPaletteTrigger({
  onOpen,
  label = "搜索页面与页签",
}: CommandPaletteTriggerProps) {
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-keyshortcuts="Control+K Meta+K"
      className="flex h-8 w-80 max-w-full items-center justify-between gap-2 rounded-md border border-edge bg-surface-muted px-2 text-xs text-fg-muted hover:border-edge-strong hover:text-fg"
    >
      <span className="truncate">{label}</span>
      <kbd className="shrink-0 rounded-sm border border-edge px-1 font-mono text-xs">Ctrl K</kbd>
    </button>
  );
}
