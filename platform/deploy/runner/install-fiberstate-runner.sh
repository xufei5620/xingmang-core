#!/usr/bin/env bash
# 在 fiberstate(生产主力机)上安装 GitHub Actions 自托管 runner。
# 隔离三原则:独立用户 ghrunner(仅 docker 组)/ systemd 资源上限 / 单 runner 串行。
# 用法(以 root 在服务器执行):
#   bash install-fiberstate-runner.sh <REGISTRATION_TOKEN>
# 注册 token 在 GitHub → 仓库 Settings → Actions → Runners → New self-hosted runner 页面获取(1 小时有效)。
set -euo pipefail

TOKEN="${1:?用法: bash install-fiberstate-runner.sh <REGISTRATION_TOKEN>}"
REPO_URL="https://github.com/xufei5620/xingmang-platform"
RUNNER_VER="2.337.0"
RUNNER_TGZ="actions-runner-linux-x64-${RUNNER_VER}.tar.gz"
RUNNER_DIR="/opt/actions-runner"
RUNNER_NAME="fiberstate-runner"

echo "[1/6] 独立用户与目录"
id -u ghrunner >/dev/null 2>&1 || useradd -m -s /bin/bash ghrunner
usermod -aG docker ghrunner
mkdir -p "$RUNNER_DIR" && chown ghrunner:ghrunner "$RUNNER_DIR"

echo "[2/6] 下载并解压 runner v${RUNNER_VER}"
sudo -u ghrunner bash -c "cd '$RUNNER_DIR' && curl -sSL -o '$RUNNER_TGZ' 'https://github.com/actions/runner/releases/download/v${RUNNER_VER}/${RUNNER_TGZ}' && tar xzf '$RUNNER_TGZ' && rm -f '$RUNNER_TGZ'"

echo "[3/6] 系统依赖(libicu 等)"
(cd "$RUNNER_DIR" && ./bin/installdependencies.sh >/dev/null 2>&1) || echo "  依赖脚本跳过(通常已满足)"

echo "[4/6] 注册到仓库(标签 self-hosted,linux)"
sudo -u ghrunner bash -c "cd '$RUNNER_DIR' && ./config.sh --unattended --url '$REPO_URL' --token '$TOKEN' --name '$RUNNER_NAME' --labels linux --work _work --replace"

echo "[5/6] 安装为 systemd 服务并启动"
(cd "$RUNNER_DIR" && ./svc.sh install ghrunner >/dev/null && ./svc.sh start >/dev/null)

echo "[6/6] 资源上限:最多 2 核 / 8G 内存 / 低优先级(不与线上项目抢资源)"
SVC="$(systemctl list-units --type=service --all --no-legend 'actions.runner.*' | awk '{print $1}' | head -1)"
mkdir -p "/etc/systemd/system/${SVC}.d"
printf '[Service]\nCPUQuota=200%%\nMemoryMax=8G\nNice=10\n' > "/etc/systemd/system/${SVC}.d/limits.conf"
systemctl daemon-reload && systemctl restart "$SVC"

echo "完成:service=${SVC} state=$(systemctl is-active "$SVC")"
echo "回滚:systemctl stop ${SVC} && (cd ${RUNNER_DIR} && ./svc.sh uninstall) —— 对线上项目零影响"
