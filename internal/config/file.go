package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultConfigRelPath は自動読込する設定ファイルの位置 (設定ディレクトリ基準)。
const DefaultConfigRelPath = "sokki/config.yaml"

// CaptureTypeDevice は macOS の音声入力デバイスから取り込む方式。
// 将来 Core Audio tap (`app`) を追加する拡張点として type を持たせている。
const CaptureTypeDevice = "device"

// supportedCaptureTypes は現在受け付ける capture.type。
var supportedCaptureTypes = []string{CaptureTypeDevice}

// File は設定ファイルの構造。
//
// 「指定されたか」と「空文字を指定したか」を区別するため、
// 各フィールドはポインタにしている。
type File struct {
	Capture       *CaptureSection       `yaml:"capture"`
	Transcription *TranscriptionSection `yaml:"transcription"`
	Output        *OutputSection        `yaml:"output"`
	Summary       *SummarySection       `yaml:"summary"`
	LLM           *LLMSection           `yaml:"llm"`
	Translation   *TranslationSection   `yaml:"translation"`
}

// CaptureSection は入力の設定。
type CaptureSection struct {
	Type   *strValue `yaml:"type"`
	Device *strValue `yaml:"device"`
}

// TranscriptionSection は認識の設定。
type TranscriptionSection struct {
	Language      *strValue `yaml:"language"`
	Model         *strValue `yaml:"model"`
	Backend       *strValue `yaml:"backend"`
	FinalStrategy *strValue `yaml:"final_strategy"`
	QwenPython    *strValue `yaml:"qwen_python"`
	QwenModel     *strValue `yaml:"qwen_model"`
}

// SummarySection は自動要約の設定。
type SummarySection struct {
	Enabled     *boolValue `yaml:"enabled"`
	Provider    *strValue  `yaml:"provider"`
	Model       *strValue  `yaml:"model"`
	OllamaURL   *strValue  `yaml:"ollama_url"`
	Command     []strValue `yaml:"command"`
	MinNewLines *intValue  `yaml:"min_new_lines"`
	MinInterval *strValue  `yaml:"min_interval"`
	MaxInterval *strValue  `yaml:"max_interval"`
	MaxChars    *intValue  `yaml:"max_chars"`
}

// TranslationSection configures optional live English-to-Japanese translation.
type TranslationSection struct {
	Mode *strValue `yaml:"mode"`
}

// LLMSection selects a single provider and model for enabled AI features.
type LLMSection struct {
	Provider  *strValue `yaml:"provider"`
	Model     *strValue `yaml:"model"`
	OllamaURL *strValue `yaml:"ollama_url"`
}

// OutputSection は保存の設定。
type OutputSection struct {
	Directory *strValue `yaml:"directory"`
}

// DefaultConfigPath は自動読込する設定ファイルのパスを返す。
//
// 自動読込の標準パスは ~/.config/sokki/config.yaml の 1 つだけ。
// 暗黙の探索先は増やさない (他の場所の設定は --config で明示する)。
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", DefaultConfigRelPath), nil
}

// Load は設定ファイルを厳格に読み込む。
//
// explicit が true (--config で明示指定) の場合、ファイルが無ければエラー。
// false (自動読込) の場合、ファイルが無ければ nil, nil を返す。
func Load(path string, explicit bool) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !explicit {
			return nil, nil // 設定ファイル無し = 既定値で動く
		}
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(
				"設定ファイルが見つかりません: %s\n対処: パスを確認するか、--config を外して既定値で実行してください",
				path)
		}
		return nil, fmt.Errorf("設定ファイルを開けません (%s): %w", path, err)
	}

	// 値なし (例: `device:`) は UnmarshalYAML が呼ばれず「未指定」と
	// 区別できないため、生のノードを見て先に弾く。
	if err := rejectExplicitNulls(path, raw); err != nil {
		return nil, err
	}

	var file File
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // 未知のキーをエラーにする
	if err := dec.Decode(&file); err != nil {
		if errors.Is(err, io.EOF) {
			return &File{}, nil // 空ファイルは「指定なし」として扱う
		}
		return nil, fmt.Errorf("設定ファイルの内容が不正です (%s):\n%s\n%s",
			path, indentLines(err.Error()), configHint())
	}

	// 設定は単一の YAML 文書とする。
	// 2 文書目以降を黙って無視すると、利用者が書いた設定が効かない。
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
		// 文書は 1 つだけ。正常。
	case err == nil:
		return nil, fmt.Errorf(
			"設定ファイルに複数の YAML 文書があります (%s, line %d)\n"+
				"対処: 文書の区切り (---) を削除し、設定を 1 つの文書にまとめてください\n"+
				"      (2 文書目以降は読み込まれないため、エラーとして止めています)",
			path, extra.Line)
	default:
		return nil, fmt.Errorf("設定ファイルの内容が不正です (%s):\n%s\n%s",
			path, indentLines(err.Error()), configHint())
	}

	if err := file.validate(path); err != nil {
		return nil, err
	}
	return &file, nil
}

// validate は値レベルの検証を行う。
func (f *File) validate(path string) error {
	// 指定された以上、空欄は設定誤り。黙って既定値へ戻さず停止する。
	// (model だけは空文字を「既定の探索順を使う」意味として許可する)
	if c := f.Capture; c != nil {
		if err := requireNonBlank(path, "capture.device", c.Device); err != nil {
			return err
		}
		if err := requireNonBlank(path, "capture.type", c.Type); err != nil {
			return err
		}
	}
	if tr := f.Transcription; tr != nil {
		if err := requireNonBlank(path, "transcription.language", tr.Language); err != nil {
			return err
		}
		for _, entry := range []struct {
			key   string
			value *strValue
		}{
			{"backend", tr.Backend}, {"final_strategy", tr.FinalStrategy},
			{"qwen_python", tr.QwenPython}, {"qwen_model", tr.QwenModel},
		} {
			if err := requireNonBlank(path, "transcription."+entry.key, entry.value); err != nil {
				return err
			}
		}
	}
	if o := f.Output; o != nil {
		if err := requireNonBlank(path, "output.directory", o.Directory); err != nil {
			return err
		}
	}
	if sm := f.Summary; sm != nil {
		if err := requireNonBlank(path, "summary.provider", sm.Provider); err != nil {
			return err
		}
		for i := range sm.Command {
			if strings.TrimSpace(sm.Command[i].Value) == "" {
				return blankValueError(path, fmt.Sprintf("summary.command[%d]", i))
			}
		}
		for _, d := range []struct {
			key string
			v   *strValue
		}{
			{"summary.min_interval", sm.MinInterval},
			{"summary.max_interval", sm.MaxInterval},
		} {
			if err := requireNonBlank(path, d.key, d.v); err != nil {
				return err
			}
			if raw, ok := d.v.get(); ok {
				if _, err := time.ParseDuration(strings.TrimSpace(raw)); err != nil {
					return fmt.Errorf(
						"%s を時間として読めません: %q (%s)\n対処: 30s や 2m のように指定してください",
						d.key, raw, path)
				}
			}
		}
	}
	if tr := f.Translation; tr != nil {
		if err := requireNonBlank(path, "translation.mode", tr.Mode); err != nil {
			return err
		}

	}
	if llm := f.LLM; llm != nil {
		if err := requireNonBlank(path, "llm.provider", llm.Provider); err != nil {
			return err
		}
		if err := requireNonBlank(path, "llm.ollama_url", llm.OllamaURL); err != nil {
			return err
		}
	}
	return f.validateCaptureType(path)
}

// requireNonBlank は「指定されたのに空」をエラーにする。
func requireNonBlank(path, key string, v *strValue) error {
	raw, ok := v.get()
	if !ok {
		return nil // 未指定は既定値でよい
	}
	if strings.TrimSpace(raw) != "" {
		return nil
	}
	return blankValueError(path, key)
}

// blankValueError は「指定されたのに空」の共通エラー。
func blankValueError(path, key string) error {
	return fmt.Errorf(
		"%s が空です (%s)\n"+
			"対処: 値を指定するか、この行ごと削除して既定値を使ってください\n"+
			"      (空欄のまま既定値へ戻すと、意図しないデバイスで録音する恐れがあるため停止しています)",
		key, path)
}

// validateCaptureType は capture.type の値を検証する。
func (f *File) validateCaptureType(path string) error {
	if f.Capture == nil || f.Capture.Type == nil {
		return nil
	}
	t := strings.TrimSpace(f.Capture.Type.Value)
	for _, ok := range supportedCaptureTypes {
		if t == ok {
			return nil
		}
	}
	if t == "app" {
		return fmt.Errorf(
			"capture.type: \"app\" は未対応です (%s)\n"+
				"対処: 現在は %q のみ対応しています。会議アプリの音声は Loopback または\n"+
				"      BlackHole 2ch の仮想デバイスを経由させ、その名前を capture.device に指定してください",
			path, CaptureTypeDevice)
	}
	return fmt.Errorf(
		"capture.type の値が不正です: %q (%s)\n対処: 対応している値は %s です",
		t, path, strings.Join(quoteAll(supportedCaptureTypes), " / "))
}

// Apply は設定ファイルの値を cfg に反映する (既定値の上書き)。
func (f *File) Apply(cfg *Config) {
	if f == nil {
		return
	}
	if c := f.Capture; c != nil {
		setString(c.Device, &cfg.Device)
	}
	if t := f.Transcription; t != nil {
		setString(t.Language, &cfg.Language)
		setString(t.Backend, &cfg.Backend)
		setString(t.FinalStrategy, &cfg.FinalStrategy)
		setString(t.QwenPython, &cfg.QwenPython)
		setString(t.QwenModel, &cfg.QwenModel)
		// model は空文字を「既定の探索順を使う」意味として許容するため、
		// 空でないときだけ反映する。
		setString(t.Model, &cfg.Model)
	}
	if o := f.Output; o != nil {
		setString(o.Directory, &cfg.OutputDir)
	}
	if sm := f.Summary; sm != nil {
		if v, ok := sm.Enabled.get(); ok {
			cfg.AutoSummary = v
		}
		setString(sm.Provider, &cfg.SummaryProvider)
		setString(sm.Model, &cfg.SummaryModel)
		setString(sm.OllamaURL, &cfg.OllamaURL)
		if len(sm.Command) > 0 {
			cmd := make([]string, 0, len(sm.Command))
			for i := range sm.Command {
				cmd = append(cmd, strings.TrimSpace(sm.Command[i].Value))
			}
			cfg.SummaryCommand = cmd
		}
		if v, ok := sm.MinNewLines.get(); ok {
			cfg.SummaryMinLines = v
		}
		if v, ok := sm.MaxChars.get(); ok {
			cfg.SummaryMaxChars = v
		}
		setDuration(sm.MinInterval, &cfg.SummaryMinWait)
		setDuration(sm.MaxInterval, &cfg.SummaryMaxWait)
	}
	if tr := f.Translation; tr != nil {
		setString(tr.Mode, &cfg.TranslateMode)
	}
	// Canonical llm fields override only their corresponding legacy aliases.
	// An explicit empty model resets summary.model for Claude.
	if llm := f.LLM; llm != nil {
		setString(llm.Provider, &cfg.SummaryProvider)
		setString(llm.Model, &cfg.SummaryModel)
		setString(llm.OllamaURL, &cfg.OllamaURL)
	}
}

// setDuration は時間の文字列を反映する (検証は validate 済み)。
func setDuration(src *strValue, dst *time.Duration) {
	raw, ok := src.get()
	if !ok {
		return
	}
	if d, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil {
		*dst = d
	}
}

// setString は指定された値を反映する。
// 空欄は validate で弾いているため、ここに来るのは有効な値だけ。
// (model の "" は「既定の探索順を使う」意味で、そのまま反映してよい)
func setString(src *strValue, dst *string) {
	if v, ok := src.get(); ok {
		*dst = strings.TrimSpace(v)
	}
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

func indentLines(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func configHint() string {
	return "対処: sokki.example.yaml を参考に、キー名・字下げ・値の型を確認してください\n" +
		"      (未知のキー・重複キー・型の誤りはすべてエラーになります)"
}

// nonNullKeys は「書いた以上、値が必要」なキー。
// transcription.model だけは空 (既定の探索順) を許可するため含めない。
var nonNullKeys = map[string][]string{
	"capture":       {"device", "type"},
	"transcription": {"language", "backend", "final_strategy", "qwen_python", "qwen_model"},
	"translation":   {"mode", "model", "ollama_url"},
	"output":        {"directory"},
	"summary": {"enabled", "provider", "ollama_url", "min_new_lines",
		"min_interval", "max_interval", "max_chars"},
}

// rejectExplicitNulls は `device:` のように値を書かなかったキーを弾く。
//
// yaml.v3 は null 値のとき UnmarshalYAML を呼ばないため、
// 型側では「未指定」と区別できない。生のノードで判定する。
func rejectExplicitNulls(path string, raw []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil // 構文エラーは後段の厳格デコードで報告する
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		section := root.Content[i].Value
		keys, ok := nonNullKeys[section]
		if !ok {
			continue
		}
		body := root.Content[i+1]
		if body.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(body.Content); j += 2 {
			key, value := body.Content[j].Value, resolveAlias(body.Content[j+1])
			if value == nil || value.Tag != "!!null" || !contains(keys, key) {
				continue
			}
			return blankValueError(path, section+"."+key)
		}
	}
	return nil
}

// resolveAlias は YAML の別名 (*anchor) を参照先まで辿る。
//
// 別名を辿らないと `model: &empty null` + `device: *empty` のような
// 書き方で null チェックをすり抜ける。
func resolveAlias(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode; i++ {
		if n.Alias == nil || i > 100 { // 循環参照の保険
			return nil
		}
		n = n.Alias
	}
	return n
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
