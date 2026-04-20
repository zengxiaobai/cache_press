package main

import (
	"testing"
)

func TestNegotiateEncoding(t *testing.T) {
	tests := []struct {
		name     string
		ae       string
		expected string
	}{
		// 空 header
		{"empty", "", ""},

		// 单个编码
		{"single_gzip", "gzip", "gzip"},
		{"single_br", "br", "br"},
		{"single_identity", "identity", ""},

		// 多编码逗号分隔（不带 q 值，都默认 q=1.0）
		{"multi_gzip_br", "gzip, br", "br"},          // 都 q=1.0，br 服务器优先级更高
		{"multi_br_gzip", "br, gzip", "br"},          // 都 q=1.0，br 服务器优先级更高
		{"multi_gzip_deflate", "gzip, deflate", "gzip"},

		// 带 q 值 - gzip 优先级更高
		{"q_gzip_higher", "gzip;q=1.0, br;q=0.5", "gzip"},
		{"q_gzip_higher_2", "gzip;q=0.8, br;q=0.3", "gzip"},

		// 带 q 值 - br 优先级更高
		{"q_br_higher", "gzip;q=0.3, br;q=1.0", "br"},
		{"q_br_higher_2", "br;q=0.9, gzip;q=0.5", "br"},

		// q=0 表示不接受
		{"q_zero_br", "gzip;q=1.0, br;q=0", "gzip"},
		{"q_zero_gzip", "gzip;q=0, br;q=1.0", "br"},
		{"q_zero_all_supported", "gzip;q=0, br;q=0", ""},

		// 通配符 *
		{"wildcard", "*", "br"},
		{"wildcard_q", "*;q=0.5", "br"},
		{"wildcard_q_zero", "*;q=0, br", "br"}, // * q=0 但 br q=1.0
		{"wildcard_q_zero_all", "*;q=0", ""},   // * q=0，所有都不接受

		// 混合场景
		{"mixed_1", "gzip;q=0.5, deflate, br;q=0.8", "br"},       // deflate q=1.0 但服务器不支持，br q=0.8 最高
		{"mixed_2", "gzip;q=0.9, deflate;q=1.0, br;q=0.5", "gzip"}, // deflate 不支持，gzip q=0.9 最高
		{"mixed_3", "deflate;q=1.0, *;q=0.1", "br"},               // deflate 不支持，* 匹配 br

		// 大小写不敏感
		{"case_insensitive_1", "GZIP", "gzip"},
		{"case_insensitive_2", "GZip, BR", "br"},
		{"case_insensitive_3", "gzip;Q=1.0, BR;q=0.5", "gzip"},

		// 复杂场景：客户端不希望压缩
		{"no_compress", "identity;q=1.0, *;q=0", ""},

		// 仅 identity
		{"identity_only", "identity", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := negotiateEncoding(tt.ae)
			if result != tt.expected {
				t.Errorf("negotiateEncoding(%q) = %q, want %q", tt.ae, result, tt.expected)
			}
		})
	}
}

func TestParseAcceptList(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []acceptItem
	}{
		{"single", "gzip", []acceptItem{{"gzip", 1.0}}},
		{"multi", "gzip, br", []acceptItem{{"gzip", 1.0}, {"br", 1.0}}},
		{"with_q", "gzip;q=0.5", []acceptItem{{"gzip", 0.5}}},
		{"mixed_q", "gzip;q=1.0, br;q=0.5", []acceptItem{{"gzip", 1.0}, {"br", 0.5}}},
		{"wildcard", "*;q=0.5, br", []acceptItem{{"*", 0.5}, {"br", 1.0}}},
		{"zero_q", "gzip;q=0", []acceptItem{{"gzip", 0.0}}},
		{"language", "zh-CN;q=1.0, en;q=0.5", []acceptItem{{"zh-CN", 1.0}, {"en", 0.5}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseAcceptList(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("parseAcceptList(%q) got %d items, want %d", tt.input, len(result), len(tt.expected))
				return
			}
			for i, item := range result {
				if item.value != tt.expected[i].value || item.q != tt.expected[i].q {
					t.Errorf("parseAcceptList(%q)[%d] = {%s, %f}, want {%s, %f}",
						tt.input, i, item.value, item.q, tt.expected[i].value, tt.expected[i].q)
				}
			}
		})
	}
}

func TestNegotiateLanguage(t *testing.T) {
	tests := []struct {
		name     string
		al       string
		expected string
	}{
		// 空 header
		{"empty", "", ""},

		// 单个语言
		{"single_zh", "zh-CN", "zh-CN"},
		{"single_en", "en-US", "en-US"},

		// 多语言逗号分隔（不带 q 值，都默认 q=1.0，选第一个）
		{"multi_zh_en", "zh-CN, en-US", "zh-CN"},
		{"multi_en_zh", "en-US, zh-CN", "en-US"},

		// 带 q 值
		{"q_en_higher", "zh-CN;q=0.5, en-US;q=1.0", "en-US"},
		{"q_zh_higher", "zh-CN;q=1.0, en-US;q=0.5", "zh-CN"},
		{"q_first_wins", "zh-CN;q=0.8, en;q=0.8", "zh-CN"}, // 同 q 选第一个

		// q=0 拒绝
		{"q_zero_zh", "zh-CN;q=0, en-US;q=1.0", "en-US"},
		{"q_zero_all", "zh-CN;q=0, en-US;q=0", ""},

		// 通配符 * 被跳过（不作为 Content-Language 值）
		{"wildcard_only", "*", ""},
		{"wildcard_with_lang", "*, en-US", "en-US"},
		{"wildcard_q", "*;q=0.5, zh-CN;q=1.0", "zh-CN"},

		// 大小写保持原样
		{"case_preserve", "ZH-CN", "ZH-CN"},

		// 复杂场景
		{"complex", "en-US;q=0.9, zh-CN;q=1.0, ja;q=0.5", "zh-CN"},
		{"complex_2", "fr;q=1.0, de;q=0.8, en;q=0.5", "fr"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := negotiateLanguage(tt.al)
			if result != tt.expected {
				t.Errorf("negotiateLanguage(%q) = %q, want %q", tt.al, result, tt.expected)
			}
		})
	}
}
