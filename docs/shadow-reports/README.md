# 影子对比报告归档

`cmd/platform-shadow` 每次运行往这里写一份 JSON:

```
shadow-<环境>-<结束业务日>.json      例：shadow-production-2026-08-27.json
```

同一天重跑会**覆盖**——归档回答的是「那一天的结论是什么」,不是「跑过几次」。
怎么跑、怎么读报告见 `docs/runbooks/SHADOW-COMPARE.md`。

## 为什么落文件,不落数据库表

三条,按重要性排:

1. **结论必须能独立于平台库存在。** 这个工具的作用正是判断平台算得对不对;
   把结论存进被审查的那个库里是循环论证——库要是错的,存在里面的结论也可疑。
2. **写库要走 Action(宪法 2 条)**,而影子对比是只读工具,不该为了存一份报告
   而开一条写路径。
3. **14 天里每天要能回看、逐日 diff、随 PR 被人看见。** git 里的 JSON 天然满足;
   一张表还要另做查询界面才能达到同样效果。

## 文件结构

```jsonc
{
  "version": 1,                    // 结构版本；跨 14 天用同一个脚本读，改了要看得出来
  "tool": "platform-shadow",
  "generated_at": "2026-08-28T06:00:00Z",   // UTC RFC3339（宪法 14 条）
  "environment": "production",
  "window": { "from": "2026-08-14", "to": "2026-08-27" },
  "tolerance_cents": 0,            // 这一天是在什么容差下得出结论的
  "verdict": "clean",              // clean = 全部对上；dirty = 有任何一项没对上
  "summary": { "pairs": 42, "equal": 42, "differs": 0,
               "missing_on_platform": 0, "missing_on_soloai": 0,
               "unknown": 0, "problems": 0 },
  "rows": [],                      // 只列**没对上**的格
  "problems": []                   // 口径错误（币种/切日偏移对不上）
}
```

**全绿那天的文件就只有一个头 + 两个空数组**——那是刻意的:14 天里大多数报告
都该长这样,`"verdict": "clean"` 一眼确认,翻起来才不累。

### 读 `rows` 时必须注意的一条

未知与缺行的金额字段是 **`null`,不是 `0`**:

```jsonc
{ "account_id": "acc-7", "day": "2026-08-27", "measure": "revenue",
  "verdict": "unknown_on_platform",
  "platform_cents": null,          // ← 没采到，不是零
  "soloai_cents": 10000, "diff_cents": null }
```

拿它做聚合时**不要 `// 0`**。一个 `null` 被当成 0 加进合计,正是这个工具存在要防的
那类错(设计稿 §5.1 在库里管这条纪律,在这里管的是文件)。`jq` 里用
`select(.platform_cents != null)` 先筛。

### `verdict` 的取值

| 值 | 含义 |
|---|---|
| `equal` | 两侧都已知,差额在容差内(不会出现在 `rows` 里) |
| `differs` | 两侧都已知,差额超出容差 |
| `missing_on_platform` / `missing_on_soloai` | 一侧压根没有这一格 |
| `unknown_on_platform` / `unknown_on_soloai` | 有这一格,但那一侧的值未知(NULL) |

`missing` 与 `unknown` 分开不是啰嗦:前者要查「那天的采集跑了吗」,后者要查
「跑了,但那一次上游读取为什么失败」。

## 14 天验收怎么统计

规格 §22.3:**连续 14 个自然日、其中 7 个 T+1 结算日无未解释重大差异**。

```bash
# 有几天是 clean 的
jq -r 'select(.verdict=="clean") | .window.to' docs/shadow-reports/shadow-production-*.json | sort

# 哪几天不 clean，各自什么原因
jq -r 'select(.verdict!="clean")
       | "\(.window.to)  differs=\(.summary.differs) missing=\(.summary.missing_on_platform+.summary.missing_on_soloai) unknown=\(.summary.unknown) problems=\(.summary.problems)"' \
   docs/shadow-reports/shadow-production-*.json | sort

# 确认没有哪天是靠放宽容差换来的绿
jq -r 'select(.tolerance_cents != 0) | "\(.window.to) tolerance=\(.tolerance_cents)"' \
   docs/shadow-reports/shadow-production-*.json
```

最后那条要特别看:§9 明确「出现 >0 差异先按清单排口径,不先放宽容差」。
一份 `tolerance_cents != 0` 的 clean 报告,不能直接算进那 14 天。
