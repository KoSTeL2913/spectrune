// Silent self-update: checked once per GUI launch (webui.go's runGUI kicks
// off Bridge.CheckAndInstallUpdate over IPC right after the window opens),
// entirely from the service side since it already runs as LocalSystem —
// msiexec's silent install of a per-machine MSI needs that elevation, and
// doing it here avoids any UAC prompt or GUI-side elevation dance. Fully
// silent by design (no dialog, no tray notification) — the same in-place
// upgrade path already proven by manual `msiexec /i ... /qn` releases this
// session, just triggered automatically instead of by hand.
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
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows/registry"
)

// latestReleaseURL points at this project's own public GitHub repo — see
// https://github.com/KoSTeL2913/spectrune/releases. Public repo, so the
// GitHub API needs no auth token here (would otherwise mean embedding a
// credential in a distributed .exe).
const latestReleaseURL = "https://api.github.com/repos/KoSTeL2913/spectrune/releases/latest"

// updateInProgress mirrors update_linux.go's flag of the same name — the
// GUI's background poll (webui_html.go's checkBackgroundUpdate, via
// Bridge.UpdateInProgress in service_windows.go) uses it to show an
// honest "installing update" overlay instead of a bare IPC connection
// error while msiexec stops and restarts this very service.
var updateInProgress atomic.Bool

// updateCheckThrottle keeps repeated GUI launches in the same sitting from
// re-hitting the GitHub API every time — unauthenticated requests are
// rate-limited per source IP (60/hour), and a user testing/reopening the
// app repeatedly (routine during development, and plausible in normal use
// too) would otherwise burn through that fast for no benefit.
const updateCheckThrottle = time.Hour

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

// checkForUpdateOnLaunch fires the update check over IPC once per GUI
// launch (see runGUI's call site) — best-effort: no service, no network, no
// problem, this just silently does nothing.
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

// CheckAndInstallUpdate is fire-and-forget from the client's point of view
// — it returns immediately and does the actual check/download/install in
// the background, so a GUI launch never blocks on network I/O here.
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
// downloading + a silent msiexec install is not something to make the
// GUI wait on.
// Deliberately does NOT wait for installRelease to finish before
// replying — the msiexec install it kicks off stops and restarts
// SpectruneService, which is this very process, so a reply written
// after that restart fires would race the process's own death and
// might never reach the client at all. The GUI instead polls
// Bridge.Version on fresh connections (webui_html.go's "Check for
// updates" button) until it sees the new version actually running,
// which tolerates the service being briefly unreachable mid-restart in
// a way a single call/reply pair can't.
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

// currentSpectruneProductCode looks up the MSI ProductCode (registry key
// name, e.g. "{CC97B33D-...}") of whatever Spectrune release is currently
// registered in Add/Remove Programs, or "" if none is found.
//
// Needed because spectrune.wxs regenerates ProductCode every release
// (required so Windows Installer treats each release as a distinct
// product for its shared-component bookkeeping — see that file's own
// top comment) and installs the new MSI's shared components
// (SpectruneExeComponent et al.) alongside the still-registered old
// product in a single RemoveExistingProducts-driven transaction.
// Confirmed live 2026-09-17: wixl's (this project's WiX-compatible
// Linux-hosted MSI compiler) handling of that transaction resolves
// SpectruneExeComponent's install Action to Null despite Installed:
// Absent / Request: Local for the new product — InstallFiles then
// silently skips copying spectrune.exe, and the InstallService custom
// action (which runs the just-installed exe with /installservice) fails
// with error 2753 ("file not marked for installation") since the file
// was never actually staged, leaving the machine with the OLD exe and
// NO registered service at all. Reproduced deterministically on a clean
// VM, not a one-off. Rather than fight wixl's internals for this
// multi-product-in-one-transaction path, currentSpectruneProductCode +
// the explicit `msiexec /x` below sidestep it entirely: uninstall the
// old product as a fully separate, already-proven-reliable transaction
// (the exact manual "clean uninstall then fresh install" process this
// project's own release notes already document as the reliable
// fallback whenever an in-place upgrade misbehaves) before installing
// the new one.
func currentSpectruneProductCode() string {
	roots := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`},
	}
	for _, r := range roots {
		k, err := registry.OpenKey(r.root, r.path, registry.READ)
		if err != nil {
			continue
		}
		names, err := k.ReadSubKeyNames(-1)
		k.Close()
		if err != nil {
			continue
		}
		for _, subName := range names {
			sk, err := registry.OpenKey(r.root, r.path+`\`+subName, registry.READ)
			if err != nil {
				continue
			}
			displayName, _, _ := sk.GetStringValue("DisplayName")
			sk.Close()
			if strings.HasPrefix(displayName, "Spectrune") {
				return subName
			}
		}
	}
	return ""
}

// installRelease downloads release's .msi asset and schedules it to be
// installed silently. Best-effort: every failure just logs and returns,
// matching the fully-silent auto-check path this is shared with.
func installRelease(release *ghRelease) {
	var msiURL string
	for _, a := range release.Assets {
		if strings.HasSuffix(strings.ToLower(a.Name), ".msi") {
			msiURL = a.BrowserDownloadURL
			break
		}
	}
	if msiURL == "" {
		log.Printf("update check: release %s has no .msi asset, skipping", release.TagName)
		return
	}

	log.Printf("update check: %s available (running %s), downloading", release.TagName, appVersion)
	updateInProgress.Store(true)
	path, err := downloadUpdate(msiURL)
	if err != nil {
		log.Printf("update check: download failed: %v", err)
		updateInProgress.Store(false)
		return
	}

	if err := scheduleInstall(release.TagName, path); err != nil {
		log.Printf("update check: scheduling install failed: %v", err)
		updateInProgress.Store(false)
		return
	}
	// Deliberately no updateInProgress.Store(false) on the success path
	// — the scheduled task below stops SpectruneService partway through
	// (see scheduleInstall's own doc), which is this very process, so
	// nothing after that point ever runs anyway. A fresh process/service
	// instance starts back at updateInProgress's zero value (false)
	// regardless, once the new release is up.
}

// scheduleInstall writes a small script that uninstalls whatever
// Spectrune release is currently registered (via currentSpectruneProductCode
// — see its own doc for why that's needed at all) and then installs
// msiPath, and runs that script via a detached, one-shot SYSTEM Scheduled
// Task rather than running both msiexec calls inline here.
//
// Can't just be two sequential exec.Command calls in this same goroutine:
// the `/x` step stops SpectruneService, which IS the process running this
// code — same class of problem webui_windows.go's closeForUpdate already
// solved for the GUI side with its own Scheduled Task relaunch. Confirmed
// live 2026-09-17 on a clean VM: an inline `/x` call completed
// successfully (verified via the log and the registry) but the
// process — and the goroutine that was supposed to run the follow-up
// `/i` — died right along with the service it had just uninstalled, so
// the machine was left with no Spectrune install at all until manually
// reinstalled. A Scheduled Task's own process is independent of the
// caller's lifetime, so it survives; SYSTEM as the run-as account avoids
// any Session-0/interactive-logon requirement (unlike closeForUpdate's
// GUI-side task, which does need a real logged-on session and so runs as
// that user instead).
func scheduleInstall(tag, msiPath string) error {
	stateDir, err := updateStateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}

	const taskName = "SpectruneSelfUpdateInstall"
	var b strings.Builder
	if oldCode := currentSpectruneProductCode(); oldCode != "" {
		fmt.Fprintf(&b, "Start-Process msiexec.exe -ArgumentList '/x','%s','/qn','/norestart' -Wait\r\n", oldCode)
	}
	fmt.Fprintf(&b, "Start-Process msiexec.exe -ArgumentList '/i','%s','/qn','/norestart' -Wait\r\n", msiPath)
	fmt.Fprintf(&b, "schtasks.exe /delete /tn '%s' /f\r\n", taskName)
	scriptPath := filepath.Join(stateDir, "self-update-install.ps1")
	if err := os.WriteFile(scriptPath, []byte(b.String()), 0o600); err != nil {
		return err
	}

	triggerTime := time.Now().Add(2 * time.Second).Format("15:04:05")
	createArgs := []string{
		"/create", "/tn", taskName,
		"/tr", fmt.Sprintf(`powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%s"`, scriptPath),
		"/sc", "once", "/st", triggerTime,
		"/ru", "SYSTEM", "/f",
	}
	if out, err := exec.Command("schtasks.exe", createArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /create: %w (%s)", err, out)
	}
	if out, err := exec.Command("schtasks.exe", "/run", "/tn", taskName).CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /run: %w (%s)", err, out)
	}
	log.Printf("update check: %s install task scheduled and started", tag)
	return nil
}

// updateStateDir mirrors runService's own placement of service.log — one
// level up from the profiles directory, i.e. ProgramData\Spectrune.
func updateStateDir() (string, error) {
	dir, err := profilesDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Dir(dir), nil
}

func lastCheckFilePath() (string, error) {
	dir, err := updateStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "last-update-check"), nil
}

// updateCheckDue reports whether enough time has passed since the last
// check — missing/unreadable/garbage state is treated as "due" (best
// effort, never blocks a check on its own bookkeeping).
func updateCheckDue() bool {
	path, err := lastCheckFilePath()
	if err != nil {
		return true
	}
	data, err := os.ReadFile(path)
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
	path, err := lastCheckFilePath()
	if err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600)
}

func fetchLatestRelease() (*ghRelease, error) {
	// 10s used to time out routinely for a real user whose path to
	// api.github.com is just slow/marginal rather than actually down —
	// confirmed live 2026-09-14 via repeated "context deadline exceeded
	// (Client.Timeout exceeded while awaiting headers)" log entries
	// spanning a long stretch of time, not a one-off blip. 25s gives a
	// slow path a fair chance without the manual "Check for updates"
	// button (the only place this delay is visibly blocking) feeling
	// broken.
	client := &http.Client{Timeout: 25 * time.Second}
	req, err := http.NewRequest(http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Spectrune-updater")

	resp, err := doWithHostsFallback(client, req)
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

// isNewerVersion compares tag (a GitHub release tag like "v1.9.1.0") against
// current (appVersion, "1.9.1.0") field by field, left to right, padding
// any missing field with 0 — mirrors how Windows Installer itself only
// looks at the numeric Major.Minor.Build.Revision fields (see
// installer/spectrune.wxs's own doc comment on that), so "newer" here means
// the same thing it means for the MSI's own upgrade detection.
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
// automatically) into ProgramData\Spectrune\update.msi, overwriting any
// previous download.
func downloadUpdate(url string) (string, error) {
	dir, err := updateStateDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "update.msi")

	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := doWithHostsFallback(client, req)
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
