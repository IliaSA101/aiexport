#!/usr/bin/env bash
# Сборка выпускных архивов aiexport (запускается на Linux, нужны GNU tar и GNU coreutils):
#   scripts/release.sh ВЕРСИЯ [--dirty]
#
# ВЕРСИЯ — строго X.Y.Z, без «v». Результат — dist/ВЕРСИЯ/: по архиву на каждую из пяти целей
# и SHA256SUMS. Собирается во временном каталоге dist/.ВЕРСИЯ.tmp и переносится в dist/ВЕРСИЯ
# одним mv после `sha256sum -c`: неполного набора в dist/ВЕРСИЯ не бывает, а прежний выпуск
# той же версии заменяется только при полном успехе. Соседние выпуски не трогаются.
# Боевой выпуск — только с чистого дерева, включая неотслеживаемые файлы: иначе по версии не
# восстановить исходники. --dirty разрешает грязное дерево для пробных прогонов и пишет в
# dist/ВЕРСИЯ-dirty/ (и имена архивов, и вшитая версия — ВЕРСИЯ-dirty), чтобы пробный набор
# нельзя было выложить вместо боевого или принять за него.
# Сборка идёт в явном окружении (GOENV=off, GOAMD64=v1, GOARM64=v8.0, без GOFLAGS), чтобы
# `go env -w` на машине не менял бинарник; настройки доступа к сети и кэшу при этом сохраняются.
#
# Порядок: проверки входа (в т.ч. каждый входящий в сборку файл отслеживается git) -> сборка всех целей ->
# проверка собранного -> упаковка -> SHA256SUMS -> перенос в dist/.
# Проверки собранного идут до упаковки: негодный бинарник не должен попасть в архив даже на минуту.
# Любой провал — сообщение по-русски и exit 1.
set -euo pipefail
# Локаль C: в UTF-8 локали [0-9] пропускает «①», «０» и т. п., а порядок sort -u зависит от
# локали — выпуск перестал бы быть воспроизводимым.
export LC_ALL=C

# Цели выпуска: ОС/архитектура.
targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64)

fail() {
  echo "ОШИБКА: $*" >&2
  exit 1
}

usage() {
  echo "использование: scripts/release.sh ВЕРСИЯ [--dirty]   (ВЕРСИЯ вида 1.2.3, без «v»)" >&2
  exit 2
}

# ---------- разбор аргументов ----------
version='' dirty=0
for a in "$@"; do
  case "$a" in
    --dirty) dirty=1 ;;
    -*) echo "неизвестный флаг: $a" >&2; usage ;;
    *)
      [ -z "$version" ] || usage
      version=$a
      ;;
  esac
done
[ -n "$version" ] || usage

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

# ---------- 1. проверки входа ----------
# Строгий формат важен вдвойне: версия идёт в имя каталога, который мы затем стираем (rm -rf).
if ! [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  fail "версия «$version» не подходит: нужен формат X.Y.Z (например 0.1.0), без «v»"
fi
[ -f LICENSE ] || fail "нет LICENSE: лицензию выбирает владелец"
[ -f README.md ] || fail "нет README.md в корне проекта"

# Go ищем как check.sh: сначала PATH, затем штатное место установки.
if ! command -v go >/dev/null 2>&1; then
  if [ -x /usr/local/go/bin/go ]; then
    PATH="$PATH:/usr/local/go/bin"
  else
    fail "go не найден ни в PATH, ни в /usr/local/go/bin"
  fi
fi
# Унаследованный GOTMPDIR с несуществующим каталогом ломает go невнятной ошибкой посреди сборки.
if [ -n "${GOTMPDIR:-}" ] && [ ! -d "$GOTMPDIR" ]; then
  fail "GOTMPDIR указывает на несуществующий каталог: $GOTMPDIR (убрать: unset GOTMPDIR)"
fi
for tool in git tar gzip zip sha256sum touch grep sort awk; do
  command -v "$tool" >/dev/null 2>&1 || fail "не найдена утилита $tool"
done
# Упаковка опирается на GNU-ключи (tar --sort/--owner/--mtime, touch -d @N). bsdtar и BSD touch
# их не знают или трактуют иначе — архивы вышли бы другими, поэтому сверяем заранее.
tar_ver=$(tar --version 2>&1 || true)
grep -q 'GNU tar' <<<"$tar_ver" || fail "нужны GNU tar/coreutils (Linux): tar не GNU"
touch_probe=$(mktemp)
touch -d @0 "$touch_probe" 2>/dev/null || { rm -f -- "$touch_probe"; fail "нужны GNU tar/coreutils (Linux): touch не понимает -d @0"; }
rm -f -- "$touch_probe"

# toolchain в go.mod — лишь минимум: где свой go новее, собирает он, и суммы со второй машины
# не сходятся (22.10.2026: здесь go1.27.3, на Windows go1.26.9). Поэтому собираем ровно им.
want_toolchain=$(awk '$1 == "toolchain" { print $2; exit }' go.mod) || fail "не удалось прочитать go.mod"
[ -n "$want_toolchain" ] || fail "в go.mod нет директивы toolchain: нечем зафиксировать компилятор"
export GOTOOLCHAIN=$want_toolchain
want_toolchain=${want_toolchain#go}
# Печатаем сразу: видно, каким тулчейном пойдёт сборка; здесь же он скачивается, если его нет.
go version || fail "нет go$want_toolchain и скачать не вышло (сеть, GOPROXY)"

# Окружение сборки задаём явно: `go env -w` и переменные оболочки на машине не должны менять
# бинарник. Пустое GOFLAGS= не перекрывает файл настроек go env -w, поэтому GOENV=off — он
# отключает этот файл целиком. GOAMD64/GOARM64 фиксируем на базовом уровне: бинарник должен
# запускаться на любом процессоре архитектуры, а не только на машине сборки.
# GOENV=off отбрасывает заодно настройки сети и кэша (прокси, приватные модули, каталоги,
# тулчейн). Код бинарника они не меняют, но без них сборка за прокси или с приватными
# модулями упала бы, поэтому читаем их ДО отключения файла и передаём переменными окружения.
for v in GOPROXY GOSUMDB GOMODCACHE GOPATH GOPRIVATE GONOSUMDB GONOPROXY GOINSECURE; do
  val=$(go env "$v") || fail "go env $v не прочитал настройку"
  if [ -n "$val" ]; then export "$v=$val"; fi
done
unset v val
export GOENV=off GOWORK=off GOFLAGS= GOEXPERIMENT= GOAMD64=v1 GOARM64=v8.0

git rev-parse --git-dir >/dev/null 2>&1 || fail "$root не git-репозиторий: не из чего брать время сборки"
# Неотслеживаемые файлы тоже считаем: забытый файл должен остановить выпуск до сборки, а не
# после пяти целей на vcs.modified. --untracked-files=all обязателен: без флага команда
# подчиняется status.showUntrackedFiles=no из конфигурации git, и новый .go-файл прошёл бы
# незамеченным.
if [ "$dirty" -eq 0 ] && [ -n "$(git status --porcelain --untracked-files=all)" ]; then
  fail "дерево не чистое (правки или неотслеживаемые файлы, см. git status) — закоммитьте или уберите (или передайте --dirty для пробной сборки)"
fi
[ "$dirty" -eq 0 ] || echo "ВНИМАНИЕ: --dirty, сборка не воспроизводится по коммиту; результат — dist/$version-dirty" >&2

# Эпоха = время последнего коммита: одни и те же исходники дают побайтно те же архивы.
epoch=$(git log -1 --format=%ct) || fail "не удалось получить время последнего коммита"
[[ $epoch =~ ^[0-9]+$ ]] || fail "в репозитории нет коммитов: нечем задать время файлов в архивах"
export SOURCE_DATE_EPOCH=$epoch

hostos=$(go env GOHOSTOS)
hostarch=$(go env GOHOSTARCH)

# ---------- подготовка каталога выпуска ----------
label=$version
[ "$dirty" -eq 0 ] || label=$version-dirty
final="$root/dist/$label"
# Всё собирается во временном каталоге внутри dist/ (та же файловая система, значит mv — переименование):
# в final набор попадает целиком или не попадает вовсе.
work="$root/dist/.$label.tmp"
stage="$work/stage"
out="$work/pub"

# Временное дерево для пробной выгрузки; trap срабатывает и при падении посреди скрипта.
smoke=''
cleanup() {
  [ -z "$smoke" ] || rm -rf -- "$smoke"
  rm -rf -- "$work"
}
trap cleanup EXIT
# Без этого Ctrl-C/kill в bash могут не пройти через EXIT-trap и оставить временный каталог.
trap 'exit 130' INT
trap 'exit 143' TERM
# Формат версии проверен выше, поэтому путь заведомо не пуст и без «..»: rm -rf безопасен.
rm -rf -- "$work"
mkdir -p -- "$stage" "$out"

# Имена и пути цели: считаем в одном месте, чтобы сборка, проверка и упаковка не разошлись.
pkgname() { echo "aiexport-$label-${1%/*}-${1#*/}"; }
binname() { if [ "${1%/*}" = windows ]; then echo aiexport.exe; else echo aiexport; fi; }
binpath() { echo "$stage/$(pkgname "$1")/$(binname "$1")"; }

# ---------- уведомления о сторонних лицензиях ----------
# MIT и BSD-3 требуют сохранять текст уведомления при распространении бинарника, поэтому он
# едет в каждом архиве. Список модулей берём у go из реального графа сборки, а не пишем руками:
# новая зависимость тогда попадёт сюда сама, а модуль без лицензии остановит выпуск.
# Граф берём по всем целям и объединяем: зависимость, подключаемая только под одну ОС
# (build-теги), иначе не попала бы в уведомления, хотя едет в её бинарнике.
make_notices() {
  local file=$1 self mods all='' part t path ver dir lic found goroot
  self=$(go list -m) || fail "go list -m не определил модуль проекта"
  for t in "${targets[@]}"; do
    part=$(CGO_ENABLED=0 GOOS=${t%/*} GOARCH=${t#*/} go list -deps \
      -f '{{if not .Standard}}{{with .Module}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' \
      ./cmd/aiexport) || fail "go list не вывел список зависимостей для $t"
    all+=$part$'\n'
  done
  mods=$(sort -u <<<"$all") || fail "не удалось объединить списки зависимостей"
  {
    echo "Сторонние компоненты, вошедшие в aiexport, и тексты их лицензий."
    while read -r path ver dir; do
      # Пустые строки дают пакеты стандартной библиотеки (шаблон для них ничего не печатает).
      [ -n "$path" ] || continue
      [ "$path" != "$self" ] || continue
      # Версия бывает пустой только у главного модуля, а он отсеян выше; пустой Dir — модуль не в кэше.
      [ -n "$dir" ] && [ -d "$dir" ] || fail "модуль $path: нет каталога с исходниками, лицензию не прочитать"
      found=0
      for lic in "$dir"/LICENSE*; do
        [ -f "$lic" ] || continue
        found=1
        printf '\n================================================================\n%s %s\n(%s)\n================================================================\n\n' \
          "$path" "$ver" "$(basename -- "$lic")"
        cat -- "$lic"
        echo
      done
      [ "$found" -eq 1 ] || fail "у модуля $path нет файла лицензии (LICENSE*) — выпуск нельзя"
    done <<<"$mods"
    goroot=$(go env GOROOT)
    [ -f "$goroot/LICENSE" ] || fail "нет $goroot/LICENSE: лицензия Go не найдена"
    printf '\n================================================================\nGo (стандартная библиотека и рантайм)\n================================================================\n\n'
    cat -- "$goroot/LICENSE"
  } >"$file" || fail "не удалось собрать $file"
}

# ---------- файлы сборки под контролем git ----------
# Go берёт файлы из рабочего каталога, а не из git: игнорируемый файл (web/debug.log для
# //go:embed, internal/x/coverage.go под незаякоренный «coverage.*») молча попал бы в бинарник,
# а vcs.modified его не видит. Поэтому каждый входящий в сборку файл (вшитые, .go/.s/.h/.syso)
# обязан отслеживаться git. Без --dirty отказ при любом неотслеживаемом. С --dirty пробный
# прогон нужен как раз для нового, ещё не добавленного файла: отказ только для игнорируемых
# (утечка, а не чистота дерева), прочие — предупреждение.
# Набор файлов зависит от ОС (теги), потому обходим все цели.
check_tracked() {
  local t lines dir f rel base rc
  local -a parts
  local -A seen=()
  for t in "${targets[@]}"; do
    # Только пакеты главного модуля: чужие модули из кэша в наше дерево не входят.
    lines=$(CGO_ENABLED=0 GOOS=${t%/*} GOARCH=${t#*/} go list -deps \
      -f '{{if not .Standard}}{{if .Module}}{{if .Module.Main}}{{.Dir}}{{range .EmbedFiles}}{{"\t"}}{{.}}{{end}}{{range .GoFiles}}{{"\t"}}{{.}}{{end}}{{range .CgoFiles}}{{"\t"}}{{.}}{{end}}{{range .SFiles}}{{"\t"}}{{.}}{{end}}{{range .HFiles}}{{"\t"}}{{.}}{{end}}{{range .SysoFiles}}{{"\t"}}{{.}}{{end}}{{end}}{{end}}{{end}}' \
      ./cmd/aiexport) || fail "go list не вывел файлы сборки для $t"
    while IFS=$'\t' read -r -a parts; do
      [ "${#parts[@]}" -gt 1 ] || continue
      dir=${parts[0]}
      if [ "$dir" = "$root" ]; then base=''; else base=${dir#"$root"/}/; fi
      for f in "${parts[@]:1}"; do
        rel=$base$f
        [ -z "${seen[$rel]:-}" ] || continue
        seen[$rel]=1
        # --literal-pathspecs: «*» и «[» в имени файла не должны трактоваться как маска.
        git --literal-pathspecs ls-files --error-unmatch -- "$rel" >/dev/null 2>&1 && continue
        if [ "$dirty" -eq 0 ]; then
          fail "файл сборки $rel не отслеживается git (игнорируется или не добавлен) — он попал бы в бинарник; удалите его или добавьте в git"
        fi
        # check-ignore: 0 — игнорируется, 1 — нет; прочее (128) — ошибка git, не гадаем.
        # Без --literal-pathspecs: команда не знает pathspec-магии (падает), пути берёт буквально.
        rc=0
        git check-ignore -q -- "$rel" >/dev/null 2>&1 || rc=$?
        case $rc in
          0) fail "файл сборки $rel игнорируется git, но попал бы в бинарник — удалите его или поправьте .gitignore (даже для --dirty)" ;;
          1) echo "ВНИМАНИЕ: файл сборки $rel не добавлен в git — пробная сборка его включит" >&2 ;;
          *) fail "git check-ignore не смог проверить $rel (код $rc)" ;;
        esac
      done
    done <<<"$lines"
  done
}

# ---------- 2. сборка ----------
build_all() {
  local t os arch notices="$stage/THIRD_PARTY_NOTICES.txt"
  make_notices "$notices"
  for t in "${targets[@]}"; do
    os=${t%/*} arch=${t#*/}
    echo "сборка $t"
    mkdir -p -- "$stage/$(pkgname "$t")"
    # CGO_ENABLED=0 явно: случайная cgo-зависимость должна падать здесь, а не у пользователя.
    # -trimpath убирает пути машины сборки, -s -w — таблицы символов и DWARF (размер).
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
      -ldflags "-s -w -X main.version=$label" \
      -o "$(binpath "$t")" ./cmd/aiexport || fail "сборка $t не удалась"
    cp -- README.md LICENSE "$notices" "$stage/$(pkgname "$t")/"
  done
}

# ---------- 5. проверки собранного ----------
# Версия go в бинарнике ровно та, что в toolchain из go.mod (GOTOOLCHAIN выше): иначе суммы
# выпуска не воспроизвести на другой машине.
check_toolchain() {
  local t=$1 info=$2 first got
  # Первая строка вывода `go version -m`: «путь: go1.26.9».
  first=$(head -n 1 <<<"$info")
  got=${first##*: }
  got=${got%% *}
  [[ $got =~ ^go[0-9] ]] || fail "$t: не разобрана версия go из «$first»"
  got=${got#go}
  [ "$got" = "$want_toolchain" ] ||
    fail "$t: бинарник собран go$got, а выпуск собирается ровно go$want_toolchain из go.mod"
}

# Метаданные сборки, которые Go вшивает в бинарник: убеждаемся, что собрано именно то, что заказано.
check_buildinfo() {
  local t=$1 os=${1%/*} arch=${1#*/} bin info
  bin=$(binpath "$t")
  info=$(go version -m "$bin") || fail "$t: go version -m не прочитал бинарник"
  check_toolchain "$t" "$info"
  # Разделитель между ключом и значением в выводе — табуляция или «=», поэтому допускаем оба.
  grep -Eq "build[[:space:]]+GOOS=$os\$" <<<"$info" || fail "$t: в бинарнике GOOS не $os"
  grep -Eq "build[[:space:]]+GOARCH=$arch\$" <<<"$info" || fail "$t: в бинарнике GOARCH не $arch"
  grep -Eq "build[[:space:]]+CGO_ENABLED=0\$" <<<"$info" || fail "$t: бинарник собран не с CGO_ENABLED=0"
  grep -Eq "build[[:space:]]+-trimpath=true\$" <<<"$info" || fail "$t: нет -trimpath=true"
  # Теги сборки меняют состав кода: в выпуске их быть не должно (перенесённый GOFLAGS и т. п.).
  if grep -Eq "build[[:space:]]+-tags=" <<<"$info"; then
    fail "$t: бинарник собран с -tags — в выпуске теги сборки недопустимы"
  fi
  # Уровень архитектуры: v1 / v8.0 — запуск на любом процессоре, а не только на машине сборки.
  case $arch in
    amd64) grep -Eq "build[[:space:]]+GOAMD64=v1\$" <<<"$info" || fail "$t: в бинарнике нет GOAMD64=v1" ;;
    arm64) grep -Eq "build[[:space:]]+GOARM64=v8\.0\$" <<<"$info" || fail "$t: в бинарнике нет GOARM64=v8.0" ;;
  esac
  # С -trimpath Go не пишет -ldflags в buildinfo (во флагах бывают пути), поэтому подстановку
  # версии так не проверить. Слабая проверка: строка версии вообще есть в бинарнике. Сильная —
  # `--version` на своей платформе, она в check_host_run.
  [ "$(grep -a -c -F -- "$label" "$bin" || true)" != 0 ] ||
    fail "$t: в бинарнике нет строки версии $label"
  # Бинарник должен быть собран ровно из коммита. vcs.modified=false страхует от правок
  # отслеживаемых файлов, но не полностью от неотслеживаемых: Go считает его той же командой
  # git status, поэтому при status.showUntrackedFiles=no забытый файл он не видит, а игнорируемые
  # git файлы не видит никогда. Входящие в сборку файлы вне git ловит check_tracked.
  grep -Eq "build[[:space:]]+vcs=git\$" <<<"$info" || fail "$t: в бинарнике нет vcs=git"
  if [ "$dirty" -eq 0 ]; then
    grep -Eq "build[[:space:]]+vcs\.modified=false\$" <<<"$info" ||
      fail "$t: бинарник собран из изменённого дерева (vcs.modified не false) — закоммитьте или уберите правки (git status)"
  fi
}

# Утечка путей, имени и почты владельца в публичный выпуск. -trimpath убирает пути сборки, но
# проверка нужна именно потому, что молча сломаться он может (забытый флаг, путь из //go:embed,
# строка в тесте). Иглы собираем один раз, а ищем и в бинарнике, и в текстах, что едут в архиве.
needles=() needle_names=()
add_needle() {
  local what=$1 value=$2
  # Пустое значение совпало бы со всем подряд — проверка потеряла бы смысл.
  [ -n "$value" ] || return 0
  # Короткая игла («/», «dev», «user») встречается в самом Go: ложная тревога хуже пропуска.
  if [ "${#value}" -lt 4 ]; then
    echo "ВНИМАНИЕ: проверка утечки «$what» пропущена: значение короче 4 символов" >&2
    return 0
  fi
  needles+=("$value")
  needle_names+=("$what")
}

init_needles() {
  local host
  add_needle '$HOME' "${HOME:-}"
  add_needle 'текущий каталог' "$(pwd)"
  add_needle 'текущий каталог (pwd -P)' "$(pwd -P)"
  # Имя пользователя — только как часть пути: голое «runner», «user», «dev» есть в самом Go.
  add_needle 'имя пользователя в пути' "/$(id -un 2>/dev/null || true)/"
  add_needle 'GOMODCACHE' "$(go env GOMODCACHE)"
  add_needle 'GOCACHE' "$(go env GOCACHE)"
  add_needle 'GOPATH' "$(go env GOPATH)"
  host=$(hostname 2>/dev/null || true)
  # «localhost» встречается в сетевом коде самого Go: на машинах с таким именем это не утечка.
  [ "$host" = localhost ] || add_needle 'имя хоста' "$host"
  add_needle 'почта из git config' "$(git config user.email 2>/dev/null || true)"
  add_needle 'имя из git config' "$(git config user.name 2>/dev/null || true)"
}

check_leaks() {
  local t=$1 pkg f i
  pkg="$stage/$(pkgname "$t")"
  # -a: читать бинарник как текст; -c: считать, а не печатать «Binary file matches».
  for f in "$(binpath "$t")" "$pkg/README.md" "$pkg/THIRD_PARTY_NOTICES.txt"; do
    for i in "${!needles[@]}"; do
      if [ "$(grep -a -c -F -- "${needles[$i]}" "$f" || true)" != 0 ]; then
        fail "$t: в $(basename -- "$f") найден(о) ${needle_names[$i]} («${needles[$i]}») — утечка данных машины сборки"
      fi
    done
  done
}

# Только для цели, совпадающей с машиной: остальные запустить негде.
check_host_run() {
  local t=$1 bin got marker=ЛИСТ-ПРОВЕРКИ-$RANDOM$RANDOM
  bin=$(binpath "$t")
  got=$("$bin" --version) || fail "$t: --version завершился с ошибкой"
  [ "$got" = "aiexport $label" ] ||
    fail "$t: --version напечатал «$got», ожидалось «aiexport $label»"

  smoke=$(mktemp -d)
  mkdir -p -- "$smoke/tree"
  printf 'пробный файл\n%s\n' "$marker" >"$smoke/tree/probe.txt"
  # timeout: зависшая выгрузка не должна повесить выпуск.
  if command -v timeout >/dev/null 2>&1; then
    timeout 120 "$bin" export --src "full|проба|$smoke/tree" --out "$smoke/out.md" >"$smoke/log" 2>&1 ||
      fail "$t: пробная выгрузка завершилась с ошибкой: $(tail -n 5 "$smoke/log")"
  else
    "$bin" export --src "full|проба|$smoke/tree" --out "$smoke/out.md" >"$smoke/log" 2>&1 ||
      fail "$t: пробная выгрузка завершилась с ошибкой: $(tail -n 5 "$smoke/log")"
  fi
  [ -s "$smoke/out.md" ] || fail "$t: пробная выгрузка пустая"
  grep -qF -- "$marker" "$smoke/out.md" || fail "$t: в выгрузке нет текста исходного файла"
  rm -rf -- "$smoke"
  smoke=''
  echo "  $t: --version и пробная выгрузка в порядке"
}

verify_all() {
  local t
  init_needles
  for t in "${targets[@]}"; do
    echo "проверка $t"
    check_buildinfo "$t"
    check_leaks "$t"
    if [ "${t%/*}" = "$hostos" ] && [ "${t#*/}" = "$hostarch" ]; then
      check_host_run "$t"
    fi
  done
}

# ---------- 3. упаковка ----------
pack_all() {
  local t name dir f
  for t in "${targets[@]}"; do
    name=$(pkgname "$t")
    dir="$stage/$name"
    # Права и время задаём явно: umask машины и время сборки не должны попадать в архив.
    chmod 755 "$dir" "$(binpath "$t")"
    chmod 644 "$dir/README.md" "$dir/LICENSE" "$dir/THIRD_PARTY_NOTICES.txt"
    if [ "${t%/*}" = windows ]; then
      # Порядок записей zip зависит от readdir — перечисляем файлы сами, по алфавиту.
      # TZ=UTC: zip хранит локальное время, без этого архив зависел бы от часового пояса машины.
      touch -d "@$epoch" "$dir" "$dir"/*
      (cd "$stage" && TZ=UTC zip -X -q "$out/$name.zip" "$name/" \
        "$name/LICENSE" "$name/README.md" "$name/THIRD_PARTY_NOTICES.txt" "$name/$(binname "$t")") || fail "$t: zip не собрал архив"
      f="$name.zip"
    else
      # --sort=name и числовой владелец 0 — порядок и метаданные не зависят от машины;
      # gzip -n не пишет в заголовок имя и время файла.
      tar --format=gnu --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$epoch" \
        -C "$stage" -cf - "$name" | gzip -n -9 >"$out/$name.tar.gz" || fail "$t: tar не собрал архив"
      f="$name.tar.gz"
    fi
    [ -s "$out/$f" ] || fail "$t: архив $f пуст"
  done
}

echo "выпуск aiexport $label -> dist/$label"
check_tracked
build_all
verify_all
pack_all

# ---------- 4. SHA256SUMS ----------
# Из каталога выпуска и по именам без путей: пользователь проверит `sha256sum -c` у себя.
(cd "$out" && sha256sum -- *.tar.gz *.zip >SHA256SUMS) || fail "не удалось посчитать SHA256SUMS"
(cd "$out" && sha256sum -c --quiet SHA256SUMS) || fail "SHA256SUMS не сошлась с архивами"

# Единственный момент, когда набор появляется в dist/: проверенный и целиком. Прежний выпуск
# той же версии (mv в существующий каталог положил бы набор внутрь него) сначала отодвигается,
# а удаляется только после успешного переноса нового: старый набор не должен исчезнуть раньше,
# чем появится новый. Остаток .old от прерванного прогона не стираем молча — это может быть
# единственная копия выпуска.
old="$final.old"
[ ! -e "$old" ] || fail "от прошлого прогона остался $old — проверьте и удалите его вручную"
if [ -e "$final" ]; then
  mv -- "$final" "$old" || fail "не удалось отодвинуть прежний выпуск $final"
fi
if ! mv -- "$out" "$final"; then
  # Возвращаем прежний набор на место, иначе выпуск этой версии был бы потерян.
  if [ -e "$old" ]; then mv -- "$old" "$final" || true; fi
  fail "не удалось перенести набор в $final (прежний выпуск возвращён на место)"
fi
rm -rf -- "$old"
out=$final

# ---------- 6. итог ----------
# Заголовок выровнен вручную: в локали C printf считает ширину в байтах, а не в буквах.
printf '\nЦЕЛЬ%12s АРХИВ%39s РАЗМЕР\n' '' ''
for t in "${targets[@]}"; do
  name=$(pkgname "$t")
  if [ "${t%/*}" = windows ]; then f="$name.zip"; else f="$name.tar.gz"; fi
  size=$(wc -c <"$out/$f")
  printf '%-16s %-44s %s\n' "$t" "$f" "$(awk -v b="$size" 'BEGIN { printf "%.1f МБ", b / 1048576 }')"
done
echo
echo "суммы: $out/SHA256SUMS"
