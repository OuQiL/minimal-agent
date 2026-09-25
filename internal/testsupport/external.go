package testsupport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// BochaItem 是模拟博查搜索返回的单条结果。
type BochaItem struct {
	Name          string
	URL           string
	Snippet       string
	Summary       string
	SiteName      string
	DatePublished string
}

// ExternalService 在一个地址上同时模拟博查搜索与 Open-Meteo 天气。
//
// 三个端点靠路径区分（/v1/web-search、/v1/search、/v1/forecast），
// 因此测试可以把同一个地址同时注入搜索工具与天气工具。
//
// 它同时记录收到的调用序列，供断言「天气工具先做地理编码、再取预报」。
type ExternalService struct {
	Server *httptest.Server

	mu sync.Mutex
	// 搜索
	bochaItems      []BochaItem
	bochaStatus     int
	bochaFailTimes  int
	bochaAuthNeeded bool
	// 地理编码
	geoName   string
	geoLat    float64
	geoLon    float64
	geoEmpty  bool
	geoStatus int
	// 预报
	temp        float64
	weatherCode int
	wind        float64
	// 通用
	delay time.Duration
	calls []string
}

// NewExternalService 创建模拟外部服务，带有可用的默认响应。
func NewExternalService() *ExternalService {
	e := &ExternalService{
		bochaItems: []BochaItem{{
			Name:          "示例搜索结果",
			URL:           "https://example.com/a",
			Snippet:       "这是一条摘要。",
			Summary:       "这是一条较长的摘要。",
			SiteName:      "example.com",
			DatePublished: "2026-01-02T03:04:05+08:00",
		}},
		geoName:     "杭州",
		geoLat:      30.29365,
		geoLon:      120.16142,
		temp:        23.3,
		weatherCode: 2,
		wind:        3.6,
	}
	e.Server = httptest.NewServer(http.HandlerFunc(e.handle))
	return e
}

// URL 返回模拟服务的地址。
func (e *ExternalService) URL() string { return e.Server.URL }

// Close 关闭模拟服务。
func (e *ExternalService) Close() { e.Server.Close() }

// SetBochaItems 设置搜索结果。
func (e *ExternalService) SetBochaItems(items ...BochaItem) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bochaItems = items
}

// SetBochaStatus 设置搜索接口固定返回的状态码；0 表示正常。
func (e *ExternalService) SetBochaStatus(code int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bochaStatus = code
}

// SetBochaFailTimes 设置搜索接口前 n 次返回 500，之后恢复正常。
// 用于验证重试。
func (e *ExternalService) SetBochaFailTimes(n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bochaFailTimes = n
}

// SetGeocode 设置地理编码返回的地点。
func (e *ExternalService) SetGeocode(name string, lat, lon float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.geoName, e.geoLat, e.geoLon, e.geoEmpty = name, lat, lon, false
}

// SetGeocodeEmpty 让地理编码返回空结果。
func (e *ExternalService) SetGeocodeEmpty() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.geoEmpty = true
}

// SetForecast 设置预报返回的数据。
func (e *ExternalService) SetForecast(temp float64, code int, wind float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.temp, e.weatherCode, e.wind = temp, code, wind
}

// SetDelay 设置每个响应的延迟，用于超时用例。
func (e *ExternalService) SetDelay(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.delay = d
}

// Calls 返回收到的请求序列，格式为「方法 路径」。
func (e *ExternalService) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calls))
	copy(out, e.calls)
	return out
}

// Paths 只返回请求路径序列，便于断言调用顺序。
func (e *ExternalService) Paths() []string {
	var out []string
	for _, c := range e.Calls() {
		if _, p, ok := strings.Cut(c, " "); ok {
			out = append(out, p)
		}
	}
	return out
}

func (e *ExternalService) record(r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, r.Method+" "+r.URL.Path)
}

func (e *ExternalService) wait() {
	e.mu.Lock()
	d := e.delay
	e.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
}

func (e *ExternalService) handle(w http.ResponseWriter, r *http.Request) {
	e.record(r)
	e.wait()

	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/web-search":
		e.handleBocha(w)
	case "/v1/search":
		e.handleGeocode(w)
	case "/v1/forecast":
		e.handleForecast(w)
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":"未知路径 %s"}`, r.URL.Path)
	}
}

func (e *ExternalService) handleBocha(w http.ResponseWriter) {
	e.mu.Lock()
	status, failTimes, items := e.bochaStatus, e.bochaFailTimes, e.bochaItems
	if failTimes > 0 {
		e.bochaFailTimes--
	}
	e.mu.Unlock()

	if failTimes > 0 {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"模拟的服务端错误"}`)
		return
	}
	if status != 0 {
		w.WriteHeader(status)
		fmt.Fprint(w, `{"error":"模拟的错误"}`)
		return
	}

	values := make([]map[string]any, 0, len(items))
	for _, it := range items {
		values = append(values, map[string]any{
			"name": it.Name, "url": it.URL, "snippet": it.Snippet,
			"summary": it.Summary, "siteName": it.SiteName,
			"datePublished": it.DatePublished,
			// 真实的博查响应里也有这个字段，且带 UTC 标注缺陷。
			// 一并给出，用于确认实现没有误用它。
			"dateLastCrawled": "2026-01-02T03:04:05Z",
		})
	}
	writeJSON(w, map[string]any{
		"code": 200,
		"data": map[string]any{
			"webPages": map[string]any{
				"totalEstimatedMatches": 123,
				"value":                 values,
			},
		},
	})
}

func (e *ExternalService) handleGeocode(w http.ResponseWriter) {
	e.mu.Lock()
	empty, status := e.geoEmpty, e.geoStatus
	name, lat, lon := e.geoName, e.geoLat, e.geoLon
	e.mu.Unlock()

	if status != 0 {
		w.WriteHeader(status)
		fmt.Fprint(w, `{"error":"模拟的错误"}`)
		return
	}
	if empty {
		writeJSON(w, map[string]any{})
		return
	}
	writeJSON(w, map[string]any{
		"results": []map[string]any{{
			"name": name, "latitude": lat, "longitude": lon,
			"country": "中国", "admin1": "浙江", "timezone": "Asia/Shanghai",
		}},
	})
}

func (e *ExternalService) handleForecast(w http.ResponseWriter) {
	e.mu.Lock()
	temp, code, wind := e.temp, e.weatherCode, e.wind
	e.mu.Unlock()

	writeJSON(w, map[string]any{
		"current_units": map[string]any{"temperature_2m": "°C", "wind_speed_10m": "km/h"},
		"current": map[string]any{
			"time":           "2026-01-02T11:00",
			"temperature_2m": temp,
			"weather_code":   code,
			"wind_speed_10m": wind,
		},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	w.Write(b)
}
