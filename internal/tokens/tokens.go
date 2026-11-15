// Пакет tokens — подсчёт токенов o200k_base полностью офлайн и зоны размера бандла.
package tokens

import (
	"fmt"
	"sync"

	"github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"
)

// Пороги зон — значения по умолчанию, живут в одном месте. Окно модели больше,
// но качество ответа заметно падает задолго до его края, поэтому зоны консервативны.
const (
	GreenMax  = 80_000  // до этого числа токенов — зелёная зона
	YellowMax = 120_000 // до этого — жёлтая, дальше красная
)

// Названия зон — часть API и UI.
const (
	ZoneGreen  = "green"
	ZoneYellow = "yellow"
	ZoneRed    = "red"
)

// Zone возвращает зону для суммарного числа токенов.
func Zone(total int) string {
	switch {
	case total <= GreenMax:
		return ZoneGreen
	case total <= YellowMax:
		return ZoneYellow
	default:
		return ZoneRed
	}
}

// Counter считает токены. Безопасен для параллельного использования: после
// создания не меняется, а regexp2 внутри tiktoken защищает свои буферы мьютексом.
type Counter struct {
	enc *tiktoken.Tiktoken
}

// loaderMu защищает глобальную настройку загрузчика внутри tiktoken-go.
// Это единственное разделяемое состояние, и оно чужое: библиотека хранит загрузчик
// в переменной пакета, поэтому два параллельных NewCounter без замка гонялись бы.
var loaderMu sync.Mutex

// NewCounter загружает словарь o200k_base из бинарника. Загрузчик подменяется до
// GetEncoding: иначе библиотека при первом вызове молча идёт в интернет за словарём,
// и инструмент перестаёт быть самодостаточным.
func NewCounter() (*Counter, error) {
	loaderMu.Lock()
	defer loaderMu.Unlock()
	tiktoken.SetBpeLoader(tiktoken_loader.NewOfflineLoader())
	enc, err := tiktoken.GetEncoding("o200k_base")
	if err != nil {
		return nil, fmt.Errorf("загрузка словаря o200k_base: %w", err)
	}
	return &Counter{enc: enc}, nil
}

// Count возвращает число токенов в строке. Спецтокены вроде «<|endoftext|>» в тексте
// кода считаются как обычный текст: Encode с настройками по умолчанию на них паникует,
// а исходник, в котором такая строка встречается (например, код токенизатора), —
// нормальный вход.
func (c *Counter) Count(s string) int {
	if s == "" {
		return 0
	}
	return len(c.enc.EncodeOrdinary(s))
}
