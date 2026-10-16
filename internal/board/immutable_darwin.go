//go:build darwin

package board

import (
	"io/fs"
	"syscall"
)

// macOS の変更不可フラグ。
const (
	ufImmutable = 0x00000002 // chflags uchg
	sfImmutable = 0x00020000 // chflags schg
)

// immutable は変更不可フラグが立っているかを返す。
//
// これが立っていると、ディレクトリでなく権限があっても
// その名前への Rename が失敗する。
func immutable(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Flags&(ufImmutable|sfImmutable) != 0
}
