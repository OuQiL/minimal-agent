package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/openai/openai-go"
)

// ErrorKind 是模型请求失败的大类，决定是否值得重试以及如何向用户解释。
type ErrorKind int

// 错误分类。
const (
	KindUnknown ErrorKind = iota
	// KindAuth 是密钥无效或权限不足。重试一万次也是同样的结果，快速失败。
	KindAuth
	// KindBadRequest 是请求格式错误。重试无意义，但要记录请求体以便排查。
	KindBadRequest
	// KindRateLimit 与 KindServer 由 SDK 负责退避重试，耗尽后才到达这里。
	KindRateLimit
	KindServer
	KindNetwork
	KindTimeout
)

// RequestError 是一次模型请求失败的归类结果。
type RequestError struct {
	Kind       ErrorKind
	StatusCode int
	Message    string
	Err        error
}

func (e *RequestError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s：%s", e.Hint(), e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s：%v", e.Hint(), e.Err)
	}
	return e.Hint()
}

func (e *RequestError) Unwrap() error { return e.Err }

// Hint 返回面向用户的说明，指向具体的排查方向。
func (e *RequestError) Hint() string {
	switch e.Kind {
	case KindAuth:
		return "模型服务鉴权失败（HTTP " + itoa(e.StatusCode) + "），请检查 OPENAI_API_KEY 是否有效，以及 OPENAI_BASE_URL 是否指向正确的服务"
	case KindBadRequest:
		return "模型服务拒绝了请求（HTTP 400），通常是模型名或请求参数不被支持，请检查 OPENAI_MODEL"
	case KindRateLimit:
		return "模型服务限流（HTTP 429），重试后仍未成功，请稍后再试"
	case KindServer:
		return "模型服务返回服务端错误（HTTP " + itoa(e.StatusCode) + "），重试后仍未成功"
	case KindTimeout:
		return "模型请求超时"
	case KindNetwork:
		return "无法连接模型服务，请检查网络与 OPENAI_BASE_URL"
	default:
		return "模型请求失败"
	}
}

// Retryable 报告这类错误是否值得再试一次。
//
// 注意：这只描述「再试一次是否有意义」，不代表调用方应该重试。
// 流式响应在已向用户输出内容之后一律不重试，见 StreamInterruptedError。
func (e *RequestError) Retryable() bool {
	switch e.Kind {
	case KindRateLimit, KindServer, KindNetwork, KindTimeout:
		return true
	default:
		return false
	}
}

// StreamInterruptedError 表示流式响应在已输出部分内容之后中断。
//
// 这类错误不重试：token 已经打印到终端，重试要么导致内容重复，
// 要么需要擦除已显示的内容，两者都劣于如实告知用户。
//
// 已产出的两部分分开携带：思维链只落库供查看，正文才是用户当作答复看过的内容。
type StreamInterruptedError struct {
	// EmittedChars 是已向用户输出的字符数。
	EmittedChars int
	Reasoning    string
	Content      string
	Err          error
}

func (e *StreamInterruptedError) Error() string {
	return fmt.Sprintf("响应在已输出 %d 个字符后中断：%v", e.EmittedChars, e.Err)
}

func (e *StreamInterruptedError) Unwrap() error { return e.Err }

// Classify 把底层错误归类为 RequestError。
func Classify(err error) error {
	if err == nil {
		return nil
	}

	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		kind := KindUnknown
		switch {
		case apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden:
			kind = KindAuth
		case apiErr.StatusCode == http.StatusBadRequest:
			kind = KindBadRequest
		case apiErr.StatusCode == http.StatusTooManyRequests:
			kind = KindRateLimit
		case apiErr.StatusCode >= 500:
			kind = KindServer
		}
		return &RequestError{
			Kind:       kind,
			StatusCode: apiErr.StatusCode,
			Message:    apiErr.Message,
			Err:        err,
		}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return &RequestError{Kind: KindTimeout, Err: err}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return &RequestError{Kind: KindTimeout, Err: err}
		}
		return &RequestError{Kind: KindNetwork, Err: err}
	}
	return &RequestError{Kind: KindUnknown, Err: err}
}

func itoa(n int) string {
	if n == 0 {
		return "?"
	}
	return fmt.Sprintf("%d", n)
}
