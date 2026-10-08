// Package buildinfo exposes build metadata injected at link time:
//
//	go build -ldflags "-X github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo.Version=1.2.3 \
//	  -X github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo.Commit=$GIT_SHA \
//	  -X github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo.BuildTime=$BUILD_TIME"
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

var (
	// Version is the release/build version.
	Version = "dev"
	// Commit is the git commit SHA the binary was built from.
	Commit = ""
	// BuildTime is the RFC3339 build timestamp.
	BuildTime = ""
)

// Info is the serialisable build information.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time,omitempty"`
	GoVersion string `json:"go_version"`
}

// Get returns build information, falling back to VCS data embedded by the Go
// toolchain when link-time values are absent.
func Get() Info {
	info := Info{Version: Version, Commit: Commit, BuildTime: BuildTime, GoVersion: runtime.Version()}
	if info.Commit == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					info.Commit = s.Value
				case "vcs.time":
					if info.BuildTime == "" {
						info.BuildTime = s.Value
					}
				}
			}
		}
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	return info
}
