package mcp

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/konono/sokki/internal/ipc"
)

// ReadTranscriptFile は保存済みの文字起こしから確定行を読む。
//
// 先頭の "# " で始まる見出し行は本文に含めない。
// from は 0 始まりの行位置で、戻り値と合わせて次回の続きを決められる。
func ReadTranscriptFile(path string, from, limit int) ([]ipc.Line, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("文字起こしがまだありません (%s)", path)
		}
		return nil, fmt.Errorf("文字起こしを読めません (%s): %w", path, err)
	}
	defer f.Close()

	var out []ipc.Line
	idx := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "[") {
			continue // 見出し・空行
		}
		if idx >= from {
			at, text := SplitLine(line)
			out = append(out, ipc.Line{Index: idx, At: at, Text: text})
			if limit > 0 && len(out) >= limit {
				idx++
				break
			}
		}
		idx++
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SplitLine は "[HH:MM:SS] 本文" を時刻と本文に分ける。
func SplitLine(line string) (at, text string) {
	if !strings.HasPrefix(line, "[") {
		return "", line
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return "", line
	}
	return line[1:end], strings.TrimSpace(line[end+1:])
}
