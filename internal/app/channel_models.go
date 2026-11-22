package app

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
)

func normalizeChannelModels(models []string) ([]string, error) {
	if len(models) > 10000 {
		return nil, errors.New("每条渠道最多保存 10000 个模型")
	}
	result := []string{}
	seen := map[string]bool{}
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || len(model) > 150 || strings.IndexFunc(model, unicode.IsControl) >= 0 {
			return nil, errors.New("模型名称须为 1–150 字节，且不能包含控制字符")
		}
		if !seen[model] {
			seen[model] = true
			result = append(result, model)
		}
	}
	return result, nil
}

func readChannelModels(raw string) ([]string, error) {
	var models []string
	if json.Unmarshal([]byte(raw), &models) != nil || models == nil {
		return nil, errors.New("已保存的渠道模型列表格式无效，请编辑后重试")
	}
	return normalizeChannelModels(models)
}
