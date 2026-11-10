// Package locale — языки, на которых агент пишет человеку: тревоги,
// еженедельный отчёт, оформление писем (трек AE, спринт 207).
//
// Перечень один — здесь. Словари тревог (`checks`) и отчёта (`digest`)
// заведены картами по коду языка, и тесты пакетов сверяют их с [Known]:
// язык, дописанный сюда и забытый в словаре, роняет тест, а не уходит
// человеку пустым письмом.
package locale

import "strings"

const (
	English    = "en"
	Russian    = "ru"
	Spanish    = "es"
	German     = "de"
	French     = "fr"
	Portuguese = "pt"
)

// Known — языки, для которых у агента есть словари.
var Known = []string{English, Russian, Spanish, German, French, Portuguese}

// Parse — язык из `AGENT_LOCALE`.
//
// Пустое значение — русский: сервер, на котором язык не задан, пишет так же,
// как писал до перевода тревог (агент старше 0.15.0 знал только русский).
// Незнакомый код — английский: язык выбрали в приложении новее этого агента,
// и английский такому человеку понятнее русского. Код сверяется по основе:
// `pt-BR` — это `pt`.
func Parse(raw string) string {
	code := strings.ToLower(strings.TrimSpace(raw))
	if code == "" {
		return Russian
	}
	if cut := strings.IndexAny(code, "-_"); cut > 0 {
		code = code[:cut]
	}
	for _, known := range Known {
		if known == code {
			return known
		}
	}
	return English
}
