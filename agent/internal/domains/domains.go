// Package domains разбирает доменные имена.
//
// Пакет крошечный и существует ради одного: разбор нужен двоим. `checks`
// спрашивает реестр о сроке регистрации — и спрашивать надо про то, что
// регистрировали, а не про поддомен, о котором реестр не знает. `config`
// складывает домены в проекты — и складывать надо по тому же признаку,
// иначе `shop.example.co.uk` уедет в проект `co.uk`, а срок регистрации
// будет спрошен у `example.co.uk`, и понять, кто из двоих врёт, станет
// нечем.
//
// Отдельным пакетом, а не функцией в `checks`, потому что `checks` уже
// импортирует `config`: положи разбор туда — и получится цикл.
package domains

import "strings"

// Составные зоны второго уровня: в них регистрируют на третьем.
//
// Список короткий и заведомо неполный — полный живёт в Public Suffix List и
// весит мегабайт. Здесь только то, что реально встречается у агентств; при
// промахе спросим на уровень выше и получим внятную ошибку, а не молчание.
//
// У приложения есть своя копия этого списка (`projects.rs`): языки разные,
// и переиспользовать один список нечем. Расхождение ловит тест
// `compound_suffixes_match_the_agent`, который читает список прямо отсюда.
var compoundSuffixes = map[string]bool{
	"co.uk": true, "org.uk": true, "me.uk": true,
	"com.au": true, "net.au": true, "org.au": true,
	"com.br": true, "com.tr": true, "co.nz": true,
	"co.jp": true, "com.cn": true, "com.ua": true,
}

// Registrable — то, что регистрируют у регистратора: `example.com` для
// `shop.example.com`.
func Registrable(domain string) string {
	parts := strings.Split(strings.Trim(strings.ToLower(domain), "."), ".")
	if len(parts) <= 2 {
		return strings.Join(parts, ".")
	}

	lastTwo := strings.Join(parts[len(parts)-2:], ".")
	if compoundSuffixes[lastTwo] && len(parts) >= 3 {
		return strings.Join(parts[len(parts)-3:], ".")
	}
	return lastTwo
}
