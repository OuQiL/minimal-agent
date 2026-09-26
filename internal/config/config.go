// Package config 装载运行期配置。
//
// 装载顺序为「内置默认值 → 配置文件 → 环境变量」，后者覆盖前者。
// 这样日常配置写在文件里便于查看与留档，而临时切换模型或密钥时用环境变量
// 覆盖即可，不必改动文件；CI 里也不必为了改一个地址去写配置文件。
//
// 每一级都可以缺席：没有配置文件、没有任何环境变量时，程序仍能启动
// （搜索工具会降级，但不会崩溃）。
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultFileName 是默认的配置文件名，在进程工作目录下查找。
	DefaultFileName = "config.yaml"
	// EnvConfigPath 指定配置文件路径，便于把配置放到工作目录之外。
	EnvConfigPath = "AGENT_CONFIG"
	// DefaultLogFile 是执行日志的默认落盘路径。
	DefaultLogFile = "agent.log"
	// DisableLogFile 是 LogFile 的关闭值，写了它表示只输出到终端、不落盘。
	//
	// 需要这个哨兵值，是因为本项目统一的「空串即未设置」语义表达不了
	// 「显式关掉一个有默认值的开关」——`LOG_FILE=""` 只会被当成没配。
	DisableLogFile = "-"
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
	// LogFile 是执行日志的落盘路径，除输出到终端外还追加写入该文件。
	// 置为 DisableLogFile 可只输出到终端。
	LogFile string
	// DBPath 是 SQLite 数据库文件路径。
	DBPath string
	// NoColor 强制关闭终端着色。
	NoColor bool

	// Source 记录本次配置实际来自哪个文件，供日志展示；未使用文件时为空。
	Source string
}

// --- 配置文件结构 ---
//
// 与 Config 分开定义，是为了让「文件里能写什么」成为一份显式的契约：
// 字段名、嵌套层级与注释都以这里为准，而不是由内部字段名决定。

type fileConfig struct {
	LLM     fileLLM     `yaml:"llm"`
	Search  fileSearch  `yaml:"search"`
	Weather fileWeather `yaml:"weather"`
	Agent   fileAgent   `yaml:"agent"`
	Storage fileStorage `yaml:"storage"`
	Output  fileOutput  `yaml:"output"`
}

type fileLLM struct {
	BaseURL    string `yaml:"base_url"`
	APIKey     string `yaml:"api_key"`
	Model      string `yaml:"model"`
	MaxRetries int    `yaml:"max_retries"`
	Timeout    string `yaml:"timeout"`
}

type fileSearch struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
}

type fileWeather struct {
	ForecastURL string `yaml:"forecast_url"`
	GeocodeURL  string `yaml:"geocode_url"`
}

type fileAgent struct {
	MaxToolTurns     int    `yaml:"max_tool_turns"`
	MaxToolParallel  int    `yaml:"max_tool_parallel"`
	ToolTimeout      string `yaml:"tool_timeout"`
	MaxHistoryMsgs   int    `yaml:"max_history_messages"`
	CompactThreshold int    `yaml:"compact_threshold"`
	KeepRecentTurns  int    `yaml:"keep_recent_turns"`
}

type fileStorage struct {
	DBPath string `yaml:"db_path"`
}

type fileOutput struct {
	LogFile string `yaml:"log_file"`
	// NoColor 用指针区分「文件里没写」与「写了 false」。
	NoColor *bool `yaml:"no_color"`
}

// --- 装载 ---

// Load 从默认位置装载配置。
//
// 位置由 AGENT_CONFIG 决定；未设置时取工作目录下的 config.yaml。
// 默认位置的文件不存在不算错误——不建配置文件时行为与纯环境变量方式一致。
func Load() (Config, error) {
	path, required := resolvePath()
	return load(path, required)
}

// LoadFrom 从指定路径装载配置，便于测试。path 为空表示不使用配置文件。
func LoadFrom(path string) (Config, error) {
	return load(path, path != "")
}

func load(path string, required bool) (Config, error) {
	cfg := defaults()

	if path != "" {
		fc, err := readFile(path)
		switch {
		case err == nil:
			applyFile(&cfg, fc)
			cfg.Source = path
		case errors.Is(err, os.ErrNotExist) && !required:
			// 默认位置没有配置文件，属正常情况。
		default:
			return Config{}, err
		}
	}

	applyEnv(&cfg)
	return cfg, nil
}

// resolvePath 决定读哪个文件，并报告它是否是显式指定的。
// 显式指定的文件不存在时应当报错——那多半是路径写错了，静默忽略会让人困惑。
func resolvePath() (path string, required bool) {
	if p := os.Getenv(EnvConfigPath); p != "" {
		return p, true
	}
	return DefaultFileName, false
}

func readFile(path string) (*fileConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	// 拒绝未知字段：把 api_key 误写成 api_kye 这类拼写错误，
	// 否则会被静默忽略，最终表现为「配了但不生效」。
	dec.KnownFields(true)

	var fc fileConfig
	if err := dec.Decode(&fc); err != nil {
		if errors.Is(err, io.EOF) {
			// 空文件按「全部使用默认值」处理
			return &fileConfig{}, nil
		}
		return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
	}
	return &fc, nil
}

func defaults() Config {
	return Config{
		BaseURL:    "https://api.deepseek.com/v1",
		APIKey:     "",
		Model:      "deepseek-chat",
		MaxRetries: 3,
		LLMTimeout: 120 * time.Second,

		BochaAPIKey:      "",
		BochaBaseURL:     "https://api.bochaai.com",
		OpenMeteoBaseURL: "https://api.open-meteo.com",
		GeocodingBaseURL: "https://geocoding-api.open-meteo.com",

		MaxToolTurns:     8,
		MaxHistoryMsgs:   40,
		CompactThreshold: 12000,
		KeepRecentTurns:  6,
		MaxToolParallel:  4,
		ToolTimeout:      10 * time.Second,

		LogFile: DefaultLogFile,
		DBPath:  "agent.db",
		NoColor: false,
	}
}

// applyFile 用配置文件中的值覆盖默认值。
// 空字符串与零值视为「未设置」，保持默认值不变。
func applyFile(cfg *Config, fc *fileConfig) {
	setString(&cfg.BaseURL, fc.LLM.BaseURL)
	setString(&cfg.APIKey, fc.LLM.APIKey)
	setString(&cfg.Model, fc.LLM.Model)
	setInt(&cfg.MaxRetries, fc.LLM.MaxRetries)
	setDuration(&cfg.LLMTimeout, fc.LLM.Timeout)

	setString(&cfg.BochaAPIKey, fc.Search.APIKey)
	setString(&cfg.BochaBaseURL, fc.Search.BaseURL)

	setString(&cfg.OpenMeteoBaseURL, fc.Weather.ForecastURL)
	setString(&cfg.GeocodingBaseURL, fc.Weather.GeocodeURL)

	setInt(&cfg.MaxToolTurns, fc.Agent.MaxToolTurns)
	setInt(&cfg.MaxToolParallel, fc.Agent.MaxToolParallel)
	setDuration(&cfg.ToolTimeout, fc.Agent.ToolTimeout)
	setInt(&cfg.MaxHistoryMsgs, fc.Agent.MaxHistoryMsgs)
	setInt(&cfg.CompactThreshold, fc.Agent.CompactThreshold)
	setInt(&cfg.KeepRecentTurns, fc.Agent.KeepRecentTurns)

	setString(&cfg.DBPath, fc.Storage.DBPath)

	setString(&cfg.LogFile, fc.Output.LogFile)
	if fc.Output.NoColor != nil {
		cfg.NoColor = *fc.Output.NoColor
	}
}

// applyEnv 用环境变量覆盖当前值。
//
// 只认非空值：这样「显式设为空串」等同于「未设置」，与之前的纯环境变量
// 行为完全一致，不会因为引入配置文件而改变既有用法。
func applyEnv(cfg *Config) {
	envString(&cfg.BaseURL, "OPENAI_BASE_URL")
	envString(&cfg.APIKey, "OPENAI_API_KEY")
	envString(&cfg.Model, "OPENAI_MODEL")
	envInt(&cfg.MaxRetries, "OPENAI_MAX_RETRIES")
	envDuration(&cfg.LLMTimeout, "OPENAI_TIMEOUT")

	envString(&cfg.BochaAPIKey, "BOCHA_API_KEY")
	envString(&cfg.BochaBaseURL, "BOCHA_BASE_URL")
	envString(&cfg.OpenMeteoBaseURL, "OPENMETEO_BASE_URL")
	envString(&cfg.GeocodingBaseURL, "GEOCODING_BASE_URL")

	envInt(&cfg.MaxToolTurns, "MAX_TOOL_TURNS")
	envInt(&cfg.MaxHistoryMsgs, "MAX_HISTORY_MESSAGES")
	envInt(&cfg.CompactThreshold, "COMPACT_THRESHOLD")
	envInt(&cfg.KeepRecentTurns, "KEEP_RECENT_TURNS")
	envInt(&cfg.MaxToolParallel, "MAX_TOOL_PARALLEL")
	envDuration(&cfg.ToolTimeout, "TOOL_TIMEOUT")

	envString(&cfg.LogFile, "LOG_FILE")
	envString(&cfg.DBPath, "DB_PATH")
	envBool(&cfg.NoColor, "NO_COLOR")
}

// --- 取值辅助 ---

func setString(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func setInt(dst *int, v int) {
	// 所有数值型配置的默认值都是正数，因此零值可以安全地表示「未设置」。
	if v > 0 {
		*dst = v
	}
}

func setDuration(dst *time.Duration, v string) {
	if d, ok := parseDuration(v); ok {
		*dst = d
	}
}

func envString(dst *string, key string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func envInt(dst *int, key string) {
	v := os.Getenv(key)
	if v == "" {
		return
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return
	}
	*dst = n
}

func envDuration(dst *time.Duration, key string) {
	if d, ok := parseDuration(os.Getenv(key)); ok {
		*dst = d
	}
}

func envBool(dst *bool, key string) {
	v := os.Getenv(key)
	if v == "" {
		return
	}
	// NO_COLOR 按惯例是「存在即生效」，但显式写 0 或 false 时视为关闭。
	if v == "0" || v == "false" {
		*dst = false
		return
	}
	*dst = true
}

// parseDuration 接受 "10s" / "2m" 这类写法，也接受纯数字（按秒解释）。
func parseDuration(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n <= 0 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// --- 派生查询 ---

// SearchConfigured 报告搜索工具是否具备可用凭据。
func (c Config) SearchConfigured() bool { return c.BochaAPIKey != "" }

// ColorEnabled 判断是否应当输出 ANSI 着色。
//
// 配置显式关闭优先；否则仅在输出目标是终端时着色——重定向到文件或管道时
// 输出转义序列只会污染内容。
func (c Config) ColorEnabled(f *os.File) bool {
	if c.NoColor {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
