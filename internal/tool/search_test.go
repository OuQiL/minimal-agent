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

func searchArgs(query string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"query": query})
	return b
}

func TestSearch_ParsesBochaResponse(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()
	svc.SetBochaItems(testsupport.BochaItem{
		Name:          "杭州亚运会",
		URL:           "https://example.com/asiad",
		Snippet:       "短摘要",
		Summary:       "长摘要内容",
		SiteName:      "example.com",
		DatePublished: "2026-01-02T03:04:05+08:00",
	})

	st := tool.NewSearch("test-key", svc.URL(), nil)
	out, err := st.Execute(context.Background(), searchArgs("杭州亚运会"))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	for _, want := range []string{"杭州亚运会", "https://example.com/asiad", "长摘要内容", "example.com", "2026-01-02"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出中缺少 %q，实际：%s", want, out)
		}
	}
}

// 博查的 dateLastCrawled 以 Z 结尾但实际是 UTC+8，官方文档已标注该缺陷。
// 实现应当使用 datePublished，不采用这个字段。
func TestSearch_DoesNotUseBuggyDateLastCrawled(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()
	svc.SetBochaItems(testsupport.BochaItem{
		Name:          "标题",
		URL:           "https://example.com/x",
		Summary:       "摘要",
		DatePublished: "2026-01-02T03:04:05+08:00",
	})

	st := tool.NewSearch("test-key", svc.URL(), nil)
	out, err := st.Execute(context.Background(), searchArgs("查询"))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	// fixture 里的 dateLastCrawled 是 2026-01-02T03:04:05Z；
	// 若实现误用它，输出里会出现这个带 Z 的时间。
	if strings.Contains(out, "03:04:05Z") {
		t.Errorf("输出不应包含带缺陷的 dateLastCrawled 字段，实际：%s", out)
	}
	if !strings.Contains(out, "+08:00") && !strings.Contains(out, "2026-01-02") {
		t.Errorf("输出应包含 datePublished 的时间，实际：%s", out)
	}
}

// 未配置凭据时返回可读说明而非报错：模型据此告知用户，也不会反复重试。
func TestSearch_MissingKeyDegradesGracefully(t *testing.T) {
	st := tool.NewSearch("", "http://127.0.0.1:1", nil)

	out, err := st.Execute(context.Background(), searchArgs("任何查询"))

	if err != nil {
		t.Fatalf("未配置凭据不应报错，实际：%v", err)
	}
	if !strings.Contains(out, "BOCHA_API_KEY") {
		t.Errorf("说明中应指明缺失的配置项，实际：%s", out)
	}
}

func TestSearch_EmptyResultIsReadable(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()
	svc.SetBochaItems()

	st := tool.NewSearch("k", svc.URL(), nil)
	out, err := st.Execute(context.Background(), searchArgs("冷门查询"))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "没有找到") {
		t.Errorf("无结果时应给出可读说明，实际：%s", out)
	}
}

func TestSearch_ResultIsTruncated(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()
	svc.SetBochaItems(testsupport.BochaItem{
		Name:    "超长结果",
		URL:     "https://example.com/big",
		Summary: strings.Repeat("很长的摘要内容", 200),
	})

	st := tool.NewSearch("k", svc.URL(), nil)
	out, err := st.Execute(context.Background(), searchArgs("查询"))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if n := len([]rune(out)); n > tool.DefaultMaxTotalChars+200 {
		t.Errorf("输出长度 = %d，明显超出总量上限 %d", n, tool.DefaultMaxTotalChars)
	}
	if !strings.Contains(out, "截断") {
		t.Errorf("截断必须被标注，实际输出：%s", out[:min(200, len(out))])
	}
}

func TestSearch_WrapsExternalContent(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()

	st := tool.NewSearch("k", svc.URL(), nil)
	out, err := st.Execute(context.Background(), searchArgs("查询"))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "external-content") {
		t.Errorf("外部内容应被边界标记包裹，实际：%s", out)
	}
}

// 限流与服务端错误应重试，恢复后正常返回。
func TestSearch_RetriesServerError(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()
	svc.SetBochaFailTimes(2)

	st := tool.NewSearch("k", svc.URL(), nil)
	_, err := st.Execute(context.Background(), searchArgs("查询"))
	if err != nil {
		t.Fatalf("重试后本应成功，实际：%v", err)
	}
	if got := len(svc.Calls()); got != 3 {
		t.Errorf("服务端收到 %d 次请求，期望 3 次（两败一成）", got)
	}
}

// 鉴权失败重试无意义，应快速失败。
func TestSearch_AuthFailureDoesNotRetry(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()
	svc.SetBochaStatus(401)

	st := tool.NewSearch("bad-key", svc.URL(), nil)
	_, err := st.Execute(context.Background(), searchArgs("查询"))
	if err == nil {
		t.Fatal("鉴权失败应返回错误")
	}
	if got := len(svc.Calls()); got != 1 {
		t.Errorf("鉴权失败不应重试，实际请求 %d 次", got)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误信息应包含状态码，实际：%v", err)
	}
}

func TestSearch_RespectsContextTimeout(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()
	svc.SetDelay(2 * time.Second)

	st := tool.NewSearch("k", svc.URL(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := st.Execute(ctx, searchArgs("查询"))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("超时应返回错误")
	}
	if elapsed > time.Second {
		t.Errorf("耗时 %v，说明未及时响应超时", elapsed)
	}
}

func TestSearch_SendsCountAndAuth(t *testing.T) {
	svc := testsupport.NewExternalService()
	defer svc.Close()

	st := tool.NewSearch("secret-key", svc.URL(), nil)
	if _, err := st.Execute(context.Background(), searchArgs("查询")); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if paths := svc.Paths(); len(paths) != 1 || paths[0] != "/v1/web-search" {
		t.Errorf("请求路径 = %v，期望 [/v1/web-search]", paths)
	}
}
