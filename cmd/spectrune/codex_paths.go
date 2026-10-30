package main

import (
	"regexp"
	"strings"
)

var codexDaemonPath = regexp.MustCompile(`^(.+)\\\.codex\\packages\\app-server-daemon\\releases\\[^\\]+\\bin\\codex\.exe$`)
var codexNPMPath = regexp.MustCompile(`^(.+)\\appdata\\roaming\\npm\\node_modules\\@openai\\codex\\node_modules\\@openai\\codex-win32-[^\\]+\\vendor\\[^\\]+\\bin\\codex\.exe$`)

// Group the CLI and its separately installed daemon for the same Windows
// user. Keep the user root in the key so another user's Codex is not selected.
func codexAppKey(path string) string {
	path = strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	for _, pattern := range []*regexp.Regexp{codexDaemonPath, codexNPMPath} {
		if match := pattern.FindStringSubmatch(path); match != nil {
			return "codex:" + match[1]
		}
	}
	return ""
}
