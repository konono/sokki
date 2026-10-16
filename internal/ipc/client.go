package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// DefaultTimeout は 1 回の要求の待ち時間。
const DefaultTimeout = 10 * time.Second

// Call は録音プロセスへ 1 回要求して応答を受け取る。
//
// 接続できない場合は、録音プロセスが動いていないことを示すエラーを返す。
// この呼び出しが失敗しても録音プロセスには影響しない。
func Call(socketPath string, req Request) (Response, error) {
	return CallTimeout(socketPath, req, DefaultTimeout)
}

// CallTimeout は待ち時間を指定して要求する。
func CallTimeout(socketPath string, req Request, timeout time.Duration) (Response, error) {
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return Response{}, fmt.Errorf(
			"録音プロセスに接続できません (%s): %w\n"+
				"対処: 録音が動いているか start_or_attach / get_status で確認してください",
			socketPath, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	data, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return Response{}, fmt.Errorf("要求を送れません: %w", err)
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return Response{}, fmt.Errorf("応答を読めません: %w", err)
		}
		return Response{}, fmt.Errorf("応答がありません (録音プロセスが終了した可能性があります)")
	}
	var resp Response
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		return Response{}, fmt.Errorf("応答を解釈できません: %w", err)
	}
	if !resp.OK && resp.Error != "" {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

// Reachable は録音プロセスが応答するかを確認する。
func Reachable(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
