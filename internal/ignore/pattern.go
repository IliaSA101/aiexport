package ignore

import (
	"errors"
	"fmt"
	"strings"

	"github.com/IliaSA101/aiexport/internal/model"
	"github.com/bmatcuk/doublestar/v4"
)

// rule — одно скомпилированное правило.
type rule struct {
	base    string // каталог своего .gitignore относительно корня; "" — корень
	glob    string // doublestar-шаблон относительно base
	negate  bool   // «!»: отмена предыдущего правила
	dirOnly bool   // завершающий «/»: правило только для каталогов
	reason  model.Reason
	text    string // что показывать пользователю в FileEntry.Rule
}

// ErrBadGlob — маска не компилируется doublestar.
var ErrBadGlob = errors.New("некорректная маска")

// errSkip — служебный признак «строку пропустить»; наружу не уходит.
var errSkip = errors.New("строка пропущена")

// parseLine разбирает строку в синтаксисе .gitignore. fromGitignore — строка из
// чужого файла .gitignore, а не пользовательская маска. errSkip — строка пустая или
// комментарий; ErrBadGlob — негодный шаблон. Оба случая вызывающий пропускает,
// как это делает git.
func parseLine(line string, fold, fromGitignore bool) (rule, error) {
	var r rule
	s := strings.TrimSuffix(line, "\r")
	// Пробелы в конце отбрасываются, кроме экранированных «\ ».
	for len(s) > 0 && s[len(s)-1] == ' ' {
		if len(s) >= 2 && s[len(s)-2] == '\\' {
			break
		}
		s = s[:len(s)-1]
	}
	if s == "" || s[0] == '#' {
		return rule{}, errSkip
	}
	r.text = s
	if s[0] == '!' {
		r.negate = true
		s = s[1:]
	}
	if strings.HasSuffix(s, "/") {
		r.dirOnly = true
		s = strings.TrimRight(s, "/")
	}
	if s == "" {
		return rule{}, errSkip
	}
	// Слэш в начале или середине привязывает шаблон к каталогу .gitignore,
	// шаблон без слэша действует на любой глубине — как в git.
	if strings.Contains(s, "/") {
		s = strings.TrimPrefix(s, "/")
	} else {
		s = "**/" + s
	}
	// В git «a/**» означает «всё внутри a», но не сам a. doublestar считает, что
	// «a/**» совпадает и с «a»; без этой правки каталог отсекался бы целиком и
	// нельзя было бы вернуть что-то из него через «!».
	if strings.HasSuffix(s, "/**") {
		s += "/*"
	}
	if fromGitignore {
		s = escapeBraces(s)
	}
	if fold {
		s = strings.ToLower(s)
	}
	if _, merr := doublestar.Match(s, ""); merr != nil {
		return rule{}, fmt.Errorf("%w %q: %w", ErrBadGlob, line, merr)
	}
	r.glob = s
	return r, nil
}

// escapeBraces экранирует «{» и «}» в шаблоне из .gitignore. doublestar раскрывает
// {a,b}, а git — нет: «*.{js,map}» в git значит файл с буквальными скобками. Без
// экранирования чужой .gitignore выбрасывал бы из бандла реальные исходники.
// Уже экранированные символы и содержимое после «\» не трогаем.
func escapeBraces(s string) string {
	if !strings.ContainsAny(s, "{}") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteByte(c)
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '{', '}':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ValidateGlob проверяет пользовательскую маску заранее, чтобы CLI и сервер
// могли сообщить об опечатке сразу, а не молча потерять правило.
func ValidateGlob(p string) error {
	if _, err := parseLine(p, false, false); errors.Is(err, ErrBadGlob) {
		return err
	}
	return nil
}

// matches проверяет совпадение пути (уже приведённого к нужному регистру).
func (r *rule) matches(rel string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}
	if r.base != "" {
		if len(rel) <= len(r.base) || rel[len(r.base)] != '/' || rel[:len(r.base)] != r.base {
			return false
		}
		rel = rel[len(r.base)+1:]
	}
	ok, err := doublestar.Match(r.glob, rel)
	return err == nil && ok
}
