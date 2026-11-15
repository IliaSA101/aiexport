//go:build !windows

package server

import "io/fs"

// isHidden: вне Windows скрытыми считаются имена с точкой, их отсекает вызывающий.
func isHidden(fs.DirEntry) bool { return false }
