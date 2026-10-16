//go:build !darwin

package board

import "io/fs"

// immutable は変更不可フラグの判定 (darwin 以外では常に false)。
func immutable(fi fs.FileInfo) bool { return false }
