package whisper

import (
	"bytes"
	"encoding/binary"
)

// EncodeWAV は 16 kHz モノラル s16le PCM を WAV バイト列にする。
func EncodeWAV(pcm []int16, sampleRate int) []byte {
	const (
		numChannels   = 1
		bitsPerSample = 16
	)
	dataLen := len(pcm) * 2
	byteRate := sampleRate * numChannels * bitsPerSample / 8
	blockAlign := numChannels * bitsPerSample / 8

	buf := bytes.NewBuffer(make([]byte, 0, 44+dataLen))
	w := func(v any) { _ = binary.Write(buf, binary.LittleEndian, v) }

	buf.WriteString("RIFF")
	w(uint32(36 + dataLen))
	buf.WriteString("WAVE")

	buf.WriteString("fmt ")
	w(uint32(16))            // fmt チャンクサイズ
	w(uint16(1))             // PCM
	w(uint16(numChannels))   //
	w(uint32(sampleRate))    //
	w(uint32(byteRate))      //
	w(uint16(blockAlign))    //
	w(uint16(bitsPerSample)) //

	buf.WriteString("data")
	w(uint32(dataLen))
	for _, s := range pcm {
		w(s)
	}
	return buf.Bytes()
}
