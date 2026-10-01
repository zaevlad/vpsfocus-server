//go:build linux

package metrics

import "syscall"

// disk смотрит на смонтированный внутрь корень сервера.
//
// Отдельным файлом под Linux, потому что `syscall.Statfs` есть только там.
// Агент едет только на Linux, но тесты гоняются на машине разработчика, а
// она бывает и на Windows: без этого разделения не собирался бы даже разбор
// `/proc`, который платформы не касается.
func (c *Collector) disk() (totalMB, freeMB uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(c.rootDir, &stat); err != nil {
		return 0, 0, err
	}

	block := uint64(stat.Bsize)
	totalMB = stat.Blocks * block / (1024 * 1024)
	// Bavail, а не Bfree: часть места зарезервирована под root, и обещать
	// её пользователю нечестно.
	freeMB = stat.Bavail * block / (1024 * 1024)
	return totalMB, freeMB, nil
}
