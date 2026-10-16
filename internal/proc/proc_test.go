package proc

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive はプロセスが生存しているかをシグナル 0 で確認する。
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestCommandRunsInOwnProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := Command(ctx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer Terminate(cmd)

	parentPgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	childPgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if childPgid == parentPgid {
		t.Fatalf("子が親と同じ process group (pgid=%d) にいる = 端末の Ctrl-C を直接受ける", childPgid)
	}
	if childPgid != cmd.Process.Pid {
		t.Errorf("子が自身の group のリーダーでない: pgid=%d pid=%d", childPgid, cmd.Process.Pid)
	}
	if !InOwnGroup(cmd, parentPgid) {
		t.Error("InOwnGroup が false を返した")
	}
}

func TestTerminateStopsChild(t *testing.T) {
	cmd := Command(context.Background(), "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	Terminate(cmd)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Terminate 後も子が生きている (pid=%d)", pid)
}

// REVIEW.md [P1] (実機確認) の回帰テスト。
//
// 端末の Ctrl-C は SIGINT を foreground process group **全体** に送る。
// これを「中間プロセスの process group へ SIGINT を送る」形で再現し、
// その配下で Command() が起動した孫プロセスが生き残ることを確認する。
//
// 音声デバイスも whisper モデルも使わない (sleep で代用)。
// 修正前 (Setpgid なし) は孫も同じ group に属するため SIGINT で死ぬ。
func TestChildSurvivesTerminalSIGINTToForegroundGroup(t *testing.T) {
	if os.Getenv("RR_PROC_HELPER") == "1" {
		return // ヘルパー実行時は何もしない (下で別途分岐)
	}

	// 中間プロセス = 「端末の foreground process group に居る CLI」役。
	// Setpgid で独自 group にし、そこへ SIGINT を送れるようにする。
	helper := exec.Command(os.Args[0], "-test.run=TestProcHelperProcess", "-test.v=false")
	helper.Env = append(os.Environ(), "RR_PROC_HELPER=1")
	helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Kill(-helper.Process.Pid, syscall.SIGKILL)
		_, _ = helper.Process.Wait()
	}()

	// ヘルパーが起動した孫プロセスの PID を受け取る。
	var grandchild int
	sc := bufio.NewScanner(stdout)
	deadline := time.Now().Add(15 * time.Second)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "GRANDCHILD="); ok {
			grandchild, _ = strconv.Atoi(rest)
			break
		}
		if time.Now().After(deadline) {
			break
		}
	}
	if grandchild == 0 {
		t.Fatal("ヘルパーから孫プロセスの PID を取得できなかった")
	}
	defer func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) }()

	if !alive(grandchild) {
		t.Fatalf("孫プロセスが起動していない (pid=%d)", grandchild)
	}

	// ここが本題: 端末の Ctrl-C 相当。
	// ヘルパーの process group 全体へ SIGINT を送る。
	if err := syscall.Kill(-helper.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatalf("SIGINT 送信に失敗: %v", err)
	}

	// 中間プロセスは SIGINT で終了するはず。
	waitDone := make(chan struct{})
	go func() { _, _ = helper.Process.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("中間プロセスが SIGINT で終了しなかった (テスト前提が崩れている)")
	}

	// 孫 (whisper-server 相当) は生き残り、ドレインに使えること。
	time.Sleep(300 * time.Millisecond)
	if !alive(grandchild) {
		t.Fatal("端末 Ctrl-C 相当の SIGINT で孫プロセスが死んだ " +
			"= whisper-server が終了し最後の確定区間を保存できない")
	}
}

// TestProcHelperProcess は上のテストから起動される補助プロセス。
// Command() で孫を起動して PID を報告し、SIGINT を待つ。
func TestProcHelperProcess(t *testing.T) {
	if os.Getenv("RR_PROC_HELPER") != "1" {
		t.Skip("ヘルパー専用 (単体では実行しない)")
	}
	cmd := Command(context.Background(), "sleep", "60")
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
	// 親テストへ孫の PID を伝える。
	os.Stdout.WriteString("GRANDCHILD=" + strconv.Itoa(cmd.Process.Pid) + "\n")
	_ = os.Stdout.Sync()

	// SIGINT を受けるまで待つ (既定動作で終了する)。
	time.Sleep(60 * time.Second)
}
