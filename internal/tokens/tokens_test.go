package tokens

import (
	"strings"
	"sync"
	"testing"
)

func TestZone(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, ZoneGreen}, {GreenMax, ZoneGreen}, {GreenMax + 1, ZoneYellow},
		{YellowMax, ZoneYellow}, {YellowMax + 1, ZoneRed},
	}
	for _, c := range cases {
		if got := Zone(c.n); got != c.want {
			t.Errorf("Zone(%d) = %s, ожидали %s", c.n, got, c.want)
		}
	}
}

// Счёт на фиксированной строке стабилен и не требует сети: словарь вшит.
// Если словарь не загрузится офлайн, NewCounter вернёт ошибку (или повиснет на
// сетевом вызове), и тест это покажет.
func TestCountStable(t *testing.T) {
	c, err := NewCounter()
	if err != nil {
		t.Fatalf("NewCounter: %v", err)
	}
	if got := c.Count("hello world"); got != 2 {
		t.Errorf("Count(\"hello world\") = %d, ожидали 2", got)
	}
	if got := c.Count(""); got != 0 {
		t.Errorf("пустая строка = %d токенов", got)
	}
	const s = "func main() {\n\tfmt.Println(\"привет, мир\")\n}\n"
	first := c.Count(s)
	if first == 0 || c.Count(s) != first {
		t.Errorf("счёт нестабилен или нулевой: %d", first)
	}
}

// Спецтокен в тексте не должен ронять процесс.
func TestCountSpecialTokenText(t *testing.T) {
	c, err := NewCounter()
	if err != nil {
		t.Fatal(err)
	}
	if c.Count("text <|endoftext|> text") < 3 {
		t.Error("спецтокен должен считаться обычным текстом")
	}
}

func TestCountConcurrent(t *testing.T) {
	c, err := NewCounter()
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Repeat("package main // привет\n", 50)
	want := c.Count(s)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if got := c.Count(s); got != want {
				t.Errorf("параллельный счёт %d != %d", got, want)
			}
		})
	}
	wg.Wait()
}
