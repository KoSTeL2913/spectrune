// Silent self-update, Linux side. Same trigger shape as update_windows.go
// (webui_linux.go's runGUI calls checkForUpdateOnLaunch right after the
// window opens, which hits Bridge.CheckAndInstallUpdate over IPC) but a
// different install mechanism: no msiexec here, so the daemon downloads
// the release's .deb asset and installs it itself with dpkg — it already
// runs as root (systemd), so this needs no separate elevation step,
// mirroring why Windows does this service-side too.
//
// One real difference from Windows: dpkg -i replaces the on-disk binary
// but does NOT make the *currently running* daemon process pick up the
// new code — Linux lets you overwrite a running executable's file freely,
// the old (now unlinked) inode just keeps serving the process that's
// already using it. So this also explicitly restarts spectrune.service
// after a successful install. That's a graceful restart (runDaemon's own
// SIGTERM handler tears down any active tunnel first, same as a normal
// "systemctl restart" would), but it does mean an update in the middle of
// a connected session drops the tunnel — it comes back on its own only if
// the active profile has AutoConnect set. Accepted trade-off, not worth
// solving here.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const latestReleaseURL = "https://api.github.com/repos/KoSTeL2913/spectrune/releases/latest"

const updateCheckThrottle = time.Hour

const updateStateDir = "/var/lib/spectrune"

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

// checkForUpdateOnLaunch fires the update check over IPC once per GUI
// launch — best-effort: no daemon, no network, no problem, this just
// silently does nothing.
func checkForUpdateOnLaunch() {
	client, err := ipcDial()
	if err != nil {
		return
	}
	defer client.Close()
	if err := client.Call("Bridge.CheckAndInstallUpdate", struct{}{}, &struct{}{}); err != nil {
		log.Printf("checkForUpdateOnLaunch: %v", err)
	}
}

// CheckAndInstallUpdate is fire-and-forget from the client's point of
// view — it returns immediately and does the actual check/download/
// install in the background, so a GUI launch never blocks on network I/O.
// Respects updateCheckThrottle — this is the automatic once-per-launch
// path, not a user pressing a button (that's CheckForUpdateNow below).
func (s *Service) CheckAndInstallUpdate(_ struct{}, _ *struct{}) error {
	go func() {
		if !updateCheckDue() {
			return
		}
		recordUpdateCheck()
		release, err := fetchLatestRelease()
		if err != nil {
			log.Printf("update check: %v", err)
			return
		}
		if !isNewerVersion(appVersion, release.TagName) {
			return
		}
		installRelease(release)
	}()
	return nil
}

// UpdateCheckReply is what the "Check for updates" button in
// webui_html.go's settings panel actually gets to show the user —
// CheckAndInstallUpdate above returns nothing because it's silent by
// design, but a button the user just clicked needs to say *something*
// back immediately, even though the download/install itself still
// happens in the background afterwards.
type UpdateCheckReply struct {
	Available bool
	Latest    string // e.g. "1.9.7.0", "" if Available is false
	Current   string
}

// CheckForUpdateNow bypasses updateCheckThrottle (a deliberate click
// should always actually check) and reports back synchronously whether
// a newer release exists, but still installs it in the background —
// downloading + dpkg -i + a service restart is not something to make
// the GUI wait on.
// Deliberately does NOT wait for installRelease to finish before
// replying — installing restarts spectrune.service, which is this very
// process, so a reply written after that restart fires would race the
// process's own death and might never reach the client at all. The GUI
// instead polls Bridge.Version on fresh connections (webui_html.go's
// "Check for updates" button) until it sees the new version actually
// running, which tolerates the daemon being briefly unreachable
// mid-restart in a way a single call/reply pair can't.
func (s *Service) CheckForUpdateNow(_ struct{}, reply *UpdateCheckReply) error {
	reply.Current = appVersion
	release, err := fetchLatestRelease()
	if err != nil {
		return err
	}
	recordUpdateCheck()
	latest := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	if !isNewerVersion(appVersion, release.TagName) {
		return nil
	}
	reply.Available = true
	reply.Latest = latest
	go installRelease(release)
	return nil
}

// installRelease downloads release's .deb asset and installs it with
// dpkg, then restarts the service so the new code actually takes
// effect. Best-effort: every failure just logs and returns, matching
// the fully-silent auto-check path this is shared with.
func installRelease(release *ghRelease) {
	var debURL string
	for _, a := range release.Assets {
		if strings.HasSuffix(strings.ToLower(a.Name), ".deb") {
			debURL = a.BrowserDownloadURL
			break
		}
	}
	if debURL == "" {
		log.Printf("update check: release %s has no .deb asset, skipping", release.TagName)
		return
	}

	log.Printf("update check: %s available (running %s), downloading", release.TagName, appVersion)
	path, err := downloadUpdate(debURL)
	if err != nil {
		log.Printf("update check: download failed: %v", err)
		return
	}

	log.Printf("update check: installing %s", path)
	out, err := exec.Command("dpkg", "-i", path).CombinedOutput()
	if err != nil {
		log.Printf("update check: dpkg -i failed: %v (%s)", err, string(out))
		return
	}

	log.Printf("update check: %s installed, restarting the service to pick it up", release.TagName)
	if err := exec.Command("systemctl", "restart", "--no-block", "spectrune.service").Start(); err != nil {
		log.Printf("update check: systemctl restart: %v", err)
	}
}

func lastCheckFilePath() string {
	return filepath.Join(updateStateDir, "last-update-check")
}

// updateCheckDue reports whether enough time has passed since the last
// check — missing/unreadable/garbage state is treated as "due" (best
// effort, never blocks a check on its own bookkeeping).
func updateCheckDue() bool {
	data, err := os.ReadFile(lastCheckFilePath())
	if err != nil {
		return true
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return true
	}
	return time.Since(time.Unix(sec, 0)) >= updateCheckThrottle
}

func recordUpdateCheck() {
	os.MkdirAll(updateStateDir, 0o700)
	_ = os.WriteFile(lastCheckFilePath(), []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600)
}

func fetchLatestRelease() (*ghRelease, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Spectrune-updater")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", latestReleaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: unexpected status %s", latestReleaseURL, resp.Status)
	}

	var release ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decoding release JSON: %w", err)
	}
	return &release, nil
}

// isNewerVersion compares tag (a GitHub release tag like "v1.9.5.0")
// against current (appVersion, "1.9.5.0") field by field, left to right,
// padding any missing field with 0.
func isNewerVersion(current, tag string) bool {
	tag = strings.TrimPrefix(strings.TrimSpace(tag), "v")
	cur := versionFields(current)
	latest := versionFields(tag)
	for i := 0; i < 4; i++ {
		if latest[i] != cur[i] {
			return latest[i] > cur[i]
		}
	}
	return false
}

func versionFields(v string) [4]int {
	var fields [4]int
	parts := strings.Split(v, ".")
	for i := 0; i < len(parts) && i < 4; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			continue
		}
		fields[i] = n
	}
	return fields
}

// downloadUpdate fetches url (a GitHub release asset — browser_download_url
// redirects to objects.githubusercontent.com, which net/http follows
// automatically) into /var/lib/spectrune/update.deb, overwriting any
// previous download.
func downloadUpdate(url string) (string, error) {
	if err := os.MkdirAll(updateStateDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(updateStateDir, "update.deb")

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: unexpected status %s", url, resp.Status)
	}

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}
