package tool_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"minimal-agent/internal/testsupport"
	"minimal-agent/internal/tool"
)

// 本文件的用例会访问真实外部服务，未配置时自动跳过。
// 门控规则见 testsupport.IntegrationConfig。

// 天气输出里的温度应当是一个真实数值。
var tempPattern = regexp.MustCompile(`温度：-?\d+(\.\d+)?`)

// Open-Meteo 无需密钥，因此这条链路只要放开集成测试就能验证。
func TestIntegration_WeatherReturnsRealData(t *testing.T) {
	cfg := testsupport.IntegrationConfig(t)

	wt := tool.NewWeather(cfg.OpenMeteoBaseURL, cfg.GeocodingBaseURL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ToolTimeout*2)
	defer cancel()

	out, err := wt.Execute(ctx, weatherArgs("北京"))
	if err != nil {
		t.Fatalf("真实天气查询失败: %v", err)
	}

	if strings.Contains(out, "未找到") {
		t.Fatalf("北京应当能被解析为地点，实际输出：%s", out)
	}
	if !tempPattern.MatchString(out) {
		t.Errorf("输出中应含真实温度数值，实际：%s", out)
	}
	// 天气码必须被翻译，而不是把机器码透传给模型。
	if strings.Contains(out, "weather_code") {
		t.Errorf("不应把原始字段名暴露给模型，实际：%s", out)
	}
	if !strings.Contains(out, "external-content") {
		t.Errorf("外部内容应被边界标记包裹，实际：%s", out)
	}
	t.Logf("真实天气返回：%s", oneLineForLog(out))
}

// 用英文城市名再验一次，确认地理编码不依赖中文。
func TestIntegration_WeatherAcceptsEnglishCityName(t *testing.T) {
	cfg := testsupport.IntegrationConfig(t)

	wt := tool.NewWeather(cfg.OpenMeteoBaseURL, cfg.GeocodingBaseURL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ToolTimeout*2)
	defer cancel()

	out, err := wt.Execute(ctx, weatherArgs("Tokyo"))
	if err != nil {
		t.Fatalf("真实天气查询失败: %v", err)
	}
	if !tempPattern.MatchString(out) {
		t.Errorf("输出中应含真实温度数值，实际：%s", out)
	}
	t.Logf("东京天气返回：%s", oneLineForLog(out))
}

func TestIntegration_WeatherUnknownPlaceIsReadable(t *testing.T) {
	cfg := testsupport.IntegrationConfig(t)

	wt := tool.NewWeather(cfg.OpenMeteoBaseURL, cfg.GeocodingBaseURL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ToolTimeout*2)
	defer cancel()

	out, err := wt.Execute(ctx, weatherArgs("zzzqqq这个地名不存在zzz"))
	if err != nil {
		t.Fatalf("未找到地点不应报错，实际：%v", err)
	}
	if !strings.Contains(out, "未找到") {
		t.Errorf("应给出可读的未找到说明，实际：%s", out)
	}
}

func TestIntegration_SearchReturnsRealResults(t *testing.T) {
	cfg := testsupport.IntegrationConfig(t)
	testsupport.RequireSearchKey(t, cfg)

	st := tool.NewSearch(cfg.BochaAPIKey, cfg.BochaBaseURL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ToolTimeout*3)
	defer cancel()

	out, err := st.Execute(ctx, searchArgs("杭州西湖"))
	if err != nil {
		t.Fatalf("真实搜索失败: %v", err)
	}

	if strings.Contains(out, "未配置") {
		t.Fatalf("已配置密钥却返回未配置说明：%s", out)
	}
	if !strings.Contains(out, "http") {
		t.Errorf("搜索结果中应含链接，实际：%s", oneLineForLog(out))
	}
	if !strings.Contains(out, "external-content") {
		t.Errorf("外部内容应被边界标记包裹")
	}
	t.Logf("真实搜索返回：%s", oneLineForLog(out))
}

// 搜索结果往往很长，必须被截断后才回填——否则一次搜索就能撑爆上下文。
func TestIntegration_SearchResultIsBounded(t *testing.T) {
	cfg := testsupport.IntegrationConfig(t)
	testsupport.RequireSearchKey(t, cfg)

	st := tool.NewSearch(cfg.BochaAPIKey, cfg.BochaBaseURL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ToolTimeout*3)
	defer cancel()

	out, err := st.Execute(ctx, searchArgs("人工智能 最新进展"))
	if err != nil {
		t.Fatalf("真实搜索失败: %v", err)
	}
	// 留出截断标注的余量。
	if n := len([]rune(out)); n > tool.DefaultMaxTotalChars+200 {
		t.Errorf("输出长度 %d 明显超出总量上限 %d", n, tool.DefaultMaxTotalChars)
	}
	t.Logf("返回长度 %d 字符（上限 %d）", len([]rune(out)), tool.DefaultMaxTotalChars)
}

// oneLineForLog 把多行输出压成一行，便于在测试日志里查看。
func oneLineForLog(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 400 {
		return string(r[:400]) + "……"
	}
	return s
}
