// Package jobs contains the platform's small, short-lived background jobs.
//
// River is deliberately used as a queue and scheduler only. This package must
// not grow workflow, signal, timer, or long-running orchestration semantics;
// those require the ADR-013 re-evaluation described in the repository rules.
package jobs
