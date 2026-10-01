package checks

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"agent/internal/metrics"
)

// Подстановка fmt: %d, %s, но не %% — это знак процента, а не аргумент.
var verb = regexp.MustCompile(`%[^%]`)

// Каждая фраза есть на обоих языках, и аргументов у пары поровну: строки
// берут одни и те же значения в одном порядке, и лишний или потерянный %d
// дал бы в письме «%!d(MISSING)».
func TestWordsAgreeBetweenLanguages(t *testing.T) {
	ru := reflect.ValueOf(WordsFor("ru"))
	en := reflect.ValueOf(WordsFor("en"))

	for i := range ru.NumField() {
		name := ru.Type().Field(i).Name
		r, e := ru.Field(i).String(), en.Field(i).String()
		if r == "" || e == "" {
			t.Errorf("%s: пустая фраза (ru %q, en %q)", name, r, e)
			continue
		}
		if a, b := len(verb.FindAllString(strings.ReplaceAll(r, "%%", ""), -1)),
			len(verb.FindAllString(strings.ReplaceAll(e, "%%", ""), -1)); a != b {
			t.Errorf("%s: подстановок ru %d, en %d", name, a, b)
		}
		for _, letter := range e {
			if unicode.Is(unicode.Cyrillic, letter) {
				t.Errorf("%s: кириллица в английской фразе %q", name, e)
				break
			}
		}
	}
}

// Язык по умолчанию — русский: сервер, на котором AGENT_LOCALE не задан,
// пишет так же, как писал до перевода.
func TestWordsDefaultToRussian(t *testing.T) {
	if WordsFor("") != WordsFor("ru") || WordsFor("de") != WordsFor("ru") {
		t.Error("незнакомый язык должен давать русский")
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
