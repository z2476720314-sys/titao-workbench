//go:build !windows

package fileutil

import "fmt"

func revealFile(string) error { return fmt.Errorf("文件定位仅支持 Windows") }
