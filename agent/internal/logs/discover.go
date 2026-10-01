package logs

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Где веб-серверы держат логи, если их никто не переносил.
//
// Это не детект веб-сервера клиента и не подстройка под него: список
// одинаковый всегда, ничего не запускается и не спрашивается, а найденное
// открывается только на чтение. Не нашлось — человек укажет путь сам.
var wellKnown = []string{
	"/var/log/nginx",
	"/var/log/apache2",
	"/var/log/httpd",
	"/var/log/apache",
}

// Хвосты повёрнутых файлов. Их читать незачем: свежие события пишутся в
// текущий, а повёрнутый мы уже прочли до поворота.
var rotatedSuffixes = []string{".gz", ".bz2", ".xz", ".zst", ".old", ".1"}

// Source — файл лога, за которым следит агент.
type Source struct {
	// Путь на сервере, а не внутри контейнера: человеку показывают его.
	Path string `json:"path"`
	// access | error.
	Kind string `json:"kind"`
	// Задан человеком, а не найден нами. Такой не исчезает из списка от
	// того, что файла сейчас нет: пропавший лог — это тоже новость.
	Manual bool `json:"manual"`
}

// Discover ищет логи в известных местах.
//
// root — корень хостовой файловой системы внутри контейнера: агент живёт в
// контейнере, а рассказывать обязан про сервер.
func Discover(root string, list func(string) ([]string, error)) []Source {
	var found []Source
	seen := map[string]bool{}

	for _, dir := range wellKnown {
		names, err := list(filepath.Join(root, dir))
		if err != nil {
			// Каталога нет или он закрыт — это норма: у клиента может не
			// быть ни nginx, ни Apache вовсе.
			continue
		}

		for _, name := range names {
			if !looksLikeLog(name) {
				continue
			}
			path := dir + "/" + name
			if seen[path] {
				continue
			}
			seen[path] = true
			found = append(found, Source{Path: path, Kind: KindOf(name)})
		}
	}

	sort.Slice(found, func(i, j int) bool { return found[i].Path < found[j].Path })
	return found
}

// looksLikeLog отсеивает повёрнутые файлы и всё, что логом не выглядит.
func looksLikeLog(name string) bool {
	lower := strings.ToLower(name)

	for _, suffix := range rotatedSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return false
		}
	}
	// access.log.2 и подобные: цифра в самом конце — признак поворота.
	if parts := strings.Split(lower, "."); len(parts) > 1 && isDigits(parts[len(parts)-1]) {
		return false
	}

	return strings.HasSuffix(lower, ".log") ||
		strings.HasSuffix(lower, "_log") ||
		strings.HasSuffix(lower, "-log")
}

// KindOf угадывает вид лога по имени файла.
//
// Угадывание тут безобидно: разбор строки всё равно пробует оба формата, а
// вид нужен интерфейсу, чтобы сгруппировать источники.
func KindOf(name string) string {
	if strings.Contains(strings.ToLower(name), "error") {
		return KindError
	}
	return KindAccess
}

// ForbiddenPathRunes — знаки, которых в пути к логу быть не может.
//
// Список один на обе стороны: тот же набор стоит в приложении
// (`agent.rs::log_path_allowed`), и разойтись им не даёт тест
// `agent::tests::log_path_rules_match_the_agent`, читающий этот файл. Приём
// тот же, что у `PathRunes` в мониторинге: набор знаков живёт одной строкой,
// а не переписывается в каждом месте, где отбирают.
//
// Кавычки и подстановки здесь потому, что путь уезжает в shell-команду
// выдачи доступа к каталогу лога. Собирает команду приложение, и отбирает
// первым тоже оно — но `logs.json` переживает выпуски приложения, а вторая
// проверка, которая слабее первой, второй проверкой не является.
const ForbiddenPathRunes = "'\"`$;&|\\"

// MaxPathLen — потолок длины пути, тот же, что в приложении.
const MaxPathLen = 300

// CheckPath решает, можно ли читать путь, заданный человеком.
//
// Агенту смонтирован весь корень сервера, пусть и на чтение. Значит строка в
// конфиге — это потенциальное «прочитай мне /etc/shadow и покажи в
// интерфейсе». Поэтому путь обязан быть абсолютным, без переходов вверх, без
// кавычек и подстановок, и именем походить на лог: у секретов имена другие.
func CheckPath(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return false
	}
	if len(path) > MaxPathLen || strings.ContainsAny(path, ForbiddenPathRunes) {
		return false
	}
	// Управляющие знаки целиком, а не только перевод строки и нуль: файл
	// читают два разных разборщика, и что из них сделает вертикальная
	// табуляция, заранее не скажет никто. Та же линия, что у чистки `.env`.
	if strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return false
	}

	name := path
	if slash := strings.LastIndexByte(path, '/'); slash >= 0 {
		name = path[slash+1:]
	}
	if name == "" {
		return false
	}
	return strings.Contains(strings.ToLower(name), "log")
}
