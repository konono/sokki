// Package vad は音声区間検出と発話単位への切り出しを行う。
//
// 外部依存を持たない純粋なロジックとして実装してあり、
// 実機の音声デバイスなしに単体テストできる。
package vad

import (
	"math"
	"time"
)

// Kind はセグメンタが出すイベント種別。
type Kind int

const (
	// Interim は発話継続中の暫定結果用スナップショット。ファイルには書かない。
	Interim Kind = iota
	// Final は無音境界 (または最大長) で確定した発話区間。
	Final
)

// Event は検出された音声区間。
type Event struct {
	Kind  Kind
	Audio []int16       // 16 kHz モノラル PCM
	Start time.Duration // 会議開始からの開始時刻
	End   time.Duration // 会議開始からの終了時刻
}

// Options は検出パラメータ。
type Options struct {
	SampleRate      int
	FrameSamples    int
	SilenceHangover time.Duration // この長さの無音で発話終了とみなす
	MinSpeech       time.Duration // これより短い区間は捨てる
	MaxSegment      time.Duration // 無音が来なくてもここで強制的に切る
	Preroll         time.Duration // 発話開始前にさかのぼって残す長さ
	FloorRatio      float64       // 雑音フロアの何倍を発話とみなすか
	AbsThreshold    float64       // 雑音フロアによらない下限しきい値
	ShowInterim     bool
	InterimAfter    time.Duration // 発話開始からこの時間で最初の暫定結果
	InterimInterval time.Duration // 以後の暫定結果の間隔
}

// Segmenter は PCM を順に受け取り、発話区間を切り出す。
type Segmenter struct {
	opt Options

	// 発話開始を判定するのに必要な連続フレーム数 (100ms)。
	onsetFrames int

	preroll     []int16 // 直近の無音を保持するリングバッファ相当
	prerollMax  int
	current     []int16
	inSpeech    bool
	onsetCount  int
	silentCount int

	noiseFloor   float64
	totalSamp    int64 // 取り込み済み総サンプル数 (時刻計算用)
	segStart     int64 // 現在の区間の開始サンプル位置
	nextInterm   int64 // 次に暫定結果を出すサンプル位置
	speechSamp   int64 // 現在の区間のうち発話と判定されたサンプル数
	keepTailSamp int64 // 確定時に末尾へ残す無音の長さ
}

// New は Segmenter を生成する。
func New(opt Options) *Segmenter {
	onset := int(0.1 * float64(opt.SampleRate) / float64(opt.FrameSamples))
	if onset < 1 {
		onset = 1
	}
	return &Segmenter{
		opt:         opt,
		onsetFrames: onset,
		prerollMax:  int(opt.Preroll.Seconds() * float64(opt.SampleRate)),
		noiseFloor:  opt.AbsThreshold,
		// 短い返答の語尾を認識できるよう、末尾を 400ms 残す。
		keepTailSamp: int64(0.4 * float64(opt.SampleRate)),
	}
}

// rms は 1 フレームの実効値を 0..1 で返す。
func rms(frame []int16) float64 {
	if len(frame) == 0 {
		return 0
	}
	var sum float64
	for _, s := range frame {
		v := float64(s) / 32768.0
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(frame)))
}

func (s *Segmenter) threshold() float64 {
	t := s.noiseFloor * s.opt.FloorRatio
	return math.Max(t, s.opt.AbsThreshold)
}

func (s *Segmenter) samplesToDur(n int64) time.Duration {
	return time.Duration(float64(n) / float64(s.opt.SampleRate) * float64(time.Second))
}

func (s *Segmenter) durToSamples(d time.Duration) int64 {
	return int64(d.Seconds() * float64(s.opt.SampleRate))
}

// Push は 1 フレーム分の PCM を与え、発生したイベントを返す。
// frame の長さは Options.FrameSamples を想定する。
func (s *Segmenter) Push(frame []int16) []Event {
	var events []Event
	level := rms(frame)
	s.totalSamp += int64(len(frame))
	speech := level > s.threshold()

	if !s.inSpeech {
		// 無音中のみ雑音フロアを更新する (発話で引き上げられないように)。
		if !speech {
			s.noiseFloor = 0.995*s.noiseFloor + 0.005*level
			if s.noiseFloor < 1e-6 {
				s.noiseFloor = 1e-6
			}
		}
		s.appendPreroll(frame)

		if speech {
			s.onsetCount++
			if s.onsetCount >= s.onsetFrames {
				s.beginSpeech()
			}
		} else {
			s.onsetCount = 0
		}
		return events
	}

	// 発話中
	s.current = append(s.current, frame...)
	if speech {
		s.silentCount = 0
		s.speechSamp += int64(len(frame))
	} else {
		s.silentCount++
	}

	hangoverFrames := int(s.opt.SilenceHangover.Seconds() * float64(s.opt.SampleRate) / float64(s.opt.FrameSamples))
	segLen := int64(len(s.current))

	switch {
	case s.silentCount >= hangoverFrames:
		if ev, ok := s.finish(); ok {
			events = append(events, ev)
		}
	case segLen >= s.durToSamples(s.opt.MaxSegment):
		// 長すぎる発話は区切って確定させ、そのまま次の区間を継続する。
		if ev, ok := s.finish(); ok {
			events = append(events, ev)
		}
		s.beginSpeech()
	case s.opt.ShowInterim && s.totalSamp >= s.nextInterm:
		events = append(events, Event{
			Kind:  Interim,
			Audio: append([]int16(nil), s.current...),
			Start: s.samplesToDur(s.segStart),
			End:   s.samplesToDur(s.totalSamp),
		})
		s.nextInterm = s.totalSamp + s.durToSamples(s.opt.InterimInterval)
	}
	return events
}

func (s *Segmenter) appendPreroll(frame []int16) {
	s.preroll = append(s.preroll, frame...)
	if over := len(s.preroll) - s.prerollMax; over > 0 {
		s.preroll = append(s.preroll[:0], s.preroll[over:]...)
	}
}

func (s *Segmenter) beginSpeech() {
	s.inSpeech = true
	s.onsetCount = 0
	s.silentCount = 0
	s.current = append([]int16(nil), s.preroll...)
	s.preroll = s.preroll[:0]
	s.speechSamp = 0
	s.segStart = s.totalSamp - int64(len(s.current))
	if s.segStart < 0 {
		s.segStart = 0
	}
	s.nextInterm = s.totalSamp + s.durToSamples(s.opt.InterimAfter)
}

// finish は現在の区間を確定する。短すぎる場合は破棄して ok=false を返す。
func (s *Segmenter) finish() (Event, bool) {
	audio := s.current
	start := s.segStart
	speech := s.speechSamp
	trailing := int64(s.silentCount) * int64(s.opt.FrameSamples)

	s.inSpeech = false
	s.current = nil
	s.silentCount = 0
	s.onsetCount = 0
	s.speechSamp = 0
	s.preroll = s.preroll[:0]

	// 発話そのものの長さで足切りする (preroll や後続無音は数えない)。
	if speech < s.durToSamples(s.opt.MinSpeech) {
		return Event{}, false
	}
	// 末尾の無音は keepTailSamp を残して削る。
	if cut := trailing - s.keepTailSamp; cut > 0 && cut < int64(len(audio)) {
		audio = audio[:int64(len(audio))-cut]
	}
	return Event{
		Kind:  Final,
		Audio: audio,
		Start: s.samplesToDur(start),
		End:   s.samplesToDur(start + int64(len(audio))),
	}, true
}

// Flush は終了時に残っている発話区間を確定させる。
func (s *Segmenter) Flush() (Event, bool) {
	if !s.inSpeech {
		return Event{}, false
	}
	return s.finish()
}
