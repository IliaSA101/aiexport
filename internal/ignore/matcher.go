// Пакет ignore — движок правил исключения: .gitignore, встроенные значения по
// умолчанию и пользовательские маски. Семантика как в git: правила идут по порядку,
// выигрывает последнее совпадение, «!» отменяет.
package ignore

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"runtime"
	"strings"

	"github.com/IliaSA101/aiexport/internal/model"
)

// maxGitignoreSize — потолок размера одного .gitignore. Файл читается целиком,
// а лежит он в чужом дереве: без лимита это вектор исчерпания памяти.
const maxGitignoreSize = 1 << 20

// gitRuleText — что видит пользователь в отчёте, когда сработала жёсткая защита.
const gitRuleText = ".git (неотменяемое правило: каталог git не выгружается)"

// hasGitSegment сообщает, есть ли среди звеньев пути «.git». Ключ уже приведён к
// нижнему регистру на Windows, где ".GIT" — тот же каталог. Файл ".git" (гитлинк
// подмодуля) тоже исключается: внутри только путь к настоящему каталогу git.
func hasGitSegment(key string) bool {
	for key != "" {
		seg, rest, _ := strings.Cut(key, "/")
		if seg == ".git" {
			return true
		}
		key = rest
	}
	return false
}

// Defaults — каталоги, которые исключаются всегда (если их не вернули явным «!»).
// Без них первый запуск на реальном проекте тащит vendor и node_modules: в замерах
// это 96,7% всех файлов.
func Defaults() []string {
	return []string{
		".git/", "node_modules/", "vendor/", "dist/", "build/", ".venv/",
		"__pycache__/", ".obsidian/", ".idea/", ".vscode/", ".gotmp/",
	}
}

// Matcher хранит правила тремя слоями; приоритет растёт слева направо:
// умолчания < .gitignore < пользовательские маски. Так явное желание пользователя
// всегда сильнее, а проектные правила сильнее встроенных.
//
// Добавлять правила нельзя параллельно с Match. Родительские .gitignore нужно
// добавлять раньше вложенных: порядок внутри слоя и есть приоритет.
type Matcher struct {
	fold     bool // регистронезависимое сравнение (Windows)
	defaults []rule
	git      []rule
	user     []rule
}

// New создаёт Matcher с умолчаниями и пользовательскими масками.
// Негодные маски пропускаются; проверить их заранее позволяет ValidateGlob.
func New(extra []string) *Matcher {
	return newMatcher(extra, runtime.GOOS == "windows", true)
}

func newMatcher(extra []string, fold, withDefaults bool) *Matcher {
	m := &Matcher{fold: fold}
	if withDefaults {
		for _, line := range Defaults() {
			if r, err := parseLine(line, fold, false); err == nil {
				r.reason = model.ReasonDefault
				m.defaults = append(m.defaults, r)
			}
		}
	}
	for _, line := range extra {
		if r, err := parseLine(line, fold, false); err == nil {
			r.reason = model.ReasonGlob
			m.user = append(m.user, r)
		}
	}
	return m
}

// AddGitignore добавляет правила файла .gitignore, лежащего в каталоге dirRel
// (слэш-путь от корня источника, "" — сам корень).
func (m *Matcher) AddGitignore(dirRel string, r io.Reader) error {
	dirRel = normDir(dirRel)
	label := ".gitignore"
	if dirRel != "" {
		label = dirRel + "/.gitignore"
	}
	base := dirRel
	if m.fold {
		base = strings.ToLower(base)
	}

	data, err := io.ReadAll(io.LimitReader(r, maxGitignoreSize+1))
	if err != nil {
		return fmt.Errorf("чтение %s: %w", label, err)
	}
	if len(data) > maxGitignoreSize {
		return fmt.Errorf("%s: файл больше %d байт", label, maxGitignoreSize)
	}
	text := strings.TrimPrefix(string(data), string(rune(0xFEFF))) // BOM от Блокнота

	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 4096), maxGitignoreSize+1)
	for sc.Scan() {
		rl, perr := parseLine(sc.Text(), m.fold, true)
		if perr != nil {
			continue // комментарий или негодный шаблон — как в git, молча
		}
		rl.base = base
		rl.reason = model.ReasonGitignore
		rl.text = label + ": " + rl.text
		m.git = append(m.git, rl)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("разбор %s: %w", label, err)
	}
	return nil
}

func normDir(d string) string {
	d = strings.ReplaceAll(d, "\\", "/")
	d = path.Clean("/" + d)
	return strings.Trim(d, "/")
}

// Match отвечает, исключён ли путь, с учётом всех его родительских каталогов:
// как в git, файл внутри исключённого каталога исключён, и «!» его не вернёт.
// rel — слэш-путь от корня источника.
func (m *Matcher) Match(rel string, isDir bool) (ignored bool, reason model.Reason, rule string) {
	key := m.key(strings.Trim(rel, "/"))
	for i := 0; i < len(key); i++ {
		if key[i] != '/' {
			continue
		}
		if ign, rs, ru := m.match(key[:i], true); ign {
			return true, rs, ru
		}
	}
	return m.match(key, isDir)
}

// MatchDir — проверка одного каталога без разбора предков. Обход вызывает её на
// каждом каталоге до рекурсии: предки уже прошли проверку, повторять её незачем,
// а при отсечении через fs.SkipDir содержимое исключённого каталога не посещается.
func (m *Matcher) MatchDir(rel string) (bool, model.Reason, string) {
	return m.match(m.key(strings.Trim(rel, "/")), true)
}

// MatchFile — то же для файла. Предки считаются уже проверенными обходом.
func (m *Matcher) MatchFile(rel string) (bool, model.Reason, string) {
	return m.match(m.key(strings.Trim(rel, "/")), false)
}

func (m *Matcher) key(rel string) string {
	if m.fold {
		return strings.ToLower(rel)
	}
	return rel
}

// match — вердикт по одному пути: последнее совпавшее правило выигрывает,
// поэтому слои перебираются с конца.
func (m *Matcher) match(key string, isDir bool) (bool, model.Reason, string) {
	// До всех слоёв: .gitignore выгружаемого репозитория («!*» и подобное) не
	// должен возвращать .git. Там лежат токены в url из config и вся история;
	// сам git защищает .git жёстко, а не правилом, и мы повторяем это.
	if hasGitSegment(key) {
		return true, model.ReasonDefault, gitRuleText
	}
	for _, layer := range [3][]rule{m.user, m.git, m.defaults} {
		for i := len(layer) - 1; i >= 0; i-- {
			r := &layer[i]
			if !r.matches(key, isDir) {
				continue
			}
			if r.negate {
				return false, model.ReasonNone, ""
			}
			return true, r.reason, r.text
		}
	}
	return false, model.ReasonNone, ""
}
