package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/webview/webview_go"
)

// Opt-in integration test against the installed daemon and a real WebKit view.
// Run under xvfb with SPECTRUNE_GUI_SMOKE=1 and access to the control socket.
func TestGUIBindingsSmoke(t *testing.T) {
	if os.Getenv("SPECTRUNE_GUI_SMOKE") != "1" {
		t.Skip("requires a display and installed daemon")
	}
	c, err := ipcDial()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.Call("Bridge.SaveDomainList", DomainListSaveRequest{Name: "audit-ui-list", Domains: []string{"example.test"}}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	defer c.Call("Bridge.DeleteDomainList", "audit-ui-list", &struct{}{})
	defer c.Call("Bridge.SetDomainRoute", DomainRouteRequest{List: "audit-ui-list"}, &struct{}{})
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w := webview.New(true)
	defer w.Destroy()
	w.SetSize(613, 700, webview.HintNone)
	bindAPI(w)
	var result string
	if err := w.Bind("auditComplete", func(s string) { result = s; w.Dispatch(func() { w.Terminate() }) }); err != nil {
		t.Fatal(err)
	}
	w.Init(`window.auditErrors=[]; window.addEventListener('error',function(e){auditErrors.push(e.message)}); window.addEventListener('unhandledrejection',function(e){auditErrors.push(String(e.reason))}); window.addEventListener('load',function(){setTimeout(function(){Promise.all([listProfiles(),getState(),getRunningVersion(),listInstalledApps()]).then(function(r){
 var originalText=document.body.innerText;
 openEdit(r[0][0], 'domains');
 setTimeout(function(){
 var row=Array.from(document.querySelectorAll('#domains-list li')).find(function(li){return li.querySelector('.app-name-wrap') && li.querySelector('.app-name-wrap').textContent==='audit-ui-list'});
 var select=row && row.querySelector('select');
 if (!select) {auditComplete(JSON.stringify({errors:['Domain route selector missing']}));return;}
 select.value=r[0][0];select.dispatchEvent(new Event('change'));
 setTimeout(function(){getDomainRoutes().then(function(routes){auditComplete(JSON.stringify({profiles:r[0],state:r[1],version:r[2],apps:r[3].length,errors:auditErrors,text:originalText,routeApplied:routes['audit-ui-list']===r[0][0]}));}).catch(function(e){auditComplete(JSON.stringify({errors:[String(e)]}));});},1000);
 },1000);
 }).catch(function(e){auditComplete(JSON.stringify({errors:[String(e)]}));});},2500);});`)

	p := filepath.Join(t.TempDir(), "ui.html")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(webUIHTML, "%%VERSION%%", appVersion)), 0600); err != nil {
		t.Fatal(err)
	}
	w.Navigate("file://" + p)
	timer := time.AfterFunc(20*time.Second, func() { w.Dispatch(func() { w.Terminate() }) })
	defer timer.Stop()
	w.Run()
	if result == "" {
		t.Fatal("GUI did not complete RPC round trip within 20 seconds")
	}
	var r struct {
		RouteApplied bool
		Profiles     []string
		Version      string
		Apps         int
		Errors       []string
		Text         string
	}
	if err := json.Unmarshal([]byte(result), &r); err != nil {
		t.Fatal(err)
	}
	t.Logf("version=%s profiles=%v apps=%d text=%s", r.Version, r.Profiles, r.Apps, r.Text)
	if len(r.Errors) > 0 {
		t.Fatalf("JavaScript errors: %v", r.Errors)
	}
	if !r.RouteApplied {
		t.Error("UI route selection did not persist through RPC")
	}
	for _, n := range r.Profiles {
		if !strings.Contains(r.Text, n) {
			t.Errorf("saved profile %q missing from rendered UI", n)
		}
	}
}
