// Пакет testkit — генератор тестовых деревьев с ловушками. Бинарных фикстур в git
// не держим: всё, что нужно тестам (NUL-байты, UTF-16 с BOM, симлинк-циклы),
// создаётся на лету во временном каталоге.
package testkit

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

// Write создаёт файл по слэш-пути rel внутри root вместе с каталогами.
func Write(tb testing.TB, root, rel string, data []byte) {
	tb.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		tb.Fatalf("testkit: каталог для %s: %v", rel, err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		tb.Fatalf("testkit: запись %s: %v", rel, err)
	}
}

// WriteString — Write для текста.
func WriteString(tb testing.TB, root, rel, s string) {
	tb.Helper()
	Write(tb, root, rel, []byte(s))
}

// Symlink создаёт симлинк link -> target (target — как есть, относительный к
// каталогу ссылки). Возвращает false, если ОС не дала: на Windows это требует
// режима разработчика или прав, и тесты в таком случае пропускают эту ловушку,
// а не падают. Для каталогов на Windows при отказе делается junction: он не
// требует прав и как раз является реальным вектором цикла на этой платформе.
func Symlink(tb testing.TB, root, target, link string) bool {
	tb.Helper()
	p := filepath.Join(root, filepath.FromSlash(link))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		tb.Fatalf("testkit: каталог для %s: %v", link, err)
	}
	if os.Symlink(filepath.FromSlash(target), p) == nil {
		return true
	}
	return runtime.GOOS == "windows" && makeJunction(tb, filepath.Join(filepath.Dir(p), filepath.FromSlash(target)), p)
}

// Junction создаёт соединение каталога (mklink /J) link -> target. target —
// слэш-путь относительно root. Возвращает false, если не вышло или ОС не Windows.
func Junction(tb testing.TB, root, target, link string) bool {
	tb.Helper()
	if runtime.GOOS != "windows" {
		return false
	}
	p := filepath.Join(root, filepath.FromSlash(link))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		tb.Fatalf("testkit: каталог для %s: %v", link, err)
	}
	return makeJunction(tb, filepath.Join(root, filepath.FromSlash(target)), p)
}

// makeJunction вызывает cmd /c mklink /J отдельными аргументами: склеенная строка
// сломалась бы на пробелах в путях. Цель junction обязана быть существующим
// каталогом, поэтому для файлов функция сразу отказывает.
func makeJunction(tb testing.TB, target, link string) bool {
	tb.Helper()
	abs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	if st, serr := os.Stat(abs); serr != nil || !st.IsDir() {
		return false
	}
	// Аргументы — пути тестового каталога, оболочка не участвует: склейки строки нет.
	cmd := exec.CommandContext(tb.Context(), "cmd", "/c", "mklink", "/J", link, abs) //nolint:gosec // G204: только тестовый хелпер, пути из t.TempDir()
	return cmd.Run() == nil
}

// UTF16 кодирует строку в UTF-16 с BOM. be выбирает порядок байт.
func UTF16(s string, be bool) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+2*len(units))
	if be {
		out = append(out, 0xFE, 0xFF)
	} else {
		out = append(out, 0xFF, 0xFE)
	}
	for _, u := range units {
		if be {
			out = binary.BigEndian.AppendUint16(out, u)
		} else {
			out = binary.LittleEndian.AppendUint16(out, u)
		}
	}
	return out
}

// ScanFixture описывает, какие ловушки удалось создать.
type ScanFixture struct {
	Symlinks bool // создан ли симлинк-цикл и симлинк на файл
	Loop     bool // создан ли цикл sub/loop (симлинком или, на Windows, junction)
}

// ScanTree создаёт дерево с ловушками для обхода и определения бинарности.
// Состав (слэш-пути от root):
//
//	ok.txt            обычный текст
//	crlf.txt          CRLF-переводы строк
//	nul.bin           NUL-байт в первых 512 байтах
//	utf16le.txt       UTF-16 LE с BOM, текст по-русски
//	utf16be.txt       UTF-16 BE с BOM
//	utf16nul.dat      BOM UTF-16 и U+0000 внутри: бинарь
//	utf8bom.txt       UTF-8 с BOM
//	cp1251.txt        невалидный UTF-8 (однобайтная кодировка)
//	empty.txt         пустой файл
//	big.txt           1 МБ текста, больше любого разумного лимита теста
//	sub/nested.go     вложенный файл
//	sub/loop          симлинк на «..»: цикл (если ОС позволила; на Windows без
//	                  прав вместо него junction)
//	link.txt          симлинк на ok.txt (если ОС позволила)
func ScanTree(tb testing.TB, root string) ScanFixture {
	tb.Helper()
	WriteString(tb, root, "ok.txt", "hello\nworld\n")
	WriteString(tb, root, "crlf.txt", "one\r\ntwo\r\nthree\r\n")
	Write(tb, root, "nul.bin", []byte("MZ\x00\x00binary\x00data"))
	Write(tb, root, "utf16le.txt", UTF16("привет, мир\nвторая строка\n", false))
	Write(tb, root, "utf16be.txt", UTF16("привет, мир\n", true))
	Write(tb, root, "utf16nul.dat", append(UTF16("a", false), 0, 0, 1, 0))
	Write(tb, root, "utf8bom.txt", append([]byte{0xEF, 0xBB, 0xBF}, "с BOM\n"...))
	Write(tb, root, "cp1251.txt", []byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2, '\n'})
	Write(tb, root, "empty.txt", nil)
	WriteString(tb, root, "big.txt", strings.Repeat("0123456789abcdef\n", 64*1024))
	WriteString(tb, root, "sub/nested.go", "package sub\n")

	fx := ScanFixture{}
	loop := Symlink(tb, root, "..", "sub/loop")
	file := Symlink(tb, root, "ok.txt", "link.txt")
	fx.Symlinks = loop && file
	fx.Loop = loop
	return fx
}

// IgnoreTree создаёт дерево для проверки семантики .gitignore: вложенные файлы,
// отрицания, якорь, правила только для каталогов, «белый список» через «*» и
// классическая ловушка «нельзя вернуть файл из исключённого каталога».
// Возвращает слэш-пути всех файлов дерева (включая сами .gitignore).
func IgnoreTree(tb testing.TB, root string) []string {
	tb.Helper()
	files := map[string]string{
		".gitignore": strings.Join([]string{
			"# комментарий",
			"*.log",
			"!keep.log",
			"/build-out/",
			"temp/",
			"docs/*.tmp",
			"**/cache/",
			"/only-root.txt",
			"a/**/z.txt",
			"\\#hash.txt",
			"*.o",
			"!lib/*.o",
			"gen/**",
			"!gen/keep.txt",
			"trap/",
			"!trap/keep.txt",
			"тест*.tmp",
			"*.{js,map}", // git скобки не раскрывает: исключается только буквальное имя
			"",
		}, "\r\n"), // CRLF в самом .gitignore — тоже ловушка
		"error.log":             "x",
		"keep.log":              "x",
		"sub/x.log":             "x",
		"sub/keep.log":          "x",
		"build-out/a.txt":       "x",
		"sub/build-out/a.txt":   "x",
		"temp/f.txt":            "x",
		"sub/temp/f.txt":        "x",
		"sub2/temp":             "это файл, а не каталог",
		"docs/a.tmp":            "x",
		"docs/sub/b.tmp":        "x",
		"docs/readme.md":        "x",
		"x/cache/c.txt":         "x",
		"only-root.txt":         "x",
		"sub/only-root.txt":     "x",
		"a/z.txt":               "x",
		"a/b/c/z.txt":           "x",
		"a/b/zz.txt":            "x",
		"#hash.txt":             "x",
		"main.o":                "x",
		"lib/x.o":               "x",
		"lib/sub/y.o":           "x",
		"gen/a.txt":             "x",
		"gen/keep.txt":          "x",
		"gen/deep/b.txt":        "x",
		"trap/keep.txt":         "x",
		"trap/other.txt":        "x",
		"тест1.tmp":             "x",
		"тест.md":               "x",
		"brace/a.js":            "x",
		"brace/a.map":           "x",
		"brace/a.{js,map}":      "x",
		"sub/.gitignore":        "*.txt\n!readme.txt\n/local.cfg\n",
		"sub/n.txt":             "x",
		"sub/readme.txt":        "x",
		"sub/local.cfg":         "x",
		"sub/code.go":           "x",
		"sub/deeper/m.txt":      "x",
		"sub/deeper/local.cfg":  "x",
		"sub/deeper/.gitignore": "!m.txt\n*.dat\n",
		"sub/deeper/z.dat":      "x",
		"sub/deeper/keep.go":    "x",
		"wl/.gitignore":         "*\n!*/\n!*.go\n",
		"wl/a.go":               "x",
		"wl/b.txt":              "x",
		"wl/deep/c.go":          "x",
		"wl/deep/d.txt":         "x",
		"README.md":             "x",
		"plain/file.md":         "x",
		"plain/deep/er/file.md": "x",
		"with space/файл с пробелом.txt": "x",
	}
	out := make([]string, 0, len(files))
	for rel, content := range files {
		WriteString(tb, root, rel, content)
		out = append(out, rel)
	}
	return out
}
