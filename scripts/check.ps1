# Все проверки одной командой:
#   powershell -ExecutionPolicy Bypass -File scripts/check.ps1 [-Race] [-Vuln] [-Cross]
#
# Без флагов — быстрый прогон (секунды): формат, vet, линтеры, тесты, web, размер файлов.
# -Race  — go test -race вместо обычного (нужен gcc/cgo). В конце крупного цикла.
# -Vuln  — govulncheck. Перед публикацией.
# -Cross — сборка под linux и darwin с CGO_ENABLED=0. Перед релизом.
#
# Правило проекта: ноль предупреждений. Шаг падает и на warning, не только на ошибке.
# Файл сохранять в UTF-8 С BOM: Windows PowerShell 5.1 без BOM ломает кириллицу.
param(
    [switch]$Race,
    [switch]$Vuln,
    [switch]$Cross
)

$ErrorActionPreference = 'Continue'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

# Go мог быть поставлен после того, как открылось это окно: PATH подхватывают
# только новые процессы. Поэтому ищем сами, а не надеемся на PATH.
$goCmd = Get-Command go -ErrorAction SilentlyContinue
$goExe = if ($goCmd) { $goCmd.Source } else { Join-Path $env:ProgramFiles 'Go\bin\go.exe' }
if (-not (Test-Path $goExe)) {
    Write-Host 'FAIL go не найден ни в PATH, ни в Program Files\Go\bin' -ForegroundColor Red
    exit 1
}
# В ту же папку лежит gofmt — добавляем её в PATH, чтобы шаги звались просто по имени
$env:PATH = (Split-Path $goExe) + [IO.Path]::PathSeparator + $env:PATH

# Антивирус ловит тестовые exe во %TEMP% — собираем их внутри проекта (.gotmp в .gitignore)
$env:GOTMPDIR = Join-Path $root '.gotmp'
if (-not (Test-Path $env:GOTMPDIR)) { New-Item -ItemType Directory -Path $env:GOTMPDIR | Out-Null }

$web = Join-Path $root 'web'
$failed = New-Object System.Collections.Generic.List[string]
$total = [Diagnostics.Stopwatch]::StartNew()

# Запускает шаг, прячет вывод, печатает одну строку; при провале — хвост вывода.
# -EmptyOut: шаг провален, если вывод непустой (gofmt -l и наши собственные проверки).
# -NoWarn:   шаг провален, если в выводе есть предупреждение (правило «ноль warnings»).
function Step([string]$Name, [scriptblock]$Cmd, [string]$Dir = $root, [switch]$EmptyOut, [switch]$NoWarn) {
    $sw = [Diagnostics.Stopwatch]::StartNew()
    Push-Location $Dir
    $global:LASTEXITCODE = 0
    # "$_" превращает ErrorRecord из stderr (в PowerShell 5.1) в обычную строку
    $out = @(& $Cmd 2>&1 | ForEach-Object { "$_" })
    $code = $LASTEXITCODE
    Pop-Location
    $ok = ($code -eq 0)
    if ($EmptyOut -and ($out | Where-Object { $_.Trim() })) { $ok = $false }
    if ($NoWarn -and ($out | Where-Object { $_ -match '(?i)\bwarn(ing)?\b|deprecat|\(!\)' })) { $ok = $false }
    $time = '{0:N1}s' -f $sw.Elapsed.TotalSeconds
    if ($ok) {
        Write-Host "OK   $Name ($time)" -ForegroundColor Green
    } else {
        Write-Host "FAIL $Name ($time)" -ForegroundColor Red
        $out | Select-Object -Last 30 | ForEach-Object { Write-Host "     $_" }
        $failed.Add($Name)
    }
}

# Размер файлов: больше 600 строк или 30 КБ — резать по смыслу, а не механически.
# Список берём из git, поэтому .gitignore учитывается сам (.gotmp, bin, dist не попадают).
# .html в проверке нет сознательно: без шаблонизатора одну страницу не декомпозируешь,
# и шаг превратился бы в постоянно подавляемое предупреждение.
function CheckSizes {
    $skip = '(^|/)(vendor|testdata|docs/private)/|_gen\.go$|\.min\.js$'
    git ls-files --cached --others --exclude-standard |
        Where-Object { $_ -match '\.(go|js|css|ps1)$' -and $_ -notmatch $skip -and (Test-Path -LiteralPath $_) } |
        ForEach-Object {
            $f = Get-Item -LiteralPath $_
            $lines = @(Get-Content -LiteralPath $f.FullName).Count
            if ($lines -gt 600 -or $f.Length -gt 30KB) { "$_ ($lines строк, $([math]::Round($f.Length / 1KB, 1)) КБ)" }
        }
    $global:LASTEXITCODE = 0
}

# Синтаксис ES-модулей веб-UI. Именно через stdin и --input-type=module:
# `node --check файл.js` на модуле молча возвращает 0 даже для битого файла (проверено).
function CheckWebSyntax {
    Get-ChildItem -LiteralPath $web -Recurse -Filter *.js -File | ForEach-Object {
        $src = Get-Content -Raw -LiteralPath $_.FullName
        $err = $src | node --check --input-type=module 2>&1
        # Node печатает позицию отдельной строкой от текста ошибки — берём несколько.
        # "$_" обязателен: в PowerShell 5.1 stderr приходит объектами ErrorRecord, у них нет Trim().
        if ($LASTEXITCODE -ne 0) {
            $msg = @($err) | ForEach-Object { "$_" } |
                Where-Object { $_.Trim() -and $_ -notmatch '^System\.Management\.Automation' } |
                Select-Object -First 4
            "$($_.FullName): $($msg -join ' | ')"
        }
    }
    $global:LASTEXITCODE = 0
}

# Совместимость с CSP default-src 'self' и требованием «ноль внешних зависимостей».
# Строгий CSP запрещает inline-скрипты и обработчики в атрибутах; innerHTML — XSS-сток;
# CDN противоречит самому смыслу одного самодостаточного бинарника.
function CheckWebCsp {
    $rules = @(
        @{ Mask = '*.html'; Pattern = '<script(?![^>]*\bsrc=)[^>]*>\s*\S'; What = 'inline <script> — вынести в файл и подключить src=' }
        @{ Mask = '*.html'; Pattern = '\son[a-z]+\s*='; What = 'обработчик в атрибуте — нужен addEventListener' }
        @{ Mask = '*.html'; Pattern = '(src|href)\s*=\s*["'']https?://'; What = 'внешний ресурс — CDN запрещён' }
        @{ Mask = '*.js'; Pattern = '\b(innerHTML|outerHTML|insertAdjacentHTML)\b'; What = 'запись HTML строкой — нужен textContent или createElement' }
    )
    foreach ($r in $rules) {
        Get-ChildItem -LiteralPath $web -Recurse -Filter $r.Mask -File | ForEach-Object {
            Select-String -LiteralPath $_.FullName -Pattern $r.Pattern |
                ForEach-Object { "$($_.Path):$($_.LineNumber) — $($r.What)" }
        }
    }
    $global:LASTEXITCODE = 0
}

# gofmt списком файлов, а не по каталогу: `gofmt -l .` и `gofmt -l <каталог>` заходят
# внутрь рекурсивно, в том числе в .gotmp, где лежат прототипы агентов и обрывки кода
# без package. Шаг падал на чужом черновике, к которому проект отношения не имеет.
Step 'gofmt' {
    $files = Get-ChildItem -Path $root -Recurse -Filter *.go -File |
        Where-Object { $_.FullName -notmatch '[\\/]\.gotmp[\\/]' } |
        ForEach-Object { $_.FullName }
    if ($files) { gofmt -l @files }
} -EmptyOut
Step 'go vet' { & $goExe vet ./... }

# golangci-lint ставится через go install — лежит в GOPATH\bin, которого может не быть в PATH
$gclCmd = Get-Command golangci-lint -ErrorAction SilentlyContinue
$gclExe = if ($gclCmd) { $gclCmd.Source } else { Join-Path (& $goExe env GOPATH) 'bin\golangci-lint.exe' }
if (Test-Path $gclExe) {
    Step 'golangci-lint' { & $gclExe run ./... }
} else {
    Write-Host 'FAIL golangci-lint не установлен (go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest)' -ForegroundColor Red
    $failed.Add('golangci-lint')
}

if ($Race) {
    # gcc от winget (WinLibs) может не попасть в PATH старого окна — ищем сами.
    # CGO_ENABLED=1 только на этот шаг: правило «без cgo» касается релизного бинарника.
    if (-not (Get-Command gcc -ErrorAction SilentlyContinue)) {
        $gcc = Get-ChildItem "$env:LOCALAPPDATA\Microsoft\WinGet\Packages\BrechtSanders.WinLibs*\mingw64\bin" -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($gcc) { $env:PATH = "$($gcc.FullName);$env:PATH" }
    }
    $env:CGO_ENABLED = '1'
    Step 'go test -race' { & $goExe test -race ./... }
    $env:CGO_ENABLED = $null
} else {
    Step 'go test' { & $goExe test ./... }
}

if ($Vuln) {
    $gvcCmd = Get-Command govulncheck -ErrorAction SilentlyContinue
    $gvcExe = if ($gvcCmd) { $gvcCmd.Source } else { Join-Path (& $goExe env GOPATH) 'bin\govulncheck.exe' }
    if (Test-Path $gvcExe) {
        Step 'govulncheck' { & $gvcExe ./... }
    } else {
        Write-Host 'FAIL govulncheck не установлен (go install golang.org/x/vuln/cmd/govulncheck@latest)' -ForegroundColor Red
        $failed.Add('govulncheck')
    }
}

if ($Cross) {
    # Один бинарник под три ОС — требование ТЗ. Ломается молча, первой же зависимостью
    # с cgo, поэтому проверяем сборкой. CGO_ENABLED=0 выставлен явно: так случайный
    # импорт с cgo падает здесь, а не при релизной кросс-компиляции.
    foreach ($t in 'linux/amd64', 'darwin/arm64') {
        $tOs, $tArch = $t -split '/'
        Step "кросс-сборка $tOs/$tArch" {
            $env:GOOS = $tOs; $env:GOARCH = $tArch; $env:CGO_ENABLED = '0'
            & $goExe build -o (Join-Path $env:GOTMPDIR "aiexport-$tOs-$tArch") ./cmd/aiexport
        }
    }
    $env:GOOS = $null; $env:GOARCH = $null; $env:CGO_ENABLED = $null
}

if (Test-Path $web) {
    if (Get-Command node -ErrorAction SilentlyContinue) {
        Step 'синтаксис web' { CheckWebSyntax } -EmptyOut
    } else {
        Write-Host 'SKIP синтаксис web (node не найден; на Linux это нормально)' -ForegroundColor Yellow
    }
    Step 'CSP web' { CheckWebCsp } -EmptyOut
}

Step 'размер файлов' { CheckSizes } -EmptyOut

$time = '{0:N1}s' -f $total.Elapsed.TotalSeconds
if ($failed.Count -gt 0) {
    Write-Host "ИТОГО: упало - $($failed -join ', ') ($time)" -ForegroundColor Red
    exit 1
}
Write-Host "ИТОГО: всё чисто ($time)" -ForegroundColor Green
exit 0
