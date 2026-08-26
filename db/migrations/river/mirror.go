// Package rivermirror embeds the reviewable copy of River's pinned PostgreSQL
// migration files. The jobs package compares this FS with River's embedded
// driver bundle before applying lifecycle migrations.
package rivermirror

import "embed"

// FS contains the v0.45.0 main-line up/down SQL files.
//
//go:embed *.sql
var FS embed.FS
