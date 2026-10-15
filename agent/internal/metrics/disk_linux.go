//go:build linux

package metrics

import "syscall"

// disk смотрит на смонтированный внутрь корень сервера.
//
// Отдельным файлом под Linux, потому что `syscall.Statfs` есть только там.
// Агент едет только на Linux, но тесты гоняются на машине разработчика, а
// она бывает и на Windows: без этого разделения не собирался бы даже разбор
// `/proc`, который платформы не касается. Пересчёт чисел — в `diskFrom`,
// и его тесты от платформы не зависят.
func (c *Collector) disk() (diskFigures, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(c.rootDir, &stat); err != nil {
		return diskFigures{}, err
	}
	return diskFrom(uint64(stat.Bsize), stat.Blocks, stat.Bavail, stat.Files, stat.Ffree), nil
}
