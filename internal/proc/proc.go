// Package proc は子プロセスを端末のシグナルから隔離して起動する。
//
// 端末で Ctrl-C を押すと、TTY ドライバは SIGINT を
// **foreground process group 全体** に送る。子プロセスを親と同じ
// process group のまま起動すると、親の終了処理が始まる前に
// 子 (whisper-server / ffmpeg) が直接 SIGINT で死ぬ。
// context を分離しても、この経路は防げない。
//
// そのため子は Setpgid で独自の process group に入れ、
// 停止は親が明示的に行う。
package proc

import (
	"context"
	"os/exec"
	"syscall"
)

// Command は独自の process group で動く子プロセスを組み立てる。
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// 子を新しい process group のリーダーにする。
		// 端末の SIGINT (foreground group 宛) は届かなくなる。
		Setpgid: true,
	}
	return cmd
}

// Terminate は子プロセスを process group ごと確実に停止する。
// まず SIGTERM、続いて SIGKILL を送る。
func Terminate(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	// Setpgid で起動していれば pgid == pid。
	// 負の pid 指定で process group 全体へ送る。
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	} else {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Process.Kill()
	}
	_, _ = cmd.Process.Wait()
}

// InOwnGroup は子が親と別の process group に居るかを返す (検証用)。
func InOwnGroup(cmd *exec.Cmd, parentPgid int) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return false
	}
	return pgid != parentPgid
}
