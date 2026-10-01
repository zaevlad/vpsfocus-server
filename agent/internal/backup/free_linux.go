//go:build linux

package backup

import "syscall"

// freeBytes — сколько места осталось в файловой системе каталога.
//
// Отдельным файлом под Linux, как и у метрик: `syscall.Statfs` есть только
// там. Агент едет только на Linux, но тесты гоняются на машине
// разработчика, а она бывает и на Windows.
func freeBytes(dir string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, err
	}
	// Bavail, а не Bfree: часть места зарезервирована под root, а копию
	// пишет агент под своим пользователем — до резерва он не дотянется.
	return stat.Bavail * uint64(stat.Bsize), nil
}
