// Package logx 提供统一的日志输出。
//
// 用 slog 的 JSON handler：控制面既要给人看（命令行窗口），
// 也要给日志分析用（将来可能接飞书告警或本地检索），结构化是必须的。
package logx

import (
	"log/slog"
	"os"
	"strings"
)

// New 按级别与格式创建 logger。
//
// level 支持 debug / info / warn / error；
// format 为 "text" 时输出人类友好的单行格式（开发期好用），
// 其余值输出 JSON（部署期好用）。
func New(level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}
