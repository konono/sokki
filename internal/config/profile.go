package config

import (
	"fmt"
	"sort"
)

// Profile is a named section of one user-owned configuration file.
type Profile struct {
	Name string
	Path string
}

func validProfileName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '-' || c == '_') {
			continue
		}
		return false
	}
	return true
}

func invalidProfileName(name string) error {
	return fmt.Errorf("profile名 %q が不正です: 英数字で始め、英数字・ハイフン・アンダースコアのみを使ってください", name)
}

// SelectProfile validates an explicit name and returns the selected section.
// A missing section is an error before capture or a pane is started.
func (f *File) SelectProfile(name, path string) (*File, error) {
	if !validProfileName(name) {
		return nil, invalidProfileName(name)
	}
	if f != nil {
		if selected, ok := f.Profile[name]; ok && selected != nil {
			return selected, nil
		}
	}
	return nil, fmt.Errorf("profile %q が見つかりません: %s\n対処: sokki profiles で一覧を確認し、設定ファイルの profile.%s を作成してください", name, path, name)
}

// ApplySelected overlays root and then the selected profile, field by field.
// Within each layer, canonical llm fields override legacy summary aliases.
func (f *File) ApplySelected(cfg *Config, name, path string) error {
	selected, err := f.SelectProfile(name, path)
	if err != nil {
		return err
	}
	f.Apply(cfg)
	selected.Apply(cfg)
	return nil
}

// ListProfiles reads and validates one configuration file without loading audio
// or a model. Missing default config is represented by an empty list.
func ListProfiles(path string, explicit bool) ([]Profile, error) {
	f, err := Load(path, explicit)
	if err != nil || f == nil {
		return nil, err
	}
	profiles := make([]Profile, 0, len(f.Profile))
	for name := range f.Profile {
		profiles = append(profiles, Profile{Name: name, Path: path})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles, nil
}
