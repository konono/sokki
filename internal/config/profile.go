package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Profile is a user-owned YAML configuration, selected explicitly by name.
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

func profileDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ホームディレクトリを特定できません: %w", err)
	}
	return filepath.Join(home, ".config", "sokki", "profiles"), nil
}

// ProfilePath rejects separators and traversal before constructing a path.
func ProfilePath(name string) (string, error) {
	if !validProfileName(name) {
		return "", fmt.Errorf("profile名 %q が不正です: 英数字で始め、英数字・ハイフン・アンダースコアのみを使ってください", name)
	}
	dir, err := profileDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".yaml"), nil
}

// LoadProfile requires the named YAML. It never merges config.yaml.
func LoadProfile(name string) (*File, string, error) {
	path, err := ProfilePath(name)
	if err != nil {
		return nil, "", err
	}
	file, err := Load(path, false)
	if err != nil {
		return nil, path, err
	}
	if file == nil {
		return nil, path, fmt.Errorf("profile %q が見つかりません: %s\n対処: sokki profiles で一覧を確認し、%s.yaml を作成してください", name, path, name)
	}
	return file, path, nil
}

// ListProfiles lists valid YAML names without opening a model or audio device.
func ListProfiles() ([]Profile, string, error) {
	dir, err := profileDir()
	if err != nil {
		return nil, "", err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, dir, nil
	}
	if err != nil {
		return nil, dir, fmt.Errorf("profileディレクトリを読めません (%s): %w", dir, err)
	}
	profiles := make([]Profile, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		if validProfileName(name) {
			profiles = append(profiles, Profile{Name: name, Path: filepath.Join(dir, entry.Name())})
		}
	}
	return profiles, dir, nil
}
