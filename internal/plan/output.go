package plan

import (
	"path/filepath"
	"strings"

	"github.com/IliaSA101/aiexport/internal/model"
)

// ExcludeOutput возвращает якорные маски для файла вывода, если он лежит внутри
// какого-либо источника. Без этого повторная сборка включила бы в бандл собственный
// предыдущий результат; маска попадёт в раздел «не включено» с видимой причиной.
func ExcludeOutput(sources []model.Source, outAbs string) []string {
	var masks []string
	for _, s := range sources {
		rel, err := filepath.Rel(s.Path, outAbs)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		masks = append(masks, "/"+escapeGlob(filepath.ToSlash(rel)))
	}
	return masks
}

// escapeGlob экранирует метасимволы doublestar в буквальном пути.
func escapeGlob(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`*?[]{}\!#`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
