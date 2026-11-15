package server

import (
	"io/fs"
	"syscall"
)

// isHidden: на Windows скрытость — атрибут, а не точка в имени. Системные каталоги
// вроде $Recycle.Bin и System Volume Information имеют оба атрибута.
func isHidden(e fs.DirEntry) bool {
	info, err := e.Info()
	if err != nil {
		return true
	}
	attr, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false
	}
	return attr.FileAttributes&(syscall.FILE_ATTRIBUTE_HIDDEN|syscall.FILE_ATTRIBUTE_SYSTEM) != 0
}
