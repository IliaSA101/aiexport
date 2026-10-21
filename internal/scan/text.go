package scan

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// sniffSize — сколько первых байт смотрим при определении бинарности. Хватает,
// чтобы поймать заголовок любого исполняемого формата, и не требует читать файл
// целиком ради решения «пропустить».
const sniffSize = 512

// Ошибки чтения текста. Это признаки для errors.Is, а не изменяемое состояние.
var (
	ErrBinary  = errors.New("файл бинарный")
	ErrTooBig  = errors.New("файл больше лимита")
	ErrNotUTF8 = errors.New("файл не в UTF-8, перекодировка не выполняется")
)

// NotUTF8RuleText — пояснение в отчёте о пропущенном. Кодировку не угадываем и
// не перекодируем: это отдельное решение владельца, а замена байт на U+FFFD
// отдала бы модели искажённый текст под видом исходника.
const NotUTF8RuleText = "файл не в UTF-8, перекодировка не выполняется"

// verdict — вердикт sniff по префиксу файла.
type verdict int

const (
	verdictText verdict = iota
	verdictBinary
	verdictNotUTF8
)

// utf16Order определяет порядок байт по BOM: 1 — LE, 2 — BE, 0 — BOM нет.
func utf16Order(b []byte) int {
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return 1
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return 2
	}
	return 0
}

// decodeUTF16 декодирует данные после BOM. ok=false — нечётная длина (обрыв) или
// непарный суррогат: utf16.Decode молча подставил бы там U+FFFD, а нечётный хвост
// пропал бы. Для целого файла это порча, и ReadText отдаёт ErrNotUTF8; для
// префикса из classify обрыв на границе окна нормален, и ok там игнорируется.
func decodeUTF16(b []byte, order int) (text string, ok bool) {
	ok = len(b)%2 == 0
	b = b[:len(b)&^1]
	units := make([]uint16, len(b)/2)
	for i := range units {
		if order == 1 {
			units[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
		} else {
			units[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		}
	}
	for i := 0; i < len(units); i++ {
		u := rune(units[i])
		switch {
		case !utf16.IsSurrogate(u):
		case i+1 < len(units) && utf16.DecodeRune(u, rune(units[i+1])) != utf8.RuneError:
			i++ // законная пара
		default:
			ok = false
		}
	}
	return string(utf16.Decode(units)), ok
}

// classify решает по префиксу файла. UTF-16 без своей ветки был бы целиком
// признан бинарным: в нём каждый второй байт — NUL, хотя это нормальный текст
// (так сохраняет, например, PowerShell). Поэтому при BOM сначала декодируем и
// только потом ищем NUL. truncated — префикс короче файла: обрыв посреди
// многобайтного символа тогда не считается порчей.
func classify(prefix []byte, truncated bool) verdict {
	if order := utf16Order(prefix); order != 0 {
		// ok игнорируем: префикс может обрываться посреди символа или пары суррогатов.
		if text, _ := decodeUTF16(prefix[2:], order); strings.ContainsRune(text, 0) {
			return verdictBinary
		}
		return verdictText
	}
	if bytes.IndexByte(prefix, 0) >= 0 {
		return verdictBinary
	}
	if truncated {
		prefix = trimPartialRune(prefix)
	}
	if !utf8.Valid(prefix) {
		return verdictNotUTF8
	}
	return verdictText
}

// trimPartialRune отбрасывает незаконченный символ в конце среза (до 3 байт).
func trimPartialRune(b []byte) []byte {
	for i := 1; i <= utf8.UTFMax-1 && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			break
		}
	}
	return b
}

// sniff читает префикс файла и возвращает вердикт.
func sniff(root *os.Root, rel string) (verdict, error) {
	f, err := root.Open(rel)
	if err != nil {
		return verdictText, fmt.Errorf("открытие %s: %w", rel, err)
	}
	defer func() { _ = f.Close() }() // чтение: ошибка закрытия ничего не теряет
	buf := make([]byte, sniffSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return verdictText, fmt.Errorf("чтение %s: %w", rel, err)
	}
	return classify(buf[:n], n == sniffSize), nil
}

// ReadText читает файл целиком и приводит к чистому UTF-8: UTF-16 по BOM
// декодируется, BOM UTF-8 снимается, CRLF становится LF. Невалидный UTF-8
// (cp1251 и прочие однобайтные кодировки) даёт ErrNotUTF8: замена на U+FFFD
// схлопывала бы серию байт в один символ, и файл числился бы включённым с
// потерянным содержимым. Лимит проверяется до и во время чтения:
// файл мог вырасти после обхода. Второй результат — размер файла на диске,
// по нему вызывающий замечает расхождение с планом.
func ReadText(root *os.Root, rel string, maxSize int64) (string, int64, error) {
	f, err := root.Open(rel)
	if err != nil {
		return "", 0, fmt.Errorf("открытие %s: %w", rel, err)
	}
	defer func() { _ = f.Close() }() // чтение: ошибка закрытия ничего не теряет
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return "", 0, fmt.Errorf("чтение %s: %w", rel, err)
	}
	size := int64(len(data))
	if size > maxSize {
		return "", size, fmt.Errorf("%s: %w (%d > %d байт)", rel, ErrTooBig, size, maxSize)
	}

	var text string
	if order := utf16Order(data); order != 0 {
		var ok bool
		if text, ok = decodeUTF16(data[2:], order); !ok {
			return "", size, fmt.Errorf("%s: %w", rel, ErrNotUTF8)
		}
	} else {
		// NUL важнее кодировки: бинарь с мусорными байтами — бинарь, а не «не UTF-8».
		if bytes.IndexByte(data, 0) >= 0 {
			return "", size, fmt.Errorf("%s: %w", rel, ErrBinary)
		}
		if !utf8.Valid(data) {
			return "", size, fmt.Errorf("%s: %w", rel, ErrNotUTF8)
		}
		text = strings.TrimPrefix(string(data), string(rune(0xFEFF)))
	}
	// NUL проверяется по всему содержимому, а не только по префиксу: так решение
	// «бинарь» не зависит от того, где в файле он встретился.
	if strings.ContainsRune(text, 0) {
		return "", size, fmt.Errorf("%s: %w", rel, ErrBinary)
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return text, size, nil
}
