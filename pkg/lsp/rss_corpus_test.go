//go:build corpus && !windows

package lsp_test

import (
	"runtime"
	"syscall"
)

// maxRSS is the peak resident size of the process, in bytes.
func maxRSS() int64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) << 10
}
