//go:build !linux

package backup

import "errors"

// freeBytes на не-Linux не существует.
//
// Круг копии там и не идёт: агент собирается только под Linux. Заглушка
// нужна ровно затем, чтобы сборку и разбор архива можно было проверять
// тестами на машине разработчика.
func freeBytes(string) (uint64, error) {
	return 0, errors.New("свободное место видно только под Linux")
}
