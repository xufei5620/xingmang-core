package action

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CallerActivity 是**观测到的**一个调用方：某个身份在一段时间内经内核执行过
// 哪些写操作、多少次、最近一次是什么时候、结果如何。
//
// 它回答的是「谁真的来过」，与任何登记簿无关——这正是它的价值所在：
// 登记簿说的是「谁本该来」，两者对不上才是要处理的事
// （internal/platform/integration 的对账逻辑消费本类型）。
//
// **覆盖面仅限写操作。** action_run 只记经 Action 内核的调用；读操作
// （GET /api/v1/...）今天只进进程的访问日志（httpapi.AccessLog 的
// `http_request` 行），既不落库也没有查询端点。所以「某个身份在这里一次都
// 没出现」只能推出「它没做过写操作」，推不出「它没来过」。消费方必须把这
// 句限定原样带到界面上（宪法 12 条：禁止裸数字冒充实时完整数据）。
type CallerActivity struct {
	PrincipalID   string
	PrincipalType string
	// RunCount 是窗口内的执行总次数（成功 + 失败）。
	RunCount int64
	// FailedCount 是其中失败的次数。
	FailedCount int64
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	// LastActionID 是窗口内最近一次执行的 Action ID。
	LastActionID string
	// LastStatus 是那一次的结果（succeeded / failed）。
	LastStatus string
}

// PgCallerStore 按身份汇总 action_run。
//
// 与 PgRunStore 分开是刻意的：那个类型的职责是**写入**执行记录并按游标翻页,
// 它被内核的热路径依赖；本类型只在一个后台页面被读一次，给它单独一个类型,
// 改这里就不必碰内核依赖的那个结构体。两者共用同一张表、同一个连接池。
type PgCallerStore struct {
	pool *pgxpool.Pool
}

// NewPgCallerStore 创建汇总查询入口。
func NewPgCallerStore(pool *pgxpool.Pool) *PgCallerStore {
	return &PgCallerStore{pool: pool}
}

// MaxCallerRows 是单次汇总返回的身份数上限。
//
// 有上限而不是不限：这张表按 principal_id 分组，理论上一个乱来的调用方能用
// 随机身份灌出无穷多组。超出上限时**如实告诉调用方被截断了**（返回值的
// Truncated），而不是悄悄少给几行——一份看起来完整的清单比一份标了截断的
// 清单危险得多。
const MaxCallerRows = 200

// CallerActivityPage 是一次汇总的结果。
type CallerActivityPage struct {
	Items []CallerActivity
	// Truncated 为真表示窗口内出现的身份数超过了 MaxCallerRows，本页不完整。
	Truncated bool
}

// AggregateCallers 汇总某环境下 since 之后出现过的全部调用方。
//
// environment 由调用方（httpapi 层）从 Principal 取，不接受请求参数指定——
// 与 RunFilter.Environment 同一条纪律（规格 §20.5）。
func (s *PgCallerStore) AggregateCallers(ctx context.Context, environment string, since time.Time) (CallerActivityPage, error) {
	if environment == "" {
		return CallerActivityPage{}, fmt.Errorf("aggregate callers: environment 为空")
	}
	// 取 MaxCallerRows+1 行来判断有没有被截断：只取上限行数的话，
	// 「刚好等于上限」与「超过上限」在结果里长得一模一样。
	rows, err := s.pool.Query(ctx, `
		SELECT principal_id,
		       (array_agg(principal_type ORDER BY started_at DESC))[1] AS principal_type,
		       count(*)                                                AS run_count,
		       count(*) FILTER (WHERE status = 'failed')               AS failed_count,
		       min(started_at)                                         AS first_seen_at,
		       max(started_at)                                         AS last_seen_at,
		       (array_agg(action_id ORDER BY started_at DESC))[1]      AS last_action_id,
		       (array_agg(status ORDER BY started_at DESC))[1]         AS last_status
		  FROM action.action_run
		 WHERE environment = $1 AND started_at >= $2
		 GROUP BY principal_id
		 ORDER BY max(started_at) DESC, principal_id
		 LIMIT $3`, environment, since.UTC(), MaxCallerRows+1)
	if err != nil {
		return CallerActivityPage{}, fmt.Errorf("aggregate callers: %w", err)
	}
	defer rows.Close()

	page := CallerActivityPage{Items: []CallerActivity{}}
	for rows.Next() {
		var a CallerActivity
		if err := rows.Scan(&a.PrincipalID, &a.PrincipalType, &a.RunCount, &a.FailedCount,
			&a.FirstSeenAt, &a.LastSeenAt, &a.LastActionID, &a.LastStatus); err != nil {
			return CallerActivityPage{}, fmt.Errorf("aggregate callers: %w", err)
		}
		a.FirstSeenAt = a.FirstSeenAt.UTC()
		a.LastSeenAt = a.LastSeenAt.UTC()
		page.Items = append(page.Items, a)
	}
	if err := rows.Err(); err != nil {
		return CallerActivityPage{}, fmt.Errorf("aggregate callers: %w", err)
	}
	if len(page.Items) > MaxCallerRows {
		page.Items = page.Items[:MaxCallerRows]
		page.Truncated = true
	}
	return page, nil
}
