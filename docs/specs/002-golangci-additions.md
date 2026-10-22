# Черновик: линтеры архитектуры и офлайна

Не спека цикла, а заготовка к пункту бэклога «Сверить с обновлённой общей базой правил».
Ставить **после** ревью цикла 1, отдельным шагом, вместе с исправлением найденного:
посреди работы свежий код сразу краснеет, и цикл встаёт на правках ради линтера.

Главное здесь — `forbidigo`: он превращает обещание «самодостаточный бинарник, который
никуда не ходит» из строчки в README в проверку, падающую на любом сетевом вызове.
И `depguard` для `internal/model`: обещание «пакет голых типов без зависимостей»
сегодня держится только на дисциплине.

Источник идей — общие правила качества Go-проектов, ревизия 20.10.2026.

```yaml
linters:
  enable:
    - funlen    # 80 строк / 50 операторов — от разрастания функций
    - gocognit  # когнитивная сложность 30
    - depguard  # слои: кто кого вправе импортировать
    - forbidigo # запрет вызовов по смыслу

  settings:
    funlen:
      lines: 80
      statements: 50

    gocognit:
      min-complexity: 30

    depguard:
      rules:
        # model — обещание «пакет голых типов без зависимостей». Сегодня это
        # поддерживается только дисциплиной; правило делает его проверяемым.
        model:
          list-mode: strict
          files:
            - '**/internal/model/*.go'
          allow:
            - $gostd
        # ignore и tokens — чистые движки, им не нужен никто, кроме своих библиотек.
        engines:
          list-mode: strict
          files:
            - '**/internal/ignore/*.go'
            - '**/internal/tokens/*.go'
          allow:
            - $gostd
            - github.com/IliaSA101/aiexport/internal/model
            - github.com/bmatcuk/doublestar/v4
            - github.com/pkoukk/tiktoken-go
            - github.com/pkoukk/tiktoken-go-loader

    forbidigo:
      analyze-types: true
      forbid:
        # Главное обещание продукта: самодостаточный бинарник, который никуда не ходит.
        # Локальный сервер это не задевает: он принимает соединения, а не открывает их.
        - pattern: ^http\.(Get|Head|Post|PostForm|NewRequest|NewRequestWithContext|DefaultClient|DefaultTransport|Client|Transport)$
          msg: >-
            aiexport не ходит в сеть. Если это понадобилось, сначала развилка владельцу:
            офлайн-работа — требование ТЗ, а не умолчание.

issues:
  # funlen и gocognit пишут на строку объявления функции и иначе прячут друг друга.
  uniq-by-line: false
```
