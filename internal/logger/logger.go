// Package logger 提供全局结构化日志初始化（基于标准库 log/slog）。
//
// 在程序入口调用 Init 设置全局 slog.Default()，之后各包直接使用
// slog.Info / slog.Warn / slog.Error 即可获得结构化输出（text 或 JSON）。
package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Config 日志配置。
type Config struct {
	// Format 输出格式："text"（默认，人类可读）| "json"（机器采集）
	Format string `yaml:"format"`
	// Level 最低日志级别："debug" | "info"（默认）| "warn" | "error"
	Level string `yaml:"level"`
	// Output 输出目标："stdout"（默认）| "stderr" | 文件路径
	Output string `yaml:"output"`
}

// Init 根据配置初始化全局 slog.Default()。
// 应在程序入口（main）尽早调用。
func Init(cfg Config) {
	level := parseLevel(cfg.Level)
	w := resolveWriter(cfg.Output)

	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: level}

	if strings.EqualFold(cfg.Format, "json") {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	slog.SetDefault(slog.New(handler))
}

// parseLevel 解析日志级别字符串。
func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// resolveWriter 解析输出目标。
func resolveWriter(output string) io.Writer {
	switch strings.ToLower(output) {
	case "stderr":
		return os.Stderr
	case "", "stdout":
		return os.Stdout
	default:
		f, err := os.OpenFile(output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return os.Stdout
		}
		return f
	}
}
