package app

import (
	"runtime/debug"
	"strconv"

	"ledgergate/web"

	"github.com/gin-gonic/gin"
)

var buildRevision, buildTime, buildModified string

type buildInfo struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	BuiltAt  string `json:"built_at"`
	Modified *bool  `json:"modified,omitempty"`
}

func readBuildInfo(revision, builtAt, modified string, info *debug.BuildInfo) buildInfo {
	result := buildInfo{Version: web.Version(), Revision: revision, BuiltAt: builtAt}
	if info != nil {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && result.Revision == "" {
				result.Revision = setting.Value
			}
			if setting.Key == "vcs.modified" && modified == "" {
				modified = setting.Value
			}
		}
	}
	if value, err := strconv.ParseBool(modified); err == nil {
		result.Modified = &value
	}
	return result
}

func currentBuildInfo() buildInfo {
	info, _ := debug.ReadBuildInfo()
	return readBuildInfo(buildRevision, buildTime, buildModified, info)
}

func (a *App) versionInfo(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(200, currentBuildInfo())
}
