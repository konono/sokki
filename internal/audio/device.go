// Package audio は avfoundation からの音声取り込みを担う。
package audio

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Device は avfoundation の音声入力デバイス。
type Device struct {
	Index int
	Name  string
}

var (
	audioHeaderRE = regexp.MustCompile(`AVFoundation audio devices:`)
	videoHeaderRE = regexp.MustCompile(`AVFoundation video devices:`)
	entryRE       = regexp.MustCompile(`\[(\d+)\]\s+(.+?)\s*$`)
)

// normalizeName はデバイス名比較用の正規化を行う。
// macOS のデバイス名には U+200E などの不可視制御文字が混ざることがある。
func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.TrimSpace(b.String())
}

// ListDevices は avfoundation の音声入力デバイス一覧を返す。
func ListDevices(ctx context.Context, ffmpegBin string) ([]Device, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "")
	// -list_devices は一覧を stderr に出し、入力が無いので常に非 0 終了する。
	out, _ := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("ffmpeg のデバイス一覧取得がタイムアウトしました")
	}
	if len(out) == 0 {
		return nil, fmt.Errorf(
			"%s を実行できません\n対処: brew install ffmpeg を実行してください", ffmpegBin)
	}

	return parseDeviceList(string(out)), nil
}

// parseDeviceList は ffmpeg -list_devices の出力から音声デバイスを抜き出す。
func parseDeviceList(out string) []Device {
	var devices []Device
	inAudio := false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case videoHeaderRE.MatchString(line):
			inAudio = false
			continue
		case audioHeaderRE.MatchString(line):
			inAudio = true
			continue
		}
		if !inAudio {
			continue
		}
		// 行頭のログ接頭辞 "[AVFoundation indev @ 0x...] " を落とす。
		body := line
		if i := strings.Index(line, "] ["); i >= 0 {
			body = line[i+2:]
		}
		m := entryRE.FindStringSubmatch(body)
		if m == nil {
			if len(devices) > 0 {
				// 音声セクションの終端に達した。
				inAudio = false
			}
			continue
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		devices = append(devices, Device{Index: idx, Name: m[2]})
	}
	return devices
}

// FormatDevices は人間向けのデバイス一覧文字列を返す。
func FormatDevices(devices []Device) string {
	var b strings.Builder
	b.WriteString("利用可能な音声入力デバイス:\n")
	for _, d := range devices {
		fmt.Fprintf(&b, "  [%d] %s\n", d.Index, d.Name)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ResolveDevice は名前から avfoundation の index を解決する。
// 完全一致 → 部分一致の順で探す。見つからない場合はシステム既定マイクへ
// フォールバックせず、対処を含むエラーを返す (PLAN.md の要件)。
func ResolveDevice(ctx context.Context, ffmpegBin, name string) (Device, error) {
	devices, err := ListDevices(ctx, ffmpegBin)
	if err != nil {
		return Device{}, err
	}
	if len(devices) == 0 {
		return Device{}, fmt.Errorf(
			"音声入力デバイスを 1 つも検出できませんでした\n" +
				"対処: 実行元のターミナルアプリに「マイク」アクセス権限を与えてください\n" +
				"      システム設定 → プライバシーとセキュリティ → マイク")
	}
	return MatchDevice(devices, name)
}

// MatchDevice は取得済みの一覧から名前で 1 件に絞り込む。
func MatchDevice(devices []Device, name string) (Device, error) {
	target := normalizeName(name)

	var exact, partial []Device
	for _, d := range devices {
		n := normalizeName(d.Name)
		switch {
		case n == target:
			exact = append(exact, d)
		case target != "" && strings.Contains(n, target):
			partial = append(partial, d)
		}
	}
	for _, set := range [][]Device{exact, partial} {
		switch len(set) {
		case 1:
			return set[0], nil
		case 0:
			continue
		default:
			return Device{}, fmt.Errorf(
				"入力デバイス '%s' が複数の候補に一致しました:\n%s\n対処: --device を完全一致で指定してください",
				name, FormatDevices(set))
		}
	}
	return Device{}, fmt.Errorf(
		"入力デバイス '%s' が見つかりません\n%s\n"+
			"対処: 上の一覧にある名前を、次のいずれかで正確に指定してください\n"+
			"      - 設定ファイル: capture.device: \"<名前>\"\n"+
			"      - コマンド:     --device \"<名前>\"\n"+
			"      一覧と正確な名前は `sokki list` で確認できます\n"+
			"      会議音声を取り込む仮想デバイス (Loopback / BlackHole 2ch など) が\n"+
			"      作成・有効化されているかも確認してください\n"+
			"      (システム既定のマイクへは自動で切り替えません)",
		name, FormatDevices(devices))
}

// virtualDeviceHints は設定例に使う仮想デバイスの候補 (小文字で比較)。
var virtualDeviceHints = []string{"conference output", "blackhole", "loopback"}

// SuggestDevice は設定例に使うデバイスを 1 つ選ぶ。
// 会議音声の取り込みに使う仮想デバイスがあればそれを、無ければ先頭を返す。
func SuggestDevice(devices []Device) (Device, bool) {
	if len(devices) == 0 {
		return Device{}, false
	}
	for _, hint := range virtualDeviceHints {
		for _, d := range devices {
			if strings.Contains(normalizeName(d.Name), hint) {
				return d, true
			}
		}
	}
	return devices[0], true
}

// FormatDeviceList は `sokki list` 用の、YAML へコピーしやすい一覧を返す。
func FormatDeviceList(devices []Device) string {
	if len(devices) == 0 {
		return "音声入力デバイスが見つかりませんでした。\n\n" +
			"対処: 実行元のターミナルアプリに「マイク」アクセス権限を与えてください\n" +
			"      システム設定 → プライバシーとセキュリティ → マイク"
	}

	var b strings.Builder
	b.WriteString("音声入力デバイス一覧 (capture.device に指定できる名前):\n\n")
	for _, d := range devices {
		fmt.Fprintf(&b, "  %s\n", quoteYAML(d.Name))
	}

	suggested, _ := SuggestDevice(devices)
	fmt.Fprintf(&b, "\n設定ファイル (~/.config/sokki/config.yaml) に書く場合:\n\n")
	fmt.Fprintf(&b, "  capture:\n    device: %s\n", quoteYAML(suggested.Name))
	fmt.Fprintf(&b, "\nその実行だけ指定する場合:\n\n")
	fmt.Fprintf(&b, "  sokki --device %s\n", quoteShell(suggested.Name))
	return strings.TrimRight(b.String(), "\n")
}

// cleanDeviceName は表示用に不可視文字を取り除いた名前を返す。
//
// macOS のデバイス名には U+200E などの不可視文字が混ざることがある。
// そのまま出すと貼り付け時に見えない差分が生まれるため取り除く
// (デバイス名の照合でも不可視文字は無視するため、この名前で解決できる)。
func cleanDeviceName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// quoteYAML は YAML の二重引用符スカラーとして貼れる形にする。
func quoteYAML(name string) string {
	out := cleanDeviceName(name)
	out = strings.ReplaceAll(out, `\`, `\\`)
	out = strings.ReplaceAll(out, `"`, `\"`)
	return `"` + out + `"`
}

// quoteShell はシェルの引数として貼れる形にする。
//
// YAML 用の二重引用符を流用すると、`BlackHole $HOME` のような名前で
// 変数展開が起き、バッククォートや $() ではコマンド置換まで起きる。
// シェルでは単一引用符で囲み、名前中の ' だけを '\” で閉じ直す。
func quoteShell(name string) string {
	out := cleanDeviceName(name)
	return `'` + strings.ReplaceAll(out, `'`, `'\''`) + `'`
}
