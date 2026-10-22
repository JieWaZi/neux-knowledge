// Package logger adapts the upstream chunker diagnostic port to the standard library without introducing host logging dependencies.
package logger

import (
	"context"
	"fmt"
	"log/slog"
)

// Debugf preserves upstream diagnostic messages and context; the process's standard logging handler controls the log level.
func Debugf(ctx context.Context, format string, args ...any) {
	slog.DebugContext(ctx, fmt.Sprintf(format, args...))
}
