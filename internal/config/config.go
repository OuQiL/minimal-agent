// Package config 从环境变量装载运行期配置。
//
// 所有配置项都有安全默认值，因此未配置任何环境变量时程序仍可启动
// （搜索工具会降级，但不会崩溃）。
package config

import (
	"os"
	"strconv"
	"time"
)

// Config 汇总全部运行期配置。
type Config struct {
	// --- 模型服务 ---
	BaseURL string
	APIKey  string
	Model   string
	// MaxRetries 交给 SDK 处理请求建立阶段的失败重试。
	MaxRetries int
	// LLMTimeout 是单次模型请求的总超时。流式响应可能持续较久，故取值较大。
	LLMTimeout time.Duration

	// --- 外部服务 ---
	BochaAPIKey      string
	BochaBaseURL     string
	OpenMeteoBaseURL string
	GeocodingBaseURL string

	// --- 循环与上下文 ---
	// MaxToolTurns 是单轮用户输入允许的最大工具调用轮次。
	MaxToolTurns int
	// MaxHistoryMsgs 是进入上下文的历史消息条数上限。
	MaxHistoryMsgs int
	// CompactThreshold 是触发压缩的消息总字符数阈值。
	CompactThreshold int
	// KeepRecentTurns 是压缩时保留原文的最近轮数。
	KeepRecentTurns int
	// MaxToolParallel 是同轮工具调用的并发上限。
	MaxToolParallel int
	// ToolTimeout 是单个工具执行（含其外部网络调用）的超时。
	ToolTimeout time.Duration

	// --- 输出 ---
	// LogFile 非空时，执行日志除输出到终端外还追加写入该文件。
	LogFile string
	// DBPath 是 SQLite 数据库文件路径。
	DBPath string
	// NoColor 关闭终端着色，便于重定向输出。
	NoColor bool
}

// Load 读取环境变量并填充默认值。
func Load() Config {
	return Config{
		BaseURL:    env("OPENAI_BASE_URL", "https://api.deepseek.com/v1"),
		APIKey:     env("OPENAI_API_KEY", ""),
		Model:      env("OPENAI_MODEL", "deepseek-chat"),
		MaxRetries: envInt("OPENAI_MAX_RETRIES", 3),
		LLMTimeout: envDuration("OPENAI_TIMEOUT", 120*time.Second),

		BochaAPIKey:      env("BOCHA_API_KEY", ""),
		BochaBaseURL:     env("BOCHA_BASE_URL", "https://api.bochaai.com"),
		OpenMeteoBaseURL: env("OPENMETEO_BASE_URL", "https://api.open-meteo.com"),
		GeocodingBaseURL: env("GEOCODING_BASE_URL", "https://geocoding-api.open-meteo.com"),

		MaxToolTurns:     envInt("MAX_TOOL_TURNS", 8),
		MaxHistoryMsgs:   envInt("MAX_HISTORY_MESSAGES", 40),
		CompactThreshold: envInt("COMPACT_THRESHOLD", 12000),
		KeepRecentTurns:  envInt("KEEP_RECENT_TURNS", 6),
		MaxToolParallel:  envInt("MAX_TOOL_PARALLEL", 4),
		ToolTimeout:      envDuration("TOOL_TIMEOUT", 10*time.Second),

		LogFile: env("LOG_FILE", ""),
		DBPath:  env("DB_PATH", "agent.db"),
		NoColor: envBool("NO_COLOR", false),
	}
}

// SearchConfigured 报告搜索工具是否具备可用凭据。
func (c Config) SearchConfigured() bool { return c.BochaAPIKey != "" }

// ColorEnabled 判断是否应当输出 ANSI 着色。
//
// 显式配置优先；否则仅在输出目标是终端时着色——重定向到文件或管道时
// 输出转义序列只会污染内容。
func (c Config) ColorEnabled(f *os.File) bool {
	if c.NoColor {
		return false
	}
	if v := os.Getenv("NO_COLOR"); v != "" && v != "0" {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	// 允许纯数字（按秒解释），也允许 "10s" / "2m" 这类写法。
	if n, err := strconv.Atoi(v); err == nil {
		if n <= 0 {
			return def
		}
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}
