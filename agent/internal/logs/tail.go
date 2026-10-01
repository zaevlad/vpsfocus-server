package logs

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
)

// Position — где мы остановились в прошлый раз.
//
// Одного смещения мало: лог поворачивают, и файл под тем же именем
// оказывается другим файлом. Inode ловит поворот, размер — усечение
// (`truncate` вместо поворота, так тоже делают).
type Position struct {
	Inode  uint64 `json:"inode"`
	Size   int64  `json:"size"`
	Offset int64  `json:"offset"`
}

// Коды бед, из-за которых источник не читается. Перевод — в приложении.
const (
	ErrNotFound     = "logFileMissing"
	ErrNoAccess     = "logAccessDenied"
	ErrUnreadable   = "logUnreadable"
	ErrPathRejected = "logPathRejected"
)

// Сколько хвоста прочитать при первом знакомстве с файлом.
//
// Не весь файл: на проде клиента лог бывает гигабайтным, и читать его
// целиком ради истории, которая никому не нужна, — плохая плата за
// установку. Но и не «с этого мгновения»: экран, пустой первые сутки,
// выглядит сломанным.
const firstReadTail = 256 << 10

// Сколько читать за один проход. Остаток дочитает следующий: агент стоит на
// чужом сервере и не должен занимать его собой.
const DefaultMaxBytes = 8 << 20

// Read дочитывает файл с прошлой позиции и разбирает новые строки.
//
// Разбор пробует оба формата: имя файла подсказывает вид, но полагаться на
// имя нельзя — access-лог у клиента может называться как угодно.
func Read(path string, from Position, maxBytes int64, emit func(Event)) (Position, string) {
	file, err := os.Open(path)
	if err != nil {
		return from, openFailure(err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return from, ErrUnreadable
	}

	position := Position{Inode: inodeOf(info), Size: info.Size()}
	offset := from.Offset

	switch {
	case from.Size == 0 && from.Offset == 0:
		// Первое знакомство: берём только хвост.
		offset = info.Size() - firstReadTail
		if offset < 0 {
			offset = 0
		}
	case position.Inode != 0 && from.Inode != 0 && position.Inode != from.Inode:
		// Файл повернули — под старым именем лежит новый файл.
		offset = 0
	case info.Size() < from.Offset:
		// Файл усекли на месте.
		offset = 0
	}

	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return from, ErrUnreadable
		}
	}

	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	reader := bufio.NewScanner(io.LimitReader(file, maxBytes))
	// Строка access-лога с длинным адресом и заголовком легко перебирает
	// умолчание bufio в 64 КБ, а брошенная на середине строка — это
	// пропущенное событие.
	reader.Buffer(make([]byte, 0, 64<<10), 1<<20)

	read := int64(0)
	skipFirst := offset > 0 && offset != from.Offset

	for reader.Scan() {
		line := reader.Text()
		read += int64(len(line)) + 1

		// После прыжка в середину файла первая строка почти наверняка
		// обрублена: начинаем со следующей.
		if skipFirst {
			skipFirst = false
			continue
		}
		if event, ok := parseAny(line); ok {
			emit(event)
		}
	}
	if err := reader.Err(); err != nil && !errors.Is(err, io.EOF) {
		// Дочитать не вышло — но то, что уже разобрали, сохраняем: позиция
		// сдвигается ровно на прочитанное.
		position.Offset = offset + read
		return position, ErrUnreadable
	}

	position.Offset = offset + read
	return position, ""
}

// parseAny разбирает строку любым из известных форматов.
//
// Сначала access — его строк на порядки больше, и лишняя попытка на каждой
// из них стоила бы заметного времени на чужом процессоре.
//
// Отдаются все строки access-лога, а не только ошибки: роботов (трек V)
// считают и по ответам 200. Отбор ошибок для групп — [Batch.AddInteresting].
func parseAny(line string) (Event, bool) {
	if event, ok := ParseAccess(line); ok {
		return event, true
	}
	return ParseError(line)
}

func openFailure(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case errors.Is(err, fs.ErrPermission):
		return ErrNoAccess
	default:
		return ErrUnreadable
	}
}
