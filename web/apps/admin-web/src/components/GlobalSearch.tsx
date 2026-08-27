import {
  CommandPalette,
  CommandPaletteTrigger,
  useCommandPaletteHotkey,
} from "@xingmang/ui-admin";
import { useCallback, useRef, useState } from "react";
import { useNavigate } from "react-router";
import {
  NAV_SEARCH_INDEX,
  SEARCH_PLACEHOLDER,
  SEARCH_SCOPE_NOTE,
  type NavSearchItem,
} from "../lib/searchIndex";

/** 顶栏的全局搜索（Ctrl/Cmd + K）。
 *
 *  把 ui-admin 的面板接到 router 上：面板本身不认识路由，应用只负责
 *  「选中这一条要去哪」。索引来自 lib/searchIndex，也就是 navigation.ts,
 *  与侧栏、面包屑、路由表同一份真相源。
 *
 *  **只导航**(ADMIN-IA §七 红线)：这里永远不放退款、审批、重试、禁用、
 *  发布之类的条目。一个能在搜索框里按 Enter 就执行的写操作，没有任何地方
 *  能承载「你确定吗」。 */
export function GlobalSearch() {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLDivElement>(null);

  const openPalette = useCallback(() => setOpen(true), []);
  // 面板开着时不再响应 Ctrl+K：否则在面板里按一次会把已经输入的词清掉
  useCommandPaletteHotkey(openPalette, !open);

  const close = useCallback(() => {
    setOpen(false);
    // 焦点回到触发它的按钮：键盘用户关掉一个模态框之后，焦点掉回 <body>
    // 就等于从头开始 Tab 一遍
    triggerRef.current?.querySelector("button")?.focus({ preventScroll: true });
  }, []);

  const select = useCallback(
    (item: NavSearchItem) => {
      void navigate(item.to);
    },
    [navigate],
  );

  return (
    <div ref={triggerRef}>
      <CommandPaletteTrigger onOpen={openPalette} />
      <CommandPalette<NavSearchItem>
        open={open}
        onClose={close}
        items={NAV_SEARCH_INDEX}
        onSelect={select}
        placeholder={SEARCH_PLACEHOLDER}
        scopeNote={SEARCH_SCOPE_NOTE}
      />
    </div>
  );
}
