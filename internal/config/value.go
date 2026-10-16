package config

import (
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

// strValue は「指定されたか」を保持する YAML 文字列値。
//
// YAML の文字列として書かれた値だけを受け付ける。
// device: 123 のような数値・真偽値・null を文字列へ暗黙変換しない
// (設定の書き誤りを黙って通さないため)。
type strValue struct {
	Value string
}

// UnmarshalYAML は scalar の文字列タグのみを受理する。
func (s *strValue) UnmarshalYAML(node *yaml.Node) error {
	// 別名 (*anchor) は参照先の値で判定する。
	if resolved := resolveAlias(node); resolved != nil {
		node = resolved
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return fmt.Errorf("line %d: 文字列で指定してください (%sが指定されました)",
			node.Line, describeYAMLTag(node))
	}
	s.Value = node.Value
	return nil
}

// describeYAMLTag はエラー文面用に YAML の型名を日本語で返す。
func describeYAMLTag(node *yaml.Node) string {
	switch node.Kind {
	case yaml.SequenceNode:
		return "配列"
	case yaml.MappingNode:
		return "マッピング"
	}
	switch node.Tag {
	case "!!int", "!!float":
		return "数値"
	case "!!bool":
		return "真偽値"
	case "!!null":
		return "空 (値なし)"
	default:
		return node.Tag
	}
}

// get は指定されていれば値を返す。
func (s *strValue) get() (string, bool) {
	if s == nil {
		return "", false
	}
	return s.Value, true
}

// intValue は「指定されたか」を保持する YAML 整数値。
type intValue struct {
	Value int
}

// UnmarshalYAML は整数だけを受理する。
func (v *intValue) UnmarshalYAML(node *yaml.Node) error {
	if resolved := resolveAlias(node); resolved != nil {
		node = resolved
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return fmt.Errorf("line %d: 整数で指定してください (%sが指定されました)",
			node.Line, describeYAMLTag(node))
	}
	n, err := strconv.Atoi(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: 整数として読めません: %q", node.Line, node.Value)
	}
	v.Value = n
	return nil
}

func (v *intValue) get() (int, bool) {
	if v == nil {
		return 0, false
	}
	return v.Value, true
}

// boolValue は「指定されたか」を保持する YAML 真偽値。
type boolValue struct {
	Value bool
}

// UnmarshalYAML は真偽値だけを受理する。
func (v *boolValue) UnmarshalYAML(node *yaml.Node) error {
	if resolved := resolveAlias(node); resolved != nil {
		node = resolved
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
		return fmt.Errorf("line %d: true / false で指定してください (%sが指定されました)",
			node.Line, describeYAMLTag(node))
	}
	v.Value = node.Value == "true" || node.Value == "True" || node.Value == "TRUE"
	return nil
}

func (v *boolValue) get() (bool, bool) {
	if v == nil {
		return false, false
	}
	return v.Value, true
}
