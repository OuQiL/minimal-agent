package tool_test

import (
	"strings"
	"testing"

	"minimal-agent/internal/tool"
)

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r := tool.NewRegistry()
	calc := okTool("calculator")
	if err := r.Register(calc); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	got, found, viaAlias := r.Lookup("calculator")
	if !found {
		t.Fatal("按规范名应能查到工具")
	}
	if got.Name() != "calculator" {
		t.Errorf("查到的工具 = %q", got.Name())
	}
	if viaAlias {
		t.Error("按规范名查找不应报告别名命中")
	}

	if names := r.Names(); len(names) != 1 || names[0] != "calculator" {
		t.Errorf("工具清单 = %v", names)
	}
}

func TestRegistry_RejectsIncompleteDeclaration(t *testing.T) {
	cases := []struct {
		name string
		tool *fakeTool
	}{
		{"缺少名称", &fakeTool{desc: "有描述", schema: tool.Schema{Type: "object"}}},
		{"缺少描述", &fakeTool{name: "n", schema: tool.Schema{Type: "object"}}},
		{"缺少参数 Schema", &fakeTool{name: "n", desc: "有描述"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tool.NewRegistry().Register(tc.tool); err == nil {
				t.Fatal("声明不完整的工具应被拒绝注册")
			}
		})
	}
}

func TestRegistry_RejectsDuplicateNameOrAlias(t *testing.T) {
	r := tool.NewRegistry()
	a := okTool("search")
	a.aliases = []string{"web_search"}
	if err := r.Register(a); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	dup := okTool("search")
	if err := r.Register(dup); err == nil {
		t.Error("重复的规范名应被拒绝")
	}

	aliasDup := okTool("another")
	aliasDup.aliases = []string{"web_search"}
	if err := r.Register(aliasDup); err == nil {
		t.Error("与已有工具冲突的别名应被拒绝")
	}
}

// 同一工具内部归一化后重叠的别名不算冲突——它们指向同一个工具。
// 例如 get_weather 与 getWeather 归一化后完全相同。
func TestRegistry_ToleratesOverlappingOwnAliases(t *testing.T) {
	r := tool.NewRegistry()
	w := okTool("get_weather")
	w.aliases = []string{"getWeather", "get-weather", "get_weather"}
	if err := r.Register(w); err != nil {
		t.Fatalf("同一工具内部重叠的别名不应被拒绝: %v", err)
	}

	for _, in := range []string{"getWeather", "get-weather", "get_weather"} {
		got, found, _ := r.Lookup(in)
		if !found || got.Name() != "get_weather" {
			t.Errorf("%q 应解析到 get_weather", in)
		}
	}
}

// 但跨工具的别名冲突必须拒绝：否则模型请求的名称会指向错误的工具。
func TestRegistry_RejectsCrossToolAliasConflict(t *testing.T) {
	r := tool.NewRegistry()
	a := okTool("alpha")
	a.aliases = []string{"shared"}
	if err := r.Register(a); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	b := okTool("beta")
	b.aliases = []string{"shared"}
	err := r.Register(b)
	if err == nil {
		t.Fatal("跨工具的别名冲突应被拒绝")
	}
	if !strings.Contains(err.Error(), "alpha") {
		t.Errorf("错误信息应指明冲突对象，实际：%v", err)
	}
}

// 归一化是为了兜住模型对工具名的常见变体写法。
func TestRegistry_NormalizationVariants(t *testing.T) {
	r := tool.NewRegistry()
	weather := okTool("weather")
	weather.aliases = []string{"get_weather", "查询天气"}
	if err := r.Register(weather); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	cases := []struct {
		input     string
		viaAlias  bool
		rationale string
	}{
		{"weather", false, "规范名"},
		{"Weather", false, "大小写变体"},
		{"WEATHER", false, "全大写"},
		{"get_weather", true, "下划线别名"},
		{"getWeather", true, "驼峰写法归一化后与别名一致"},
		{"Get-Weather", true, "连字符与大小写混合"},
		{"  weather  ", false, "首尾空白"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, found, viaAlias := r.Lookup(tc.input)
			if !found {
				t.Fatalf("应能解析 %q（%s）", tc.input, tc.rationale)
			}
			if got.Name() != "weather" {
				t.Errorf("解析到 %q，期望 weather", got.Name())
			}
			if viaAlias != tc.viaAlias {
				t.Errorf("别名命中 = %v，期望 %v（%s）", viaAlias, tc.viaAlias, tc.rationale)
			}
		})
	}
}

// 不做模糊匹配：把拼写错误当成正确调用会掩盖模型正在幻觉工具名这一信号。
func TestRegistry_NoFuzzyMatching(t *testing.T) {
	r := tool.NewRegistry()
	weather := okTool("weather")
	weather.aliases = []string{"get_weather"}
	if err := r.Register(weather); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	for _, near := range []string{"wether", "weathr", "wheather", "weathers", "get_wether"} {
		t.Run(near, func(t *testing.T) {
			if _, found, _ := r.Lookup(near); found {
				t.Errorf("%q 与 weather 拼写相近但非别名，不应被匹配", near)
			}
		})
	}
}

func TestRegistry_UnknownMessageListsCandidates(t *testing.T) {
	r := tool.NewRegistry()
	for _, n := range []string{"calculator", "weather", "todo"} {
		if err := r.Register(okTool(n)); err != nil {
			t.Fatalf("注册失败: %v", err)
		}
	}

	msg := r.UnknownMessage("get_wether")

	if !strings.Contains(msg, "get_wether") {
		t.Error("提示中应包含模型请求的名称")
	}
	for _, n := range []string{"calculator", "weather", "todo"} {
		if !strings.Contains(msg, n) {
			t.Errorf("提示中应列出可用工具 %q，实际：%s", n, msg)
		}
	}
}

func TestRegistry_AllPreservesRegistrationOrder(t *testing.T) {
	r := tool.NewRegistry()
	order := []string{"calculator", "search", "weather", "todo"}
	for _, n := range order {
		if err := r.Register(okTool(n)); err != nil {
			t.Fatalf("注册失败: %v", err)
		}
	}
	all := r.All()
	if len(all) != len(order) {
		t.Fatalf("工具数量 = %d，期望 %d", len(all), len(order))
	}
	for i, want := range order {
		if all[i].Name() != want {
			t.Errorf("第 %d 个工具 = %q，期望 %q", i, all[i].Name(), want)
		}
	}
}
