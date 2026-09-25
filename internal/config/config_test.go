package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimal-agent/internal/config"
)

// clearEnv 清空本包读取的全部环境变量，使用例不受外部环境影响。
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_MAX_RETRIES", "OPENAI_TIMEOUT",
		"BOCHA_API_KEY", "BOCHA_BASE_URL", "OPENMETEO_BASE_URL", "GEOCODING_BASE_URL",
		"MAX_TOOL_TURNS", "MAX_HISTORY_MESSAGES", "COMPACT_THRESHOLD", "KEEP_RECENT_TURNS",
		"MAX_TOOL_PARALLEL", "TOOL_TIMEOUT", "LOG_FILE", "DB_PATH", "NO_COLOR",
		config.EnvConfigPath,
	} {
		t.Setenv(k, "")
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入配置文件失败: %v", err)
	}
	return path
}

func TestLoadFrom_DefaultsWithoutFile(t *testing.T) {
	clearEnv(t)

	cfg, err := config.LoadFrom("")
	if err != nil {
		t.Fatalf("装载失败: %v", err)
	}
	if cfg.Model == "" || cfg.BaseURL == "" {
		t.Error("未配置时应有可用的默认模型与地址")
	}
	if cfg.MaxToolTurns <= 0 || cfg.ToolTimeout <= 0 || cfg.MaxToolParallel <= 0 {
		t.Error("未配置时各项限额应有安全默认值")
	}
	if cfg.Source != "" {
		t.Errorf("未使用配置文件时 Source 应为空，实际 %q", cfg.Source)
	}
}

func TestLoadFrom_FileOverridesDefaults(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `
llm:
  base_url: https://example.com/v1
  api_key: file-key
  model: file-model
  max_retries: 7
  timeout: 45s
search:
  api_key: file-bocha
agent:
  max_tool_turns: 12
  tool_timeout: 30s
  compact_threshold: 5000
storage:
  db_path: /tmp/x.db
output:
  log_file: run.log
`)
	cfg, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("装载失败: %v", err)
	}

	if cfg.BaseURL != "https://example.com/v1" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.APIKey != "file-key" {
		t.Errorf("APIKey = %q", cfg.APIKey)
	}
	if cfg.Model != "file-model" {
		t.Errorf("Model = %q", cfg.Model)
	}
	if cfg.MaxRetries != 7 {
		t.Errorf("MaxRetries = %d", cfg.MaxRetries)
	}
	if cfg.LLMTimeout != 45*time.Second {
		t.Errorf("LLMTimeout = %v", cfg.LLMTimeout)
	}
	if cfg.BochaAPIKey != "file-bocha" {
		t.Errorf("BochaAPIKey = %q", cfg.BochaAPIKey)
	}
	if cfg.MaxToolTurns != 12 {
		t.Errorf("MaxToolTurns = %d", cfg.MaxToolTurns)
	}
	if cfg.ToolTimeout != 30*time.Second {
		t.Errorf("ToolTimeout = %v", cfg.ToolTimeout)
	}
	if cfg.CompactThreshold != 5000 {
		t.Errorf("CompactThreshold = %d", cfg.CompactThreshold)
	}
	if cfg.DBPath != "/tmp/x.db" {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
	if cfg.LogFile != "run.log" {
		t.Errorf("LogFile = %q", cfg.LogFile)
	}
	if cfg.Source != path {
		t.Errorf("Source = %q，期望 %q", cfg.Source, path)
	}

	// 文件里没写的字段应保持默认值，而不是被清零。
	if cfg.KeepRecentTurns != 6 {
		t.Errorf("未在文件中配置的 KeepRecentTurns 应保持默认 6，实际 %d", cfg.KeepRecentTurns)
	}
	if cfg.OpenMeteoBaseURL == "" {
		t.Error("未在文件中配置的 OpenMeteoBaseURL 应保持默认值")
	}
}

// 优先级：环境变量 > 配置文件 > 默认值。
func TestLoadFrom_EnvOverridesFile(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, `
llm:
  model: from-file
  max_retries: 5
agent:
  max_tool_turns: 12
`)
	t.Setenv("OPENAI_MODEL", "from-env")
	t.Setenv("MAX_TOOL_TURNS", "3")

	cfg, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("装载失败: %v", err)
	}

	if cfg.Model != "from-env" {
		t.Errorf("Model = %q，环境变量应覆盖文件", cfg.Model)
	}
	if cfg.MaxToolTurns != 3 {
		t.Errorf("MaxToolTurns = %d，环境变量应覆盖文件", cfg.MaxToolTurns)
	}
	// 环境变量没设的字段仍以文件为准。
	if cfg.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d，未被环境变量覆盖时应取文件值", cfg.MaxRetries)
	}
}

// 显式设为空串等同于未设置，保持与纯环境变量方式一致的行为。
func TestLoadFrom_EmptyEnvDoesNotOverride(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, "llm:\n  model: from-file\n")
	t.Setenv("OPENAI_MODEL", "")

	cfg, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("装载失败: %v", err)
	}
	if cfg.Model != "from-file" {
		t.Errorf("Model = %q，空的环境变量不应覆盖文件值", cfg.Model)
	}
}

func TestLoadFrom_MissingExplicitPathIsError(t *testing.T) {
	clearEnv(t)

	_, err := config.LoadFrom(filepath.Join(t.TempDir(), "并不存在.yaml"))
	if err == nil {
		t.Fatal("显式指定的配置文件不存在时应当报错")
	}
}

// 默认位置没有配置文件是正常情况，不应报错。
func TestLoad_DefaultPathMissingIsFine(t *testing.T) {
	clearEnv(t)
	t.Chdir(t.TempDir())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("默认位置无配置文件时不应报错: %v", err)
	}
	if cfg.Model == "" {
		t.Error("应回退到默认值")
	}
}

func TestLoad_ReadsDefaultPathFromWorkingDirectory(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.DefaultFileName),
		[]byte("llm:\n  model: cwd-model\n"), 0o600); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	t.Chdir(dir)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("装载失败: %v", err)
	}
	if cfg.Model != "cwd-model" {
		t.Errorf("Model = %q，应读取工作目录下的 %s", cfg.Model, config.DefaultFileName)
	}
}

// AGENT_CONFIG 可以把配置放到工作目录之外。
func TestLoad_EnvConfigPathTakesPrecedence(t *testing.T) {
	clearEnv(t)

	dir := t.TempDir()
	// 工作目录下放一个「错」的配置，确认不会被读走。
	if err := os.WriteFile(filepath.Join(dir, config.DefaultFileName),
		[]byte("llm:\n  model: wrong\n"), 0o600); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	explicit := filepath.Join(t.TempDir(), "elsewhere.yaml")
	if err := os.WriteFile(explicit, []byte("llm:\n  model: right\n"), 0o600); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	t.Chdir(dir)
	t.Setenv(config.EnvConfigPath, explicit)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("装载失败: %v", err)
	}
	if cfg.Model != "right" {
		t.Errorf("Model = %q，AGENT_CONFIG 指定的文件应优先", cfg.Model)
	}
}

func TestLoadFrom_MalformedYAMLErrorsClearly(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, "llm:\n  model: [unclosed\n")
	_, err := config.LoadFrom(path)
	if err == nil {
		t.Fatal("格式错误的配置文件应报错")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("错误信息应指明是哪个文件：%v", err)
	}
}

// 拼错的字段名必须被发现——否则会表现为「配了但不生效」，极难排查。
func TestLoadFrom_UnknownFieldIsRejected(t *testing.T) {
	clearEnv(t)

	path := writeConfig(t, "llm:\n  api_kye: oops\n")
	_, err := config.LoadFrom(path)
	if err == nil {
		t.Fatal("未知字段应被拒绝，以暴露拼写错误")
	}
	if !strings.Contains(err.Error(), "api_kye") {
		t.Errorf("错误信息应指出问题字段名：%v", err)
	}
}

func TestLoadFrom_EmptyFileUsesDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := config.LoadFrom(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("空配置文件不应报错: %v", err)
	}
	if cfg.Model != "deepseek-chat" {
		t.Errorf("Model = %q，空文件应全部使用默认值", cfg.Model)
	}
}

func TestLoadFrom_OnlyCommentsIsFine(t *testing.T) {
	clearEnv(t)

	cfg, err := config.LoadFrom(writeConfig(t, "# 只有注释\n# 没有任何配置项\n"))
	if err != nil {
		t.Fatalf("仅含注释的配置文件不应报错: %v", err)
	}
	if cfg.MaxToolTurns <= 0 {
		t.Error("应使用默认值")
	}
}

// 超时既接受 "10s" 这类写法，也接受纯数字（按秒解释）。
func TestLoadFrom_DurationFormats(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"10s", 10 * time.Second},
		{"2m", 2 * time.Minute},
		{"90", 90 * time.Second},
		{"1500ms", 1500 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			clearEnv(t)
			path := writeConfig(t, "agent:\n  tool_timeout: "+tc.in+"\n")
			cfg, err := config.LoadFrom(path)
			if err != nil {
				t.Fatalf("装载失败: %v", err)
			}
			if cfg.ToolTimeout != tc.want {
				t.Errorf("ToolTimeout = %v，期望 %v", cfg.ToolTimeout, tc.want)
			}
		})
	}
}

func TestLoadFrom_NoColorPointerSemantics(t *testing.T) {
	t.Run("写了 false", func(t *testing.T) {
		clearEnv(t)
		cfg, err := config.LoadFrom(writeConfig(t, "output:\n  no_color: false\n"))
		if err != nil {
			t.Fatalf("装载失败: %v", err)
		}
		if cfg.NoColor {
			t.Error("显式 false 应关闭该开关")
		}
	})
	t.Run("写了 true", func(t *testing.T) {
		clearEnv(t)
		cfg, err := config.LoadFrom(writeConfig(t, "output:\n  no_color: true\n"))
		if err != nil {
			t.Fatalf("装载失败: %v", err)
		}
		if !cfg.NoColor {
			t.Error("显式 true 应开启该开关")
		}
	})
}

func TestSearchConfigured(t *testing.T) {
	clearEnv(t)

	cfg, err := config.LoadFrom("")
	if err != nil {
		t.Fatalf("装载失败: %v", err)
	}
	if cfg.SearchConfigured() {
		t.Error("未配置密钥时不应报告搜索可用")
	}

	cfg, err = config.LoadFrom(writeConfig(t, "search:\n  api_key: k\n"))
	if err != nil {
		t.Fatalf("装载失败: %v", err)
	}
	if !cfg.SearchConfigured() {
		t.Error("配置了密钥后应报告搜索可用")
	}
}

// 文件与样例必须保持同步：样例里出现的字段名都应当是合法字段，
// 否则用户照抄样例反而会触发「未知字段」错误。
func TestExampleConfigIsValid(t *testing.T) {
	clearEnv(t)

	data, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Skipf("找不到样例配置文件: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	cfg, err := config.LoadFrom(path)
	if err != nil {
		t.Fatalf("样例配置文件无法装载（说明样例与实际字段不一致）: %v", err)
	}
	if !cfg.SearchConfigured() {
		t.Log("样例中搜索密钥为空，搜索工具将降级——这是预期行为")
	}
	if cfg.OpenMeteoBaseURL == "" || cfg.GeocodingBaseURL == "" {
		t.Error("样例应包含天气服务的默认地址")
	}
}
