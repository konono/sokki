//go:build !darwin

package ui

import (
	"fmt"
	"os"
)

// Key は画面操作のキー。
type Key int

const (
	KeyNone Key = iota
	KeyUp
	KeyDown
	KeyPageUp
	KeyPageDown
	KeyHome
	KeyEnd
	KeyHalfUp
	KeyHalfDown
	KeyFocusNext
)

// Keys は TTY のキー入力を読む (darwin 以外は未対応)。
type Keys struct{}

// StartKeys は darwin 以外では常に失敗する。
// 呼び出し側はキー操作なしで録音を続ける。
func StartKeys(f *os.File, fn func(Key)) (*Keys, error) {
	return nil, fmt.Errorf("この環境ではキー入力に対応していません")
}

// Stop は何もしない。
func (k *Keys) Stop() {}
