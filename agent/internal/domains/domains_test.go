package domains

import "testing"

// Регистрируют зону второго уровня, а сайт живёт на поддомене. Спрашивать
// про shop.example.com бессмысленно: реестр о нём не знает — и в один
// проект такие домены складывать надо по той же причине, по которой они
// принадлежат одному владельцу.
func TestRegistrable(t *testing.T) {
	cases := map[string]string{
		"example.com":        "example.com",
		"shop.example.com":   "example.com",
		"a.b.c.example.ru":   "example.ru",
		"example.co.uk":      "example.co.uk",
		"shop.example.co.uk": "example.co.uk",
		"example.com.ua":     "example.com.ua",
		"localhost":          "localhost",
		"WWW.Example.COM.":   "example.com",
	}

	for input, want := range cases {
		if got := Registrable(input); got != want {
			t.Errorf("%s -> %s, ожидали %s", input, got, want)
		}
	}
}
