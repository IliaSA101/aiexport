# 001 — Цикл 1: ядро, режимы на источник, веб-UI

Статус: реализовано 20.10.2026, прошло ревью, аудит безопасности, QA и повторный круг исправлений.
Что разошлось со спекой по ходу и почему — в рабочих записях; главное отличие:
появилась причина исключения `encoding` (файл не в UTF-8) и поле `totals.treeOnly`, а проверка
путей вынесена в отдельный пакет `internal/paths` — в спеке она была размазана по двум местам,
и одна копия отстала, что аудит и нашёл.
 Требования — `docs/ТЗ.md` (п. 1, 2, 3, 4, 5, 7, 9, 10),
решения — `docs/ПЛАН_v1.md`. Закрывает этапы 1 и 3 бэклога и часть этапа 5.

## Зачем именно этот объём

Владельцу нужен работающий инструмент для реальной задачи, а не этап ядра без интерфейса:
собрать бандл из двух корней — `social_network_system_design` полностью и
`SystemDesignFromBalun/homework` только деревом. Поэтому в один цикл сведены ядро (этап 1),
минимальный UI (этап 3) и два режима из трёх (этап 5): `full` и `tree`. Режим `skeleton`
(регулярки по объявлениям) в этот цикл **не входит** — он не нужен для задачи и требует
отдельного разбора по языкам.

## Критерий готовности цикла

1. `aiexport` без аргументов поднимает UI на `127.0.0.1:8471` и открывает браузер.
2. В UI можно добавить два источника с подписями, выбрать режим каждому, увидеть
   число файлов и токенов с зоной, список пропущенного с причиной, и сохранить бандл в файл.
3. `aiexport export --src "full|Подпись|D:\путь" --out bundle.md` делает то же без браузера.
4. `scripts/check.ps1 -Race` зелёный, ноль предупреждений.
5. Оракул-тест ignore-движка против git зелёный на сгенерированном дереве.

## Пакеты

```
internal/model/     типы без зависимостей: Spec, Source, Plan, FileEntry
internal/ignore/    движок правил: .gitignore, дефолты, пользовательские глобы
internal/scan/      обход ФС, лимиты, текст/бинарь, UTF-16
internal/tokens/    o200k_base офлайн, зоны
internal/bundle/    Plan -> Markdown потоком
internal/server/    HTTP API + embed веб-UI
internal/testkit/   генератор тестовых деревьев с ловушками
web/                index.html, app.js, style.css
```

## model

```go
type Mode string // "full" | "tree"

type Source struct {
    Path  string `json:"path"`  // абсолютный путь к корню
    Label string `json:"label"` // человеческая подпись, попадает в заголовки секций
    Mode  Mode   `json:"mode"`
}

type Spec struct {
    Sources      []Source `json:"sources"`
    Exclude      []string `json:"exclude"`      // пользовательские глобы, doublestar
    UseGitignore bool     `json:"useGitignore"` // читать .gitignore в деревьях источников
    MaxFileSize  int64    `json:"maxFileSize"`  // 0 -> DefaultMaxFileSize (256 КБ)
    Instructions string   `json:"instructions"` // свободный текст в конец бандла
}

type Status string // "included" | "skipped"
type Reason string // "", "gitignore", "default", "glob", "binary", "too-big", "mode-tree", "unreadable"

type FileEntry struct {
    Source int    `json:"source"` // индекс в Spec.Sources
    Rel    string `json:"rel"`    // слэш-путь относительно корня источника
    Size   int64  `json:"size"`
    Status Status `json:"status"`
    Reason Reason `json:"reason"`
    Rule   string `json:"rule"`   // конкретный паттерн или файл правила
    Tokens int    `json:"tokens"` // только для included
}

type Plan struct {
    Spec   Spec        `json:"-"`
    Files  []FileEntry `json:"files"`
    Totals Totals      `json:"totals"`
}
```

**Содержимое файлов в `Plan` не хранится.** На 12 500 файлах и 110 МБ это бюджет памяти,
который нечем оправдать: `Plan` строится один раз, рендер читает файлы повторно, попадая
в кэш ОС. Цена — окно между планом и рендером, в котором файл может измениться; при
расхождении размера рендер пишет пометку в бандл, а не молчит.

## ignore

```go
type Matcher struct{ ... }
func New(extra []string) *Matcher            // дефолты + пользовательские глобы
func (m *Matcher) AddGitignore(dirRel string, r io.Reader) error
func (m *Matcher) Match(rel string, isDir bool) (ignored bool, reason model.Reason, rule string)
```

Семантика как в git: правила применяются по порядку, **последнее совпадение выигрывает**,
`!` отменяет, ведущий `/` якорит к каталогу своего `.gitignore`, завершающий `/` матчит
только каталоги, вложенный `.gitignore` переопределяет родительский. Пути внутри — всегда
слэш; на Windows сравнение регистронезависимое.

Дефолтный список: `.git/`, `node_modules/`, `vendor/`, `dist/`, `build/`, `.venv/`,
`__pycache__/`, `.obsidian/`, `.idea/`, `.vscode/`, `.gotmp/`.

Отдельный метод для каталога обязателен: обход отсекает каталог целиком через `fs.SkipDir`
до рекурсии, иначе 12 117 записей `vendor` и `node_modules` всё равно будут обойдены.

## scan

```go
type Limits struct{ MaxFileSize int64; MaxFiles int }
func Walk(ctx context.Context, src model.Source, idx int, lim Limits) ([]model.FileEntry, error)
```

- `filepath.WalkDir`; на игнорируемом каталоге — `fs.SkipDir`.
- Симлинки на каталоги не разворачиваем (циклы и выход за корень); файл-симлинк читаем.
- Чтение внутри корня — через `os.Root`, склейки путей строками быть не должно.
- Лимит размера проверяется **до** чтения, по `DirEntry.Info()`.
- Бинарность: первые 512 байт; BOM UTF-16 (LE/BE) -> декодировать `unicode/utf16` и
  проверять уже декодированное; NUL-байт -> `Reason("binary")`.
- `ctx.Err()` проверять на каждом N-ном файле, обход прерываемый.
- Ошибка чтения отдельного файла не валит обход: `Reason("unreadable")` + `Rule` с текстом.

## tokens

```go
func NewCounter() (*Counter, error) // офлайн-словарь, o200k_base
func (c *Counter) Count(s string) int
func Zone(total int) string // "green" <= 80000, "yellow" <= 120000, иначе "red"
```

Офлайн обязателен: `tiktoken.SetBpeLoader(tiktoken_loader.NewOfflineLoader())` до
`GetEncoding`. Без этого библиотека молча лезет в интернет при первом вызове, и бинарник
перестаёт быть самодостаточным. Проверено 20.10.2026: словарь вшивается, сеть не нужна.

Пороги зон — значение по умолчанию, вынести в константы пакета одним местом.

## bundle

```go
func Render(ctx context.Context, p model.Plan, w io.Writer) error
```

Потоком в `io.Writer`, без накопления всего бандла в памяти. Порядок секций:

1. Шапка: дата, число источников, включено файлов, токенов, зона.
2. **Манифест** — нумерованный список включённых файлов `[Подпись] rel/path (N токенов)`.
   Это инструмент проверки: «сколько файлов в манифесте и все ли у тебя есть».
3. Дерево по каждому источнику.
4. **«Что сознательно не включено»** — агрегировано по причинам, с числом файлов и
   примерами. Без этой секции модель считает отсутствующий каталог несуществующим.
5. Содержимое файлов: `## [Подпись] rel/path`, затем фенс с языком по расширению.
   Фенс длиннее, если внутри файла встречается тройной обратный апостроф — иначе бандл рвётся.
6. Источники в режиме `tree`: только дерево и явная строка, что содержимое не включено
   намеренно. Тихая потеря — главный класс ошибок этого инструмента.
7. Хвост: свободные инструкции из `Spec.Instructions`.

Запись в файл — атомарная: временный файл рядом, `Sync`, `Close`, `Rename`.

## server

Только локальный режим. Токен, `serve` и `--allow-root` — этап 7, в этот цикл не входят.

- Слушает строго `127.0.0.1:8471`; при занятом порте — внятная ошибка, не молчаливый
  переход на другой.
- **Проверка заголовка `Host`**: только `127.0.0.1:порт` и `localhost:порт`. Иначе 403.
  Без неё любой сайт в браузере пользователя читает его диск через DNS rebinding.
- Заголовок `Content-Security-Policy: default-src 'self'`.
- `http.Server` с `ReadHeaderTimeout`, graceful shutdown по SIGINT.
- Браузер открывается `exec.Command` отдельными аргументами, без shell.

API:

| Метод | Путь | Тело / параметры | Ответ |
| --- | --- | --- | --- |
| GET | `/` | — | `index.html` из `embed.FS` |
| GET | `/app.js`, `/style.css` | — | из `embed.FS` |
| POST | `/api/plan` | `Spec` | `PlanResponse` |
| POST | `/api/export` | `Spec` + `out`, `download` | `ExportResponse` или файл |
| GET | `/api/browse` | `?path=` | `BrowseResponse` |

```go
type PlanResponse struct {
    Totals  Totals        `json:"totals"`  // included, skipped, bytes, tokens, zone
    Sources []SourceStat  `json:"sources"` // label, path, mode, files, tokens
    Files   []FileEntry   `json:"files"`   // только included
    Skipped []FileEntry   `json:"skipped"` // первые 500
    SkipByReason map[string]int `json:"skipByReason"`
}

type BrowseResponse struct {
    Path   string   `json:"path"`
    Parent string   `json:"parent"`
    Dirs   []string `json:"dirs"`
    Drives []string `json:"drives"` // непусто, когда path пуст (Windows)
}
```

`/api/browse` отвергает UNC (`\\server\share`), `\\?\` и `\??\`: Windows при обращении к
UNC отправляет NTLM-хэш на чужой сервер. Нормализация — **до** проверки, не после.

## Уточнения контракта (после того, как UI был написан)

UI сделан первым и упёрся в недосказанности спеки. Зафиксировано здесь, сервер обязан
соответствовать:

1. **Раздавать весь `web/`**, а не три файла: UI разложен на ES-модули
   (`app.js`, `state.js`, `api.js`, `sources.js`, `picker.js`, `result.js`, `dom.js`)
   плюс `index.html`, `style.css`, `favicon.svg`. Отдавать через `http.FileServerFS` по
   `embed.FS` с MIME `text/javascript` для `.js` — иначе браузер не загрузит модули.
2. **`ExportResponse`**: `{"path":"<абсолютный путь сохранённого файла>","bytes":N,"tokens":N}`.
   Если в запросе `out` пустой — сервер сам выбирает имя (`aiexport-ГГГГ-ММ-ДД-ЧЧММ.md`
   в текущем каталоге) и возвращает выбранный путь в `path`. Поле `download: true`
   отдаёт тело файла с `Content-Disposition` вместо записи на диск.
3. **`BrowseResponse.dirs` — полные пути**, не имена. `drives` — вида `C:\`.
   Недоступные и скрытые каталоги сервер отфильтровывает сам, а не роняет запрос.
   На корне диска `parent` — пустая строка: UI по ней показывает список дисков.
4. **Ошибки** — JSON `{"error":"текст по-русски"}` с кодом 4xx/5xx. Это касается и отказа
   на UNC в `/api/browse`, и 403 по чужому `Host`.
5. **Ключи JSON**: `Totals` — `included`, `skipped`, `bytes`, `tokens`, `zone`;
   `SourceStat` — `label`, `path`, `mode`, `files`, `tokens`. Порядок `sources` в ответе
   совпадает с порядком в `Spec`: UI берёт подпись как `sources[file.source].label`.
6. Пустую подпись источника UI подставляет сам (имя последней папки пути), но сервер
   обязан пережить и пустую: подпись участвует в заголовках секций.

## CLI

```
aiexport                                 # UI на 127.0.0.1:8471
aiexport --port 8479 --no-browser        # то же, но не открывать браузер
aiexport export --src "full|Подпись|D:\путь" [--src ...] --out bundle.md
       [--no-gitignore] [--exclude "**/*.min.js"] [--max-size 262144]
aiexport --version
```

`--no-browser` обязателен, а не удобство: QA и автоматические прогоны поднимают программу
на своём порту, и открывшееся окно браузера — это окно на экране владельца посреди его
работы. Такой случай уже был в соседнем проекте (команда «показать в проводнике», вызванная
из теста). `--port` нужен затем же: рабочий порт 8471 не должен занимать тестовый прогон.

Разделитель `|`, а не `:`: в путях Windows двоеточие после буквы диска.
Ошибка разбора аргументов — код 2 через существующий `errUsage`, без дублирования текста.

## web

Без сборщика, нативные ES-модули, без CDN, без inline-скриптов и `onclick=`.
Имена файлов приходят из чужой ФС — это недоверенный ввод, только `textContent`,
никаких `innerHTML`.

Экран один:
- список источников (путь, подпись, режим, кнопки вверх/вниз/удалить);
- «Добавить источник» с полем пути и кнопкой «Обзор» (ходит в `/api/browse`);
- поля: глобы исключений, учитывать `.gitignore`, лимит размера, свободные инструкции;
- кнопка «Посчитать» -> сводка: файлов, токенов, зона цветом, таблица пропущенного
  с причинами, свёрнутый список включённых файлов;
- кнопка «Собрать» -> сохранение в файл, путь показывается строкой.

Порядок источников задаёт порядок секций в бандле: релевантное ставится в конец,
вплотную к вопросу.

## Тесты

- **ignore**: таблица случаев семантики git (порядок, `!`, якорь, каталог, вложенность);
  **оракул** против `git ls-files --others --exclude-standard` на дереве из `testkit`.
  Код выхода `git check-ignore` вердиктом не считать: при отрицаниях он печатает последнее
  совпавшее правило, а не итог (проверено 07.10.2026).
- **scan**: NUL, UTF-16+BOM, CRLF, файл сверх лимита, симлинк-цикл — через `testkit`.
- **bundle**: golden-файл; отдельный случай — файл с тройным обратным апострофом внутри.
- **tokens**: офлайн-счёт без сети; стабильность числа на фиксированной строке.
- **server**: `httptest` — чужой `Host` даёт 403, `/api/browse` отвергает UNC.
- **fuzz**: `FuzzGitignoreParse`, `FuzzGlob`.

## Зависимости, добавляемые в этом цикле

| Библиотека | Версия | Зачем |
| --- | --- | --- |
| `bmatcuk/doublestar/v4` | последняя стабильная | глобы с `**`, которых нет в `filepath.Match` |
| `pkoukk/tiktoken-go` | v0.1.8 | токены `o200k_base` |
| `pkoukk/tiktoken-go-loader` | v0.0.2 | офлайн-словарь BPE, без сети |

YAML-парсер не добавляется: `.aiexport.yaml` и профили — следующий цикл.
