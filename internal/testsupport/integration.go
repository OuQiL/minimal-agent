package testsupport

import (
	"os"
	"testing"

	"minimal-agent/internal/config"
)

// 控制集成测试开关的环境变量。
const (
	// EnvEnable 强制启用需要真实外部服务的测试。
	// 用于「只验证免密钥的 Open-Meteo 链路」这类场景。
	EnvEnable = "AGENT_INTEGRATION"
	// EnvSkip 强制跳过，优先级高于一切。
	// 用于「环境里有密钥但不想让测试真的联网花钱」的场合。
	EnvSkip = "AGENT_SKIP_INTEGRATION"
)

// IntegrationConfig 装载真实配置，并决定需要真实外部服务的测试是否应当运行。
//
// 门控规则（按顺序）：
//  1. 设置了 AGENT_SKIP_INTEGRATION → 跳过
//  2. 处于 -short 模式 → 跳过
//  3. 设置了 AGENT_INTEGRATION → 运行
//  4. 配置里存在模型密钥 → 运行
//  5. 否则 → 跳过
//
// 第 4 条是默认行为：配置好密钥后测试自动开始跑，不必额外设置开关。
// 这与「默认离线全绿」并不矛盾——跳过不是失败，不带密钥运行时
// go test ./... 仍然整体通过。
func IntegrationConfig(t *testing.T) config.Config {
	t.Helper()

	if v := os.Getenv(EnvSkip); v != "" {
		t.Skipf("设置了 %s，跳过需要真实外部服务的测试", EnvSkip)
	}
	if testing.Short() {
		t.Skip("处于 -short 模式，跳过需要真实外部服务的测试")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Skipf("配置装载失败，跳过需要真实外部服务的测试: %v", err)
	}

	if os.Getenv(EnvEnable) == "" && !cfg.SearchConfigured() && cfg.APIKey == "" {
		t.Skipf("未配置模型密钥与搜索密钥，跳过需要真实外部服务的测试。"+
			"配置 %s 或写入 config.yaml 后会自动启用；也可用 %s=1 只跑免密钥的用例",
			"OPENAI_API_KEY", EnvEnable)
	}
	return cfg
}

// RequireModelKey 在缺少模型密钥时跳过。
//
// 供确实需要调用模型的用例使用：AGENT_INTEGRATION=1 可以启用整组测试，
// 但那不该让「必须有模型」的用例在无密钥时失败。
func RequireModelKey(t *testing.T, cfg config.Config) {
	t.Helper()
	if cfg.APIKey == "" {
		t.Skipf("未配置模型密钥（%s 或 config.yaml 的 llm.api_key），跳过该用例", "OPENAI_API_KEY")
	}
}

// RequireSearchKey 在缺少搜索密钥时跳过。
func RequireSearchKey(t *testing.T, cfg config.Config) {
	t.Helper()
	if !cfg.SearchConfigured() {
		t.Skipf("未配置搜索密钥（%s 或 config.yaml 的 search.api_key），跳过该用例", "BOCHA_API_KEY")
	}
}
