package whisper

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestValidateLocalURLAllowsLoopback(t *testing.T) {
	ok := []string{
		"", // 未指定 (自前起動)
		"http://127.0.0.1:8080",
		"http://localhost:8080",
		"http://LocalHost:8080",
		"http://[::1]:8080",
		"https://127.0.0.1:8443",
		"http://127.0.0.2:8080", // 127.0.0.0/8 はループバック
	}
	for _, u := range ok {
		if err := ValidateLocalURL(u); err != nil {
			t.Errorf("ValidateLocalURL(%q) = %v, want nil", u, err)
		}
	}
}

func TestValidateLocalURLRejectsRemote(t *testing.T) {
	// 音声を外部へ送る設定は受け付けない (PLAN.md: クラウド API を呼ばない)。
	ng := []string{
		"http://example.com:8080",
		"https://api.openai.com",
		"http://192.168.1.10:8080",
		"http://10.0.0.5:8080",
		"http://[2001:db8::1]:8080",
	}
	for _, u := range ng {
		err := ValidateLocalURL(u)
		if err == nil {
			t.Errorf("ValidateLocalURL(%q) = nil, want error", u)
			continue
		}
		if !strings.Contains(err.Error(), "対処:") {
			t.Errorf("対処が示されていない: %v", err)
		}
	}
}

func TestValidateLocalURLRejectsBadScheme(t *testing.T) {
	for _, u := range []string{"ftp://127.0.0.1", "ws://127.0.0.1:8080"} {
		if err := ValidateLocalURL(u); err == nil {
			t.Errorf("ValidateLocalURL(%q) = nil, want error", u)
		}
	}
}

// REVIEW.md [P1] の回帰テスト。
// ローカルのサーバーが 307/308 で外部ホストへ飛ばしても、
// 音声 (POST 本文) が外部へ送られないこと。
func TestTranscribeDoesNotFollowRedirectToExternalHost(t *testing.T) {
	// 「外部ホスト」役。ここに本文が届いたら漏洩。
	var leaked atomic.Bool
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) > 0 {
			leaked.Store(true)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"漏洩"}`))
	}))
	defer external.Close()

	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		leaked.Store(false)

		// ローカルの whisper-server 役が外部へリダイレクトする。
		local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, external.URL+"/inference", code)
		}))

		c, err := Start(context.Background(), Options{ServerURL: local.URL, Language: "ja"})
		if err != nil {
			// waitReady がリダイレクト拒否で失敗するのが期待動作。
			local.Close()
			if leaked.Load() {
				t.Fatalf("%d: 音声が外部ホストへ送信された", code)
			}
			continue
		}

		_, err = c.Transcribe(context.Background(), make([]int16, 16000), 16000)
		local.Close()

		if err == nil {
			t.Errorf("%d: リダイレクトが拒否されなかった", code)
		}
		if leaked.Load() {
			t.Errorf("%d: 音声が外部ホストへ送信された", code)
		}
	}
}

// ループバック内のリダイレクトも拒否する (転送自体を使わない設計)。
func TestRedirectRejectedEvenToLoopback(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer target.Close()

	client := newLocalHTTPClient()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer src.Close()

	resp, err := client.Post(src.URL, "application/octet-stream", strings.NewReader("audio"))
	if err == nil {
		resp.Body.Close()
		t.Fatal("リダイレクトが追跡された")
	}
	if !strings.Contains(err.Error(), "音声を外部へ送らない") {
		t.Fatalf("拒否理由が不明瞭: %v", err)
	}
}
