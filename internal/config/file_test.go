package config

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustLoad(t *testing.T, body string) *File {
	t.Helper()
	f, err := Load(writeConfig(t, body), true)
	if err != nil {
		t.Fatalf("読み込みに失敗: %v", err)
	}
	return f
}

// --- 正常系 ---

func TestLoadAppliesAllSections(t *testing.T) {
	f := mustLoad(t, `
capture:
  type: device
  device: "BlackHole 2ch"
transcription:
  language: en
  model: /models/ggml-base.bin
output:
  directory: /tmp/out
`)
	cfg := Default()
	f.Apply(&cfg)

	if cfg.Device != "BlackHole 2ch" {
		t.Errorf("device = %q", cfg.Device)
	}
	if cfg.Language != "en" {
		t.Errorf("language = %q", cfg.Language)
	}
	if cfg.Model != "/models/ggml-base.bin" {
		t.Errorf("model = %q", cfg.Model)
	}
	if cfg.OutputDir != "/tmp/out" {
		t.Errorf("directory = %q", cfg.OutputDir)
	}
}

// 別名の Loopback デバイスを指定できること (完了条件 1)。
func TestLoadAcceptsRenamedLoopbackDevice(t *testing.T) {
	f := mustLoad(t, "capture:\n  device: \"社内会議キャプチャ\"\n")
	cfg := Default()
	f.Apply(&cfg)
	if cfg.Device != "社内会議キャプチャ" {
		t.Fatalf("device = %q", cfg.Device)
	}
}

// 日英混在向けに language: auto を指定できること。
func TestLoadAcceptsAutoLanguage(t *testing.T) {
	f := mustLoad(t, "transcription:\n  language: auto\n")
	cfg := Default()
	f.Apply(&cfg)
	if cfg.Language != "auto" {
		t.Fatalf("language = %q, want auto", cfg.Language)
	}
}

// model: "" は「既定の探索順を使う」意味で、既定値を壊さないこと。
func TestEmptyModelKeepsDefaultSearch(t *testing.T) {
	f := mustLoad(t, "transcription:\n  model: \"\"\n")
	cfg := Default()
	cfg.Model = ""
	f.Apply(&cfg)
	if cfg.Model != "" {
		t.Fatalf("model = %q, want \"\" (既定の探索順)", cfg.Model)
	}
}

// 一部だけ書いた設定は、他を既定値のまま残すこと。
func TestPartialConfigKeepsOtherDefaults(t *testing.T) {
	f := mustLoad(t, "capture:\n  device: \"BlackHole 2ch\"\n")
	cfg := Default()
	f.Apply(&cfg)

	if cfg.Device != "BlackHole 2ch" {
		t.Errorf("device = %q", cfg.Device)
	}
	if cfg.Language != Default().Language {
		t.Errorf("language が既定から変わった: %q", cfg.Language)
	}
	if cfg.OutputDir != Default().OutputDir {
		t.Errorf("directory が既定から変わった: %q", cfg.OutputDir)
	}
}

func TestEmptyFileIsNotAnError(t *testing.T) {
	f, err := Load(writeConfig(t, ""), true)
	if err != nil {
		t.Fatalf("空ファイルがエラーになった: %v", err)
	}
	cfg := Default()
	f.Apply(&cfg)
	if cfg.Device != Default().Device {
		t.Errorf("空ファイルで既定値が変わった: %q", cfg.Device)
	}
}

// --- 異常系 (完了条件 4) ---

func TestLoadRejectsUnknownKey(t *testing.T) {
	path := writeConfig(t, "capture:\n  device: \"A\"\n  bogus: 1\n")
	_, err := Load(path, true)
	if err == nil {
		t.Fatal("未知のキーがエラーにならなかった")
	}
	assertErrorMentions(t, err, path, "bogus")
}

func TestLoadRejectsUnknownTopLevelKey(t *testing.T) {
	path := writeConfig(t, "capturex:\n  device: \"A\"\n")
	if _, err := Load(path, true); err == nil {
		t.Fatal("未知のトップレベルキーがエラーにならなかった")
	}
}

func TestLoadRejectsDuplicateKey(t *testing.T) {
	path := writeConfig(t, "capture:\n  device: \"A\"\n  device: \"B\"\n")
	_, err := Load(path, true)
	if err == nil {
		t.Fatal("重複キーがエラーにならなかった")
	}
	assertErrorMentions(t, err, path, "already defined")
}

func TestLoadRejectsWrongType(t *testing.T) {
	path := writeConfig(t, "capture:\n  device: [1, 2]\n")
	_, err := Load(path, true)
	if err == nil {
		t.Fatal("型の誤りがエラーにならなかった")
	}
	assertErrorMentions(t, err, path, "")
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	path := writeConfig(t, "capture:\n\ttype: device\n") // タブ字下げ
	if _, err := Load(path, true); err == nil {
		t.Fatal("壊れた YAML がエラーにならなかった")
	}
}

func TestLoadRejectsUnsupportedCaptureType(t *testing.T) {
	path := writeConfig(t, "capture:\n  type: bogus\n")
	_, err := Load(path, true)
	if err == nil {
		t.Fatal("未対応の capture.type がエラーにならなかった")
	}
	assertErrorMentions(t, err, path, "device")
}

// capture.type: app は後続対応。分かる形で止まること。
func TestLoadRejectsCaptureTypeAppWithGuidance(t *testing.T) {
	path := writeConfig(t, "capture:\n  type: app\n")
	_, err := Load(path, true)
	if err == nil {
		t.Fatal("capture.type: app がエラーにならなかった")
	}
	msg := err.Error()
	for _, want := range []string{"未対応", "Loopback", "BlackHole"} {
		if !strings.Contains(msg, want) {
			t.Errorf("案内に %q が無い:\n%s", want, msg)
		}
	}
}

func TestLoadMissingExplicitPathIsError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), true)
	if err == nil {
		t.Fatal("--config の指定先が無いのにエラーにならなかった")
	}
	if !strings.Contains(err.Error(), "対処:") {
		t.Errorf("対処が示されていない: %v", err)
	}
}

// 自動読込では、ファイルが無くてもエラーにしない (従来動作の維持)。
func TestLoadMissingAutoPathIsNotError(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), false)
	if err != nil {
		t.Fatalf("自動読込でエラーになった: %v", err)
	}
	if f != nil {
		t.Fatalf("存在しない設定で非 nil が返った: %+v", f)
	}
	cfg := Default()
	f.Apply(&cfg) // nil レシーバでも安全なこと
	if cfg.Device != Default().Device {
		t.Error("nil 設定で既定値が変わった")
	}
}

func assertErrorMentions(t *testing.T, err error, path, detail string) {
	t.Helper()
	msg := err.Error()
	if !strings.Contains(msg, path) {
		t.Errorf("エラーにファイル名が無い:\n%s", msg)
	}
	if detail != "" && !strings.Contains(msg, detail) {
		t.Errorf("エラーに原因 %q が無い:\n%s", detail, msg)
	}
}

// --- 優先順位 (完了条件 3) ---

// テスト用に、本番と同じ名前のフラグを用意する。
func newTestFlagSet(defaults Config) (*flag.FlagSet, *Config) {
	cfg := defaults
	fs := flag.NewFlagSet("sokki", flag.ContinueOnError)
	fs.StringVar(&cfg.Device, "device", cfg.Device, "")
	fs.StringVar(&cfg.Model, "model", "", "")
	fs.StringVar(&cfg.Language, "lang", cfg.Language, "")
	fs.StringVar(&cfg.Language, "l", cfg.Language, "")
	fs.StringVar(&cfg.OutputDir, "out", cfg.OutputDir, "")
	return fs, &cfg
}

func resolveWith(t *testing.T, args []string, yaml string) Config {
	t.Helper()
	defaults := Default()
	fs, parsed := newTestFlagSet(defaults)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	var file *File
	if yaml != "" {
		file = mustLoad(t, yaml)
	}
	return Resolve(defaults, *parsed, fs, file)
}

func TestPrecedenceExplicitFlagBeatsYAML(t *testing.T) {
	cfg := resolveWith(t,
		[]string{"--device", "CLI Device"},
		"capture:\n  device: \"YAML Device\"\n")
	if cfg.Device != "CLI Device" {
		t.Fatalf("device = %q, want CLI Device (明示フラグが YAML に負けた)", cfg.Device)
	}
}

func TestPrecedenceYAMLBeatsDefaults(t *testing.T) {
	cfg := resolveWith(t, nil, "capture:\n  device: \"YAML Device\"\n")
	if cfg.Device != "YAML Device" {
		t.Fatalf("device = %q, want YAML Device", cfg.Device)
	}
}

func TestPrecedenceDefaultsWhenNothingGiven(t *testing.T) {
	cfg := resolveWith(t, nil, "")
	if cfg.Device != DefaultDevice {
		t.Fatalf("device = %q, want %q", cfg.Device, DefaultDevice)
	}
	if cfg.Language != "ja" {
		t.Fatalf("language = %q, want ja", cfg.Language)
	}
}

// フラグに「既定値と同じ値」を明示した場合も、明示として扱うこと。
func TestPrecedenceExplicitFlagEqualToDefaultStillWins(t *testing.T) {
	cfg := resolveWith(t,
		[]string{"--device", DefaultDevice},
		"capture:\n  device: \"YAML Device\"\n")
	if cfg.Device != DefaultDevice {
		t.Fatalf("device = %q, want %q (既定値と同じ明示指定が無視された)", cfg.Device, DefaultDevice)
	}
}

// 明示していないフラグは YAML に譲ること (フィールドごとに独立)。
func TestPrecedenceIsPerField(t *testing.T) {
	cfg := resolveWith(t,
		[]string{"--lang", "en"},
		"capture:\n  device: \"YAML Device\"\ntranscription:\n  language: ja\noutput:\n  directory: /tmp/yaml\n")
	if cfg.Language != "en" {
		t.Errorf("language = %q, want en (明示フラグ)", cfg.Language)
	}
	if cfg.Device != "YAML Device" {
		t.Errorf("device = %q, want YAML Device (未指定なので YAML)", cfg.Device)
	}
	if cfg.OutputDir != "/tmp/yaml" {
		t.Errorf("directory = %q, want /tmp/yaml (未指定なので YAML)", cfg.OutputDir)
	}
}

func TestShortLanguageFlagOverridesYAML(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"-l", "en"}, "en"},
		{[]string{"-l=en"}, "en"},
		{[]string{"-l", "ja"}, "ja"},
		{[]string{"--lang", "ja", "-l", "en"}, "en"},
	} {
		cfg := resolveWith(t, tt.args, "transcription:\n  language: auto\n")
		if cfg.Language != tt.want {
			t.Errorf("args=%v: language = %q, want %q", tt.args, cfg.Language, tt.want)
		}
	}
}

// YAML に無い CLI 専用の設定が失われないこと。
func TestResolveKeepsCLIOnlySettings(t *testing.T) {
	defaults := Default()
	fs, parsed := newTestFlagSet(defaults)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	parsed.Threads = 3
	parsed.ServerURL = "http://127.0.0.1:9999"
	parsed.ShowInterim = false

	cfg := Resolve(defaults, *parsed, fs, mustLoad(t, "capture:\n  device: \"X\"\n"))
	if cfg.Threads != 3 || cfg.ServerURL != "http://127.0.0.1:9999" || cfg.ShowInterim {
		t.Fatalf("CLI 専用設定が失われた: %+v", cfg)
	}
	if cfg.Device != "X" {
		t.Fatalf("device = %q", cfg.Device)
	}
}

// --- 設定ファイルの場所 ---

// 自動読込の標準パスは ~/.config/sokki/config.yaml の 1 つだけ。
func TestDefaultConfigPathIsHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config/sokki/config.yaml"); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

// 暗黙の探索先を増やさないこと。XDG_CONFIG_HOME が設定されていても
// 標準パスは ~/.config/sokki/config.yaml のまま。
func TestDefaultConfigPathIgnoresXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "/tmp/should-be-ignored")
	got, err := DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config/sokki/config.yaml"); got != want {
		t.Fatalf("path = %q, want %q (XDG_CONFIG_HOME を見てしまっている)", got, want)
	}
}

// 同梱のサンプルがそのまま読め、説明どおりの既定を示すこと。
func TestExampleConfigIsValid(t *testing.T) {
	path := filepath.Join("..", "..", "sokki.example.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sokki.example.yaml が無い: %v", err)
	}
	f, err := Load(path, true)
	if err != nil {
		t.Fatalf("同梱サンプルが読み込めない: %v", err)
	}
	cfg := Default()
	f.Apply(&cfg)
	if cfg.Device == "" {
		t.Error("サンプルから device が反映されない")
	}
}

// --- REVIEW.md [P1] 空欄を黙って既定値へ戻さない ---

// 指定された capture.device / transcription.language / output.directory の
// 空文字・空白は、キー名とファイル名を示して停止する。
func TestLoadRejectsBlankValues(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		key  string
	}{
		{"device 空文字", "capture:\n  device: \"\"\n", "capture.device"},
		{"device 空白のみ", "capture:\n  device: \"   \"\n", "capture.device"},
		{"language 空文字", "transcription:\n  language: \"\"\n", "transcription.language"},
		{"language 空白のみ", "transcription:\n  language: \"  \"\n", "transcription.language"},
		{"directory 空文字", "output:\n  directory: \"\"\n", "output.directory"},
		{"directory 空白のみ", "output:\n  directory: \" \"\n", "output.directory"},
		{"type 空文字", "capture:\n  type: \"\"\n", "capture.type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.yaml)
			_, err := Load(path, true)
			if err == nil {
				t.Fatalf("空欄が受理された (黙って既定値へ戻る)")
			}
			assertErrorMentions(t, err, path, tc.key)
		})
	}
}

// 値なし (device:) も「空」として停止する。
func TestLoadRejectsNullValue(t *testing.T) {
	path := writeConfig(t, "capture:\n  device:\n")
	if _, err := Load(path, true); err == nil {
		t.Fatal("値なしの device が受理された")
	}
}

// model: "" は計画どおり「既定の探索順」として許可する。
func TestLoadAllowsBlankModel(t *testing.T) {
	f, err := Load(writeConfig(t, "transcription:\n  model: \"\"\n"), true)
	if err != nil {
		t.Fatalf("model: \"\" が拒否された: %v", err)
	}
	cfg := Default()
	cfg.Model = ""
	f.Apply(&cfg)
	if cfg.Model != "" {
		t.Fatalf("model = %q, want \"\"", cfg.Model)
	}
}

// 未指定のフィールドは従来どおり既定値を使う (空欄とは区別する)。
func TestLoadKeepsDefaultsForOmittedFields(t *testing.T) {
	f, err := Load(writeConfig(t, "capture:\n  device: \"BlackHole 2ch\"\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	f.Apply(&cfg)
	if cfg.Device != "BlackHole 2ch" {
		t.Errorf("device = %q", cfg.Device)
	}
	if cfg.Language != Default().Language || cfg.OutputDir != Default().OutputDir {
		t.Errorf("未指定フィールドが既定から変わった: lang=%q dir=%q", cfg.Language, cfg.OutputDir)
	}
}

// --- REVIEW.md [P1] 単一文書 + YAML 文字列型の厳格化 ---

// 2 文書目を黙って無視しない (正常キー・未知キーのどちらでも停止する)。
func TestLoadRejectsMultipleDocuments(t *testing.T) {
	cases := map[string]string{
		"2 文書目が正常キー": "capture:\n  device: \"A\"\n---\ncapture:\n  device: \"B\"\n",
		"2 文書目が未知キー": "capture:\n  device: \"A\"\n---\ncapture:\n  bogus: 1\n",
		"2 文書目が空":    "capture:\n  device: \"A\"\n---\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, body)
			_, err := Load(path, true)
			if err == nil {
				t.Fatal("2 文書目が黙って無視された")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("エラーにファイル名が無い:\n%s", err)
			}
		})
	}
}

// 文字列フィールドは YAML の文字列だけを受け付ける。
func TestLoadRejectsNonStringScalars(t *testing.T) {
	cases := map[string]string{
		"数値 device":    "capture:\n  device: 123\n",
		"小数 device":    "capture:\n  device: 1.5\n",
		"真偽値 device":   "capture:\n  device: true\n",
		"数値 language":  "transcription:\n  language: 1\n",
		"数値 directory": "output:\n  directory: 2026\n",
		"配列 device":    "capture:\n  device: [1, 2]\n",
		"マッピング device": "capture:\n  device:\n    a: 1\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, body)
			_, err := Load(path, true)
			if err == nil {
				t.Fatal("文字列以外が受理された")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("エラーにファイル名が無い:\n%s", err)
			}
			if !strings.Contains(err.Error(), "line ") {
				t.Errorf("エラーに行番号が無い:\n%s", err)
			}
		})
	}
}

// 引用符を付けた数字はデバイス名として受け付ける。
func TestLoadAcceptsQuotedNumericDeviceName(t *testing.T) {
	f, err := Load(writeConfig(t, "capture:\n  device: \"123\"\n"), true)
	if err != nil {
		t.Fatalf("引用符付きの数字が拒否された: %v", err)
	}
	cfg := Default()
	f.Apply(&cfg)
	if cfg.Device != "123" {
		t.Fatalf("device = %q, want \"123\"", cfg.Device)
	}
}

// 引用符なしの通常のデバイス名は従来どおり受け付ける。
func TestLoadAcceptsUnquotedDeviceName(t *testing.T) {
	f, err := Load(writeConfig(t, "capture:\n  device: BlackHole 2ch\n"), true)
	if err != nil {
		t.Fatalf("引用符なしのデバイス名が拒否された: %v", err)
	}
	cfg := Default()
	f.Apply(&cfg)
	if cfg.Device != "BlackHole 2ch" {
		t.Fatalf("device = %q", cfg.Device)
	}
}

// --- REVIEW.md [P1] YAML 別名 (anchor/alias) 経由の null ---

// `model: &empty null` + `device: *empty` で null チェックをすり抜けない。
// 直接 `device: null` と同じエラーにする。
func TestLoadRejectsAliasToNull(t *testing.T) {
	cases := map[string]string{
		"model を参照":     "transcription:\n  model: &empty null\ncapture:\n  device: *empty\n",
		"同一セクション内":      "capture:\n  type: &e null\n  device: *e\n",
		"language を参照":  "transcription:\n  model: &x null\n  language: *x\n",
		"directory を参照": "transcription:\n  model: &y null\noutput:\n  directory: *y\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, body)
			f, err := Load(path, true)
			if err == nil {
				cfg := Default()
				f.Apply(&cfg)
				t.Fatalf("別名経由の null が受理された (device=%q へ黙って戻った)", cfg.Device)
			}
			if !strings.Contains(err.Error(), "空") {
				t.Errorf("直接 null と同じ「空」エラーになっていない:\n%s", err)
			}
			assertErrorMentions(t, err, path, "")
		})
	}
}

// 正常な文字列の別名は従来どおり受理する。
func TestLoadAcceptsAliasToString(t *testing.T) {
	f, err := Load(writeConfig(t,
		"transcription:\n  model: &name \"/models/ggml.bin\"\ncapture:\n  device: *name\n"), true)
	if err != nil {
		t.Fatalf("文字列の別名が拒否された: %v", err)
	}
	cfg := Default()
	f.Apply(&cfg)
	if cfg.Device != "/models/ggml.bin" || cfg.Model != "/models/ggml.bin" {
		t.Fatalf("別名が解決されていない: device=%q model=%q", cfg.Device, cfg.Model)
	}
}

// 別名経由でも文字列以外は拒否する。
func TestLoadRejectsAliasToNonString(t *testing.T) {
	for name, body := range map[string]string{
		"数値":  "transcription:\n  model: &n 123\ncapture:\n  device: *n\n",
		"真偽値": "transcription:\n  model: &b true\ncapture:\n  device: *b\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body), true); err == nil {
				t.Fatal("別名経由で文字列以外が受理された")
			}
		})
	}
}

// 別名経由の空文字も「空」として停止する。
func TestLoadRejectsAliasToBlankString(t *testing.T) {
	path := writeConfig(t,
		"transcription:\n  model: &e \"\"\ncapture:\n  device: *e\n")
	_, err := Load(path, true)
	if err == nil {
		t.Fatal("別名経由の空文字が受理された")
	}
	assertErrorMentions(t, err, path, "capture.device")
}

// --- リアルタイム表示の既定値と CLI 専用設定 ---

// REALTIME_PLAN の既定値 (暫定 1 秒 / 更新 1 秒 / 無音 0.5 秒)。
func TestRealtimeDefaults(t *testing.T) {
	d := Default()
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"InterimAfter", d.InterimAfter, 1000 * time.Millisecond},
		{"InterimInterval", d.InterimInterval, 1000 * time.Millisecond},
		{"SilenceHangover", d.SilenceHangover, 500 * time.Millisecond},
		{"MaxSegment", d.MaxSegment, 12 * time.Second},
		{"MinSpeech", d.MinSpeech, 400 * time.Millisecond},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if !d.ShowInterim {
		t.Error("暫定表示が既定で無効になっている")
	}
}

// 暫定表示の設定は CLI 専用。YAML を読んでも失われないこと。
func TestResolveKeepsInterimTimings(t *testing.T) {
	defaults := Default()
	fs, parsed := newTestFlagSet(defaults)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	parsed.InterimAfter = 500 * time.Millisecond
	parsed.InterimInterval = 250 * time.Millisecond
	parsed.SilenceHangover = 800 * time.Millisecond
	parsed.ShowInterim = false

	cfg := Resolve(defaults, *parsed, fs, mustLoad(t, "capture:\n  device: \"X\"\n"))
	if cfg.InterimAfter != 500*time.Millisecond {
		t.Errorf("InterimAfter = %v", cfg.InterimAfter)
	}
	if cfg.InterimInterval != 250*time.Millisecond {
		t.Errorf("InterimInterval = %v", cfg.InterimInterval)
	}
	if cfg.SilenceHangover != 800*time.Millisecond {
		t.Errorf("SilenceHangover = %v", cfg.SilenceHangover)
	}
	if cfg.ShowInterim {
		t.Error("ShowInterim が失われた")
	}
	if cfg.Device != "X" {
		t.Errorf("device = %q", cfg.Device)
	}
}

// --- REVIEW.md [P2] summary セクションの入口 ---

// 案内している summary.command が実際に使えること。
func TestSummaryCommandIsAccepted(t *testing.T) {
	f := mustLoad(t, "summary:\n  enabled: true\n  command: [\"codex\", \"exec\", \"--sandbox\", \"read-only\", \"-\"]\n")
	cfg := Default()
	f.Apply(&cfg)

	if !cfg.AutoSummary {
		t.Error("enabled が反映されない")
	}
	want := []string{"codex", "exec", "--sandbox", "read-only", "-"}
	if len(cfg.SummaryCommand) != len(want) {
		t.Fatalf("command = %v, want %v", cfg.SummaryCommand, want)
	}
	for i := range want {
		if cfg.SummaryCommand[i] != want[i] {
			t.Errorf("command[%d] = %q, want %q", i, cfg.SummaryCommand[i], want[i])
		}
	}
}

func TestSummarySectionAppliesAllFields(t *testing.T) {
	f := mustLoad(t, `summary:
  enabled: true
  provider: claude
  model: some-model
  min_new_lines: 5
  min_interval: 20s
  max_interval: 2m
  max_chars: 1200
`)
	cfg := Default()
	f.Apply(&cfg)

	if cfg.SummaryProvider != "claude" || cfg.SummaryModel != "some-model" {
		t.Errorf("provider/model = %q/%q", cfg.SummaryProvider, cfg.SummaryModel)
	}
	if cfg.SummaryMinLines != 5 || cfg.SummaryMaxChars != 1200 {
		t.Errorf("min_new_lines/max_chars = %d/%d", cfg.SummaryMinLines, cfg.SummaryMaxChars)
	}
	if cfg.SummaryMinWait != 20*time.Second || cfg.SummaryMaxWait != 2*time.Minute {
		t.Errorf("interval = %v/%v", cfg.SummaryMinWait, cfg.SummaryMaxWait)
	}
}

// 設定がない場合は自動要約の既定値を保つこと。
func TestSummaryEnabledByDefaultInConfig(t *testing.T) {
	f := mustLoad(t, "capture:\n  device: \"X\"\n")
	cfg := Default()
	f.Apply(&cfg)
	if !cfg.AutoSummary {
		t.Error("設定していないのに自動要約が無効")
	}
}

// 型の誤りを弾くこと。
func TestSummaryRejectsWrongTypes(t *testing.T) {
	cases := map[string]string{
		"min_new_lines が文字列": "summary:\n  min_new_lines: \"3\"\n",
		"enabled が文字列":       "summary:\n  enabled: \"true\"\n",
		"command が文字列":       "summary:\n  command: \"claude -p\"\n",
		"command の要素が数値":     "summary:\n  command: [1, 2]\n",
		"max_chars が真偽値":     "summary:\n  max_chars: true\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body), true); err == nil {
				t.Fatal("型の誤りが受理された")
			}
		})
	}
}

// 時間として読めない値を弾くこと。
func TestSummaryRejectsBadDuration(t *testing.T) {
	path := writeConfig(t, "summary:\n  min_interval: \"yesterday\"\n")
	_, err := Load(path, true)
	if err == nil {
		t.Fatal("不正な時間が受理された")
	}
	for _, want := range []string{"min_interval", "対処:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("案内に %q が無い: %v", want, err)
		}
	}
}

// 空欄・値なしを弾くこと。
func TestSummaryRejectsBlankValues(t *testing.T) {
	for name, body := range map[string]string{
		"provider 空":  "summary:\n  provider: \"\"\n",
		"provider なし": "summary:\n  provider:\n",
		"command に空":  "summary:\n  command: [\"claude\", \"\"]\n",
		"enabled なし":  "summary:\n  enabled:\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body), true); err == nil {
				t.Fatal("空欄が受理された")
			}
		})
	}
}

// 未知のキーは従来どおり弾くこと。
func TestSummaryRejectsUnknownKey(t *testing.T) {
	if _, err := Load(writeConfig(t, "summary:\n  bogus: 1\n"), true); err == nil {
		t.Fatal("未知のキーが受理された")
	}
}

// --- REVIEW.md [P1] YAML の summary が既定値で潰されないこと ---

// summary の各項目が Load → Resolve を通しても残ること。
func TestResolveKeepsSummaryFromYAML(t *testing.T) {
	defaults := Default()
	fs, parsed := newSummaryFlagSet(defaults)
	if err := fs.Parse(nil); err != nil { // フラグは何も指定しない
		t.Fatal(err)
	}
	file := mustLoad(t, `summary:
  enabled: true
  provider: claude
  model: some-model
  command: ["my-ai", "--quiet"]
  min_new_lines: 7
  min_interval: 15s
  max_interval: 90s
  max_chars: 900
`)
	got := Resolve(defaults, *parsed, fs, file)

	if !got.AutoSummary {
		t.Error("enabled が既定値で潰れた")
	}
	if got.SummaryModel != "some-model" {
		t.Errorf("model = %q", got.SummaryModel)
	}
	if len(got.SummaryCommand) != 2 || got.SummaryCommand[0] != "my-ai" {
		t.Errorf("command が潰れた: %v", got.SummaryCommand)
	}
	if got.SummaryMinLines != 7 {
		t.Errorf("min_new_lines = %d, want 7", got.SummaryMinLines)
	}
	if got.SummaryMinWait != 15*time.Second || got.SummaryMaxWait != 90*time.Second {
		t.Errorf("interval = %v / %v", got.SummaryMinWait, got.SummaryMaxWait)
	}
	if got.SummaryMaxChars != 900 {
		t.Errorf("max_chars = %d, want 900", got.SummaryMaxChars)
	}
}

// 明示したフラグは YAML より優先されること。
func TestExplicitSummaryFlagBeatsYAML(t *testing.T) {
	defaults := Default()
	fs, parsed := newSummaryFlagSet(defaults)
	if err := fs.Parse([]string{"--auto-summary=false", "--summary-provider", "cli-ai"}); err != nil {
		t.Fatal(err)
	}
	file := mustLoad(t, "summary:\n  enabled: true\n  provider: yaml-ai\n")
	got := Resolve(defaults, *parsed, fs, file)

	if got.AutoSummary {
		t.Error("明示した --auto-summary=false が効いていない")
	}
	if got.SummaryProvider != "cli-ai" {
		t.Errorf("provider = %q, want cli-ai", got.SummaryProvider)
	}
}

// 設定も指定も無ければ既定値のままであること。
func TestSummaryDefaultsWhenNothingGiven(t *testing.T) {
	defaults := Default()
	fs, parsed := newSummaryFlagSet(defaults)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	got := Resolve(defaults, *parsed, fs, nil)
	if !got.AutoSummary {
		t.Error("既定で自動要約が無効")
	}
	if got.SummaryMinLines != 8 || got.SummaryMinWait != 90*time.Second || got.SummaryMaxWait != 180*time.Second {
		t.Errorf("既定値が違う: %d / %v / %v", got.SummaryMinLines, got.SummaryMinWait, got.SummaryMaxWait)
	}
}

// newSummaryFlagSet は本番と同じ名前の自動要約フラグを足した FlagSet。
func newSummaryFlagSet(defaults Config) (*flag.FlagSet, *Config) {
	fs, cfg := newTestFlagSet(defaults)
	fs.BoolVar(&cfg.AutoSummary, "auto-summary", cfg.AutoSummary, "")
	fs.StringVar(&cfg.SummaryProvider, "summary-provider", cfg.SummaryProvider, "")
	fs.StringVar(&cfg.SummaryModel, "summary-model", "", "")
	return fs, cfg
}
