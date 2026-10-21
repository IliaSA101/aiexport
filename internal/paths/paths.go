// Пакет paths — единственное место, где решается, можно ли принять путь от
// пользователя (из CLI, JSON запроса, конфига). Правило жило в двух копиях с
// разным содержанием, и одна из них отстала (не знала про \??\): теперь копия одна.
package paths

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// Признаки для errors.Is. Тексты — для пользователя, поэтому по-русски.
var (
	ErrService     = errors.New("сетевые и служебные пути (UNC, \\\\?\\, \\??\\) не поддерживаются")
	ErrNotAbsolute = errors.New("путь должен быть абсолютным")
	ErrReserved    = errors.New("зарезервированное имя Windows (con, nul, prn, aux, com1-9, lpt1-9)")
)

// Clean превращает пользовательский путь в чистый абсолютный локальный.
//
// Порядок такой: сначала нормализация (слэши, Clean), и только потом проверка по
// белому списку — «X:\…» на Windows, «/…» на остальных ОС. Чёрный список префиксов
// до нормализации пропускал «/\host\share» и «\??\UNC\…»: Clean превращал их в
// настоящий UNC или объектный путь NT, а обращение к UNC отправляет NTLM-хэш
// пользователя на чужой сервер. relativeOK разрешает относительный путь (файл
// вывода): он станет абсолютным от текущего каталога и пройдёт ту же проверку.
func Clean(raw string, relativeOK bool) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", errors.New("пустой путь")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("путь содержит недопустимый символ")
	}
	if runtime.GOOS != "windows" {
		return cleanPosix(p, relativeOK)
	}
	// На Windows оба слэша равноправны, а «//host» и «/\host» — тоже UNC.
	n := filepath.Clean(strings.ReplaceAll(p, "/", `\`))
	switch {
	case isDriveAbs(n):
		// нормальный путь, проверим ниже
	case relativeOK && isPlainRelative(n):
		// До Abs: GetFullPathName превращает «con» в «\.\con», и отказ вышел бы
		// с невнятной причиной «служебный путь».
		if err := checkReserved(n); err != nil {
			return "", err
		}
		abs, err := filepath.Abs(n)
		if err != nil {
			return "", errors.New("путь не удалось привести к абсолютному")
		}
		n = abs
	case hasServiceShape(n):
		return "", ErrService
	default:
		return "", fmt.Errorf("%w: %q", ErrNotAbsolute, raw)
	}
	// Итог обязан быть путём вида X:\… — второй раз, уже после Abs.
	if !isDriveAbs(n) {
		return "", ErrService
	}
	// Двоеточие допустимо только после буквы диска; иначе это альтернативный
	// поток данных (file:stream) или мусор.
	if strings.Contains(n[2:], ":") {
		return "", errors.New("путь содержит недопустимый символ «:»")
	}
	if err := checkReserved(n[3:]); err != nil {
		return "", err
	}
	return n, nil
}

func cleanPosix(p string, relativeOK bool) (string, error) {
	n := filepath.Clean(p)
	if !strings.HasPrefix(n, "/") {
		if !relativeOK {
			return "", fmt.Errorf("%w: %q", ErrNotAbsolute, p)
		}
		abs, err := filepath.Abs(n)
		if err != nil {
			return "", errors.New("путь не удалось привести к абсолютному")
		}
		n = abs
	}
	if !strings.HasPrefix(n, "/") {
		return "", fmt.Errorf("%w: %q", ErrNotAbsolute, p)
	}
	return n, nil
}

// isDriveAbs — путь вида «X:\…» (после Clean с обратными слэшами).
func isDriveAbs(p string) bool {
	if len(p) < 3 || p[1] != ':' || p[2] != '\\' {
		return false
	}
	c := p[0] | 0x20
	return c >= 'a' && c <= 'z'
}

// isPlainRelative — относительный путь без привязки к диску и корню: ни «C:foo»,
// ни «\foo». Только такой безопасно отдавать в filepath.Abs.
func isPlainRelative(p string) bool {
	if p == "" || p[0] == '\\' {
		return false
	}
	return len(p) < 2 || p[1] != ':'
}

// hasServiceShape узнаёт UNC, \\?\, \\.\ и \??\ — только чтобы дать точное
// сообщение; решение «принять» остаётся за белым списком.
func hasServiceShape(p string) bool {
	return strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, `\??\`)
}

// checkReserved отвергает имена устройств Windows в любом звене пути, в том числе
// с расширением: «con.md» открывается как консоль, а файл с таким именем Проводник
// и Блокнот не открывают и не удаляют. filepath.IsLocal такие имена пропускает.
func checkReserved(rest string) error {
	for _, part := range strings.Split(rest, `\`) {
		if isReservedName(part) {
			return fmt.Errorf("%w: %q", ErrReserved, part)
		}
	}
	return nil
}

func isReservedName(name string) bool {
	// Устройство определяет имя до первой точки; хвостовые пробелы Windows отбрасывает.
	stem, _, _ := strings.Cut(name, ".")
	stem = strings.ToLower(strings.TrimRight(stem, " "))
	switch stem {
	case "con", "prn", "aux", "nul", "conin$", "conout$":
		return true
	}
	if strings.HasPrefix(stem, "com") || strings.HasPrefix(stem, "lpt") {
		switch stem[3:] {
		case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
			return true
		}
	}
	return false
}
