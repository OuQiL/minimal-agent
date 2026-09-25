package tool

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// Validate 按 Schema 校验模型给出的参数。
//
// 返回的错误是给模型看的，因此措辞要能指导它如何纠正，而不是给开发者看的
// 调试信息——校验失败时这条错误会原样作为工具结果回填。
func Validate(schema Schema, args json.RawMessage) error {
	raw := strings.TrimSpace(string(args))
	if raw == "" {
		raw = "{}"
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return fmt.Errorf("参数不是合法的 JSON 对象：%v。请提供一个 JSON 对象。", err)
	}

	for _, name := range schema.Required {
		v, ok := fields[name]
		if !ok || isJSONNull(v) {
			return fmt.Errorf("缺少必填参数 %q。", name)
		}
	}

	// 按名称排序后校验，使同一份输入总是产生同样的错误信息，
	// 避免因 map 遍历顺序不定造成测试与日志抖动。
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		prop, declared := schema.Properties[name]
		if !declared {
			continue // 未声明的多余参数不阻断执行，交由工具自行忽略
		}
		if err := validateOne(name, prop, fields[name]); err != nil {
			return err
		}
	}
	return nil
}

func validateOne(name string, prop Property, raw json.RawMessage) error {
	if isJSONNull(raw) {
		return nil
	}

	switch prop.Type {
	case "string":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("参数 %q 应为字符串，实际收到 %s。", name, describe(raw))
		}
		if len(prop.Enum) > 0 && !slices.Contains(prop.Enum, s) {
			return fmt.Errorf("参数 %q 的取值 %q 不在允许范围内，可选值为：%s。",
				name, s, strings.Join(prop.Enum, "、"))
		}
	case "number":
		if _, err := asNumber(raw); err != nil {
			return fmt.Errorf("参数 %q 应为数字，实际收到 %s。", name, describe(raw))
		}
	case "integer":
		f, err := asNumber(raw)
		if err != nil {
			return fmt.Errorf("参数 %q 应为整数，实际收到 %s。", name, describe(raw))
		}
		if f != math.Trunc(f) {
			return fmt.Errorf("参数 %q 应为整数，实际收到 %v。", name, f)
		}
	default:
		// 未支持的类型不做判断，避免误伤。
		return nil
	}
	return nil
}

func asNumber(raw json.RawMessage) (float64, error) {
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, err
	}
	return f, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// describe 用模型能理解的方式描述它实际给出的值。
func describe(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	switch v.(type) {
	case []any:
		return "数组"
	case map[string]any:
		return "对象"
	case bool:
		return "布尔值"
	default:
		return fmt.Sprintf("%v", v)
	}
}
