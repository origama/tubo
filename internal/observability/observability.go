package observability

import "log/slog"

func NewLogger() *slog.Logger {
	return slog.Default()
}
