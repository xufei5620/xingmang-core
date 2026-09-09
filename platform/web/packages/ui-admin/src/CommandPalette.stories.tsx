import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { CommandPalette, CommandPaletteTrigger, type CommandItem } from "./CommandPalette";
import { navStageHint, NAV_GROUPS, PLATFORM_NAV_ITEMS } from "./navigation";

/** 演示用的索引，与应用里那份同源（navigation.ts）。 */
const items: CommandItem[] = [
  ...NAV_GROUPS.flatMap((group) =>
    group.items.map((item) => ({
      id: `page:${item.path}`,
      type: "页面",
      title: item.label,
      meta: navStageHint(item) ? `${group.title} · ${navStageHint(item)}` : group.title,
      keywords: item.path,
    })),
  ),
  ...PLATFORM_NAV_ITEMS.flatMap((platform) =>
    platform.tabs.map((tab) => ({
      id: `tab:${platform.serviceType}:${tab.value}`,
      type: "页签",
      title: tab.label,
      meta: platform.label,
      keywords: `${platform.serviceType} ${tab.value}`,
    })),
  ),
];

const SCOPE_NOTE =
  "本阶段只搜导航：页面、平台、页签与子页签，选中即跳转。搜索只导航，不执行任何写操作。";
const PLACEHOLDER = "搜索页面、平台、页签（例如：告警、渠道管理、开票）";

const meta = {
  title: "Admin/CommandPalette",
  component: CommandPalette,
  parameters: { layout: "fullscreen" },
} satisfies Meta<typeof CommandPalette>;
export default meta;
type Story = StoryObj<typeof meta>;

const base = {
  open: true,
  onClose: () => {},
  items,
  onSelect: () => {},
  placeholder: PLACEHOLDER,
  scopeNote: SCOPE_NOTE,
};

/** 刚打开的样子：不输入就是一份可浏览的导航目录。 */
export const Default: Story = { args: base };

/** 搜不到时明说没有，不做模糊兜底——
 *  一个「搜什么都能出点东西」的框，人没法判断自己搜的东西到底存不存在。 */
export const 无匹配: Story = { args: { ...base, items: [] } };

/** 顶栏入口 + 面板的完整交互。Storybook 里可以真的按 Esc 与 ↑↓。 */
export const 可交互: Story = {
  args: base,
  render: (args) => {
    // eslint-disable-next-line react-hooks/rules-of-hooks
    const [open, setOpen] = useState(false);
    return (
      <div className="flex min-h-96 flex-col gap-4 bg-canvas p-6">
        <CommandPaletteTrigger onOpen={() => setOpen(true)} />
        <p className="text-xs text-fg-muted">点上面的按钮打开；面板里可以试 ↑↓ / Enter / Esc。</p>
        <CommandPalette {...args} open={open} onClose={() => setOpen(false)} />
      </div>
    );
  },
};

/** 暗色下遮罩仍然压得住背景，面板本身用内容区的 surface。 */
export const DarkMode: Story = {
  args: base,
  globals: { theme: "dark" },
};
