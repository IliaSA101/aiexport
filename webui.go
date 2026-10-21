// Пакет aiexport содержит только вшитый веб-UI. Каталог web/ лежит в корне
// репозитория, а директива go:embed не умеет подниматься через «..», поэтому
// встраивание сделано здесь, а не в internal/server.
package aiexport

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed web
var webFiles embed.FS

// WebFS возвращает содержимое web/ с index.html в корне.
func WebFS() (fs.FS, error) {
	sub, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, fmt.Errorf("встроенный веб-UI: %w", err)
	}
	return sub, nil
}
