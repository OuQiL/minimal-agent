package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// WeatherTool 通过 Open-Meteo 查询真实天气。
//
// 需要两次调用：先把城市名解析成经纬度（地理编码），再取该坐标的当前天气。
// Open-Meteo 无需 API Key，因此本工具在任何环境下都可直接使用。
//
// 两个端点均可注入，测试时指向 httptest.Server。
type WeatherTool struct {
	forecastURL string
	geocodeURL  string
	client      HTTPDoer
}

// NewWeather 创建天气工具。client 为 nil 时使用默认 HTTP 客户端。
func NewWeather(forecastURL, geocodeURL string, client HTTPDoer) *WeatherTool {
	if client == nil {
		client = http.DefaultClient
	}
	return &WeatherTool{
		forecastURL: strings.TrimRight(forecastURL, "/"),
		geocodeURL:  strings.TrimRight(geocodeURL, "/"),
		client:      client,
	}
}

func (w *WeatherTool) Name() string { return "weather" }

func (w *WeatherTool) Description() string {
	return "查询指定城市的当前天气，返回温度、天气状况与风速。" +
		"当用户询问某地天气时使用。城市名可以是中文或英文。"
}

func (w *WeatherTool) Aliases() []string { return []string{"get_weather", "weather_query", "tianqi"} }

func (w *WeatherTool) Parameters() Schema {
	return Schema{
		Type: "object",
		Properties: map[string]Property{
			"city": {
				Type:        "string",
				Description: "城市名称，例如「北京」「杭州」或「Tokyo」。",
			},
		},
		Required: []string{"city"},
	}
}

// geocodeResponse 是地理编码接口的返回结构。
type geocodeResponse struct {
	Results []struct {
		Name      string  `json:"name"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Country   string  `json:"country"`
		Admin1    string  `json:"admin1"`
	} `json:"results"`
}

// forecastResponse 是预报接口的返回结构。
type forecastResponse struct {
	Current struct {
		Time        string  `json:"time"`
		Temperature float64 `json:"temperature_2m"`
		WeatherCode int     `json:"weather_code"`
		WindSpeed   float64 `json:"wind_speed_10m"`
	} `json:"current"`
	CurrentUnits struct {
		Temperature string `json:"temperature_2m"`
		WindSpeed   string `json:"wind_speed_10m"`
	} `json:"current_units"`
}

func (w *WeatherTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		City string `json:"city"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%v", err)
	}
	city := strings.TrimSpace(in.City)
	if city == "" {
		return "", fmt.Errorf("城市名称为空")
	}

	// 第一步：地名 → 经纬度
	geoQuery := url.Values{}
	geoQuery.Set("name", city)
	geoQuery.Set("count", "1")
	geoQuery.Set("language", "zh")
	geoQuery.Set("format", "json")

	var geo geocodeResponse
	if err := DoJSON(ctx, w.client, "Open-Meteo 地理编码", http.MethodGet,
		w.geocodeURL+"/v1/search?"+geoQuery.Encode(), nil, nil, &geo); err != nil {
		return "", err
	}
	if len(geo.Results) == 0 {
		return fmt.Sprintf("未找到地点 %q。请确认城市名称是否正确，或换用更常见的写法。", city), nil
	}
	loc := geo.Results[0]

	// 第二步：经纬度 → 当前天气
	fcQuery := url.Values{}
	fcQuery.Set("latitude", fmt.Sprintf("%g", loc.Latitude))
	fcQuery.Set("longitude", fmt.Sprintf("%g", loc.Longitude))
	fcQuery.Set("current", "temperature_2m,weather_code,wind_speed_10m")
	fcQuery.Set("timezone", "auto")

	var fc forecastResponse
	if err := DoJSON(ctx, w.client, "Open-Meteo 天气", http.MethodGet,
		w.forecastURL+"/v1/forecast?"+fcQuery.Encode(), nil, nil, &fc); err != nil {
		return "", err
	}

	place := loc.Name
	if loc.Admin1 != "" && loc.Admin1 != loc.Name {
		place += "（" + loc.Admin1 + "）"
	}
	if loc.Country != "" {
		place += " " + loc.Country
	}

	tempUnit := fc.CurrentUnits.Temperature
	if tempUnit == "" {
		tempUnit = "°C"
	}
	windUnit := fc.CurrentUnits.WindSpeed
	if windUnit == "" {
		windUnit = "km/h"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s 当前天气：\n", place)
	fmt.Fprintf(&b, "  天气：%s\n", weatherCodeText(fc.Current.WeatherCode))
	fmt.Fprintf(&b, "  温度：%g%s\n", fc.Current.Temperature, tempUnit)
	fmt.Fprintf(&b, "  风速：%g%s\n", fc.Current.WindSpeed, windUnit)
	fmt.Fprintf(&b, "  观测时间：%s\n", fc.Current.Time)
	fmt.Fprintf(&b, "  坐标：%g, %g", loc.Latitude, loc.Longitude)

	// 天气是外部数据，同样加边界标记——城市名可能来自用户或搜索结果，
	// 返回的地点名等内容不应被模型当作指令。
	return WrapExternal("open-meteo", b.String()), nil
}

// weatherCodeText 把 WMO 天气代码翻译成中文描述。
//
// 原始代码对模型和用户都没有意义，必须翻译；未知代码如实标注而不是猜测。
func weatherCodeText(code int) string {
	if s, ok := wmoCodes[code]; ok {
		return s
	}
	return fmt.Sprintf("未知天气代码（%d）", code)
}

var wmoCodes = map[int]string{
	0:  "晴",
	1:  "大部晴朗",
	2:  "局部多云",
	3:  "阴",
	45: "雾",
	48: "雾凇",
	51: "轻度毛毛雨",
	53: "中度毛毛雨",
	55: "强毛毛雨",
	56: "轻度冻毛毛雨",
	57: "强冻毛毛雨",
	61: "小雨",
	63: "中雨",
	65: "大雨",
	66: "轻度冻雨",
	67: "强冻雨",
	71: "小雪",
	73: "中雪",
	75: "大雪",
	77: "雪粒",
	80: "小阵雨",
	81: "中阵雨",
	82: "强阵雨",
	85: "小阵雪",
	86: "大阵雪",
	95: "雷阵雨",
	96: "雷阵雨伴小冰雹",
	99: "雷阵雨伴大冰雹",
}
