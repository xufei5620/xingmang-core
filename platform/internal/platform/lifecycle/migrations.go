// Package lifecycle 回答「这套部署的数据库跑到哪一版了」。
//
// 迁移是 Platform Lifecycle Operation（宪法 2、3 条）：走版本化脚本 + 变更单
// + 人工批准，**不经 Action 通道**。所以本包里只有读，没有任何写路径——它
// 不是执行迁移的第二条通道，也不该长成一条。
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	dbfiles "github.com/xufei5620/xingmang-platform/db"
)

// ScopeRead 是读取迁移状态所需的权限。
//
// **复用 ops.read**（ops.ScopeRead 的字面量），不新建 scope：迁移版本回答的
// 是「这套部署处在什么状态」，与心跳、队列积压、控制平面健康同一类运行保障
// 知识面，泄漏面相同。归 registry.read 反而不对——资源目录说的是「平台管着
// 哪些被管系统」，而迁移是平台**自己**的运行事实。
//
// 这里不 import ops 包只为取一个常量：本包被 httpapi 引用，而路由上写的就是
// ops.ScopeRead 本身（router.go），两处同源。consistency_test 钉住二者相等。
const ScopeRead = "ops.read"

// migrationFilePattern 匹配 golang-migrate 的文件名：{版本}_{名字}.{方向}.sql。
var migrationFilePattern = regexp.MustCompile(`^([0-9]+)_(.+)\.(up|down)\.sql$`)

// Script 是仓库里的一个版本化迁移脚本。
type Script struct {
	Version int64
	// Name 是去掉版本号与方向后缀的脚本名，如 "init_core_registry"。
	Name string
	// HasDown 报告这一版有没有配套的 down 脚本。
	//
	// forward-only（规格 §5.7）意味着 down 脚本**不是**回滚承诺：它存在只是
	// 让「这一版理论上怎么退」有据可查。界面上要如实这么说，不能显示成
	// 「可回滚」——那会让人以为按一下就能退回去。
	HasDown bool
}

// State 是数据库自己记着的迁移状态（core.schema_migration_state 的唯一一行，
// 底下是 golang-migrate 的 public.schema_migrations）。
type State struct {
	// Version 是已应用到的版本号；0 = 一条都没跑过。
	Version int64
	// Dirty = 上一次迁移跑到一半失败了，schema 处在半截状态。
	//
	// 这是本包最要紧的一个字段：dirty 的库既不能继续迁移也不该继续服务，
	// 而它在任何别的地方都看不出来（服务照常起、端点照常回 200）。
	Dirty bool
}

// Migration 是一个脚本 + 它在这套部署里应用了没有。
type Migration struct {
	Script
	Applied bool
}

// Report 是一次完整的迁移状态读数。
type Report struct {
	State
	// Items 按版本升序。
	Items []Migration
	// Ahead 是「二进制里带着、库里还没应用」的脚本数。
	//
	// 单拎出来是因为它对应一个真实的部署事故形态：镜像更新了而迁移容器没跑
	// （或跑失败了没人看日志）。那时服务照常起、功能静静地缺一块表。
	Ahead int
}

// Inventory 读出二进制里嵌着的全部迁移脚本，按版本升序。
//
// 同一个版本号的 up/down 两个文件合成一条 Script。
func Inventory() ([]Script, error) {
	entries, err := fs.ReadDir(dbfiles.MigrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌迁移目录: %w", err)
	}
	byVersion := make(map[int64]*Script, len(entries)/2)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			// 认不出名字的文件**报错**而不是跳过：db/migrations 下只该有
			// 版本化脚本，出现别的东西说明有人往这个目录里放了不该放的
			// 文件，而静默跳过会让「少了一版」看起来像正常。
			return nil, fmt.Errorf("内嵌迁移目录里有认不出的文件名 %q", e.Name())
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("迁移文件 %q 的版本号不是整数: %w", e.Name(), err)
		}
		s, ok := byVersion[version]
		if !ok {
			s = &Script{Version: version, Name: m[2]}
			byVersion[version] = s
		}
		if strings.EqualFold(m[3], "down") {
			s.HasDown = true
		}
	}
	out := make([]Script, 0, len(byVersion))
	for _, s := range byVersion {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Store 读数据库侧的迁移状态。
type Store struct {
	pool *pgxpool.Pool
}

// NewStore 构造只读的迁移状态查询。
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// undefinedTable 是 PostgreSQL 的 42P01（undefined_table，视图也归它）。
const undefinedTable = "42P01"

// state 读 core.schema_migration_state。
//
// **不直接读 public.schema_migrations**：`public` schema 被角色策略契约化为
// River 专用（contracts/database/role-policy.v1.json 的
// `public_schema_contract: "river-only"`），而 schema_migrations 在同一份策略里
// 标着 no-runtime-access——API 运行身份对它没有、也不该有权限。
// core.schema_migration_state（迁移 000050）是设计稿 §7.6 自己留的出口
// （「ops 若后续需要任务诊断，另批只读视图」）：视图 owner 是 xm_migrator，
// PostgreSQL 默认 security_invoker=false，所以对基表的权限检查走 owner，
// 读者只需要 core 的 USAGE + 本视图的 SELECT。
//
// 视图不存在（42P01）当作「一条都没跑过」：迁移 000050 之前的库上它确实不存在，
// 而那种库要么是全新的、要么就是还没迁到这一版——两种情形下「不知道跑到哪一版」
// 都比编一个数字诚实。
//
// **只放行 42P01，别的错误一律往上抛**——尤其是 42501（insufficient_privilege）。
// 把它一并吞成「版本 0」会让一套授权没配好的部署在界面上显示「一条迁移都没
// 应用」——那是一句笃定的假话，比报错难查得多（宪法 12 条：缺数据要说出来）。
func (s *Store) state(ctx context.Context) (State, error) {
	var st State
	err := s.pool.QueryRow(ctx,
		`SELECT version, dirty FROM core.schema_migration_state`).Scan(&st.Version, &st.Dirty)
	switch {
	case err == nil:
		return st, nil
	case errors.Is(err, pgx.ErrNoRows):
		// 视图在但没有行：golang-migrate 在 down 到底之后就是这个样子。
		return State{}, nil
	default:
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == undefinedTable {
			return State{}, nil
		}
		return State{}, fmt.Errorf("读取 core.schema_migration_state: %w", err)
	}
}

// MigrationReport 把库里的版本与二进制里的脚本清单合成一份读数。
func (s *Store) MigrationReport(ctx context.Context) (Report, error) {
	scripts, err := Inventory()
	if err != nil {
		return Report{}, err
	}
	st, err := s.state(ctx)
	if err != nil {
		return Report{}, err
	}
	report := Report{State: st, Items: make([]Migration, 0, len(scripts))}
	for _, script := range scripts {
		// golang-migrate 是顺序 forward-only 的：库里记的那个版本号意味着
		// 「不大于它的都跑过了」。库里**没有**逐条台账，所以这里只能这么
		// 推断——推断的依据写在这儿，免得后来的人以为它是逐条查出来的。
		applied := script.Version <= st.Version
		if !applied {
			report.Ahead++
		}
		report.Items = append(report.Items, Migration{Script: script, Applied: applied})
	}
	return report, nil
}
