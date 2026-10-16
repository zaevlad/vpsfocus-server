package mailhtml

import (
	"html"
	"regexp"
	"strings"
)

var (
	headPart = regexp.MustCompile(`(?s)<head>.*?</head>`)
	anyTag   = regexp.MustCompile(`(?s)<[^>]*>`)
)

// Missing — слова текстовой части, которых нет в HTML (Р24): пусто, если
// разбор ничего не потерял.
//
// Слова сверяются по порядку: HTML без разметки обязан содержать все слова
// текста в том же порядке (лишние — логотип, полоса, прехедер, кнопка —
// допустимы). Черта подвала «—» в HTML — линия, а не знак, поэтому сама по
// себе не считается. Нужна тестам обеих сторон: ошибка разбора видна
// тестом, а не в ящике клиента.
func Missing(text, page string) []string {
	page = headPart.ReplaceAllString(page, " ")
	page = html.UnescapeString(anyTag.ReplaceAllString(page, " "))
	have := strings.Fields(page)

	var lost []string
	at := 0
	for _, word := range strings.Fields(text) {
		if word == "—" {
			continue
		}
		found := false
		for i := at; i < len(have); i++ {
			if have[i] == word {
				at, found = i+1, true
				break
			}
		}
		if !found {
			lost = append(lost, word)
		}
	}
	return lost
}
