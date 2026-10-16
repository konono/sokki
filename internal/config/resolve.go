package config

import "flag"

// yamlBackedFlags は YAML でも指定できるフラグ名。
// ここに挙げたフラグは「明示指定されたときだけ」 YAML より優先する。
var yamlBackedFlags = map[string]func(dst, src *Config){
	"device": func(dst, src *Config) { dst.Device = src.Device },
	"model":  func(dst, src *Config) { dst.Model = src.Model },
	"lang":   func(dst, src *Config) { dst.Language = src.Language },
	"l":      func(dst, src *Config) { dst.Language = src.Language },
	"out":    func(dst, src *Config) { dst.OutputDir = src.OutputDir },

	// 自動要約も設定ファイルで指定できるので、
	// 明示したフラグのときだけ上書きする。
	// 無条件に写すと YAML の値が既定値で潰れる。
	"auto-summary":     func(dst, src *Config) { dst.AutoSummary = src.AutoSummary },
	"summary-provider": func(dst, src *Config) { dst.SummaryProvider = src.SummaryProvider },
	"summary-model":    func(dst, src *Config) { dst.SummaryModel = src.SummaryModel },
}

// Resolve は「明示した CLI フラグ > YAML > 既定値」の優先順位で設定を決める。
//
// base   : 既定値 (Default())
// parsed : フラグ解析後の値 (未指定のフラグには既定値が入っている)
// fs     : 解析済みの FlagSet (どのフラグが明示されたかの判定に使う)
// file   : 読み込んだ設定ファイル (nil 可)
func Resolve(base, parsed Config, fs *flag.FlagSet, file *File) Config {
	// 1. 既定値から始める。
	out := base

	// 2. YAML を重ねる。
	file.Apply(&out)

	// 3. 明示された CLI フラグで上書きする。
	explicit := explicitFlags(fs)
	for name, apply := range yamlBackedFlags {
		if explicit[name] {
			apply(&out, &parsed)
		}
	}

	// YAML に無い (CLI 専用の) 設定は、解析後の値をそのまま使う。
	out.ShowInterim = parsed.ShowInterim
	out.Threads = parsed.Threads
	out.ServerURL = parsed.ServerURL
	out.ServerBin = parsed.ServerBin
	out.FFmpegBin = parsed.FFmpegBin
	out.InitialPrompt = parsed.InitialPrompt
	out.PaneRole = parsed.PaneRole
	out.SessionID = parsed.SessionID
	out.MuxSession = parsed.MuxSession
	out.ShowBoard = parsed.ShowBoard
	out.EnableAI = parsed.EnableAI
	out.SilenceHangover = parsed.SilenceHangover
	out.InterimAfter = parsed.InterimAfter
	out.InterimInterval = parsed.InterimInterval
	out.MaxSegment = parsed.MaxSegment
	out.VADAbsThreshold = parsed.VADAbsThreshold

	return out
}

// explicitFlags はコマンドラインで実際に指定されたフラグ名を返す。
func explicitFlags(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	if fs == nil {
		return set
	}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}
