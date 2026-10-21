package server

import "syscall"

// listDrives возвращает буквы дисков вида «C:\». GetLogicalDrives отдаёт битовую
// маску без обращения к самим дискам, поэтому отключённый сетевой диск не подвесит
// запрос (os.Stat по каждой букве подвесил бы). kernel32 — системная библиотека,
// уже загруженная в процесс, подмена через рабочий каталог исключена.
func listDrives() []string {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetLogicalDrives")
	mask, _, _ := proc.Call()
	drives := []string{}
	for i := range 26 {
		if mask&(1<<uint(i)) != 0 {
			drives = append(drives, string(rune('A'+i))+`:\`)
		}
	}
	return drives
}
