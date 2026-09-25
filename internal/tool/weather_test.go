package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"minimal-agent/internal/testsupport"
	"minimal-agent/internal/tool"
)

func weatherArgs(city string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"city": city})
	return b
}

func TestWeather_GeocodesThenFetchesForecast(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()
	svc.SetGeocode("杭州", 30.29, 120.16)
	svc.SetForecast(23.3, 2, 3.6)

	wt := tool.NewWeather(svc.URL(), svc.URL(), nil)
	out, err := wt.Execute(context.Background(), weatherArgs("杭州"))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	// 先地理编码、再取预报，顺序不能颠倒。
	paths := svc.Paths()
	if len(paths) != 2 {
		t.Fatalf("调用次数 = %d，期望 2 次：%v", len(paths), paths)
	}
	if paths[0] != "/v1/search" || paths[1] != "/v1/forecast" {
		t.Errorf("调用顺序 = %v，期望先 /v1/search 再 /v1/forecast", paths)
	}

	for _, want := range []string{"杭州", "23.3", "3.6"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出中缺少 %q，实际：%s", want, out)
		}
	}
}

// 天气代码是机器码，对模型和用户都没有意义，必须翻译成文本。
func TestWeather_TranslatesWeatherCode(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{0, "晴"},
		{2, "局部多云"},
		{61, "小雨"},
		{95, "雷阵雨"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			svc := testsupport.NewExternalService()
			defer svc.Close()
			svc.SetForecast(20, tc.code, 1)

			wt := tool.NewWeather(svc.URL(), svc.URL(), nil)
			out, err := wt.Execute(context.Background(), weatherArgs("杭州"))
			if err != nil {
				t.Fatalf("执行失败: %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("天气码 %d 应翻译为 %q，实际输出：%s", tc.code, tc.want, out)
			}
			if strings.Contains(out, "weather_code") {
				t.Errorf("不应把原始字段名暴露给模型，实际：%s", out)
			}
		})
	}
}

func TestWeather_UnknownCodeIsReportedNotGuessed(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()
	svc.SetForecast(20, 12345, 1)

	wt := tool.NewWeather(svc.URL(), svc.URL(), nil)
	out, err := wt.Execute(context.Background(), weatherArgs("杭州"))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "未知") {
		t.Errorf("未知天气码应如实标注而非猜测，实际：%s", out)
	}
}

func TestWeather_GeocodeEmpty(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()
	svc.SetGeocodeEmpty()

	wt := tool.NewWeather(svc.URL(), svc.URL(), nil)
	out, err := wt.Execute(context.Background(), weatherArgs("不存在的地名"))
	if err != nil {
		t.Fatalf("未找到地点不应报错，实际：%v", err)
	}
	if !strings.Contains(out, "未找到") {
		t.Errorf("应给出「未找到该地点」的说明，实际：%s", out)
	}
	// 地点都找不到，就不该再去请求预报。
	if paths := svc.Paths(); len(paths) != 1 {
		t.Errorf("地理编码失败后不应继续请求预报，实际调用 %v", paths)
	}
}

func TestWeather_WrapsExternalContent(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()

	wt := tool.NewWeather(svc.URL(), svc.URL(), nil)
	out, err := wt.Execute(context.Background(), weatherArgs("杭州"))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "external-content") {
		t.Errorf("外部内容应被边界标记包裹，实际：%s", out)
	}
}

func TestWeather_ServiceErrorIsReported(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()
	svc.SetGeocodeEmpty()
	svc.SetDelay(3 * time.Second)

	wt := tool.NewWeather(svc.URL(), svc.URL(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if _, err := wt.Execute(ctx, weatherArgs("杭州")); err == nil {
		t.Fatal("超时应返回错误")
	}
}

func TestWeather_Metadata(t *testing.T) {
	wt := tool.NewWeather("http://a", "http://b", nil)
	if wt.Name() != "weather" {
		t.Errorf("名称 = %q", wt.Name())
	}
	if _, ok := wt.Parameters().Properties["city"]; !ok {
		t.Error("应声明 city 参数")
	}
	if len(wt.Aliases()) == 0 {
		t.Error("应声明至少一个别名")
	}
}
