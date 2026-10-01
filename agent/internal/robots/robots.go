// Package robots — разбор и загрузка robots.txt по RFC 9309 плюс то, чего
// в нём нет, но что все пишут: Crawl-delay и Sitemap.
//
// Пакет общий, а не часть SEO-обхода: по чужому проду ходят два краулера —
// SEO-обход и сбор страниц для проверки ссылок, — и вежливость у них обязана
// быть одна. Правило проекта «агент остаётся ботом на живом сервере»
// сформулировано как общее; выполняться одним из двух оно не может.
//
// Своя реализация, а не библиотека: правил здесь три с половиной, зато
// зависимость в агенте, который стоит на чужом проде, стоит дороже. Всё,
// чего мы не понимаем в файле, молча пропускается — так же поступает и
// Google, и падать из-за чужой опечатки в robots.txt мы не станем.
package robots

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
	"time"
)

// Потолок паузы между запросами.
//
// Crawl-delay в сотни секунд встречается в живых robots.txt, и обход по нему
// не закончится никогда. Тридцать секунд — это уже щадящий режим, а обход,
// который не заканчивается, не отличим от сломанного.
const maxCrawlDelay = 30 * time.Second

// Rules — правила, относящиеся к нам.
type Rules struct {
	rules []rule
	// Пауза между запросами, которую попросил сайт.
	Delay time.Duration
	// Карты сайта. Директива внегрупповая: относится ко всему файлу, а не
	// к какому-то одному боту.
	Sitemaps []string
	// Прочитанный файл целиком. Нужен, чтобы спросить тот же файл о чужом
	// боте (`AllowedFor`): SEO-аудит говорит, открыт ли сайт Google и
	// ИИ-поиску, и второй разборщик ради этого разошёлся бы с первым.
	body []byte
}

type rule struct {
	pattern string
	allow   bool
}

// AllowAll — правила, разрешающие всё. Такими они становятся, когда
// robots.txt на сайте нет: отсутствие файла означает «ходи куда хочешь», в
// отличие от файла, который не отдался.
func AllowAll() *Rules { return &Rules{} }

// Parse собирает правила для нашего бота.
//
// Группы, адресованные нам по имени, вытесняют группу со звёздочкой целиком,
// а не дополняют её: так же поступают поисковики, и «правило для всех» в
// присутствии правила лично для нас — это не сумма, а более общий случай.
func Parse(body []byte, agent string) *Rules {
	agent = strings.ToLower(agent)

	var (
		robots       Rules
		named        []rule
		wildcard     []rule
		agents       []string
		inGroup      bool
		matchesUs    bool
		matchesStar  bool
		namedPresent bool
		// Пауза копится по корзинам вместе с правилами: Crawl-delay — это
		// директива группы, а не файла. `Crawl-delay: 30` в чужой группе —
		// обычное дело в живых robots.txt, и взятая максимумом по всему
		// файлу она растягивала наш обход до получаса на пятьсот страниц,
		// то есть гарантированно за пределы отведённого времени.
		namedDelay    time.Duration
		wildcardDelay time.Duration
	)

	// collect кладёт правило в ту корзину, к которой относится текущая
	// группа. Групп, адресованных нам, может быть несколько — по спецификации
	// они складываются.
	collect := func(item rule) {
		if matchesUs {
			named = append(named, item)
			namedPresent = true
		}
		if matchesStar {
			wildcard = append(wildcard, item)
		}
	}

	// Пауза складывается по тому же правилу, что и остальное в группе, но
	// «складывается» для неё значит «берём наибольшую»: две группы для нас с
	// разной паузой — это две просьбы, и уважить надо более осторожную.
	collectDelay := func(delay time.Duration) {
		if matchesUs {
			namedPresent = true
			if delay > namedDelay {
				namedDelay = delay
			}
		}
		if matchesStar && delay > wildcardDelay {
			wildcardDelay = delay
		}
	}

	scanner := bufio.NewScanner(bytes.NewReader(body))
	// Строка robots.txt длиннее буфера по умолчанию — редкость, но бывает у
	// сайтов с километровыми списками параметров.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for scanner.Scan() {
		line := scanner.Text()
		if hash := strings.IndexByte(line, '#'); hash >= 0 {
			line = line[:hash]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		field := strings.ToLower(strings.TrimSpace(line[:colon]))
		value := strings.TrimSpace(line[colon+1:])

		switch field {
		case "user-agent":
			// Подряд идущие user-agent — это заголовок одной группы. Первое
			// правило после них закрывает заголовок, и следующий user-agent
			// начинает новую группу.
			if inGroup {
				agents = nil
				inGroup = false
			}
			agents = append(agents, strings.ToLower(value))
			matchesUs, matchesStar = false, false
			for _, name := range agents {
				if name == "*" {
					matchesStar = true
					continue
				}
				// Совпадение по началу имени: боты представляются с версией
				// («vpsfocus-seo/1.0»), а правило пишут на имя.
				if name != "" && strings.HasPrefix(agent, name) {
					matchesUs = true
				}
			}

		case "disallow":
			inGroup = true
			// Пустой Disallow — это «разрешено всё», а не «запрещено всё».
			// Разница ровно в противоположность, и путают её постоянно.
			if value == "" {
				continue
			}
			collect(rule{pattern: value, allow: false})

		case "allow":
			inGroup = true
			if value == "" {
				continue
			}
			collect(rule{pattern: value, allow: true})

		case "crawl-delay":
			inGroup = true
			if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 {
				collectDelay(time.Duration(seconds * float64(time.Second)))
			}

		case "sitemap":
			// Директива вне групп: относится к файлу целиком.
			if value != "" {
				robots.Sitemaps = append(robots.Sitemaps, value)
			}
		}
	}

	if namedPresent {
		robots.rules = named
		robots.Delay = namedDelay
	} else {
		robots.rules = wildcard
		robots.Delay = wildcardDelay
	}
	if robots.Delay > maxCrawlDelay {
		robots.Delay = maxCrawlDelay
	}
	robots.body = body
	return &robots
}

// AllowedFor решает, можно ли этот путь другому боту — тем же разбором, что
// и для нас.
//
// Правила, собранные без файла (`AllowAll` — robots.txt на сайте нет),
// разрешают всё и любому: отсутствие файла — это разрешение для всех, а не
// только для нас.
func (r *Rules) AllowedFor(agent, path string) bool {
	if r == nil || r.body == nil {
		return true
	}
	return Parse(r.body, agent).Allowed(path)
}

// Allowed решает, можно ли забирать этот путь.
//
// Побеждает самое длинное совпавшее правило, при равной длине — разрешающее.
// Так это описано у Google и так же ведут себя все, кто читает эти файлы;
// собственная трактовка означала бы, что мы ходим не туда, куда ходит
// поисковик, — и аудит показывал бы не то, что видит он.
func (r *Rules) Allowed(path string) bool {
	if r == nil || len(r.rules) == 0 {
		return true
	}
	if path == "" {
		path = "/"
	}

	best := rule{}
	bestLength := -1
	for _, item := range r.rules {
		if !matchPattern(item.pattern, path) {
			continue
		}
		length := len(item.pattern)
		if length > bestLength || (length == bestLength && item.allow) {
			best, bestLength = item, length
		}
	}
	if bestLength < 0 {
		return true
	}
	return best.allow
}

// matchPattern сверяет путь с шаблоном правила: `*` — любая
// последовательность, `$` в конце — конец пути, всё прочее буквально.
func matchPattern(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = pattern[:len(pattern)-1]
	}

	parts := strings.Split(pattern, "*")

	// Шаблон без звёздочек: с `$` это точное совпадение, без него —
	// совпадение по началу пути.
	if len(parts) == 1 {
		if anchored {
			return path == pattern
		}
		return strings.HasPrefix(path, pattern)
	}

	// Ведущий кусок привязан к началу пути.
	position := 0
	if parts[0] != "" {
		if !strings.HasPrefix(path, parts[0]) {
			return false
		}
		position = len(parts[0])
	}
	middle := parts[1:]

	// При `$` последний кусок ищется с конца, а не первым попавшимся
	// вхождением. Жадный поиск слева направо на `/*.pdf$` находил первое
	// `.pdf` в `/a.pdf.pdf`, до конца пути оно не дотягивало, и правило
	// объявлялось неподходящим. У Allow такая ошибка безобидна, у Disallow —
	// нет: мы пошли бы туда, куда запрещено, то есть оказались бы не там,
	// где ходит поисковик.
	var tail string
	if anchored {
		if last := middle[len(middle)-1]; last != "" {
			tail = last
			middle = middle[:len(middle)-1]
		}
	}

	for _, part := range middle {
		if part == "" {
			continue
		}
		found := strings.Index(path[position:], part)
		if found < 0 {
			return false
		}
		position += found + len(part)
	}

	if !anchored || tail == "" {
		// Без якоря хвост пути свободен; `*$` на конце шаблона — то же
		// самое, что просто `*`.
		return true
	}
	// Хвост обязан упираться в конец пути и не залезать на то, что уже
	// сопоставили левее.
	if !strings.HasSuffix(path, tail) {
		return false
	}
	return len(path)-len(tail) >= position
}
