package whisper

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEncodeWAVHeader(t *testing.T) {
	pcm := []int16{0, 1000, -1000, 32767, -32768}
	out := EncodeWAV(pcm, 16000)

	if len(out) != 44+len(pcm)*2 {
		t.Fatalf("長さ = %d, want %d", len(out), 44+len(pcm)*2)
	}
	if string(out[0:4]) != "RIFF" || string(out[8:12]) != "WAVE" {
		t.Fatal("RIFF/WAVE マジックが不正")
	}
	if string(out[12:16]) != "fmt " || string(out[36:40]) != "data" {
		t.Fatal("チャンク識別子が不正")
	}
	if got := binary.LittleEndian.Uint16(out[22:24]); got != 1 {
		t.Fatalf("チャンネル数 = %d, want 1", got)
	}
	if got := binary.LittleEndian.Uint32(out[24:28]); got != 16000 {
		t.Fatalf("サンプルレート = %d, want 16000", got)
	}
	if got := binary.LittleEndian.Uint16(out[34:36]); got != 16 {
		t.Fatalf("ビット深度 = %d, want 16", got)
	}
	if got := binary.LittleEndian.Uint32(out[4:8]); got != uint32(36+len(pcm)*2) {
		t.Fatalf("RIFF サイズ = %d", got)
	}
	if got := binary.LittleEndian.Uint32(out[40:44]); got != uint32(len(pcm)*2) {
		t.Fatalf("data サイズ = %d", got)
	}

	// サンプルがそのまま復元できること。
	var back []int16
	r := bytes.NewReader(out[44:])
	for range pcm {
		var v int16
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
			t.Fatal(err)
		}
		back = append(back, v)
	}
	for i := range pcm {
		if back[i] != pcm[i] {
			t.Fatalf("サンプル %d = %d, want %d", i, back[i], pcm[i])
		}
	}
}

func TestEncodeWAVEmpty(t *testing.T) {
	out := EncodeWAV(nil, 16000)
	if len(out) != 44 {
		t.Fatalf("空 PCM でヘッダのみにならない: %d bytes", len(out))
	}
}
