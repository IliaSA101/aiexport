// Пакет model — общие типы без зависимостей: их используют и обход, и рендер,
// и HTTP API, поэтому здесь нет ничего, кроме данных и констант.
package model

import "time"

// DefaultMaxFileSize — лимит размера файла по умолчанию (256 КБ). Файл крупнее
// почти всегда сгенерирован или является данными, а не кодом: в бандле он съел бы
// бюджет токенов впустую.
const DefaultMaxFileSize int64 = 256 * 1024

// Mode — режим выгрузки источника.
type Mode string

// Режимы источника. skeleton в этот цикл не входит.
const (
	ModeFull Mode = "full" // дерево и содержимое файлов
	ModeTree Mode = "tree" // только дерево, содержимое не включается намеренно
)

// Valid сообщает, известен ли режим. Неизвестный режим нельзя молча считать full:
// пользователь, заказавший «только дерево», получил бы весь код.
func (m Mode) Valid() bool { return m == ModeFull || m == ModeTree }

// Source — корень, из которого собирается часть бандла.
type Source struct {
	Path  string `json:"path"`  // абсолютный путь к корню
	Label string `json:"label"` // человеческая подпись, попадает в заголовки секций
	Mode  Mode   `json:"mode"`
}

// Spec — задание на сборку: что и как собирать.
type Spec struct {
	Sources      []Source `json:"sources"`
	Exclude      []string `json:"exclude"`      // пользовательские глобы, doublestar
	UseGitignore bool     `json:"useGitignore"` // читать .gitignore в деревьях источников
	MaxFileSize  int64    `json:"maxFileSize"`  // 0 -> DefaultMaxFileSize
	Instructions string   `json:"instructions"` // свободный текст в конец бандла
}

// Status — судьба файла в бандле.
type Status string

// Статусы записи плана.
const (
	StatusIncluded Status = "included"
	StatusSkipped  Status = "skipped"
)

// Reason — причина, по которой запись не попала в содержимое бандла.
type Reason string

// Причины пропуска. Список расширен относительно спеки тремя значениями —
// ReasonSymlink, ReasonTooDeep и ReasonEncoding: все три случая иначе пришлось
// бы терять молча (или, для кодировки, отдавать модели мусор вместо текста).
const (
	ReasonNone       Reason = ""
	ReasonGitignore  Reason = "gitignore"
	ReasonDefault    Reason = "default"
	ReasonGlob       Reason = "glob"
	ReasonBinary     Reason = "binary"
	ReasonTooBig     Reason = "too-big"
	ReasonModeTree   Reason = "mode-tree"
	ReasonUnreadable Reason = "unreadable"
	ReasonEncoding   Reason = "encoding" // не UTF-8 (cp1251 и т.п.): перекодировка не выполняется
	ReasonSymlink    Reason = "symlink"  // симлинк на каталог: не разворачиваем
	ReasonTooDeep    Reason = "too-deep" // каталог глубже лимита обхода
)

// FileEntry — одна запись плана. Содержимое файла здесь не хранится.
type FileEntry struct {
	Source int    `json:"source"` // индекс в Spec.Sources
	Rel    string `json:"rel"`    // слэш-путь относительно корня источника
	Size   int64  `json:"size"`
	Status Status `json:"status"`
	Reason Reason `json:"reason"`
	Rule   string `json:"rule"`   // конкретный паттерн или файл правила
	Tokens int    `json:"tokens"` // только для included
	// Dir — запись описывает каталог, отсечённый целиком (без обхода внутри).
	// Поле добавлено к спеке: «vendor/» как один пропущенный элемент честнее,
	// чем 12 000 записей, и честнее, чем молчание.
	Dir bool `json:"dir,omitempty"`
}

// Totals — сводка плана.
type Totals struct {
	Included int `json:"included"`
	// TreeOnly — файлы источников в режиме tree: присутствуют в дереве намеренно,
	// содержимое не включается. В Skipped не входят — это не потеря, и они не
	// должны выглядеть «пропущенными» в одном ряду с бинарниками и секретами.
	TreeOnly int `json:"treeOnly"`
	// Skipped — настоящие исключения (все причины, кроме mode-tree).
	Skipped int    `json:"skipped"`
	Bytes   int64  `json:"bytes"` // суммарный размер включённых файлов
	Tokens  int    `json:"tokens"`
	Zone    string `json:"zone"` // green | yellow | red
}

// Plan — результат обхода всех источников; по нему рендерится бандл.
type Plan struct {
	Spec    Spec        `json:"-"`
	Files   []FileEntry `json:"files"`
	Totals  Totals      `json:"totals"`
	Created time.Time   `json:"created"` // время построения; рендер печатает его в шапку
}
