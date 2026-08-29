# 服务器裸仓库 hooks

本目录的脚本随仓库版本化，由 `deploy/scripts/install-git-server.sh` 原子安装到
`/srv/git/xingmang-platform.git/hooks/`；服务器上不要手工编辑已安装副本。

## pre-receive

- 只接受 `refs/heads/*` 的非删除、非快进更新；旧 SHA 必须与当前 ref 一致；
- `main` 必须由 `promote.sh` 推送，且安装器登记的授权锁
  `<marker>.promote.lock/{pid,sha}` 仍由该脚本持有；marker 严格为单行
  `sha=<40hex>`，校验后原子消费；
- 生产默认强制授权锁。只有测试夹具显式设置
  `PRE_RECEIVE_ALLOW_MARKER_OVERRIDE=1` 才能运行 legacy marker-only 流程；
- hook 本身使用固定 PATH、`/bin/bash -p` 和无全局 Git 配置环境。

## post-receive

只对 `refs/heads/release/v0.1-launch` 启动受控 CI。每个 SHA 在配置的状态目录写
`<sha>.log` 与严格状态文件（`pending`/`green`/`red`）；CI 容器默认
`--pull=never --network none`，只挂载安装器登记且 SHA-256 匹配的可信
`ci-local.sh`。Git receive 返回成功不等于 CI green，部署脚本必须再读 status。

### 变更流程

1. 在分支中修改 hook，并更新对应 Handoff/测试；
2. 本地运行 `tests/deploy/deploy0-a.test.sh` 及适用门禁；
3. 由产品负责人安排服务器 root 重新运行安装器，确认新 hook hash 后再接收推送；
4. 不在服务器上直接编辑 hook 或关闭 `denyNonFastForwards`/`denyDeletes`。
