package checks

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"agent/internal/locale"
	"agent/internal/metrics"
)

// Подстановка fmt: %d, %s, но не %% — это знак процента, а не аргумент.
var verb = regexp.MustCompile(`%[^%]`)

// Словарь есть у каждого языка перечня `locale.Known` и только у них
// (трек AE): язык, дописанный в перечень и забытый здесь, ушёл бы человеку
// пустыми письмами.
func TestEveryKnownLanguageHasWords(t *testing.T) {
	for _, code := range locale.Known {
		if _, ok := words[code]; !ok {
			t.Errorf("языку %s нет словаря тревог", code)
		}
	}
	if len(words) != len(locale.Known) {
		t.Errorf("словарей %d, а языков в locale.Known %d", len(words), len(locale.Known))
	}
}

// Каждая фраза есть на каждом языке, и аргументов у неё столько же, сколько
// в английской: строки берут одни и те же значения в одном порядке, и лишний
// или потерянный %d дал бы в письме «%!d(MISSING)».
func TestWordsAgreeBetweenLanguages(t *testing.T) {
	en := reflect.ValueOf(words[locale.English])
	for code, dictionary := range words {
		other := reflect.ValueOf(dictionary)
		for i := range en.NumField() {
			name := en.Type().Field(i).Name
			o, e := other.Field(i).String(), en.Field(i).String()
			if o == "" || e == "" {
				t.Errorf("%s %s: пустая фраза (%q, en %q)", code, name, o, e)
				continue
			}
			if a, b := len(verb.FindAllString(strings.ReplaceAll(o, "%%", ""), -1)),
				len(verb.FindAllString(strings.ReplaceAll(e, "%%", ""), -1)); a != b {
				t.Errorf("%s %s: подстановок %d, в английском %d", code, name, a, b)
			}
			if code == locale.Russian {
				continue
			}
			for _, letter := range o {
				if unicode.Is(unicode.Cyrillic, letter) {
					t.Errorf("%s %s: кириллица во фразе %q", code, name, o)
					break
				}
			}
		}
	}
}

// Язык по умолчанию — русский: сервер, на котором AGENT_LOCALE не задан,
// пишет так же, как писал до перевода. Незнакомый код — английский (трек AE):
// язык выбран в приложении новее агента, и английский понятнее русского.
func TestWordsDefaultToRussian(t *testing.T) {
	if WordsFor("") != WordsFor("ru") {
		t.Error("пустой язык должен давать русский")
	}
	if WordsFor("ja") != WordsFor("en") {
		t.Error("незнакомый язык должен давать английский")
	}
}

// Тревога идёт на выбранном языке целиком — тема и текст.
func TestAlertsSpeakTheChosenLanguage(t *testing.T) {
	full := metrics.Sample{DiskTotalMB: 1000, DiskFreeMB: 50}

	firing, _ := Evaluate(thresholds(), WordsFor("en"), full, nil, nil)
	if len(firing) != 1 {
		t.Fatalf("ожидалась одна тревога, пришло %d", len(firing))
	}
	if firing[0].Subject != "Server disk is 95% full" {
		t.Errorf("тема: %q", firing[0].Subject)
	}
	if !strings.Contains(firing[0].Body, "50 MB free out of 1000 MB") {
		t.Errorf("текст: %q", firing[0].Body)
	}

	firing, _ = Evaluate(thresholds(), WordsFor("ru"), full, nil, nil)
	if firing[0].Subject != "Диск сервера занят на 95%" {
		t.Errorf("русская тема изменилась: %q", firing[0].Subject)
	}
}
