#!/usr/bin/env python3
"""校验前端版本入口全部为精确版本（宪法：禁止范围表达式与 latest）。

扫描范围（红队发现 XM-R004 / Issue #24 指出仅扫依赖段不够——版本改写入口
同样能引入范围与 latest）：

  package.json        dependencies / devDependencies / peerDependencies /
                      optionalDependencies / overrides / resolutions /
                      pnpm.overrides / pnpm.packageExtensions.*
  pnpm-workspace.yaml catalog / catalogs.* / overrides

放行 workspace: / catalog: / link: 协议前缀——它们是工作区链接，不是注册表版本。
engines 段不在扫描范围内：那是运行时下限声明，不是依赖版本钉死。

用法: check-versions.py <file> [<file> ...]
退出码 0 = 全部精确；1 = 存在范围表达式（明细打到 stderr）。
"""

from __future__ import annotations

import json
import re
import sys

EXACT = re.compile(r"\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?")
LINK_PREFIXES = ("workspace:", "catalog:", "link:")


def scan(section: str, mapping: object, bad: list[str]) -> None:
    """递归扫描 name→spec 映射；spec 为嵌套字典时下钻（catalogs 是两层）。"""
    if not isinstance(mapping, dict):
        return
    for name, spec in mapping.items():
        if isinstance(spec, dict):
            scan(f"{section}.{name}", spec, bad)
            continue
        if not isinstance(spec, str):
            continue
        if spec.startswith(LINK_PREFIXES):
            continue
        if not EXACT.fullmatch(spec):
            bad.append(f"{section}.{name}={spec}")


def scan_package_json(path: str, bad: list[str]) -> None:
    with open(path, encoding="utf-8") as fh:
        pkg = json.load(fh)

    for section in (
        "dependencies",
        "devDependencies",
        "peerDependencies",
        "optionalDependencies",
        "overrides",
        "resolutions",
    ):
        scan(section, pkg.get(section), bad)

    pnpm_cfg = pkg.get("pnpm") or {}
    scan("pnpm.overrides", pnpm_cfg.get("overrides"), bad)
    for ext_name, ext in (pnpm_cfg.get("packageExtensions") or {}).items():
        if not isinstance(ext, dict):
            continue
        for sub in ("dependencies", "peerDependencies", "optionalDependencies"):
            scan(f"pnpm.packageExtensions.{ext_name}.{sub}", ext.get(sub), bad)


def scan_workspace_yaml(path: str, bad: list[str]) -> None:
    try:
        import yaml
    except ModuleNotFoundError:
        print(f"跳过 {path}：无 pyyaml", file=sys.stderr)
        return
    with open(path, encoding="utf-8") as fh:
        cfg = yaml.safe_load(fh) or {}
    scan("catalog", cfg.get("catalog"), bad)
    scan("catalogs", cfg.get("catalogs"), bad)
    scan("overrides", cfg.get("overrides"), bad)
    # pnpm 11 的 packageExtensions 既可写在 package.json 的 pnpm 字段里，
    # 也可写在 workspace 文件里。只扫前者的话，后者就是一条写 latest 的暗道
    # （XM-R007）。
    scan("packageExtensions", cfg.get("packageExtensions"), bad)


def main(argv: list[str]) -> int:
    if not argv:
        print("用法: check-versions.py <file> [<file> ...]", file=sys.stderr)
        return 2
    bad: list[str] = []
    for path in argv:
        if path.endswith("package.json"):
            scan_package_json(path, bad)
        elif path.endswith((".yaml", ".yml")):
            scan_workspace_yaml(path, bad)
        else:
            print(f"跳过未知文件类型: {path}", file=sys.stderr)
    if bad:
        for entry in bad:
            print(f"  非精确版本: {entry}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
