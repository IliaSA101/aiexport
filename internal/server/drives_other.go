//go:build !windows

package server

// listDrives: вне Windows дисков нет, корень один — «/».
func listDrives() []string { return []string{} }
