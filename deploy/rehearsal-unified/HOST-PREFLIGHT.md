# D/E preflight candidate

R1 当前合同：所有模式均只消费明确审核的本地 Cloudflare 公开 JSON 与独立 SHA；不会联网抓取或自动刷新列表。下方旧测试时间/SHA仅是历史证据，不描述 R1 当前代码。

Only this external candidate directory was changed. No Git/index/production/server/Docker probe was executed by this task. Implementation and tests are imported into this rehearsal package.

- `preflight.py`: `run(driver, config, allowed_occupied_ports=None) -> dict`; no standalone shell or remote execution entry point.
- `test_preflight.py`: `python -m unittest -v test_preflight` from this directory. When imported under `tests/`, keep `preflight.py` on Python's import path.
- Driver contract: `command(name, argv, check=False)` returns `CompletedProcess` with bytes; `compose(project, name, args)` likewise; `docker` is the reviewed explicit Docker binary/context argv. Driver must persist only UTC/exit/hash/size, including for resolved Compose JSON. `artifact_preflight` must already establish the local Docker endpoint and exact immutable artifacts.
- Return schema `xingmang.unified.host-preflight/v1`. Success requires `status == PASS`, `exit_code == 0`, exact requested `mode`, expected `qualification_scope`, and all four named checks: `source-versions`, `host-mode-guards`, `network-addressing`, `compose-environment`. Input failures return 2; collection/guard failures return 1. Interrupts propagate and never return PASS.

`config` retains the operator fields, with this strict additional object:

```json
{
  "mode": "server-rehearsal",
  "host_preflight": {
    "sources": {
      "sub2api": "sub2api-mig",
      "sub2api_db": "sub2api-mig-postgres",
      "newapi": "new-api",
      "newapi_db": "postgres"
    },
    "expected_sub2_version": "0.1.179",
    "expected_newapi_tag": "v1.0.0-rc.25",
    "required_loopback_ports": [58088, 58090, 58180, 58181],
    "planned_networks": [
      {"name": "qualification-proxy", "cidr": "172.30.250.0/28", "internal": false},
      {"name": "qualification-ingest", "cidr": "172.30.251.0/27", "internal": true}
    ],
    "proxy": {"network_name": "qualification-proxy", "gateway_ip": "172.30.250.1", "trusted_cidr": "172.30.250.1/32"},
    "ingest": {"network_name": "qualification-ingest", "dynamic_range": "172.30.251.0/28", "proxy_ip": "172.30.251.30", "proxy_cidr": "172.30.251.30/32"},
    "cloudflare_config_file": "/www/server/panel/vhost/nginx/0.cloudflare.conf",
    "cloudflare_review": {
      "path": "/reviewed/public/cloudflare-ips.json",
      "sha256": "REPLACE_WITH_REVIEWED_64_HEX_SHA256",
      "reviewed_by": "REPLACE_WITH_RESPONSIBLE_REVIEWER_ID",
      "reviewed_at": "REPLACE_WITH_TIMEZONE_AWARE_REVIEW_TIME",
      "expires_at": "REPLACE_WITH_EXPLICIT_REVIEW_VALIDITY_DEADLINE",
      "source_url": "https://api.cloudflare.com/client/v4/ips",
      "scope": "reviewed-offline"
    },
    "required_env_keys": {
      "qualification-unified": {"api": ["AUTH_MODE"], "web": []},
      "qualification-sources": {"sub2api-payments": ["SOURCE_INSTANCE_ID"]}
    }
  }
}
```

Names above are illustrative. `planned_networks` must enumerate the **complete actual new plan**, not just these two examples. `required_env_keys` keys must exactly cover the candidate project names and every service in each resolved Compose JSON, including env-free services (empty list). Every explicitly required environment key must exist and be nonnull/nonblank. Optional declared values may remain empty or null, preserving disabled card/bootstrap features. All declared values still reject unresolved interpolation and invalid structured values; optional declarations cannot exempt any required key. Caller must derive the complete mandatory key inventory from the reviewed new deployment contract. Old IdP services are intentionally absent from these new-plan requirements; their rollback inventory remains the lifecycle owner's guard.

Server modes (`server-rehearsal`, `production`) reject any `host_preflight.local` object and run actual Linux host `uname`, `df -P / <DockerRootDir from docker info>`, `timedatectl`, `ss`, and read the named public real-IP file. They never request the Cloudflare URL; it records provenance only. Missing or malformed output fails. The actual host and Docker context must refer to the same machine; the driver artifact check establishes the local-only Docker endpoint.

`cloudflare_review` 的七字段全部必需，`sha256` 对原始 JSON 字节核验，审核时间和截止时间须有明确时区，执行时间须在批准的区间内。示例占位值故意不可执行。负责人在窗口前按自己的授权流程取得官方完整 IPv4/IPv6 回复，审核并提交独立 SHA、审核人及有效期限；操作器不下载、不生成审核通过、不自动延长有效期。公开文件须为非链接的 `.json`，不能放在 private/secrets/credentials 目录。列表与实际 real-IP 配置的完整集合及 header/recursive 三项规则仍须精确匹配。

输出始终声明 `current_live_verified:false` 和 `server_freshness_action`。离线 PASS 只证明已批准文件未变、尚在明确审核区间内、主机配置与它一致；不能证明执行瞬间的官方列表仍相同。该当前性由服务器负责人窗口前核对，过期/未来审核或字节变化直接拒绝，不能把有效期声明当在线查询结果。

For `local-synthetic`, add exactly:

```json
"local": {
  "task_directory": "G:/xingmang/logs/unified-deploy-endpoint-20260912/task-owned-runtime",
  "storage_probe": {"container": "qualification-storage", "project": "qualification-fixture", "volume": "qualification-storage", "mount": "/probe"}
}
```

`local-synthetic` 也提供同一 `host_preflight.cloudflare_review` 对象，`scope` 必须为 `synthetic`；文件/SHA/审核区间仍必须完整。旧 `local.cloudflare_document` 字段已移除，避免两份文档来源。服务器模式禁止使用 synthetic 审核材料。

The public real-IP file path must then be a local absolute public `.conf` path. The engine supplies the existing running, task-owned storage container and actual named volume; no preflight container is created. The code inspects its Compose project and exact volume mount, then executes fixed `df -P -- /probe`. This proves `container-storage`, **not the Linux host root or DockerRootDir**. The local task disk uses real `shutil.disk_usage`; loopback ports use actual exclusive IPv4/IPv6 binds. Local fixture source names can differ, but all four actual running containers and the actual binary/tag output are still checked. The fixed SUB command is `/app/sub2api -version`; its fixture shim may report an explicit synthetic version chosen in the reviewed local config. NEW's actual image must carry the configured exact fixture tag. They are labeled `synthetic-contract`, never production upstream proof.

Local PASS includes nonempty `inherited_server_checks_requires_server`: host root/DockerRootDir disk, host NTP, host listening-port inventory, current authoritative Cloudflare list, production real-IP config, live upstream versions. No local PASS may be promoted to server qualification or clear these fields. CF local shape/real-IP correspondence is checked against the supplied public synthetic document; it is labeled as such and hashed.

## Actual evidence

- First test-first skeleton red: `01-red.json` (exit 1). It established missing implementation, not a business execution result.
- Concrete missing-network-count/builtin-IPAM regression red: `03-network-red.json`, 2026-09-11 18:07:09.664581–18:07:09.869404 UTC, exit 1, two actual assertion failures.
- Final restored green: `06-restored-green.json`, 2026-09-11 18:09:30.681715–18:09:30.857605 UTC, exit 0, **20 tests**.
- `05-mutations.json`: **23 isolated-copy mutations**, each exit 1 with a real failing assertion; source-running, exact SUB/NEW versions, disk threshold/row count, NTP, occupied ports, CF set/header/recursive, network count/same-name/overlap, ingest capacity/containment/nonintersection, proxy /32 boundaries, environment presence, local disk/volume ownership/mode labeling. Candidate bytes remained unchanged. Raw stderr/UTC metadata under `mutations/<name>/`.
- Unit tests use FakeDriver for Docker, Linux commands and network HTTP; they do **not** prove any real server/production state. Local loopback occupy/free scenarios use real sockets. No live Docker environment or source service was started by this subtask.

SHA-256:

- `preflight.py`: `84dbd894436e9a819eca7c6793890797d9665bfcd2c305958060c9b430a103ef`
- `test_preflight.py`: `45021cdba4b61769f9c3d82ea3cd66d60c5a777f4b10eb2014c4897e3cb722ba`

## Reviewed normal-configuration corrections

- `07-optional-env-red.json` captures the real false rejection of legal optional empty card/bootstrap values. The corrected rule checks null/blank only for `required_env_keys`, and unresolved interpolation for every declaration.
- `run(..., allowed_occupied_ports=set())` accepts an engine-only reviewed subset of planned ports; never load this argument from user config. Engine must independently validate old container image/project/service and exact loopback HostConfig bindings. Other occupied ports remain rejected. After stopping old services, call `check_ports(driver, config)` with no allow-set before any new start; it raises `PreflightError` if a port remains occupied. The public evidence records `reviewed_occupied_ports` for the first stage. D passes no allowance.
- Final current validation: `10-final-green.json`, 2026-09-11 18:13:45.035849–18:13:45.205989 UTC, exit 0, **23 tests**. Latest `05-mutations.json`: **26** isolated-copy effective assertion failures; candidate unchanged. Earlier evidence remains historical and does not describe current candidate hashes.

Current SHA-256:

- `preflight.py`: `88d7a58407a5849856eecb34910ffda28c67de0378d67e8e3027c7fcb774334a`
- `test_preflight.py`: `23ca6ce96e5a8638d9b0cea634a79bca77f5308946fd395c5b4bb652b06f0131`
