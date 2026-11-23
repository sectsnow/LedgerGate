package web

import (
	"embed"
	"encoding/json"
)

//go:embed all:static
var Assets embed.FS

//go:embed package.json
var packageJSON []byte

func Version() string {
	var value struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(packageJSON, &value) != nil || value.Version == "" {
		return "unknown"
	}
	return value.Version
}
