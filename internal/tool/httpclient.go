package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPDoer 是发起 HTTP 请求所需的最小接口。
//
// 工具依赖这个接口而非具体的 *http.Client，使测试可以把请求导向
// httptest.Server。这样测试跑的是真实的解析代码路径，只是数据来自
// 可控服务器——比替换工具输出更能捕获解析缺陷。
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

const (
	// maxAttempts 是外部调用的总尝试次数（含首次）。
	maxAttempts = 3
	// baseBackoff 是退避基数，逐次翻倍。
	baseBackoff = 300 * time.Millisecond
	// maxBodyBytes 限制读取的响应体大小，避免异常响应耗尽内存。
	maxBodyBytes = 1 << 20 // 1 MiB
)

// ExternalError 表示一次外部服务调用失败，并带上足够定位问题的上下文。
type ExternalError struct {
	Service string
	Status  int
	Detail  string
}

func (e *ExternalError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("%s 返回错误（HTTP %d）：%s", e.Service, e.Status, e.Detail)
	}
	return fmt.Sprintf("%s 调用失败：%s", e.Service, e.Detail)
}

// DoJSON 发起一次 JSON 请求并把响应解码到 out。
//
// 重试策略：网络错误与 429、5xx 视为可重试，做有限次退避；其余 4xx 立即返回。
// 鉴权类错误重试一万次也是同样的结果，快速失败并把原因说清楚，信息量更大。
func DoJSON(ctx context.Context, client HTTPDoer, service, method, url string,
	headers map[string]string, body, out any) error {

	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("序列化请求体失败: %w", err)
		}
		payload = b
	}

	var lastErr error
	for attempt := range maxAttempts {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s 调用被取消或超时: %w", service, err)
		}
		if attempt > 0 {
			backoff := baseBackoff << (attempt - 1)
			select {
			case <-ctx.Done():
				return fmt.Errorf("%s 调用被取消或超时: %w", service, ctx.Err())
			case <-time.After(backoff):
			}
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("构造请求失败: %w", err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = &ExternalError{Service: service, Detail: err.Error()}
			continue // 网络层错误可重试
		}

		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		resp.Body.Close()

		if readErr != nil {
			lastErr = &ExternalError{Service: service, Detail: readErr.Error()}
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = &ExternalError{
				Service: service,
				Status:  resp.StatusCode,
				Detail:  snippet(data),
			}
			continue // 限流与服务端错误可重试
		}
		if resp.StatusCode >= 400 {
			// 鉴权、参数等错误重试无意义，直接失败。
			return &ExternalError{
				Service: service,
				Status:  resp.StatusCode,
				Detail:  snippet(data),
			}
		}

		if out == nil {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return &ExternalError{
				Service: service,
				Detail:  fmt.Sprintf("响应无法解析：%v；原始内容：%s", err, snippet(data)),
			}
		}
		return nil
	}

	if lastErr == nil {
		lastErr = &ExternalError{Service: service, Detail: "重试次数已用尽"}
	}
	return fmt.Errorf("%s（已重试 %d 次）: %w", service, maxAttempts-1, lastErr)
}

// snippet 截取一段内容用于错误信息，避免把整个响应体塞进日志。
func snippet(b []byte) string {
	s := string(b)
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "……"
	}
	return s
}
