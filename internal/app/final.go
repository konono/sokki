package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/konono/sokki/internal/config"
	"github.com/konono/sokki/internal/transcript"
)

// finalRange は確定認識に渡す、重ならない PCM の範囲。
type finalRange struct{ start, end int }

// transcribeFinal は長い発話を音声全体と短い区間の両方で認識する。
// 全体の認識が途中を省く場合と、短い区間が文頭や区間境界を失う場合の
// 双方に備える。暫定文は確定文の材料にしない。
func transcribeFinal(ctx context.Context, client transcriber, audio []int16) (string, error) {
	ranges := finalRanges(audio)
	if len(ranges) == 1 {
		return client.Transcribe(ctx, audio, config.SampleRate)
	}
	whole, wholeErr := client.Transcribe(ctx, audio, config.SampleRate)
	parts := make([]string, 0, len(ranges))
	var firstErr error
	for i, r := range ranges {
		text, err := client.Transcribe(ctx, audio[r.start:r.end], config.SampleRate)
		if err != nil {
			// 音声はまだメモリ内にある。局所的な失敗で区間全体を失わない。
			text, err = client.Transcribe(ctx, audio[r.start:r.end], config.SampleRate)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("音声区間 %d/%d: %w", i+1, len(ranges), err)
			}
			continue
		}
		text = transcript.Clean(text)
		if text != "" && !transcript.IsNoise(text) {
			parts = append(parts, text)
		}
	}
	text := reconcileTranscripts(transcript.Clean(whole), strings.Join(parts, " "))
	if text != "" {
		return text, nil
	}
	if wholeErr != nil {
		return "", wholeErr
	}
	return "", firstErr
}

// reconcileTranscripts は同じ音声の全体認識と分割認識を照合する。
// 一致する長い箇所を固定し、その間の不一致ではより内容の多い候補を残す。
// 小さい言い換えでは文脈を持つ全体認識を優先する。
func reconcileTranscripts(whole, chunks string) string {
	return string(reconcileRunes([]rune(whole), []rune(chunks)))
}

func reconcileRunes(whole, chunks []rune) []rune {
	if len(whole) == 0 {
		return chunks
	}
	if len(chunks) == 0 {
		return whole
	}
	i, j, n := longestCommonRun(whole, chunks)
	if n < 4 {
		if len(chunks) > len(whole)+3 {
			return chunks
		}
		return whole
	}
	out := append([]rune(nil), reconcileRunes(whole[:i], chunks[:j])...)
	out = append(out, whole[i:i+n]...)
	out = append(out, reconcileRunes(whole[i+n:], chunks[j+n:])...)
	return out
}

func longestCommonRun(a, b []rune) (bestI, bestJ, bestN int) {
	prev := make([]int, len(b)+1)
	for i, r := range a {
		curr := make([]int, len(b)+1)
		for j, s := range b {
			if r != s {
				continue
			}
			curr[j+1] = prev[j] + 1
			if curr[j+1] > bestN {
				bestN = curr[j+1]
				bestI = i + 1 - bestN
				bestJ = j + 1 - bestN
			}
		}
		prev = curr
	}
	return
}

// finalRanges は約 5 秒ごとに音の小さい境目を探して分ける。
// 各サンプルを一度だけ使うため、窓の重複による文の二重記録がない。
func finalRanges(audio []int16) []finalRange {
	n := len(audio)
	if n <= 7*config.SampleRate {
		return []finalRange{{0, n}}
	}
	const targetSize = 5 * config.SampleRate
	const minSize = 2 * config.SampleRate
	count := (n + targetSize - 1) / targetSize
	ranges := make([]finalRange, 0, count)
	start := 0
	for i := 1; i < count; i++ {
		target := n * i / count
		lo := target - config.SampleRate*3/4
		hi := target + config.SampleRate*3/4
		if lo < start+minSize {
			lo = start + minSize
		}
		if max := n - (count-i)*minSize; hi > max {
			hi = max
		}
		cut := quietestCut(audio, lo, hi, target)
		ranges = append(ranges, finalRange{start, cut})
		start = cut
	}
	return append(ranges, finalRange{start, n})
}

// quietestCut は指定位置の近くで 100ms の平均振幅が最小の位置を返す。
// 同じ振幅なら目標位置に近い方を選ぶ。
func quietestCut(audio []int16, lo, hi, target int) int {
	if lo >= hi {
		return lo
	}
	const halfWindow = config.SampleRate / 20 // 前後 50ms
	best := lo
	bestScore := int64(-1)
	for cut := lo; cut <= hi; cut += config.FrameSamples {
		from, to := cut-halfWindow, cut+halfWindow
		if from < 0 {
			from = 0
		}
		if to > len(audio) {
			to = len(audio)
		}
		var score int64
		for _, sample := range audio[from:to] {
			v := int64(sample)
			if v < 0 {
				v = -v
			}
			score += v
		}
		if bestScore < 0 || score < bestScore || score == bestScore && absInt(cut-target) < absInt(best-target) {
			best, bestScore = cut, score
		}
	}
	return best
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
