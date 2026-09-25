package tool_test

import (
	"encoding/json"
	"strings"
	"testing"

	"minimal-agent/internal/tool"
)

func TestValidate_AcceptsValidInput(t *testing.T) {
	schema := tool.Schema{
		Type: "object",
		Properties: map[string]tool.Property{
			"city":  {Type: "string"},
			"days":  {Type: "integer"},
			"ratio": {Type: "number"},
			"unit":  {Type: "string", Enum: []string{"c", "f"}},
		},
		Required: []string{"city"},
	}

	cases := []string{
		`{"city":"北京"}`,
		`{"city":"北京","days":3}`,
		`{"city":"北京","ratio":1.5}`,
		`{"city":"北京","unit":"c"}`,
		`{"city":"北京","days":3,"ratio":1.5,"unit":"f"}`,
		`{"city":"北京","extra":"未声明的参数被忽略"}`,
	}
	for _, in := range cases {
		if err := tool.Validate(schema, json.RawMessage(in)); err != nil {
			t.Errorf("输入 %s 应通过校验，实际报错: %v", in, err)
		}
	}
}

func TestValidate_RejectsBadInput(t *testing.T) {
	schema := tool.Schema{
		Type: "object",
		Properties: map[string]tool.Property{
			"city": {Type: "string"},
			"days": {Type: "integer"},
			"unit": {Type: "string", Enum: []string{"c", "f"}},
		},
		Required: []string{"city"},
	}

	cases := []struct {
		name string
		in   string
		want string // 错误信息中应当出现的关键词
	}{
		{"缺少必填", `{}`, "city"},
		{"必填为 null", `{"city":null}`, "city"},
		{"字符串类型不符", `{"city":123}`, "city"},
		{"字符串传了对象", `{"city":{"a":1}}`, "city"},
		{"整数传了小数", `{"city":"北京","days":1.5}`, "days"},
		{"整数传了字符串", `{"city":"北京","days":"3"}`, "days"},
		{"枚举越界", `{"city":"北京","unit":"k"}`, "unit"},
		{"非法 JSON", `{`, "JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tool.Validate(schema, json.RawMessage(tc.in))
			if err == nil {
				t.Fatalf("输入 %s 应被拒绝", tc.in)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误信息应包含 %q 以便模型定位，实际：%s", tc.want, err.Error())
			}
		})
	}
}

func TestValidate_EmptyArgsTreatedAsEmptyObject(t *testing.T) {
	schema := tool.Schema{Type: "object"}
	if err := tool.Validate(schema, nil); err != nil {
		t.Errorf("无参数工具应接受空参数，实际报错: %v", err)
	}
}

// 错误信息是回填给模型看的，必须说清「哪个参数、什么期望、实际收到什么」。
func TestValidate_ErrorIsActionableForModel(t *testing.T) {
	schema := tool.Schema{
		Type:       "object",
		Properties: map[string]tool.Property{"city": {Type: "string"}},
		Required:   []string{"city"},
	}
	err := tool.Validate(schema, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "缺少") || !strings.Contains(msg, "city") {
		t.Errorf("错误信息应指明缺失的字段，实际：%s", msg)
	}
}
