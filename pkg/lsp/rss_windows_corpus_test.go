//go:build corpus && windows

package lsp_test

// maxRSS is not measured on Windows.
func maxRSS() int64 { return 0 }
