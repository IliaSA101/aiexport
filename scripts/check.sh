#!/usr/bin/env bash
# Все проверки одной командой — версия для Linux и macOS, двойник scripts/check.ps1:
#   scripts/check.sh [--race] [--vuln] [--cross]
#
# Без флагов — быстрый прогон (секунды): формат, vet, линтеры, тесты, web, размер файлов.
# --race  — go test -race вместо обычного (нужен gcc/cgo). В конце крупного цикла.
# --vuln  — govulncheck. Перед публикацией.
# --cross — сборка под windows, linux и darwin с CGO_ENABLED=0. Перед релизом.
#
# Правило проекта: ноль предупреждений. Шаг падает и на warning, не только на ошибке.
# Шаги и пороги держать одинаковыми с check.ps1: два скрипта, разошедшиеся молча, хуже одного.
set -u

race=0 vuln=0 cross=0
for a in "$@"; do
  case "$a" in
    --race | -race | -Race) race=1 ;;
    --vuln | -vuln | -Vuln) vuln=1 ;;
    --cross | -cross | -Cross) cross=1 ;;
    *) echo "неизвестный флаг: $a (есть --race, --vuln, --cross)" >&2; exit 2 ;;
  esac
done

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root" || exit 1

if [ -t 1 ]; then red=$'\e[31m' green=$'\e[32m' yellow=$'\e[33m' off=$'\e[0m'; else red='' green='' yellow='' off=''; fi

if ! command -v go >/dev/null 2>&1; then
  echo "${red}FAIL go не найден в PATH${off}"
  exit 1
fi
# GOTMPDIR на Linux не нужен (антивируса нет), но унаследованный путь Windows ломает любую команду go
# невнятной ошибкой посреди прогона. Лучше сказать сразу и прямо.
if [ -n "${GOTMPDIR:-}" ] && [ ! -d "$GOTMPDIR" ]; then
  echo "${red}FAIL GOTMPDIR указывает на несуществующий каталог: $GOTMPDIR (убрать: unset GOTMPDIR)${off}"
  exit 1
fi
gobin="$(go env GOPATH)/bin"
tmp="$root/.gotmp"
mkdir -p "$tmp"

failed=()
total_start=$(date +%s.%N)

# Запускает шаг, прячет вывод, печатает одну строку; при провале — хвост вывода.
# --empty-out: шаг провален, если вывод непустой (gofmt -l и наши собственные проверки).
step() {
  local name=$1 empty=0
  shift
  if [ "$1" = --empty-out ]; then empty=1; shift; fi
  local start out code ok=1
  start=$(date +%s.%N)
  out=$("$@" 2>&1)
  code=$?
  [ "$code" -ne 0 ] && ok=0
  [ "$empty" -eq 1 ] && [ -n "$(printf '%s' "$out" | tr -d '[:space:]')" ] && ok=0
  local time
  time=$(awk -v a="$start" -v b="$(date +%s.%N)" 'BEGIN { printf "%.1fs", b - a }')
  if [ "$ok" -eq 1 ]; then
    echo "${green}OK   $name ($time)${off}"
  else
    echo "${red}FAIL $name ($time)${off}"
    printf '%s\n' "$out" | tail -n 30 | sed 's/^/     /'
    failed+=("$name")
  fi
}

# gofmt списком файлов, а не по каталогу: `gofmt -l .` заходит рекурсивно и в .gotmp, где лежат
# прототипы агентов и обрывки кода без package. Шаг падал на чужом черновике.
gofmt_files() {
  find . -name '*.go' -type f -not -path './.gotmp/*' -not -path './.git/*' -print0 | xargs -0 -r gofmt -l
}

# Размер файлов: больше 600 строк или 30 КБ — резать по смыслу, а не механически.
# Список берём из git, поэтому .gitignore учитывается сам (.gotmp, bin, dist не попадают).
# .html в проверке нет сознательно: без шаблонизатора одну страницу не декомпозируешь.
check_sizes() {
  git ls-files --cached --others --exclude-standard |
    grep -E '\.(go|js|css|ps1|sh)$' |
    grep -vE '(^|/)(vendor|testdata|docs/private)/|_gen\.go$|\.min\.js$' |
    while IFS= read -r f; do
      [ -f "$f" ] || continue
      local lines bytes
      lines=$(awk 'END { print NR }' "$f")
      bytes=$(wc -c <"$f")
      if [ "$lines" -gt 600 ] || [ "$bytes" -gt 30720 ]; then
        awk -v f="$f" -v l="$lines" -v b="$bytes" 'BEGIN { printf "%s (%d строк, %.1f КБ)\n", f, l, b / 1024 }'
      fi
    done
  return 0
}

# Синтаксис ES-модулей веб-UI. Именно через stdin и --input-type=module:
# `node --check файл.js` на модуле молча возвращает 0 даже для битого файла (проверено).
check_web_syntax() {
  find web -name '*.js' -type f | while IFS= read -r f; do
    local err
    if ! err=$(node --check --input-type=module <"$f" 2>&1); then
      echo "$f: $(printf '%s\n' "$err" | grep -v '^\s*$' | head -n 4 | paste -sd '|')"
    fi
  done
  return 0
}

# Совместимость с CSP default-src 'self' и требованием «ноль внешних зависимостей».
# Строгий CSP запрещает inline-скрипты и обработчики в атрибутах; innerHTML — XSS-сток;
# CDN противоречит самому смыслу одного самодостаточного бинарника.
check_web_csp() {
  local mask pattern what
  while IFS=$'\t' read -r mask pattern what; do
    find web -name "$mask" -type f -print0 | xargs -0 -r grep -HnP -- "$pattern" | sed "s|:\([0-9]*\):.*|:\1 — $what|"
  done <<'EOF'
*.html	<script(?![^>]*\bsrc=)[^>]*>\s*\S	inline <script> — вынести в файл и подключить src=
*.html	\son[a-z]+\s*=	обработчик в атрибуте — нужен addEventListener
*.html	(src|href)\s*=\s*["']https?://	внешний ресурс — CDN запрещён
*.js	\b(innerHTML|outerHTML|insertAdjacentHTML)\b	запись HTML строкой — нужен textContent или createElement
EOF
  return 0
}

step 'gofmt' --empty-out gofmt_files
step 'go vet' go vet ./...

# golangci-lint ставится через go install — лежит в GOPATH/bin, которого может не быть в PATH
gcl=$(command -v golangci-lint || echo "$gobin/golangci-lint")
if [ -x "$gcl" ]; then
  step 'golangci-lint' "$gcl" run ./...
else
  echo "${red}FAIL golangci-lint не установлен (go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest)${off}"
  failed+=('golangci-lint')
fi

if [ "$race" -eq 1 ]; then
  # CGO_ENABLED=1 только на этот шаг: правило «без cgo» касается релизного бинарника.
  step 'go test -race' env CGO_ENABLED=1 go test -race ./...
else
  step 'go test' go test ./...
fi

if [ "$vuln" -eq 1 ]; then
  gvc=$(command -v govulncheck || echo "$gobin/govulncheck")
  if [ -x "$gvc" ]; then
    step 'govulncheck' "$gvc" ./...
  else
    echo "${red}FAIL govulncheck не установлен (go install golang.org/x/vuln/cmd/govulncheck@latest)${off}"
    failed+=('govulncheck')
  fi
fi

if [ "$cross" -eq 1 ]; then
  # Один бинарник под три ОС — требование ТЗ. Ломается молча, первой же зависимостью с cgo,
  # поэтому проверяем сборкой. CGO_ENABLED=0 явно: случайный импорт с cgo падает здесь.
  for t in windows/amd64 linux/amd64 darwin/arm64; do
    os=${t%/*} arch=${t#*/} ext=''
    [ "$os" = windows ] && ext=.exe
    step "кросс-сборка $t" env GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -o "$tmp/aiexport-$os-$arch$ext" ./cmd/aiexport
  done
fi

if [ -d web ]; then
  if command -v node >/dev/null 2>&1; then
    step 'синтаксис web' --empty-out check_web_syntax
  else
    echo "${yellow}SKIP синтаксис web (node не найден)${off}"
  fi
  step 'CSP web' --empty-out check_web_csp
fi

step 'размер файлов' --empty-out check_sizes

time=$(awk -v a="$total_start" -v b="$(date +%s.%N)" 'BEGIN { printf "%.1fs", b - a }')
if [ "${#failed[@]}" -gt 0 ]; then
  echo "${red}ИТОГО: упало - $(IFS=,; echo "${failed[*]}" | sed 's/,/, /g') ($time)${off}"
  exit 1
fi
echo "${green}ИТОГО: всё чисто ($time)${off}"
exit 0
