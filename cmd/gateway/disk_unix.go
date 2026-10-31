//go:build unix

package main

import "syscall"

// diskUsage devuelve el porcentaje usado y los GB libres del sistema de ficheros.
func diskUsage(path string) (usedPct int, freeGB float64) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil || st.Blocks == 0 {
		return 0, 0
	}
	return int(100 - st.Bavail*100/st.Blocks), float64(st.Bavail) * float64(st.Bsize) / 1e9
}
