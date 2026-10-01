//go:build linux

package logs

import (
	"io/fs"
	"syscall"
)

// inodeOf достаёт inode файла.
//
// Отдельным файлом под Linux, потому что `syscall.Stat_t` есть только там.
// По inode ловится поворот лога: под тем же именем оказывается другой файл,
// и читать его надо с начала, а не с прошлого смещения.
func inodeOf(info fs.FileInfo) uint64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(stat.Ino)
}
