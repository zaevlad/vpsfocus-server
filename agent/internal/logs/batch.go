package logs

import (
	"strconv"
	"time"
)

// Batch сводит события одного прохода в счётчики.
//
// Складывать их в памяти, а не писать в базу построчно, обязательно: на
// сайте под ботами за минуту набегают тысячи строк, и тысяча запросов к
// SQLite ради одной цифры — это нагрузка на сервер клиента, ради снятия
// которой нас туда и поставили.
type Batch struct {
	groups map[string]*Counted
	limit  int
	// Сколько групп «страницы нет» уже завели и сколько их разрешено.
	//
	// Отдельный счёт, а не общий потолок: перебор ботами — это тысячи
	// разных адресов, отвечающих четыреста четвёртой, и под общим потолком
	// они за минуту вытеснили бы настоящие пятисотки в группу «прочее».
	// Функция, заведённая в тридцатом спринте, сломала бы работающую с
	// семнадцатого.
	notFound      int
	notFoundLimit int
}

// Counted — группа за одно окно вместе с числом повторов.
type Counted struct {
	Event
	Count  int
	LastAt time.Time
}

// Сколько разных групп удерживать за проход.
//
// У сайта, который перебирают ботами, уникальных путей бывают десятки
// тысяч. Без потолка проход съел бы память чужого сервера, а экран
// превратился бы в ленту без единого повторяющегося события.
const DefaultGroupLimit = 2000

// Сколько из них разрешено занять ответам «страницы нет».
//
// Десятая часть общего потолка: чтобы увидеть всплеск, подробности не нужны
// вовсе — он виден по числу, — а чтобы разобрать, какие именно страницы
// пропали, двухсот адресов хватает с запасом.
const DefaultNotFoundLimit = 200

// Путь, под которым копится всё, что не поместилось в потолок. Врать про
// количество нельзя: «прочее» честнее, чем недосчитанные ошибки.
const OverflowPath = "{прочее}"

func NewBatch(limit int) *Batch {
	if limit <= 0 {
		limit = DefaultGroupLimit
	}
	notFound := DefaultNotFoundLimit
	if notFound > limit {
		notFound = limit
	}
	return &Batch{
		groups:        make(map[string]*Counted),
		limit:         limit,
		notFoundLimit: notFound,
	}
}

// AddInteresting добавляет событие, если оно про ошибку: строку error-лога,
// ошибку сервера или «страницы нет». Код 200 в базе — это гигабайты
// счётчиков ради «сайт работает», о чём и так есть кому рассказать.
func (b *Batch) AddInteresting(event Event) {
	if event.Kind == KindAccess && !Interesting(event.Status) {
		return
	}
	b.Add(event)
}

func (b *Batch) Add(event Event) {
	bucket := event.Bucket()
	key := event.Key() + "\x00" + strconv.FormatInt(bucket, 10)

	if group, ok := b.groups[key]; ok {
		group.Count++
		if event.At.After(group.LastAt) {
			group.LastAt = event.At
		}
		return
	}

	if b.crowded(event) {
		// Потолок: дальше считаем в одну общую группу того же вида. Число
		// не теряется, теряются подробности, — и это честнее недосчитанных
		// ошибок.
		overflow := Event{
			Kind:   event.Kind,
			At:     event.At,
			Status: event.Status,
			Level:  event.Level,
			Path:   OverflowPath,
		}
		key = overflow.Key() + "\x00" + strconv.FormatInt(bucket, 10)
		if group, ok := b.groups[key]; ok {
			group.Count++
			if event.At.After(group.LastAt) {
				group.LastAt = event.At
			}
			return
		}
		b.remember(overflow)
		b.groups[key] = &Counted{Event: overflow, Count: 1, LastAt: event.At}
		return
	}

	b.remember(event)
	b.groups[key] = &Counted{Event: event, Count: 1, LastAt: event.At}
}

// crowded — упёрлись ли мы в потолок, годный для этого события.
//
// Потолков два: общий и свой у «страницы нет». Второй не даёт перебору
// ботами занять весь проход и вытеснить пятисотки в группу «прочее» — то
// есть защищает не диск, а старую функцию от новой.
func (b *Batch) crowded(event Event) bool {
	if event.Status == StatusNotFound && b.notFound >= b.notFoundLimit {
		return true
	}
	return len(b.groups) >= b.limit
}

// remember считает заведённые группы «страницы нет».
func (b *Batch) remember(event Event) {
	if event.Status == StatusNotFound {
		b.notFound++
	}
}

// Groups отдаёт собранное. Порядок не важен: дальше их складывают в базу.
func (b *Batch) Groups() []Counted {
	groups := make([]Counted, 0, len(b.groups))
	for _, group := range b.groups {
		groups = append(groups, *group)
	}
	return groups
}

// Total — сколько событий всего попало в проход.
func (b *Batch) Total() int {
	total := 0
	for _, group := range b.groups {
		total += group.Count
	}
	return total
}
