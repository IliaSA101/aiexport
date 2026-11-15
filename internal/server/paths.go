package server

import "github.com/IliaSA101/aiexport/internal/paths"

// cleanLocalPath — тонкая обёртка: правило приёма путей одно на весь проект и
// живёт в internal/paths, чтобы сервер, план и CLI не расходились в деталях.
// relativeOK — разрешить относительный путь (для файла вывода).
func cleanLocalPath(raw string, relativeOK bool) (string, error) {
	return paths.Clean(raw, relativeOK)
}
