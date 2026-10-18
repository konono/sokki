package main

import (
	"os"
	"testing"
)

func TestApplyParentMuxEnvRecoversStrippedZellijIdentity(t *testing.T) {
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("ZELLIJ_PANE_ID", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	parent := parseParentMuxEnv("123 codex HOME=/tmp ZELLIJ=0 ZELLIJ_PANE_ID=241 ZELLIJ_SESSION_NAME=ope PATH=/bin")
	applyParentMuxEnv(parent)
	if got := os.Getenv("ZELLIJ_SESSION_NAME"); got != "ope" {
		t.Errorf("session = %q", got)
	}
	if got := os.Getenv("ZELLIJ_PANE_ID"); got != "241" {
		t.Errorf("pane = %q", got)
	}
	if !hasMuxIdentity() {
		t.Fatal("親プロセスから復元した identity が不完全")
	}
}

func TestApplyParentMuxEnvDoesNotMixDifferentSessions(t *testing.T) {
	t.Setenv("ZELLIJ_SESSION_NAME", "current")
	t.Setenv("ZELLIJ_PANE_ID", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	applyParentMuxEnv(map[string]string{
		"ZELLIJ_SESSION_NAME": "other", "ZELLIJ_PANE_ID": "99",
	})
	if got := os.Getenv("ZELLIJ_PANE_ID"); got != "" {
		t.Errorf("別セッションの pane を採用した: %q", got)
	}
}

func TestApplyParentMuxEnvRequiresPaneID(t *testing.T) {
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("ZELLIJ_PANE_ID", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	applyParentMuxEnv(map[string]string{"ZELLIJ_SESSION_NAME": "ope"})
	if hasMuxIdentity() {
		t.Fatal("pane ID なしでセッションを特定した")
	}
}
