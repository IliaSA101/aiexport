module github.com/IliaSA101/aiexport

go 1.26.7

// Выпуск и проверки собираются ровно этой версией: release.sh и check.sh выставляют GOTOOLCHAIN
// из директивы, иначе более новый Go на машине собирает сам и суммы выпуска не воспроизвести.
// Версия — первая без известных уязвимостей в вызываемой стандартной библиотеке (decisions §9, §14).
toolchain go1.27.3

require (
	github.com/bmatcuk/doublestar/v4 v4.10.3
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/pkoukk/tiktoken-go v0.1.8
	github.com/pkoukk/tiktoken-go-loader v0.0.2
)
