package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// recoverMuxEnvFromParent fills mux variables stripped by an MCP client.
// A complete identity is required: the session alone cannot identify the
// calling pane, and guessing a pane could send commands to the wrong place.
func recoverMuxEnvFromParent() {
	if hasMuxIdentity() || os.Getenv("SOKKI_DISABLE_PARENT_MUX") == "1" {
		return
	}
	out, err := exec.Command("ps", "eww", "-p", strconv.Itoa(os.Getppid())).Output()
	if err != nil {
		return
	}
	applyParentMuxEnv(parseParentMuxEnv(string(out)))
}

func hasMuxIdentity() bool {
	return os.Getenv("ZELLIJ_SESSION_NAME") != "" && os.Getenv("ZELLIJ_PANE_ID") != "" ||
		os.Getenv("TMUX") != "" && os.Getenv("TMUX_PANE") != ""
}

func parseParentMuxEnv(output string) map[string]string {
	wanted := map[string]bool{
		"ZELLIJ_SESSION_NAME": true, "ZELLIJ_PANE_ID": true,
		"TMUX": true, "TMUX_PANE": true,
	}
	vars := make(map[string]string)
	for _, field := range strings.Fields(output) {
		key, value, ok := strings.Cut(field, "=")
		if ok && wanted[key] && value != "" {
			vars[key] = value
		}
	}
	return vars
}

func applyParentMuxEnv(parent map[string]string) {
	zSession, zPane := os.Getenv("ZELLIJ_SESSION_NAME"), os.Getenv("ZELLIJ_PANE_ID")
	tSession, tPane := os.Getenv("TMUX"), os.Getenv("TMUX_PANE")
	if zSession != "" || zPane != "" {
		if parent["ZELLIJ_SESSION_NAME"] == zSession && zSession != "" && zPane == "" {
			_ = os.Setenv("ZELLIJ_PANE_ID", parent["ZELLIJ_PANE_ID"])
		}
		return
	}
	if tSession != "" || tPane != "" {
		if parent["TMUX"] == tSession && tSession != "" && tPane == "" {
			_ = os.Setenv("TMUX_PANE", parent["TMUX_PANE"])
		}
		return
	}
	if parent["ZELLIJ_SESSION_NAME"] != "" && parent["ZELLIJ_PANE_ID"] != "" {
		_ = os.Setenv("ZELLIJ_SESSION_NAME", parent["ZELLIJ_SESSION_NAME"])
		_ = os.Setenv("ZELLIJ_PANE_ID", parent["ZELLIJ_PANE_ID"])
	} else if parent["TMUX"] != "" && parent["TMUX_PANE"] != "" {
		_ = os.Setenv("TMUX", parent["TMUX"])
		_ = os.Setenv("TMUX_PANE", parent["TMUX_PANE"])
	}
}
