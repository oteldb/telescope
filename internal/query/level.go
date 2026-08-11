package query

import (
	"strconv"
	"strings"

	"go.uber.org/zap/zapcore"
)

// ParseLevel reads a severity as it is written in the wild: a zap level, a
// syslog or OTEL name, or the number either of those is sometimes reduced to.
func ParseLevel(s string) (zapcore.Level, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "":
		return 0, false
	case "trace", "trace1", "trace2", "trace3", "trace4":
		return zapcore.DebugLevel, true
	case "warning":
		return zapcore.WarnLevel, true
	case "err", "critical", "crit", "alert", "emerg", "emergency":
		return zapcore.ErrorLevel, true
	case "notice":
		return zapcore.InfoLevel, true
	}
	if n, err := strconv.Atoi(s); err == nil {
		return SeverityNumber(n), true
	}
	var l zapcore.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return 0, false
	}
	return l, true
}

// SeverityNumber maps an OTEL severity number, or a syslog priority small
// enough to be one, onto a zap level.
func SeverityNumber(n int) zapcore.Level {
	switch {
	case n <= 4:
		return zapcore.DebugLevel
	case n <= 12:
		return zapcore.InfoLevel
	case n <= 16:
		return zapcore.WarnLevel
	case n <= 20:
		return zapcore.ErrorLevel
	default:
		return zapcore.FatalLevel
	}
}
