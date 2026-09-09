// Package db 只做一件事：把版本化迁移脚本嵌进二进制。
//
// 为什么要嵌：迁移是 Platform Lifecycle Operation（宪法 2、3 条），跑它的是
// migrate 镜像——那个镜像里有 /app/db/migrations，而 **platform-api 镜像里
// 没有**（deploy/docker/go.Dockerfile 只往 migrate 阶段 COPY 那个目录）。
// 「后台要能显示已应用到哪一版」于是不能靠读文件系统：读不到。
//
// 嵌进来之后还多一个好处：脚本清单与二进制**同源**。库说 40、二进制里带着
// 41 个脚本，就说明镜像更新了而迁移没跑——那是一个真实发生过的部署形态，
// 靠读运行时目录反而看不出来（目录里有什么取决于挂了什么）。
//
// 放在 db/ 而不是 db/migrations/ 下是刻意的：那个目录被三样东西按目录读
// （cmd/migrate 的 golang-migrate、sqlc.yaml 的 schema、check-governance.sh
// 的不可变性检查），往里塞一个 .go 文件等于给三条链路各加一次风险。
// 对照 db/migrations/river/mirror.go——那份镜像自成一个目录，所以可以就地嵌。
package db

import "embed"

// MigrationsFS 是 db/migrations 下平台自己的 forward-only 迁移脚本。
//
// 只嵌本目录层的 *.sql：`river/` 子目录不在内（那是 River 自带 bundle 的
// 可审阅镜像，不是第二条独立迁移线，见 deploy/docker/migrate-entrypoint.sh）。
//
//go:embed migrations/*.sql
var MigrationsFS embed.FS
