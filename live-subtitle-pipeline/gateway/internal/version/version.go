// Package version 保存构建信息，用于运行时确认容器里跑的是否为最新包。
//
// Version 是随源码走的静态构建标识：每次新增网关能力（路由等）时递增。
// 旧二进制的 /health 不含 version 字段，也没有新路由，可据此一眼区分新旧包，
// 不依赖 Docker 构建参数或 ldflags 注入。
package version

// 可选：构建期通过 -ldflags "-X .../Commit=... -X .../BuildTime=..." 覆盖。
var (
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Version 静态能力标识（随源码提交）。
const Version = "2026.09.14-pipeline-status"

// String 返回简短构建标识。
func String() string {
	return Version + " (" + Commit + ")"
}
