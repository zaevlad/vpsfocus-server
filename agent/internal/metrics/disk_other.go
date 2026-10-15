//go:build !linux

package metrics

import "errors"

// disk на не-Linux не существует.
//
// Агент собирается и работает только под Linux — он едет на VPS. Эта
// заглушка нужна ровно для того, чтобы разбор `/proc`, который ни от какой
// платформы не зависит, можно было проверить тестами на машине
// разработчика.
func (c *Collector) disk() (diskFigures, error) {
	return diskFigures{}, errors.New("сведения о диске доступны только под Linux")
}
