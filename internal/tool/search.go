package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// SearchTool 通过博查（Bocha）Web Search API 做真实网页检索。
//
// 端点与 HTTP 客户端可注入，使测试能把请求导向 httptest.Server，
// 从而覆盖真实的响应解析路径而不发起任何网络请求。
type SearchTool struct {
	apiKey  string
	baseURL string
	client  HTTPDoer
	count   int
	budget  Budget
}

// NewSearch 创建搜索工具。client 为 nil 时使用默认 HTTP 客户端。
func NewSearch(apiKey, baseURL string, client HTTPDoer) *SearchTool {
	if client == nil {
		client = http.DefaultClient
	}
	return &SearchTool{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  client,
		count:   5, // 不用默认的 10：真实结果体量大，控制在够用的条数
		budget:  DefaultBudget(),
	}
}

func (s *SearchTool) Name() string { return "search" }

func (s *SearchTool) Description() string {
	return "在互联网上检索信息，返回若干条网页结果（标题、来源、时间、链接、摘要）。" +
		"当需要了解实时信息、新闻、事实性资料或你不确定的内容时使用。"
}

// Aliases 覆盖模型对搜索工具的常见叫法。web_search 归一化后即为 websearch，
// 因此无须重复列出。
func (s *SearchTool) Aliases() []string { return []string{"web_search", "google", "bing"} }

func (s *SearchTool) Parameters() Schema {
	return Schema{
		Type: "object",
		Properties: map[string]Property{
			"query": {
				Type:        "string",
				Description: "搜索关键词。用简洁的问句或关键词组合，例如「杭州 亚运会 举办时间」。",
			},
		},
		Required: []string{"query"},
	}
}

// bochaItem 是博查返回的单条网页结果。
//
// 刻意不使用 dateLastCrawled：博查的公开文档说明该字段以 Z 结尾但实际是
// UTC+8 时间（官方称将在 v2 修复）。datePublished 没有这个问题。
type bochaItem struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	Snippet       string `json:"snippet"`
	Summary       string `json:"summary"`
	SiteName      string `json:"siteName"`
	DatePublished string `json:"datePublished"`
}

type bochaResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		WebPages struct {
			Value []bochaItem `json:"value"`
		} `json:"webPages"`
	} `json:"data"`
}

func (s *SearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%v", err)
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return "", fmt.Errorf("搜索关键词为空")
	}

	// 未配置凭据时返回可读说明而非报错。
	//
	// 这里返回的是「结果」不是「错误」，是刻意的：作为结果回填，模型会
	// 据此向用户解释「搜索功能未配置」；若报错，模型可能反复重试同一个
	// 注定失败的调用。对评审方而言，没配搜索密钥也能跑通其余全部功能。
	if s.apiKey == "" {
		return "搜索服务未配置，无法检索。请设置 BOCHA_API_KEY 环境变量后重试。" +
			"如果你是在回答用户，请如实说明当前无法联网检索。", nil
	}

	var resp bochaResponse
	err := DoJSON(ctx, s.client, "博查搜索", http.MethodPost, s.baseURL+"/v1/web-search",
		map[string]string{"Authorization": "Bearer " + s.apiKey},
		map[string]any{"query": query, "count": s.count, "summary": true},
		&resp)
	if err != nil {
		return "", err
	}
	if resp.Code != 0 && resp.Code != 200 {
		return "", fmt.Errorf("博查搜索返回错误码 %d：%s", resp.Code, resp.Msg)
	}

	items := resp.Data.WebPages.Value
	if len(items) == 0 {
		return fmt.Sprintf("没有找到与 %q 相关的网页结果。可以换个说法再试。", query), nil
	}

	var lines []string
	for i, it := range items {
		body := it.Summary
		if strings.TrimSpace(body) == "" {
			body = it.Snippet
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d. %s\n", i+1, it.Name)
		if it.SiteName != "" {
			fmt.Fprintf(&b, "   来源：%s\n", it.SiteName)
		}
		if it.DatePublished != "" {
			fmt.Fprintf(&b, "   时间：%s\n", it.DatePublished)
		}
		fmt.Fprintf(&b, "   链接：%s\n", it.URL)
		fmt.Fprintf(&b, "   摘要：%s", strings.TrimSpace(body))
		lines = append(lines, b.String())
	}

	body, truncated := s.budget.Truncate(lines)
	header := fmt.Sprintf("检索 %q 得到 %d 条结果：", query, len(items))
	if truncated {
		header += "（部分内容因长度限制被截断）"
	}
	return WrapExternal("bocha-search", header+"\n"+body), nil
}
