# XM-DESIGN0：界面规范页建成（/design 从占位毕业）

- **status:** implemented，未上线。纯前端，无后端改动、无端点、无 scope。
- **branch:** `ai/claude/XM-0030a-approval-core`
- **来源：** 产品负责人 2026-09-07 指示「管理后端未建的需要全部建立」。
- **同批：** XM-FINANCE-GLOBAL0、XM-CHANGES0。

## 改了什么

**`web/apps/admin-web/src/pages/DesignPage.tsx`（新增）** —— 六格设计系统活文档。
**`web/apps/admin-web/src/lib/designSpec.ts`（新增）** —— 令牌清单推导与纯函数。

六格：颜色与排版 / 按钮与表单 / 卡片与状态 / 表格与详情 / 页面状态 / 复杂组件。
每一格都是**仓库里真实组件的当场渲染**，不是截图、不是复刻。

## 这一页与其余每一页都不同的三点

1. **不读任何后端端点。** 因此它也是唯一**刻意不挂控制台红线横幅**的页——
   它不展示任何业务数据、不涉及任何操作，挂一条「不执行真实操作」反而是噪音
   （原型同样没给它挂）。
2. **不抄色值。** 令牌名单从 `@xingmang/design-tokens` 的 `tokens` 对象**遍历推导**，
   当前值在页面上用 `getComputedStyle` **实读**。
   - 为什么不手抄名单：手抄之后 design-tokens 新增一个语义色，这一页会安静地少
     展示一条——而「少了一条」在一屏色卡里根本看不出来。
   - 为什么不写死回退值：读不到就如实显示「读不到」，**绝不退回一个写死的十六进制**。
     否则这一页自己就成了第二份会漂移的色值定义，而它恰恰是「禁止硬编码颜色/圆角/
     阴影」这条红线的说明面。
3. **`cssVariableOf` 只认单一 `var(--x)` 引用，组合值一律返回 null。**
   从 `1px solid var(--a)` 里挑出 `--a` 读回来的是「那一个颜色」而不是这条声明本身的
   值——显示出来是错的，**而且错得看不出来**。宁可承认取不出。

## 与 Storybook 的分工

Storybook 是组件开发时的工作台（每个组件的全部 props 组合）；这一页是**给运营和设计
看的规范说明**（这套后台长什么样、为什么这么摆）。两者共用同一批真实组件，不是两套规范。

## tests_run

- `designSpec.test.ts` 16 条 + `DesignPage.test.tsx` 29 条
- 前端 typecheck / test / build 三项 ✅（2063 条全绿）
- `bash scripts/check-governance.sh` ✅ / `gitleaks protect --staged` ✅
- **变异验证**：`/design` 退回 `built: false` → `navigation.test.ts` 三条同时变红。
  （这条特意做了：三条断言里有两条是「某项不在名单里」的缺席型断言，不变异跑一次
  不能算数——`absence-assertions-must-be-mutation-checked` 那条教训。）

## risks

- `getComputedStyle` 在 jsdom 下不返回真实的自定义属性值，所以测试里读到的是「读不到」
  分支。**这意味着「读得到」那条分支没有被单测覆盖**——它只在浏览器里成立。
  已在测试里显式断言「读不到时显示『读不到』而不是空白」，把 jsdom 能验的那半钉住了。

## follow_ups

- 「读得到」分支的浏览器级验证（需要 Playwright 或 Storybook 交互测试，本仓库目前两者
  都没有跑起来的通道），登记待定。
