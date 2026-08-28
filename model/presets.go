package model

// DefaultModelRatio 仿 new-api 的内置模型倍率预设（提示词计费倍率）
// 数值参考公开价格换算，可按需在设置页覆盖
func DefaultModelRatio() map[string]float64 {
	return map[string]float64{
		"gpt-4o":                 1,
		"gpt-4o-mini":            0.5,
		"gpt-4":                  10,
		"gpt-4-turbo":            10,
		"gpt-3.5-turbo":          0.5,
		"text-embedding-3-small": 0.02,
		"text-embedding-3-large": 0.02,
		"text-embedding-ada-002": 0.02,
		"whisper-1":              0.1,
		"tts-1":                  1,
		"tts-1-hd":               1,
		"dall-e-2":               1,
		"dall-e-3":               1,
		"claude-3-opus":          15,
		"claude-3-sonnet":        5,
		"claude-3-haiku":         1,
		"claude-3.5-sonnet":      5,
		"gemini-1.5-pro":         7,
		"gemini-1.5-flash":       1,
		"deepseek-chat":          1,
		"deepseek-reasoner":      2,
		"o1":                     20,
		"o1-mini":                5,
	}
}

// DefaultCompletionRatio 仿 new-api 的内置生成倍率预设（补全词计费倍率）
// 缺省情况下营业模式会用 ModelRatio 代替；此处给出常见偏移
func DefaultCompletionRatio() map[string]float64 {
	return map[string]float64{
		"gpt-4o":                 2,
		"gpt-4o-mini":            1.5,
		"gpt-4":                  20,
		"gpt-4-turbo":            20,
		"gpt-3.5-turbo":          1.5,
		"claude-3-opus":          30,
		"claude-3-sonnet":        15,
		"claude-3-haiku":         3,
		"claude-3.5-sonnet":      15,
		"gemini-1.5-pro":         14,
		"gemini-1.5-flash":       2,
		"deepseek-chat":          1,
		"deepseek-reasoner":      4,
		"o1":                     40,
		"o1-mini":                10,
		"text-embedding-3-small": 0.02,
		"text-embedding-3-large": 0.02,
		"text-embedding-ada-002": 0.02,
		"whisper-1":              0.1,
		"tts-1":                  1,
		"tts-1-hd":               1,
		"dall-e-2":               1,
		"dall-e-3":               1,
	}
}
