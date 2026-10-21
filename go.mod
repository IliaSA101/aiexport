module github.com/IliaSA101/aiexport

go 1.26.7

// Собираем 1.26.9: govulncheck 20.10.2026 нашёл в стандартной библиотеке 1.26.7 одиннадцать
// уязвимостей, которые наш код реально вызывает (net/http, net/textproto, crypto/tls, os), и все
// они исправлены в 1.26.9. Мы поднимаем HTTP-сервер на машине пользователя, поэтому это блокер
// выпуска. Директива toolchain решает его без установки ПО: go подхватывает нужную версию сам.
toolchain go1.26.9

require (
	github.com/bmatcuk/doublestar/v4 v4.10.3
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/pkoukk/tiktoken-go v0.1.8
	github.com/pkoukk/tiktoken-go-loader v0.0.2
)
