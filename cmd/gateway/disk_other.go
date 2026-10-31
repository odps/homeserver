//go:build !unix

package main

// diskUsage no está implementado fuera de Unix (solo se usa para desarrollar).
func diskUsage(string) (int, float64) { return 0, 0 }
