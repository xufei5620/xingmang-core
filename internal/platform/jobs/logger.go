package jobs

import (
	"log/slog"
	"os"
)

// structuredDefaultLogger keeps the zero-value worker and lifecycle helpers
// on the same JSON field format as the process entry point.
func structuredDefaultLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}
