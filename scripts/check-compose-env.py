#!/usr/bin/env python3
"""进程读的每一个环境变量，compose 里都必须透传。

2026-09-06 的教训：接码（XM-SMS0）整个做完、合入、推送之后，产品负责人问
「我要去哪里删那个变量」，一查才发现 XM_SMS_MODE 从来没接进 launch.yaml——
也就是说这个功能**部署不了**：在 .env 里写 XM_SMS_MODE=real 也进不了容器，
进程读到空串，接码端点整组不挂载，页面显示「当前环境未启用」。同一次检查
还翻出另外四个早就存在的同类洞（保障探测的两个、平台支付、reqlog 的 v2
token 映射）。

这类缺陷有三个特征，凑在一起就特别难发现：

  1. **不报错。** 变量读到空串，而每一个都有「空 = 关掉」的合理默认，
     于是进程正常启动、健康检查全绿。
  2. **本地测不出来。** 本地直接给进程喂环境变量，根本不经过 compose。
  3. **症状指向错误的方向。** 人看到的是「功能没生效」，第一反应是去查功能
     代码、查权限、查密钥，而真正的原因在一个谁都没想到要看的 YAML 文件里。

开票线 2026-09-06 的 RC99 是同一个模子刻出来的：docker-compose.prod.yml 的
api 段是显式清单，从未透传 ELIGIBILITY_EVIDENCE_BATCH_LIMIT，于是 RC94 设了
那个变量也进不去容器，一直到有人专门去容器里 echo 才发现。

所以这条检查不是「顺手加的整洁性检查」，它挡的是一整类「绿灯下线」的故障。
"""

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
COMPOSE = ROOT / "deploy" / "compose" / "launch.yaml"
SOURCES = ["cmd/platform-api", "cmd/platform-worker"]

# 读到的字面量环境变量名。只认字面量：动态拼出来的（XM_CARDS_<账号>_LIMIT_…）
# 本来就没法在这里静态检查，它们由各自的配置解析在启动时报错兜底。
ENV_RE = re.compile(r'(?:os\.)?[Gg]etenv\("(XM_[A-Z0-9_]+)"\)')

# 已废弃的变量：代码仍然读它，但**只是为了在它还留在配置里时让启动失败**。
# 这种变量绝不能出现在 compose 里——透传它等于把它复活。
#
# 每加一条都要写清为什么，否则这份清单会退化成「让门禁闭嘴的地方」。
RETIRED = {
    # XM-SMS0：供应商启用开关已搬进管理后台（sms.provider_status.enabled）。
    # loadSMSConfig 仍然读它，读到非空就让启动失败——一个还写着
    # XM_SMS_PROVIDERS=sms62 的配置文件会让人确信 hero_sms 已经关掉了。
    "XM_SMS_PROVIDERS",
}


def main() -> int:
    if not COMPOSE.exists():
        print(f"check-compose-env: 找不到 {COMPOSE}", file=sys.stderr)
        return 1
    compose_text = COMPOSE.read_text(encoding="utf-8")

    wanted: dict[str, set[str]] = {}
    for source in SOURCES:
        directory = ROOT / source
        if not directory.is_dir():
            continue
        for path in sorted(directory.glob("*.go")):
            if path.name.endswith("_test.go"):
                continue
            for name in ENV_RE.findall(path.read_text(encoding="utf-8")):
                wanted.setdefault(name, set()).add(f"{source}/{path.name}")

    failures = []
    for name in sorted(wanted):
        present = re.search(rf"^\s+{re.escape(name)}:", compose_text, re.M) is not None
        if name in RETIRED:
            if present:
                failures.append(
                    f"{name} 已废弃却出现在 launch.yaml——透传它等于把它复活；"
                    f"读它的代码只是为了在它还留在配置里时让启动失败"
                )
            continue
        if not present:
            readers = "、".join(sorted(wanted[name]))
            failures.append(
                f"{name} 被 {readers} 读取，但 launch.yaml 没有透传它——"
                f"在 .env 里配它也进不了容器，而进程读到空串会安静地当作「没配」"
            )

    for line in failures:
        print(f"check-compose-env: {line}", file=sys.stderr)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
