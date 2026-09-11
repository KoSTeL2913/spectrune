package main

// appVersion is shown in the bottom-right corner of the GUI (webui.go's
// runGUI, webui_html.go's .version-tag) so a running instance can be
// matched to a release at a glance. Keep in sync with
// installer/spectrune.wxs's ProductVersion <?define?> by hand — there's
// no single source of truth between the Go build and the MSI, so bump
// both on every release.
const appVersion = "2.0.0.0"
