package ignore

import (
	"errors"
	"strings"
	"testing"

	"github.com/IliaSA101/aiexport/internal/model"
)

type matchCase struct {
	name  string
	rules string // содержимое корневого .gitignore
	rel   string
	isDir bool
	want  bool
}

func TestGitignoreSemantics(t *testing.T) {
	cases := []matchCase{
		{"звёздочка на любой глубине", "*.log", "a/b/c.log", false, true},
		{"звёздочка не совпала", "*.log", "a/b/c.txt", false, false},
		{"звёздочка не пересекает слэш", "docs/*.tmp", "docs/sub/a.tmp", false, false},
		{"отрицание возвращает файл", "*.log\n!keep.log", "keep.log", false, false},
		{"порядок: последнее выигрывает", "!keep.log\n*.log", "keep.log", false, true},
		{"якорь по ведущему слэшу", "/only", "only", false, true},
		{"якорь не совпадает глубже", "/only", "sub/only", false, false},
		{"слэш в середине тоже якорь", "a/b", "x/a/b", false, false},
		{"слэш в середине совпал", "a/b", "a/b", false, true},
		{"только каталог: файл не совпал", "temp/", "temp", false, false},
		{"только каталог: каталог совпал", "temp/", "temp", true, true},
		{"файл в исключённом каталоге", "temp/", "temp/f.txt", false, true},
		{"отрицание не возвращает файл из исключённого каталога", "trap/\n!trap/keep.txt", "trap/keep.txt", false, true},
		{"двойная звёздочка в середине: ноль каталогов", "a/**/z.txt", "a/z.txt", false, true},
		{"двойная звёздочка в середине: много каталогов", "a/**/z.txt", "a/b/c/z.txt", false, true},
		{"двойная звёздочка в начале", "**/cache/", "x/y/cache", true, true},
		{"a/** не затрагивает сам a", "gen/**", "gen", true, false},
		{"a/** затрагивает содержимое", "gen/**", "gen/a.txt", false, true},
		{"экранированная решётка", "\\#x", "#x", false, true},
		{"комментарий", "#x", "#x", false, false},
		{"пустые строки и CRLF", "\r\n\r\n*.o\r\n", "m.o", false, true},
		{"экранированный пробел в конце", "a\\ ", "a ", false, true},
		{"пробелы в конце отбрасываются", "a   ", "a", false, true},
		{"класс символов", "f[0-9].txt", "f7.txt", false, true},
		{"класс символов не совпал", "f[0-9].txt", "fa.txt", false, false},
		{"вопросительный знак", "?.md", "a.md", false, true},
		{"BOM в начале файла", string(rune(0xFEFF)) + "*.bak", "x.bak", false, true},
	}

	for _, fold := range []bool{false, true} {
		for _, c := range cases {
			t.Run(c.name+map[bool]string{false: "", true: "/fold"}[fold], func(t *testing.T) {
				m := newMatcher(nil, fold, false)
				if err := m.AddGitignore("", strings.NewReader(c.rules)); err != nil {
					t.Fatalf("AddGitignore: %v", err)
				}
				got, _, _ := m.Match(c.rel, c.isDir)
				if got != c.want {
					t.Errorf("Match(%q, dir=%v) при правилах %q = %v, ожидали %v", c.rel, c.isDir, c.rules, got, c.want)
				}
			})
		}
	}
}

func TestNestedGitignoreOverridesParent(t *testing.T) {
	m := newMatcher(nil, false, false)
	_ = m.AddGitignore("", strings.NewReader("*.txt\n"))
	_ = m.AddGitignore("sub", strings.NewReader("!keep.txt\n/local\n"))

	check := func(rel string, want bool) {
		t.Helper()
		if got, _, _ := m.Match(rel, false); got != want {
			t.Errorf("Match(%q) = %v, ожидали %v", rel, got, want)
		}
	}
	check("a.txt", true)
	check("sub/keep.txt", false)   // вложенный .gitignore отменил родительский
	check("sub/x/keep.txt", false) // и на глубине
	check("other/keep.txt", true)  // но только в своём каталоге
	check("sub/local", true)
	check("sub/x/local", false) // якорь привязан к каталогу вложенного файла
	check("local", false)
}

func TestReasonAndRule(t *testing.T) {
	m := newMatcher([]string{"**/*.min.js"}, false, true)
	_ = m.AddGitignore("sub", strings.NewReader("*.gen\n"))

	cases := []struct {
		rel    string
		isDir  bool
		reason model.Reason
		rule   string
	}{
		{"node_modules", true, model.ReasonDefault, "node_modules/"},
		{"app/vendor", true, model.ReasonDefault, "vendor/"},
		{"a/b.min.js", false, model.ReasonGlob, "**/*.min.js"},
		{"sub/x.gen", false, model.ReasonGitignore, "sub/.gitignore: *.gen"},
		{"ok.go", false, model.ReasonNone, ""},
	}
	for _, c := range cases {
		ign, reason, rule := m.Match(c.rel, c.isDir)
		if ign != (c.reason != model.ReasonNone) || reason != c.reason || rule != c.rule {
			t.Errorf("Match(%q) = (%v, %q, %q), ожидали причину %q и правило %q", c.rel, ign, reason, rule, c.reason, c.rule)
		}
	}
}

// Пользовательская маска сильнее .gitignore: явное желание пользователя
// не должно перебиваться проектным файлом.
func TestUserGlobBeatsGitignore(t *testing.T) {
	m := newMatcher([]string{"!vendor/"}, false, true)
	if ign, _, _ := m.MatchDir("vendor"); ign {
		t.Error("явное «!vendor/» должно вернуть каталог из умолчаний")
	}
	m = newMatcher([]string{"*.md"}, false, true)
	_ = m.AddGitignore("", strings.NewReader("!*.md\n"))
	if ign, reason, _ := m.Match("a.md", false); !ign || reason != model.ReasonGlob {
		t.Errorf("маска пользователя должна перебить .gitignore, получили %v %q", ign, reason)
	}
}

func TestCaseFold(t *testing.T) {
	m := newMatcher([]string{"*.LOG"}, true, false)
	_ = m.AddGitignore("Sub", strings.NewReader("Secret.TXT\n"))
	if ign, _, _ := m.Match("a/b.log", false); !ign {
		t.Error("при fold регистр маски не должен иметь значения")
	}
	if ign, _, _ := m.Match("SUB/secret.txt", false); !ign {
		t.Error("при fold регистр каталога .gitignore не должен иметь значения")
	}
	m = newMatcher([]string{"*.LOG"}, false, false)
	if ign, _, _ := m.Match("a/b.log", false); ign {
		t.Error("без fold регистр различается")
	}
}

func TestAddGitignoreTooBig(t *testing.T) {
	m := newMatcher(nil, false, false)
	err := m.AddGitignore("", strings.NewReader(strings.Repeat("a\n", maxGitignoreSize)))
	if err == nil {
		t.Error("гигантский .gitignore должен отвергаться")
	}
}

func TestValidateGlob(t *testing.T) {
	if err := ValidateGlob("**/*.min.js"); err != nil {
		t.Errorf("корректная маска отвергнута: %v", err)
	}
	if err := ValidateGlob("[abc"); !errors.Is(err, ErrBadGlob) {
		t.Errorf("ожидали ErrBadGlob, получили %v", err)
	}
	if err := ValidateGlob("# комментарий"); err != nil {
		t.Errorf("комментарий не ошибка: %v", err)
	}
}

func TestDefaults(t *testing.T) {
	m := New(nil)
	for _, d := range Defaults() {
		name := strings.TrimSuffix(d, "/")
		if ign, reason, _ := m.MatchDir("x/" + name); !ign || reason != model.ReasonDefault {
			t.Errorf("каталог %s из умолчаний не исключён", name)
		}
		if name == ".git" {
			continue // .git исключается жёстко и как файл (гитлинк подмодуля)
		}
		if ign, _, _ := m.MatchFile("x/" + name); ign {
			t.Errorf("файл с именем %s не должен совпадать с правилом для каталога", name)
		}
	}
}

func FuzzGitignoreParse(f *testing.F) {
	for _, s := range []string{"*.log", "!x", "a/**/b", "\\#", "[", "/", "**", "a\\ ", "\r\n", "!/**/"} {
		f.Add(s, "a/b/c.log")
	}
	f.Fuzz(func(t *testing.T, rules, rel string) {
		m := newMatcher(nil, false, true)
		_ = m.AddGitignore("sub", strings.NewReader(rules)) // не должно паниковать
		m.Match(rel, false)
		m.Match(rel, true)
	})
}

func FuzzGlob(f *testing.F) {
	for _, s := range []string{"**/*.js", "[a-", "a/**", "{a,b}", "\\", "!", "***", "?/?"} {
		f.Add(s, "x/y.js")
	}
	f.Fuzz(func(t *testing.T, glob, rel string) {
		_ = ValidateGlob(glob)
		m := New([]string{glob})
		m.Match(rel, false)
	})
}

// .git нельзя вернуть чужим .gitignore: «!*» в корне выгружаемого репозитория
// иначе втащило бы в бандл .git/config с токеном в url.
func TestGitDirImmutable(t *testing.T) {
	for _, withDefaults := range []bool{true, false} {
		for _, fold := range []bool{false, true} {
			m := newMatcher([]string{"!.git/", "!.git/**"}, fold, withDefaults)
			_ = m.AddGitignore("", strings.NewReader("!*\n!.git/\n!.git/**\n!config\n"))
			_ = m.AddGitignore("sub", strings.NewReader("!*\n"))
			for _, c := range []struct {
				rel   string
				isDir bool
			}{
				{".git", true}, {".git/config", false}, {".git/objects/ab/cd", false},
				{"sub/.git", true}, {"sub/.git/config", false}, {"sub/.git", false},
			} {
				ign, reason, rule := m.Match(c.rel, c.isDir)
				if !ign || reason != model.ReasonDefault || !strings.Contains(rule, "неотменяемое") {
					t.Errorf("defaults=%v fold=%v Match(%q): (%v, %q, %q), ожидали жёсткое исключение", withDefaults, fold, c.rel, ign, reason, rule)
				}
				if c.isDir {
					if ign, _, _ := m.MatchDir(c.rel); !ign {
						t.Errorf("MatchDir(%q) не исключил", c.rel)
					}
				} else if ign, _, _ := m.MatchFile(c.rel); !ign {
					t.Errorf("MatchFile(%q) не исключил", c.rel)
				}
			}
			// Похожие имена не задеваются.
			for _, rel := range []string{".github/x.yml", ".gitignore", ".gitattributes", "a.git/x"} {
				if ign, _, _ := m.Match(rel, false); ign {
					t.Errorf("Match(%q): лишнее исключение", rel)
				}
			}
		}
	}
	if ign, _, _ := newMatcher(nil, true, false).Match(".GIT/config", false); !ign {
		t.Error("на Windows .GIT — тот же каталог")
	}
}

// Фигурные скобки: git их не раскрывает, doublestar раскрывает. Из .gitignore
// скобки буквальны, а в пользовательских масках разрешены спекой.
func TestBracesLiteralInGitignoreExpandedInUserGlob(t *testing.T) {
	for _, fold := range []bool{false, true} {
		m := newMatcher([]string{"user.{js,map}"}, fold, false)
		_ = m.AddGitignore("", strings.NewReader("*.{js,map}\n"))
		cases := []struct {
			rel  string
			want bool
		}{
			{"a.js", false},         // git его не исключает
			{"a.map", false},        //
			{"a.{js,map}", true},    // буквальные скобки совпадают
			{"x/b.{js,map}", true},  // на любой глубине
			{"user.js", true},       // пользовательская маска раскрывается
			{"user.map", true},      //
			{"user.{js,map}", true}, // буквальное имя ловит .gitignore-правило
		}
		for _, c := range cases {
			if got, _, _ := m.Match(c.rel, false); got != c.want {
				t.Errorf("fold=%v Match(%q) = %v, ожидали %v", fold, c.rel, got, c.want)
			}
		}
	}
	// Уже экранированные скобки и класс символов не ломаются.
	if got := escapeBraces(`a\{b}`); got != `a\{b\}` {
		t.Errorf("escapeBraces = %q", got)
	}
	m := newMatcher(nil, false, false)
	_ = m.AddGitignore("", strings.NewReader("f[{]x\n"))
	if got, _, _ := m.Match("f{x", false); !got {
		t.Error("класс [{] должен совпадать с буквальной скобкой")
	}
}
