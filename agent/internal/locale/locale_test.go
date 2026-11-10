package locale

import "testing"

// Пустое — русский (старые серверы), незнакомое — английский, основа кода
// решает за вариант.
func TestParse(t *testing.T) {
	for raw, want := range map[string]string{
		"":       Russian,
		"  ":     Russian,
		"ru":     Russian,
		"RU":     Russian,
		"en":     English,
		"en-GB":  English,
		"ru_RU":  Russian,
		"es":     Spanish,
		"es-MX":  Spanish,
		"de":     German,
		"de-AT":  German,
		"fr":     French,
		"fr-CA":  French,
		"pt":     Portuguese,
		"pt-br":  Portuguese,
		"pt-PT":  Portuguese,
		"klingo": English,
	} {
		if got := Parse(raw); got != want {
			t.Errorf("Parse(%q) = %q, ждали %q", raw, got, want)
		}
	}
}
