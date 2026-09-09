#!/usr/bin/env python3
"""校验 Compose 文件能被解析，且同一个映射里没有重复键。

为什么需要这个门禁：2026-09-04 卡片功能上线时，launch.yaml 的 platform-api
段里 XM_CARDS_MODE 被写了两次（本该给 worker 的一段落错了服务）。YAML 的
重复键在 PyYAML 默认行为下是「后者覆盖前者」，静默通过；而 docker compose
用的是 Go 的严格解析器，直接拒绝整个文件。于是：

  * 全部本地门禁绿灯（没有任何一处解析过 compose 文件）
  * 合并、推送、生产部署全部照常
  * 在生产服务器上 preflight 才炸，栈起不来

「跑得起来的门禁没覆盖到的文件」比「没有门禁」更危险，因为它给出的是
虚假的绿。这个脚本补上那一格。

重复键之外还顺带做整体解析：语法错、缩进错同样会在这里当场暴露，
而不是等到生产机上。
"""

from __future__ import annotations

import sys

import yaml


class DuplicateKeyError(Exception):
    pass


class StrictLoader(yaml.SafeLoader):
    """SafeLoader 的严格版：重复键报错而不是静默覆盖。

    PyYAML 默认让后一个键赢，这正是本门禁要抓的那类 bug 的成因——
    本地怎么看都正常，只有真正的 compose 解析器会拒绝。
    """


def _no_duplicates(loader: StrictLoader, node: yaml.MappingNode) -> dict:
    seen: dict[object, int] = {}
    for key_node, _ in node.value:
        key = loader.construct_object(key_node, deep=True)
        line = key_node.start_mark.line + 1
        if key in seen:
            raise DuplicateKeyError(
                f"第 {line} 行：重复的键 {key!r}（首次出现在第 {seen[key]} 行）"
            )
        seen[key] = line
    return loader.construct_mapping(node, deep=True)


StrictLoader.add_constructor(
    yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, _no_duplicates
)


def check(path: str) -> list[str]:
    try:
        with open(path, encoding="utf-8") as fh:
            # compose 的 !override / !reset 是 Compose 自己的标签，
            # 对本检查无意义：当成普通节点放行，别因为不认识就报错。
            StrictLoader.add_multi_constructor(
                "!", lambda loader, suffix, node: None
            )
            yaml.load(fh, Loader=StrictLoader)
    except DuplicateKeyError as exc:
        return [f"{path}: {exc}"]
    except yaml.YAMLError as exc:
        return [f"{path}: 无法解析：{exc}"]
    except OSError as exc:
        return [f"{path}: 读取失败：{exc}"]
    return []


def main(argv: list[str]) -> int:
    if len(argv) < 2:
        print("用法: check-compose.py <compose 文件>...", file=sys.stderr)
        return 2
    problems: list[str] = []
    for path in argv[1:]:
        problems.extend(check(path))
    for line in problems:
        print(line, file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
