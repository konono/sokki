package whisper

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ValidateLocalURL は --server-url がローカルホストを指していることを検証する。
//
// PLAN.md の「実行中にクラウド API を呼び出さない」を守るため、
// 外部ホストへ音声を送る設定を受け付けない。空文字列 (自前起動) は常に可。
func ValidateLocalURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("--server-url を解釈できません (%s): %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("--server-url のスキームは http/https のみです: %s", raw)
	}

	host := u.Hostname()
	if isLoopbackHost(host) {
		return nil
	}
	return fmt.Errorf(
		"--server-url にローカルホスト以外を指定できません: %s\n"+
			"このツールは音声を外部へ送らない方針のため、127.0.0.1 / ::1 / localhost のみ許可しています\n"+
			"対処: ローカルで whisper-server を起動して指定するか、--server-url を外してください",
		raw)
}

// newLocalHTTPClient は音声が外部へ出ないことを担保した HTTP クライアントを返す。
//
// 標準の http.Client は 307/308 で POST 本文を転送先へ再送するため、
// ローカルのサーバーが外部 URL を返すと音声が外へ出てしまう。
// /inference にリダイレクトは不要なので、一律で拒否する。
func newLocalHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 120 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf(
				"whisper-server がリダイレクトを返しました (転送先 %s)。"+
					"音声を外部へ送らないため転送しません", req.URL.Redacted())
		},
	}
}

// isLoopbackHost はホスト名がループバックを指すかを判定する。
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	// 127.0.0.1 や ::1 などの IP リテラル。
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
