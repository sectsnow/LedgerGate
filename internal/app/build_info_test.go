package app

import (
	"encoding/json"
	"runtime/debug"
	"testing"

	"ledgergate/web"
)

func TestBuildInfoSeparatesCommitTimeFromBuildTime(t *testing.T) {
	info := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "source-revision"}, {Key: "vcs.time", Value: "2026-01-01T00:00:00Z"}, {Key: "vcs.modified", Value: "true"}}}
	result := readBuildInfo("", "", "", info)
	if result.Version != web.Version() || result.Revision != "source-revision" || result.BuiltAt != "" || result.Modified == nil || !*result.Modified {
		t.Fatalf("commit timestamp mistaken for build timestamp: %+v", result)
	}
	result = readBuildInfo("release-revision", "2026-11-23T08:00:00Z", "false", info)
	if result.Revision != "release-revision" || result.BuiltAt != "2026-11-23T08:00:00Z" || result.Modified == nil || *result.Modified {
		t.Fatalf("injected release metadata not used: %+v", result)
	}
	result = readBuildInfo("", "", "", nil)
	if result.Revision != "" || result.BuiltAt != "" || result.Modified != nil {
		t.Fatal("missing build information was fabricated")
	}
}

func TestBuildVersionEndpoints(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	version := call(h, "GET", "/api/version", "", cookie)
	var actual buildInfo
	if version.Code != 200 || json.Unmarshal(version.Body.Bytes(), &actual) != nil || actual.Version != web.Version() || version.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("backend version unavailable", version.Body.String())
	}
	health := call(h, "GET", "/healthz", "", "")
	var body struct {
		Status string    `json:"status"`
		Build  buildInfo `json:"build"`
	}
	if health.Code != 200 || json.Unmarshal(health.Body.Bytes(), &body) != nil || body.Status != "ok" || body.Build.Version != actual.Version || body.Build.Revision != actual.Revision || body.Build.BuiltAt != actual.BuiltAt {
		t.Fatal("health metadata did not describe the running backend", health.Body.String())
	}
	if response := call(h, "GET", "/api/version", "", ""); response.Code != 401 {
		t.Fatal("admin version endpoint bypassed authentication")
	}
}
