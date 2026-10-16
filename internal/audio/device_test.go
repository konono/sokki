package audio

import (
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var sample = []Device{
	{0, "vMic"},
	{1, "MacBook Proのマイク"},
	{2, "Microsoft Teams Audio"},
	{3, "C505 HD Webcam"},
	{4, "‎山下祐生のiPhoneのマイク"},
	{5, "Jabra Elite 10"},
	{6, "Conference Output"},
}

func TestMatchDeviceExact(t *testing.T) {
	d, err := MatchDevice(sample, "Conference Output")
	if err != nil {
		t.Fatal(err)
	}
	if d.Index != 6 {
		t.Fatalf("index = %d, want 6", d.Index)
	}
}

func TestMatchDeviceIsCaseInsensitive(t *testing.T) {
	d, err := MatchDevice(sample, "conference output")
	if err != nil || d.Index != 6 {
		t.Fatalf("d = %+v, err = %v", d, err)
	}
}

func TestMatchDeviceIgnoresInvisibleControlChars(t *testing.T) {
	// 一覧側に U+200E が含まれていても名前で引ける。
	d, err := MatchDevice(sample, "山下祐生のiPhoneのマイク")
	if err != nil || d.Index != 4 {
		t.Fatalf("d = %+v, err = %v", d, err)
	}
}

func TestMatchDevicePartial(t *testing.T) {
	d, err := MatchDevice(sample, "Jabra")
	if err != nil || d.Index != 5 {
		t.Fatalf("d = %+v, err = %v", d, err)
	}
}

func TestMatchDeviceAmbiguousIsError(t *testing.T) {
	_, err := MatchDevice(sample, "マイク")
	if err == nil {
		t.Fatal("曖昧な指定がエラーにならなかった")
	}
	if !strings.Contains(err.Error(), "複数の候補") {
		t.Fatalf("エラー文面が不親切: %v", err)
	}
}

func TestMatchDeviceMissingListsAlternatives(t *testing.T) {
	_, err := MatchDevice(sample, "Conference Input")
	if err == nil {
		t.Fatal("存在しないデバイスがエラーにならなかった")
	}
	msg := err.Error()
	// 原因と対処、そして代替候補が示されること (PLAN.md 完了条件 4)。
	for _, want := range []string{"見つかりません", "対処:", "Conference Output", "自動で切り替えません"} {
		if !strings.Contains(msg, want) {
			t.Errorf("エラー文面に %q がない:\n%s", want, msg)
		}
	}
}

func TestParseDeviceListFromFFmpegOutput(t *testing.T) {
	// ListDevices のパース部と同じ経路を通す実出力サンプル。
	raw := `[AVFoundation indev @ 0x123] AVFoundation video devices:
[AVFoundation indev @ 0x123] [0] C505 HD Webcam
[AVFoundation indev @ 0x123] [1] MacBook Proカメラ
[AVFoundation indev @ 0x123] AVFoundation audio devices:
[AVFoundation indev @ 0x123] [0] vMic
[AVFoundation indev @ 0x123] [1] MacBook Proのマイク
[AVFoundation indev @ 0x123] [6] Conference Output
[in#0 @ 0x456] Error opening input: Input/output error`

	devices := parseDeviceList(raw)
	if len(devices) != 3 {
		t.Fatalf("解析結果 = %+v, want 3 件", devices)
	}
	if devices[2].Index != 6 || devices[2].Name != "Conference Output" {
		t.Fatalf("末尾の要素が不正: %+v", devices[2])
	}
	// 動画デバイスを拾っていないこと。
	for _, d := range devices {
		if strings.Contains(d.Name, "Webcam") || strings.Contains(d.Name, "カメラ") {
			t.Fatalf("動画デバイスが混入: %+v", d)
		}
	}
}

// デバイス不在の案内が Loopback 固有でないこと (BlackHole 利用者も救う)。
// CONFIG_PLAN 完了条件 2。
func TestMissingDeviceGuidanceIsVendorNeutral(t *testing.T) {
	_, err := MatchDevice(sample, "BlackHole 2ch")
	if err == nil {
		t.Fatal("存在しないデバイスがエラーにならなかった")
	}
	msg := err.Error()
	// Loopback / BlackHole の双方に通じる案内であること。
	for _, want := range []string{"Loopback", "BlackHole", "sokki list", "capture.device", "--device"} {
		if !strings.Contains(msg, want) {
			t.Errorf("案内に %q が無い:\n%s", want, msg)
		}
	}
	// 「Loopback で Conference Output を作れ」と決め打ちしないこと。
	if strings.Contains(msg, "Loopback で 'Conference Output' を作成") {
		t.Errorf("Loopback 固有の決め打ち案内が残っている:\n%s", msg)
	}
}

// BlackHole が一覧にあれば名前で引けること。
func TestMatchDeviceFindsBlackHole(t *testing.T) {
	devices := append(append([]Device{}, sample...), Device{7, "BlackHole 2ch"})
	d, err := MatchDevice(devices, "BlackHole 2ch")
	if err != nil {
		t.Fatal(err)
	}
	if d.Index != 7 {
		t.Fatalf("index = %d, want 7", d.Index)
	}
}

// --- sokki list の表示 ---

func TestFormatDeviceListQuotesNamesForYAML(t *testing.T) {
	out := FormatDeviceList(sample)
	// 表示された名前をそのままコピーすれば、同じデバイスに解決できること。
	// (U+200E などの不可視文字を \u200e と展開してしまうと貼り付けても一致しない)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `"`) || !strings.HasSuffix(line, `"`) {
			continue
		}
		name := strings.Trim(line, `"`)
		if strings.Contains(name, `\u`) {
			t.Errorf("不可視文字がエスケープされ、貼り付けても一致しない: %s", line)
			continue
		}
		if _, err := MatchDevice(sample, name); err != nil {
			t.Errorf("表示された名前 %q が解決できない: %v", name, err)
		}
	}
	// 設定ファイルへの書き方を案内すること。
	for _, want := range []string{"capture:", "device:", "sokki --device", "capture.device"} {
		if !strings.Contains(out, want) {
			t.Errorf("案内に %q が無い:\n%s", want, out)
		}
	}
}

// 設定例には会議用の仮想デバイスを選ぶこと。
func TestSuggestDevicePrefersVirtualDevice(t *testing.T) {
	d, ok := SuggestDevice(sample)
	if !ok {
		t.Fatal("候補が返らない")
	}
	if d.Name != "Conference Output" {
		t.Fatalf("suggested = %q, want Conference Output", d.Name)
	}
}

func TestSuggestDevicePrefersBlackHole(t *testing.T) {
	devices := []Device{
		{0, "MacBook Proのマイク"},
		{1, "BlackHole 2ch"},
	}
	d, ok := SuggestDevice(devices)
	if !ok || d.Name != "BlackHole 2ch" {
		t.Fatalf("suggested = %+v, want BlackHole 2ch", d)
	}
}

func TestSuggestDeviceFallsBackToFirst(t *testing.T) {
	devices := []Device{{0, "MacBook Proのマイク"}, {1, "C505 HD Webcam"}}
	d, ok := SuggestDevice(devices)
	if !ok || d.Name != "MacBook Proのマイク" {
		t.Fatalf("suggested = %+v, want 先頭", d)
	}
}

func TestSuggestDeviceEmpty(t *testing.T) {
	if _, ok := SuggestDevice(nil); ok {
		t.Fatal("空の一覧で候補が返った")
	}
}

// デバイスが 1 つも無い場合はマイク権限の案内を出すこと。
func TestFormatDeviceListEmptyExplainsPermission(t *testing.T) {
	out := FormatDeviceList(nil)
	for _, want := range []string{"見つかりませんでした", "マイク", "対処:"} {
		if !strings.Contains(out, want) {
			t.Errorf("案内に %q が無い:\n%s", want, out)
		}
	}
}

// --- REVIEW.md [P1] 引用の分離 (YAML 用 / shell 用) ---

// 特殊文字を含むデバイス名の一覧。
var trickyNames = []string{
	"BlackHole $HOME",
	"Dev `id -u`",
	"Dev $(id -u)",
	`Quote"Inside`,
	`Back\slash`,
	"Single'Quote",
	"Semi; rm -rf /tmp/x",
	"Space  Double",
	"普通のデバイス",
	"‎不可視文字つき", // 先頭に U+200E
}

// shell 用の引用は、実際にシェルへ渡しても元の名前のままであること。
// (二重引用符を流用すると $HOME 展開やコマンド置換が起きる)
func TestQuoteShellRoundTripsThroughShell(t *testing.T) {
	for _, name := range trickyNames {
		t.Run(name, func(t *testing.T) {
			quoted := quoteShell(name)
			// 表示されたとおりの引数をシェルに通して受け取り直す。
			out, err := exec.Command("sh", "-c", "printf %s "+quoted).Output()
			if err != nil {
				t.Fatalf("シェル実行に失敗: %v (quoted=%s)", err, quoted)
			}
			if got, want := string(out), cleanDeviceName(name); got != want {
				t.Errorf("シェル経由で名前が変わった\n  表示: %s\n  受信: %q\n  期待: %q",
					quoted, got, want)
			}
		})
	}
}

// YAML 用の引用は、YAML として読み戻すと元の名前になること。
func TestQuoteYAMLRoundTripsThroughYAML(t *testing.T) {
	for _, name := range trickyNames {
		t.Run(name, func(t *testing.T) {
			doc := "device: " + quoteYAML(name) + "\n"
			var parsed struct {
				Device string `yaml:"device"`
			}
			if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
				t.Fatalf("YAML として読めない: %v\n%s", err, doc)
			}
			if want := cleanDeviceName(name); parsed.Device != want {
				t.Errorf("YAML 経由で名前が変わった\n  表示: %s\n  受信: %q\n  期待: %q",
					doc, parsed.Device, want)
			}
		})
	}
}

// 読み戻した名前で実際にデバイスを解決できること (往復の総仕上げ)。
func TestQuotedNamesResolveBackToDevice(t *testing.T) {
	for i, name := range trickyNames {
		devices := []Device{{i, name}}

		// shell 経由
		out, err := exec.Command("sh", "-c", "printf %s "+quoteShell(name)).Output()
		if err != nil {
			t.Fatalf("シェル実行に失敗: %v", err)
		}
		if _, err := MatchDevice(devices, string(out)); err != nil {
			t.Errorf("shell 経由の名前が解決できない: %q → %v", name, err)
		}

		// YAML 経由
		var parsed struct {
			Device string `yaml:"device"`
		}
		if err := yaml.Unmarshal([]byte("device: "+quoteYAML(name)+"\n"), &parsed); err != nil {
			t.Fatalf("YAML として読めない: %v", err)
		}
		if _, err := MatchDevice(devices, parsed.Device); err != nil {
			t.Errorf("YAML 経由の名前が解決できない: %q → %v", name, err)
		}
	}
}

// 一覧出力に含まれる shell の例が、変数展開を起こさない形であること。
func TestFormatDeviceListUsesShellSafeQuotingForCommand(t *testing.T) {
	devices := []Device{{0, "BlackHole $HOME"}}
	out := FormatDeviceList(devices)

	var cmdLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "sokki --device") {
			cmdLine = strings.TrimSpace(line)
		}
	}
	if cmdLine == "" {
		t.Fatalf("コマンド例が出ていない:\n%s", out)
	}
	arg := strings.TrimPrefix(cmdLine, "sokki --device ")
	got, err := exec.Command("sh", "-c", "printf %s "+arg).Output()
	if err != nil {
		t.Fatalf("シェル実行に失敗: %v", err)
	}
	if string(got) != "BlackHole $HOME" {
		t.Errorf("コマンド例で変数展開が起きた: %q (表示: %s)", string(got), cmdLine)
	}
}

// YAML の例は YAML として読め、shell の例とは引用が分かれていること。
func TestFormatDeviceListSeparatesYAMLAndShellQuoting(t *testing.T) {
	devices := []Device{{0, "BlackHole $HOME"}}
	out := FormatDeviceList(devices)

	if !strings.Contains(out, `device: "BlackHole $HOME"`) {
		t.Errorf("YAML の例が二重引用符になっていない:\n%s", out)
	}
	if !strings.Contains(out, `sokki --device 'BlackHole $HOME'`) {
		t.Errorf("shell の例が単一引用符になっていない:\n%s", out)
	}
}

// --- REVIEW.md [P2] hintFor の案内をベンダー中立に ---

func TestHintForIsVendorNeutral(t *testing.T) {
	cases := map[string]string{
		"device 文言": "Input device not found",
		"no such":   "no such file or directory",
		"その他":       "unknown failure",
	}
	for name, stderr := range cases {
		t.Run(name, func(t *testing.T) {
			hint := hintFor(stderr)
			if !strings.Contains(hint, "sokki list") {
				t.Errorf("`sokki list` の案内が無い:\n%s", hint)
			}
			if strings.Contains(hint, "--list-devices") {
				t.Errorf("古い --list-devices の案内が残っている:\n%s", hint)
			}
			// Loopback 固定の決め打ちになっていないこと。
			if strings.Contains(hint, "Loopback で対象デバイス") {
				t.Errorf("Loopback 固定の案内が残っている:\n%s", hint)
			}
		})
	}
}

// デバイス関連のヒントは BlackHole 利用者にも通じること。
func TestHintForDeviceMentionsBothVendors(t *testing.T) {
	hint := hintFor("Input device not found")
	for _, want := range []string{"Loopback", "BlackHole"} {
		if !strings.Contains(hint, want) {
			t.Errorf("案内に %q が無い:\n%s", want, hint)
		}
	}
}

// マイク権限のヒントは従来どおり権限の案内であること。
func TestHintForPermissionUnchanged(t *testing.T) {
	hint := hintFor("Operation not permitted")
	if !strings.Contains(hint, "マイク") {
		t.Errorf("権限の案内になっていない:\n%s", hint)
	}
}
