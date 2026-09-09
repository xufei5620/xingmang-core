package jobs

import (
	"math"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// annotateRollupMetadata stamps the policy version and the cadence that was
// actually used by a periodic writer.  A zero/invalid cadence intentionally
// remains nil: legacy or manually-driven samples must report unknown coverage,
// never infer a value from the current deployment configuration.
func annotateRollupMetadata(observation *ops.Observation, interval time.Duration) {
	if observation == nil {
		return
	}
	observation.RollupPolicyVersion = ops.RollupPolicyVersion
	observation.ExpectedIntervalSeconds = nil
	if interval <= 0 || interval%time.Second != 0 {
		return
	}
	seconds := interval / time.Second
	if seconds <= 0 || seconds > math.MaxInt32 {
		return
	}
	v := int32(seconds)
	observation.ExpectedIntervalSeconds = &v
}
