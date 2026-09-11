package main

// webUIHTML is the entire GUI: one self-contained page (no external
// resources — everything inline, since it's loaded via SetHtml rather
// than served from a real origin), with plain JS switching between three
// views (list / edit-profile / apps-picker) rather than separate native
// windows. Talks to Go only through the functions bound in webui.go's
// bindAPI (window.listProfiles(), window.connect(name), etc. — each
// returns a Promise per jchv/go-webview2's Bind).
const webUIHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<style>
  :root {
    color-scheme: dark;
    /* --grad/--accent/--accent-dark are set at runtime by applyTheme() —
       see the theme picker script below. These are just the fallback
       values before JS runs. */
    --grad: linear-gradient(135deg, #7a5cff 0%, #ff3ea5 100%);
    --accent: #7a5cff;
    --accent-dark: #ff3ea5;
    --ink: #eef0f8;
    --muted: #9693ad;
    --border: #322f47;
    --card: #1a1826;
    --input-bg: #14121e;
    --chip-bg: #221f33;
    --chip-hover: #2b2843;
    --selected-bg: #2a2545;
    --page-bg-1: #241f3d;
    --page-bg-2: #2b2140;
    --page-base: #100e19;
    --err-fg: #ff9bbd;
    --err-bg: #3a1a2c;
    --err-border: #5c2540;
    --scroll-thumb: #3a3850;
    --scroll-thumb-hover: #4a4766;
    --radius: 10px;
    --shadow: 0 14px 34px -14px rgba(0, 0, 0, 0.55);
  }
  :root.light {
    color-scheme: light;
    --ink: #1c1b29;
    --muted: #6b6980;
    --border: #e3e1ed;
    --card: #ffffff;
    --input-bg: #fbfbfd;
    --chip-bg: #f4f3fa;
    --chip-hover: #eae7f7;
    --selected-bg: #f1edff;
    --page-bg-1: #eef0fb;
    --page-bg-2: #f7eef7;
    --page-base: #f4f4fa;
    --err-fg: #d1275a;
    --err-bg: #fdeef3;
    --err-border: #f7d0de;
    --scroll-thumb: #d8d6e6;
    --scroll-thumb-hover: #c3c0da;
    --shadow: 0 10px 30px -14px rgba(56, 41, 120, 0.28);
  }
  * { box-sizing: border-box; }
  html, body { height: 100%; }
  body {
    margin: 0; padding: 22px 18px; color: var(--ink);
    background: radial-gradient(circle at 15% 0%, var(--page-bg-1), transparent 55%),
                radial-gradient(circle at 100% 100%, var(--page-bg-2), transparent 45%),
                var(--page-base);
    font-family: "Segoe UI", sans-serif; font-size: 14px;
    display: flex; justify-content: center;
  }
  /* Fills the window instead of staying pinned at a fixed size — resizing
     or maximizing the WebView2 window (it's a normal resizable
     WS_OVERLAPPEDWINDOW, see webui.go) used to just leave empty bars
     around a static 560x640 card. body's flex row centers .card
     horizontally and (default align-items:stretch) stretches it to the
     full window height; .card itself is a flex column so its one
     scrollable list per view can grow into whatever's left over instead
     of a hardcoded max-height. */
  .card {
    background: var(--card); border-radius: var(--radius); box-shadow: var(--shadow);
    border: 1px solid var(--border); padding: 18px 20px 20px;
    width: 100%; max-width: 900px;
    display: flex; flex-direction: column; min-height: 0;
  }
  .topbar {
    display: flex; align-items: center; justify-content: space-between;
    margin: -4px -4px 14px; padding-bottom: 12px; border-bottom: 1px solid var(--border);
    flex-shrink: 0;
  }
  .brand { display: flex; align-items: center; gap: 8px; }
  .brand-icon { width: 26px; height: 26px; position: relative; }
  .brand-icon svg { position: absolute; inset: 0; transition: opacity .2s ease; }
  .brand-title { font-size: 15px; font-weight: 700; letter-spacing: .2px; }
  .topbar-actions { display: flex; align-items: center; gap: 8px; }
  .icon-btn { padding: 4px 8px; font-size: 14px; line-height: 1; border-radius: 6px; }
  .icon-btn.active { border-color: var(--accent); }
  h1 { font-size: 15px; margin: 0 0 12px; font-weight: 700; flex-shrink: 0; }
  .view { display: none; }
  /* Each view is itself a flex column filling the remaining card height,
     so exactly one element inside it (the profile/app list, or the
     config textarea) can be marked to grow — see .flex-grow below. */
  .view.active { display: flex; flex-direction: column; flex: 1; min-height: 0; }
  .flex-grow { flex: 1; min-height: 0; }
  .profile-list { list-style: none; margin: 0 0 14px; padding: 0; border: 1px solid var(--border); border-radius: var(--radius); overflow-y: auto; transition: background .12s ease, border-color .12s ease; }
  .profile-list.drag-over { border: 1px dashed var(--accent); background: var(--chip-bg); }
  .profile-list li { padding: 9px 12px; cursor: pointer; border-bottom: 1px solid var(--border); transition: background .12s ease; }
  .profile-list li:last-child { border-bottom: none; }
  .profile-list li:hover { background: var(--chip-bg); }
  .profile-list li.selected { background: var(--selected-bg); border-left: 3px solid var(--accent); padding-left: 9px; font-weight: 600; }
  .profile-list li.empty { color: var(--muted); cursor: default; }
  .profile-list li.empty:hover { background: transparent; }
  .profile-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
  .profile-row-info { min-width: 0; flex: 1; }
  .profile-row-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .profile-row-status { font-size: 11px; color: var(--muted); font-weight: 400; margin-top: 1px; min-height: 13px; }
  .profile-row-status.connected { color: #2ee6a0; }
  .profile-row-status.connecting { color: #e6b422; }
  .profile-row-routing { font-size: 10.5px; color: var(--muted); margin-top: 1px; }
  .profile-row-connect {
    flex-shrink: 0; padding: 4px 12px; font-size: 12px; opacity: 0;
    pointer-events: none; transition: opacity .12s ease;
  }
  .profile-row:hover .profile-row-connect { opacity: 1; pointer-events: auto; }
  .row { display: flex; gap: 8px; margin-bottom: 8px; flex-wrap: wrap; flex-shrink: 0; }
  button {
    padding: 7px 14px; border: 1px solid var(--border); border-radius: 7px; background: var(--chip-bg);
    color: var(--ink); cursor: pointer; font-size: 13px; transition: background .12s ease, filter .12s ease;
  }
  button:hover { background: var(--chip-hover); }
  button:disabled { opacity: 0.4; cursor: default; }
  button:disabled:hover { background: var(--chip-bg); }
  button.primary { background: var(--grad); color: white; border-color: transparent; font-weight: 600; }
  button.primary:hover { filter: brightness(1.12); }
  .status-row { display: flex; align-items: center; justify-content: space-between; margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--border); flex-shrink: 0; }
  .status-text { font-weight: 600; display: flex; align-items: center; gap: 7px; }
  .status-text::before { content: ''; width: 9px; height: 9px; border-radius: 50%; background: #8a879c; flex-shrink: 0; }
  .status-text.connected { color: #1fae7a; }
  .status-text.connected::before { background: #2ee6a0; box-shadow: 0 0 0 3px rgba(46,230,160,.22); }
  .status-text.connecting { color: #b8860b; }
  .status-text.connecting::before { background: #e6b422; box-shadow: 0 0 0 3px rgba(230,180,34,.22); }
  .status-text.disconnected { color: var(--muted); }
  label { display: block; margin: 10px 0 4px; font-weight: 600; font-size: 13px; }
  input[type=text], textarea {
    width: 100%; padding: 8px 10px; border: 1px solid var(--border); border-radius: 7px;
    font-family: Consolas, monospace; font-size: 13px; background: var(--input-bg); color: var(--ink);
  }
  input[type=text]:focus, textarea:focus { outline: none; border-color: var(--accent); background: var(--page-base); }
  textarea { height: 220px; resize: vertical; }
  select {
    padding: 7px 8px; border: 1px solid var(--border); border-radius: 7px;
    background: var(--input-bg); color: var(--ink); font-size: 13px; flex: 1; min-width: 0;
  }
  select:focus { outline: none; border-color: var(--accent); }
  textarea.flex-grow { height: auto; resize: none; }
  .hint { color: var(--muted); font-size: 12px; margin: 4px 0 0; flex-shrink: 0; }
  .error { color: var(--err-fg); background: var(--err-bg); border: 1px solid var(--err-border); border-radius: 7px; padding: 8px 10px; margin: 8px 0; white-space: pre-wrap; font-size: 12.5px; flex-shrink: 0; }
  .app-list { list-style: none; margin: 8px 0; padding: 0; border: 1px solid var(--border); border-radius: var(--radius); overflow-y: auto; }
  .app-list li { padding: 7px 10px; border-bottom: 1px solid var(--border); cursor: pointer; display: flex; align-items: center; gap: 9px; transition: background .12s ease; }
  .app-list li:last-child { border-bottom: none; }
  .app-list li:hover { background: var(--chip-bg); }
  .app-icon { width: 20px; height: 20px; flex-shrink: 0; object-fit: contain; }
  .app-icon:not([src]) { visibility: hidden; }
  .app-name { font-weight: 600; }
  .app-path { color: var(--muted); font-size: 11px; }
  .checkbox { width: 16px; text-align: center; font-size: 15px; color: var(--muted); }
  .checkbox.checked { color: var(--accent); }
  .app-list li > .app-name-wrap { flex-grow: 1; min-width: 0; }
  .app-launch-btn { flex-shrink: 0; font-size: 11.5px; padding: 4px 9px; border-radius: 6px; border: 1px solid var(--border); background: var(--chip-bg); color: var(--fg); cursor: pointer; }
  .app-launch-btn:hover { background: var(--accent); color: #fff; border-color: var(--accent); }
  .app-launch-btn:disabled { opacity: .5; cursor: default; }
  .backlink { cursor: pointer; color: var(--accent); margin-bottom: 8px; display: inline-block; font-weight: 600; font-size: 13px; }
  .backlink:hover { text-decoration: underline; }
  .lang-switch { display: flex; gap: 4px; }
  .lang-switch button { padding: 3px 9px; font-size: 11px; border-radius: 5px; }
  .lang-switch button.active { background: var(--grad); color: #fff; border-color: transparent; }
  ::-webkit-scrollbar { width: 9px; }
  ::-webkit-scrollbar-thumb { background: var(--scroll-thumb); border-radius: 5px; }
  ::-webkit-scrollbar-thumb:hover { background: var(--scroll-thumb-hover); }
  .included-apps { margin-top: 10px; }
  .included-apps-label { color: var(--muted); font-size: 11px; margin-bottom: 5px; }
  .included-apps-icons { display: flex; flex-wrap: wrap; gap: 6px; }
  .included-app-icon {
    width: 22px; height: 22px; border-radius: 6px; object-fit: contain;
    background: var(--chip-bg); border: 1px solid var(--border); padding: 2px;
  }
  .included-app-icon:not([src]) { visibility: hidden; }
  .included-apps-fulltunnel { color: var(--muted); font-size: 12px; }
  .theme-panel-row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .theme-panel-row + .theme-panel-row { margin-top: 9px; }
  .theme-panel-label { color: var(--muted); font-size: 11px; width: 100%; }
  .theme-mode-btn { padding: 4px 12px; font-size: 12px; }
  .theme-mode-btn.active { background: var(--grad); color: #fff; border-color: transparent; }
  .theme-swatch {
    width: 22px; height: 22px; padding: 0; border-radius: 50%; border: 2px solid transparent;
    cursor: pointer;
  }
  .theme-swatch.active { border-color: var(--ink); }
  input[type=color] {
    width: 32px; height: 24px; padding: 2px; border: 1px solid var(--border); border-radius: 6px;
    background: var(--chip-bg); cursor: pointer;
  }
  /* When a background photo is set: the photo covers the whole window,
     and the card becomes a translucent frosted panel over it instead of
     a fully opaque one — otherwise the photo would only ever be visible
     in the margins around a max-width card. color-mix()/backdrop-filter
     both need a reasonably modern Chromium, which WebView2 always is. */
  /* --bg-fit toggles between contain (whole photo visible, letterboxed —
     the letterbox shows the theme's normal --page-base color, since
     background-image only overrides the image layer, not the shorthand
     rule's color layer beneath it) and cover (fills the window, crops
     whatever doesn't fit) — user's choice, no single default suits every
     photo/window-size combination. */
  body.has-bg-image { background-size: var(--bg-fit, contain); background-position: center; background-repeat: no-repeat; background-attachment: fixed; }
  body.has-bg-image .card { background: color-mix(in srgb, var(--card) 20%, transparent); backdrop-filter: blur(8px); }
  /* The card going translucent left its own children — the list boxes —
     still fully opaque with their normal solid dark background, which
     then looked like solid black rectangles floating over the now much-
     more-visible photo (found live 2026-09-04). Same translucent
     treatment here, just without re-blurring what the card's own
     backdrop-filter already blurred. Settings is its own modal over a
     dark backdrop now, not a child of .card, so it's excluded — same
     opaque treatment confirm-box/prompt-box already get. */
  body.has-bg-image .profile-list,
  body.has-bg-image .app-list {
    background: color-mix(in srgb, var(--chip-bg) 45%, transparent);
  }
  body.has-bg-image .profile-list li:hover,
  body.has-bg-image .app-list li:hover {
    background: color-mix(in srgb, var(--chip-bg) 70%, transparent);
  }
  body.has-bg-image .profile-list li.selected {
    background: color-mix(in srgb, var(--selected-bg) 60%, transparent);
  }
  .switch { position: relative; display: inline-block; width: 38px; height: 22px; flex-shrink: 0; }
  .switch input { opacity: 0; width: 0; height: 0; position: absolute; }
  .switch-slider {
    position: absolute; inset: 0; background: var(--chip-bg); border: 1px solid var(--border);
    border-radius: 22px; cursor: pointer; transition: background .15s ease;
  }
  .switch-slider::before {
    content: ''; position: absolute; width: 16px; height: 16px; left: 2px; top: 2px;
    background: var(--card); border-radius: 50%; transition: transform .15s ease;
    box-shadow: 0 1px 2px rgba(0,0,0,.3);
  }
  .switch input:checked + .switch-slider { background: var(--grad); border-color: transparent; }
  .switch input:checked + .switch-slider::before { transform: translateX(16px); background: #fff; }
  .version-tag {
    position: fixed; right: 10px; bottom: 6px; font-size: 10px; color: var(--muted);
    opacity: 0.55; pointer-events: none; user-select: none;
  }
  .confirm-overlay {
    position: fixed; inset: 0; background: rgba(0,0,0,.55);
    display: flex; align-items: center; justify-content: center; z-index: 1000;
  }
  .confirm-box {
    background: var(--card); border: 1px solid var(--border); border-radius: 12px;
    padding: 18px 20px; max-width: 320px; box-shadow: var(--shadow);
  }
  .settings-box {
    background: var(--card); border: 1px solid var(--border); border-radius: 12px;
    padding: 18px 20px; width: 380px; max-width: 90vw; box-shadow: var(--shadow);
  }
  .confirm-message { font-size: 13.5px; line-height: 1.5; }
  button.primary.danger { background: linear-gradient(135deg, #ff5c6a 0%, #c2185b 100%); }
</style>
</head>
<body>

<div class="card">
<div class="topbar">
  <div class="brand">
    <span class="brand-icon">
      <svg id="brand-icon-active" viewBox="0 0 48 48" width="26" height="26" style="opacity:0">
        <defs><linearGradient id="hdrActive" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="48" y2="48">
          <stop offset="0" stop-color="#00e5ff"/><stop offset=".5" stop-color="#7a5cff"/><stop offset="1" stop-color="#ff3ea5"/>
        </linearGradient></defs>
        <path fill="url(#hdrActive)" d="M21.6 6h10.2L42 42h-8.4l-1.9-7H19.2l-1.9 7H6zm2.6 9.5-2.7 11h6.6z"/>
        <path fill="#ffffff" opacity=".35" d="M18.4 20.5h11.3l.7 2.6H17.7z"/>
      </svg>
      <svg id="brand-icon-inactive" viewBox="0 0 48 48" width="26" height="26" style="opacity:1">
        <defs><linearGradient id="hdrInactive" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="48" y2="48">
          <stop offset="0" stop-color="#b4babf"/><stop offset=".5" stop-color="#9aa0a6"/><stop offset="1" stop-color="#7e848a"/>
        </linearGradient></defs>
        <path fill="url(#hdrInactive)" d="M21.6 6h10.2L42 42h-8.4l-1.9-7H19.2l-1.9 7H6zm2.6 9.5-2.7 11h6.6z"/>
        <path fill="#ffffff" opacity=".28" d="M18.4 20.5h11.3l.7 2.6H17.7z"/>
      </svg>
    </span>
    <span class="brand-title">Spectrune</span>
  </div>
  <div class="topbar-actions">
    <button id="settings-toggle-btn" class="icon-btn" title="Settings">⚙️</button>
  </div>
</div>

<div id="settings-overlay" class="confirm-overlay" style="display:none">
  <div class="settings-box">
    <div class="row" style="justify-content:space-between;align-items:center;margin-bottom:4px">
      <h2 style="margin:0;font-size:15px" data-i18n="settingsTitle">Settings</h2>
      <span class="backlink" id="settings-close" style="margin:0">✕</span>
    </div>
    <div class="theme-panel-row">
      <span class="theme-panel-label" data-i18n="settingsLanguage">Language</span>
      <div class="lang-switch">
        <button id="lang-en">EN</button>
        <button id="lang-ru">RU</button>
      </div>
    </div>
    <div class="theme-panel-row" id="settings-autostart-row" style="display:none;justify-content:space-between">
      <span data-i18n="settingsAutostart" style="width:auto">Launch on Windows startup</span>
      <label class="switch">
        <input type="checkbox" id="settings-autostart-toggle">
        <span class="switch-slider"></span>
      </label>
    </div>
    <div class="theme-panel-row">
      <span class="theme-panel-label" data-i18n="themeMode">Mode</span>
      <button id="theme-mode-dark" class="theme-mode-btn" data-i18n="themeDark">Dark</button>
      <button id="theme-mode-light" class="theme-mode-btn" data-i18n="themeLight">Light</button>
    </div>
    <div class="theme-panel-row">
      <span class="theme-panel-label" data-i18n="themeAccent">Accent color</span>
      <button class="theme-swatch" data-accent="violet" style="background:linear-gradient(135deg,#7a5cff,#ff3ea5)"></button>
      <button class="theme-swatch" data-accent="ocean" style="background:linear-gradient(135deg,#00c2ff,#2f6bff)"></button>
      <button class="theme-swatch" data-accent="emerald" style="background:linear-gradient(135deg,#00e5b0,#12b76a)"></button>
      <button class="theme-swatch" data-accent="sunset" style="background:linear-gradient(135deg,#ffb347,#ff5f6d)"></button>
      <button class="theme-swatch" data-accent="crimson" style="background:linear-gradient(135deg,#ff5c8a,#c2185b)"></button>
      <button class="theme-swatch" data-accent="slate" style="background:linear-gradient(135deg,#8a8fa3,#545a70)"></button>
    </div>
    <div class="theme-panel-row">
      <span class="theme-panel-label" data-i18n="themeCustomColors">Custom colors (full RGB)</span>
      <input type="color" id="theme-custom-color1" value="#7a5cff">
      <input type="color" id="theme-custom-color2" value="#ff3ea5">
    </div>
    <div class="theme-panel-row">
      <span class="theme-panel-label" data-i18n="themeBackground">Background photo</span>
      <button id="btn-choose-bg" data-i18n="themeChooseBg">Choose photo…</button>
      <button id="btn-clear-bg" data-i18n="themeClearBg">Clear</button>
    </div>
    <div class="theme-panel-row" id="theme-bg-fit-row" style="display:none">
      <span class="theme-panel-label" data-i18n="themeBgFit">Fit</span>
      <button id="theme-bg-fit-contain" class="theme-mode-btn" data-i18n="themeBgFitContain">Whole photo</button>
      <button id="theme-bg-fit-cover" class="theme-mode-btn" data-i18n="themeBgFitCover">Fill (crop)</button>
    </div>
    <div class="theme-panel-row" style="justify-content:space-between;margin-top:4px;border-top:1px solid var(--border);padding-top:9px">
      <span id="update-status-text" class="hint" style="margin:0"></span>
      <button id="btn-check-update" data-i18n="updateCheckButton">Check for updates</button>
    </div>
  </div>
</div>

<div id="view-list" class="view active">
  <div id="list-error" class="error" style="display:none"></div>
  <ul id="profile-list" class="profile-list flex-grow"></ul>
  <div class="row">
    <button id="btn-add" data-i18n="add">Add…</button>
    <button id="btn-edit" disabled data-i18n="edit">Edit…</button>
    <button id="btn-apps" disabled data-i18n="apps">Apps…</button>
    <button id="btn-domains" disabled data-i18n="domains">Domains…</button>
    <button id="btn-delete" disabled data-i18n="delete">Delete</button>
  </div>
  <div class="status-row">
    <span id="status-text" class="status-text disconnected" data-i18n="checkingStatus">Checking status…</span>
    <button id="btn-connect" class="primary" disabled data-i18n="connect">Connect</button>
  </div>
  <div id="included-apps-row" class="included-apps" style="display:none"></div>
</div>

<div id="view-edit" class="view">
  <span class="backlink" id="edit-back" data-i18n="back">&larr; Back</span>
  <h1 id="edit-title">Add profile</h1>
  <div id="edit-error" class="error" style="display:none"></div>
  <label for="edit-name" data-i18n="name">Name</label>
  <input type="text" id="edit-name">
  <div class="row" style="margin-top:10px;align-items:center;justify-content:space-between">
    <label style="margin:0" data-i18n="autoConnect">Connect automatically on startup</label>
    <label class="switch">
      <input type="checkbox" id="edit-autoconnect">
      <span class="switch-slider"></span>
    </label>
  </div>
  <div class="row" style="margin-top:10px;align-items:center;justify-content:space-between">
    <label style="margin:0" data-i18n="hotkey">Hotkey</label>
    <div class="row" style="margin:0">
      <button id="btn-hotkey-capture" type="button"></button>
      <button id="btn-hotkey-clear" type="button" data-i18n="hotkeyClear">Clear</button>
    </div>
  </div>
  <div class="row" style="margin-top:6px;justify-content:space-between;align-items:baseline">
    <label for="edit-config" style="margin:0" data-i18n="configLabel">Configuration (wg-quick format — same as AmneziaWG's own export)</label>
    <button id="btn-import" data-i18n="importFile">Import from file…</button>
  </div>
  <textarea id="edit-config" class="flex-grow" spellcheck="false"></textarea>
  <div class="row" style="margin-top:10px">
    <button id="btn-open-apps">Apps: 0 selected…</button>
    <button id="btn-open-domains">Domains: 0 enabled…</button>
  </div>
  <div class="row">
    <button id="btn-save" class="primary" data-i18n="save">Save</button>
    <button id="btn-cancel" data-i18n="cancel">Cancel</button>
  </div>
</div>

<div id="view-apps" class="view">
  <span class="backlink" id="apps-back" data-i18n="done">&larr; Done</span>
  <h1 data-i18n="applications">Applications</h1>
  <p class="hint" data-i18n="appsHint">Select the applications whose traffic should be routed through this tunnel. Click a row to toggle it, or use "Add…" to browse for one not listed here.</p>
  <input type="text" id="apps-search" data-i18n-placeholder="searchPlaceholder" placeholder="Search installed applications…">
  <div class="row">
    <button id="btn-select-all" data-i18n="selectAll">Select all</button>
    <button id="btn-clear-all" data-i18n="clearSelection">Clear selection</button>
  </div>
  <div class="row" style="align-items:center">
    <select id="apps-preset-select"></select>
    <button id="btn-load-preset" data-i18n="presetLoad">Load</button>
    <button id="btn-save-preset" data-i18n="presetSaveAs">Save as…</button>
    <button id="btn-delete-preset" data-i18n="presetDelete">Delete</button>
  </div>
  <ul id="apps-list" class="app-list flex-grow"></ul>
  <div class="row">
    <button id="btn-browse-app" data-i18n="add">Add…</button>
  </div>
</div>

<div id="view-domains" class="view">
  <span class="backlink" id="domains-back" data-i18n="done">&larr; Done</span>
  <h1 data-i18n="domainLists">Domain lists</h1>
  <p class="hint" data-i18n="domainsHint">Toggle a list on to route every domain in it through this tunnel, regardless of which application accesses it. Edit a list's domains with "Edit".</p>
  <ul id="domains-list" class="app-list flex-grow"></ul>
  <div class="row">
    <button id="btn-new-domain-list" data-i18n="domainsNewList">+ New list…</button>
  </div>
</div>

<div id="view-domain-edit" class="view">
  <span class="backlink" id="domain-edit-back" data-i18n="back">&larr; Back</span>
  <h1 id="domain-edit-title"></h1>
  <p class="hint" data-i18n="domainEditHint">One domain per line (e.g. discord.com) — subdomains are matched automatically.</p>
  <textarea id="domain-edit-text" class="flex-grow" spellcheck="false"></textarea>
  <div class="row">
    <button id="btn-domain-edit-save" class="primary" data-i18n="save">Save</button>
    <button id="btn-domain-edit-cancel" data-i18n="cancel">Cancel</button>
  </div>
</div>

</div>

<div class="version-tag">v%%VERSION%%</div>

<div id="confirm-overlay" class="confirm-overlay" style="display:none">
  <div class="confirm-box">
    <div id="confirm-message" class="confirm-message"></div>
    <div class="row" style="margin-top:16px;justify-content:flex-end">
      <button id="confirm-cancel" data-i18n="cancel">Cancel</button>
      <button id="confirm-ok" class="primary danger" data-i18n="delete">Delete</button>
    </div>
  </div>
</div>

<div id="prompt-overlay" class="confirm-overlay" style="display:none">
  <div class="confirm-box">
    <div id="prompt-message" class="confirm-message"></div>
    <input type="text" id="prompt-input" style="margin-top:10px">
    <div class="row" style="margin-top:16px;justify-content:flex-end">
      <button id="prompt-cancel" data-i18n="cancel">Cancel</button>
      <button id="prompt-ok" class="primary" data-i18n="save">Save</button>
    </div>
  </div>
</div>

<script>
'use strict';

var I18N = {
  en: {
    add: 'Add…', edit: 'Edit…', apps: 'Apps…', domains: 'Domains…', delete: 'Delete',
    checkingStatus: 'Checking status…', connected: 'Connected: %s', disconnected: 'Disconnected',
    connecting: 'Connecting to %s…',
    connectedShort: 'Connected', connectingShort: 'Connecting…',
    routingFullTunnel: 'All traffic', routingAppsCount: 'Apps: %d',
    connect: 'Connect', disconnect: 'Disconnect',
    noProfiles: 'No profiles yet — click Add… to create one.',
    confirmDelete: 'Delete profile "%s"?',
    back: '← Back', addProfile: 'Add profile', editProfile: 'Edit profile',
    name: 'Name', configLabel: "Configuration (wg-quick format — same as AmneziaWG's own export)",
    importFile: 'Import from file…', appsSelected: 'Apps: %d selected…',
    save: 'Save', cancel: 'Cancel', nameRequired: 'A name is required.',
    done: '← Done', applications: 'Applications',
    appsHint: 'Select the applications whose traffic should be routed through this tunnel. Click a row to toggle it, or use "Add…" to browse for one not listed here.',
    searchPlaceholder: 'Search installed applications…',
    selectAll: 'Select all', clearSelection: 'Clear selection',
    routedApps: 'Routed through the tunnel:',
    noAppsFullTunnel: 'Nothing selected — all traffic goes through the VPN.',
    settingsTitle: 'Settings', settingsLanguage: 'Language',
    settingsAutostart: 'Launch on Windows startup',
    updateVersionLine: 'Version %%VERSION%%', updateCheckButton: 'Check for updates',
    updateChecking: 'Checking…', updateUpToDate: 'Up to date (%s)',
    updateAvailable: 'Updating to %s…', updateCheckFailed: 'Update check failed: %s',
    updateRestarting: 'Update installed — restarting…',
    updateStillInstalling: 'Still installing — reopen the app in a moment.',
    themeMode: 'Mode', themeDark: 'Dark', themeLight: 'Light', themeAccent: 'Accent color',
    autoConnect: 'Connect automatically on startup',
    presetLoad: 'Load', presetSaveAs: 'Save as…', presetDelete: 'Delete',
    presetNone: '— no presets —',
    presetSaveMessage: 'Name for this app preset:',
    presetNameRequired: 'A name is required.',
    presetSelectFirst: 'Select a preset first.',
    presetConfirmDelete: 'Delete preset "%s"?',
    dropNotConf: 'Not a .conf file: %s',
    dropOverwriteConfirm: 'Profile "%s" already exists — overwrite it with the dropped file?',
    dropReadError: 'Could not read the dropped file.',
    appsLaunch: 'Launch',
    hotkey: 'Hotkey', hotkeyNotSet: 'Not set', hotkeyPress: 'Press keys… (Esc to cancel)',
    hotkeyClear: 'Clear', hotkeyNeedsModifier: 'Hotkey needs at least one modifier key (Ctrl/Alt/Shift) plus a letter, digit, or F-key.',
    themeCustomColors: 'Custom colors (full RGB)', themeBackground: 'Background photo',
    themeChooseBg: 'Choose photo…', themeClearBg: 'Clear',
    themeBgFit: 'Fit', themeBgFitContain: 'Whole photo', themeBgFitCover: 'Fill (crop)',
    domainsSelected: 'Domains: %d enabled…', domainLists: 'Domain lists',
    domainsHint: 'Toggle a list on to route every domain in it through this tunnel, regardless of which application accesses it. Edit a list\'s domains with "Edit".',
    domainsNewList: '+ New list…', domainEditHint: 'One domain per line (e.g. discord.com) — subdomains are matched automatically.',
    domainListNamePrompt: 'Name for this domain list:', domainListConfirmDelete: 'Delete domain list "%s"?',
    domainListEdit: 'Edit', domainListNoLists: 'No domain lists yet — use "+ New list…" to create one.',
  },
  ru: {
    add: 'Добавить…', edit: 'Изменить…', apps: 'Приложения…', domains: 'Домены…', delete: 'Удалить',
    checkingStatus: 'Проверка статуса…', connected: 'Подключено: %s', disconnected: 'Отключено',
    connecting: 'Подключение к %s…',
    connectedShort: 'Подключено', connectingShort: 'Подключение…',
    routingFullTunnel: 'Весь трафик', routingAppsCount: 'Приложений: %d',
    connect: 'Подключить', disconnect: 'Отключить',
    noProfiles: 'Пока нет профилей — нажмите «Добавить…», чтобы создать.',
    confirmDelete: 'Удалить профиль «%s»?',
    back: '← Назад', addProfile: 'Новый профиль', editProfile: 'Изменение профиля',
    name: 'Название', configLabel: 'Конфигурация (формат wg-quick — как экспорт из AmneziaWG)',
    importFile: 'Импорт из файла…', appsSelected: 'Приложения: выбрано %d…',
    save: 'Сохранить', cancel: 'Отмена', nameRequired: 'Укажите название.',
    done: '← Готово', applications: 'Приложения',
    appsHint: 'Выберите приложения, трафик которых должен идти через этот туннель. Нажмите на строку, чтобы переключить, или используйте «Добавить…», чтобы указать путь вручную.',
    searchPlaceholder: 'Поиск установленных приложений…',
    selectAll: 'Выбрать все', clearSelection: 'Очистить выбор',
    routedApps: 'Маршрутизируются через туннель:',
    noAppsFullTunnel: 'Ничего не выбрано — весь трафик идёт через VPN.',
    settingsTitle: 'Настройки', settingsLanguage: 'Язык',
    settingsAutostart: 'Запускать при старте Windows',
    updateVersionLine: 'Версия %%VERSION%%', updateCheckButton: 'Проверить обновления',
    updateChecking: 'Проверка…', updateUpToDate: 'Актуальная версия (%s)',
    updateAvailable: 'Обновление до %s…', updateCheckFailed: 'Ошибка проверки: %s',
    updateRestarting: 'Обновление установлено — перезапуск…',
    updateStillInstalling: 'Ещё устанавливается — откройте приложение чуть позже.',
    themeMode: 'Режим', themeDark: 'Тёмная', themeLight: 'Светлая', themeAccent: 'Акцентный цвет',
    autoConnect: 'Автоподключение при запуске',
    presetLoad: 'Загрузить', presetSaveAs: 'Сохранить как…', presetDelete: 'Удалить',
    presetNone: '— нет пресетов —',
    presetSaveMessage: 'Название набора приложений:',
    presetNameRequired: 'Укажите название.',
    presetSelectFirst: 'Сначала выберите пресет.',
    presetConfirmDelete: 'Удалить пресет «%s»?',
    dropNotConf: 'Не .conf файл: %s',
    dropOverwriteConfirm: 'Профиль «%s» уже существует — перезаписать перетащенным файлом?',
    dropReadError: 'Не удалось прочитать перетащенный файл.',
    appsLaunch: 'Запустить',
    hotkey: 'Горячая клавиша', hotkeyNotSet: 'Не задано', hotkeyPress: 'Нажмите комбинацию… (Esc — отмена)',
    hotkeyClear: 'Очистить', hotkeyNeedsModifier: 'Нужен хотя бы один модификатор (Ctrl/Alt/Shift) плюс буква, цифра или F-клавиша.',
    themeCustomColors: 'Свои цвета (вся RGB-палитра)', themeBackground: 'Фоновое фото',
    themeChooseBg: 'Выбрать фото…', themeClearBg: 'Очистить',
    themeBgFit: 'Заполнение', themeBgFitContain: 'Целиком', themeBgFitCover: 'На весь экран (обрезка)',
    domainsSelected: 'Домены: включено %d…', domainLists: 'Списки доменов',
    domainsHint: 'Включите список, чтобы направить все его домены через этот туннель — независимо от того, какое приложение к ним обращается. Кнопка «Изменить» редактирует домены списка.',
    domainsNewList: '+ Новый список…', domainEditHint: 'По одному домену на строку (например, discord.com) — поддомены учитываются автоматически.',
    domainListNamePrompt: 'Название списка доменов:', domainListConfirmDelete: 'Удалить список доменов «%s»?',
    domainListEdit: 'Изменить', domainListNoLists: 'Списков доменов пока нет — нажмите «+ Новый список…», чтобы создать.',
  },
};

function t(key) {
  var dict = I18N[state.lang] || I18N.en;
  return dict[key] !== undefined ? dict[key] : (I18N.en[key] || key);
}
function tf(key, val) { return t(key).replace(/%s|%d/, val); }

// %%VERSION%% is substituted server-side (webui_linux.go/webui_windows.go's
// runGUI, over the whole HTML document text) before this script ever
// runs — same placeholder the .version-tag footer already uses. Stashed
// into a JS var, not read straight off a data-i18n element, because
// applyI18n() re-renders those from the dictionary (which still holds
// the literal, unsubstituted placeholder) on every language switch.
var appVersionStr = '%%VERSION%%';

function renderUpdateStatus() {
  $('update-status-text').textContent = tf('updateVersionLine', appVersionStr);
}

var state = {
  profiles: [],
  selected: null,
  connectBusy: false, // a connect()/disconnect() call is in flight — keep
                       // btn-connect disabled regardless of what the 3s
                       // status poll thinks, so a slow Connect can't be
                       // double-clicked into "already connected"
  connected: false,
  connectedProfile: '',
  handshakeOK: false,
  editingExisting: false,
  installedApps: [],
  includedApps: [],   // lowercase path -> path, preserved case
  appsFilter: '',
  appPresets: [],
  includedDomainLists: [], // names of domain lists enabled for this profile
  domainLists: [],         // every saved domain list's name
  editingDomainList: null, // name of the domain list open in view-domain-edit
  editHotkey: '',
  editOriginalName: null,
  lang: (function() { try { return localStorage.getItem('lang') || 'en'; } catch (e) { return 'en'; } })(),
  theme: (function() {
    var fallback = { mode: 'dark', accent: 'violet', customColor1: '#7a5cff', customColor2: '#ff3ea5', bgImage: '', bgFit: 'contain' };
    try {
      return {
        mode: localStorage.getItem('themeMode') || fallback.mode,
        accent: localStorage.getItem('themeAccent') || fallback.accent,
        customColor1: localStorage.getItem('themeCustomColor1') || fallback.customColor1,
        customColor2: localStorage.getItem('themeCustomColor2') || fallback.customColor2,
        bgImage: localStorage.getItem('themeBgImage') || fallback.bgImage,
        bgFit: localStorage.getItem('themeBgFit') || fallback.bgFit,
      };
    } catch (e) { return fallback; }
  })(),
};

function $(id) { return document.getElementById(id); }
function show(id) {
  document.querySelectorAll('.view').forEach(function(v) { v.classList.remove('active'); });
  $(id).classList.add('active');
}

// ---------- list view ----------

function renderProfileList() {
  var ul = $('profile-list');
  ul.innerHTML = '';
  if (state.profiles.length === 0) {
    var li = document.createElement('li');
    li.className = 'empty';
    li.textContent = t('noProfiles');
    ul.appendChild(li);
  }
  state.profiles.forEach(function(name) {
    var li = document.createElement('li');
    li.className = 'profile-row';
    if (name === state.selected) li.classList.add('selected');

    var info = document.createElement('div');
    info.className = 'profile-row-info';
    var nameEl = document.createElement('div');
    nameEl.className = 'profile-row-name';
    nameEl.textContent = name;
    var statusEl = document.createElement('div');
    statusEl.className = 'profile-row-status';
    var isActive = state.connected && state.connectedProfile === name;
    if (isActive) {
      // Short, unparametrized labels — not the tf('connected'/'connecting',
      // name) versions the main status bar uses, since this row already
      // shows the name in nameEl right above; reusing those templates
      // here literally printed "%s" (nothing filled it in — found live
      // 2026-09-04).
      statusEl.textContent = state.handshakeOK ? t('connectedShort') : t('connectingShort');
      statusEl.className = 'profile-row-status ' + (state.handshakeOK ? 'connected' : 'connecting');
    }
    var routingEl = document.createElement('div');
    routingEl.className = 'profile-row-routing';
    loadRoutingSummaryInto(name, routingEl);
    info.appendChild(nameEl);
    info.appendChild(statusEl);
    info.appendChild(routingEl);

    var connectBtn = document.createElement('button');
    connectBtn.className = 'profile-row-connect primary';
    connectBtn.textContent = isActive ? t('disconnect') : t('connect');
    connectBtn.onclick = function(e) {
      e.stopPropagation();
      toggleConnectRow(name);
    };

    li.appendChild(info);
    li.appendChild(connectBtn);
    li.onclick = function() { state.selected = name; renderProfileList(); updateButtons(); };
    ul.appendChild(li);
  });
  updateButtons();
  refreshIncludedAppsPreview();
}

// toggleConnectRow is the per-row hover Connect/Disconnect button —
// Disconnect if this row is the active profile, otherwise Connect to it
// (disconnecting whatever else is active first, since Bridge.Connect
// refuses to run alongside an existing connection — see service.go).
function toggleConnectRow(name) {
  var p;
  if (state.connected && state.connectedProfile === name) {
    p = disconnect();
  } else if (state.connected) {
    p = disconnect().then(function() { return connect(name); });
  } else {
    p = connect(name);
  }
  p.then(refreshStatus).catch(function(err) { showListError(err); refreshStatus(); });
}

function updateButtons() {
  var has = state.selected !== null;
  $('btn-edit').disabled = !has;
  $('btn-apps').disabled = !has;
  $('btn-domains').disabled = !has;
  $('btn-delete').disabled = !has;
  $('btn-connect').disabled = state.connectBusy || (!has && !state.connected);
  if (state.connected && state.handshakeOK) {
    $('status-text').textContent = tf('connected', state.connectedProfile);
    $('status-text').className = 'status-text connected';
    $('btn-connect').textContent = t('disconnect');
    if (!state.connectBusy) $('btn-connect').disabled = false;
  } else if (state.connected) {
    // Adapter/local bridge is up but the WireGuard handshake with the
    // peer hasn't completed (or never will, for a bad config/dead
    // server) — see StateReply.HandshakeOK's doc in service.go. Used to
    // just say "Connected" here regardless, which was actively
    // misleading: confirmed live 2026-09-04 with a profile whose tunnel
    // never actually passed traffic.
    $('status-text').textContent = tf('connecting', state.connectedProfile);
    $('status-text').className = 'status-text connecting';
    $('btn-connect').textContent = t('disconnect');
    if (!state.connectBusy) $('btn-connect').disabled = false;
  } else {
    $('status-text').textContent = t('disconnected');
    $('status-text').className = 'status-text disconnected';
    $('btn-connect').textContent = t('connect');
  }
  // Same colored-when-connected / grey-when-disconnected swap as the tray
  // icon (tray.go) and the Linux awg-gui counterpart's AppIndicator —
  // colored only once the handshake actually confirms the tunnel works.
  var reallyConnected = state.connected && state.handshakeOK;
  $('brand-icon-active').style.opacity = reallyConnected ? '1' : '0';
  $('brand-icon-inactive').style.opacity = reallyConnected ? '0' : '1';
}

function refreshProfiles() {
  listProfiles().then(function(names) {
    state.profiles = names || [];
    if (state.selected !== null && state.profiles.indexOf(state.selected) === -1) {
      state.selected = null;
    }
    renderProfileList();
  }).catch(showListError);
}

// ---------- drag-and-drop .conf import ----------
// WebView2's Chromium engine handles OS-level file drag-and-drop into the
// page automatically (same as dropping a file onto any web page's drop
// zone in a real browser) — no native Win32 code needed, just the
// standard HTML5 dragover/drop events. Dropping a .conf from Explorer/the
// desktop straight onto the profile list imports it exactly like "Import
// from file…" + Save, just skipping the file-picker dialog since the
// browser already handed over a File object.

(function() {
  var zone = $('view-list');
  var highlight = function() { $('profile-list').classList.add('drag-over'); };
  var unhighlight = function() { $('profile-list').classList.remove('drag-over'); };

  zone.addEventListener('dragover', function(e) {
    e.preventDefault();
    highlight();
  });
  zone.addEventListener('dragleave', function(e) {
    if (e.target === zone) unhighlight();
  });
  zone.addEventListener('drop', function(e) {
    e.preventDefault();
    unhighlight();
    var files = e.dataTransfer ? e.dataTransfer.files : [];
    for (var i = 0; i < files.length; i++) {
      importDroppedConfFile(files[i]);
    }
  });
})();

function importDroppedConfFile(file) {
  if (!/\.conf$/i.test(file.name)) {
    showListError(tf('dropNotConf', file.name));
    return;
  }
  var name = file.name.replace(/\.conf$/i, '');
  var reader = new FileReader();
  reader.onerror = function() { showListError(t('dropReadError')); };
  reader.onload = function() {
    var text = String(reader.result);
    var doImport = function() {
      saveProfile(name, text, [], false, '').then(function() {
        refreshProfiles();
      }).catch(showListError);
    };
    if (state.profiles.indexOf(name) !== -1) {
      showConfirm(tf('dropOverwriteConfirm', name), doImport);
    } else {
      doImport();
    }
  };
  reader.readAsText(file);
}

// routingSummaryText/loadRoutingSummaryInto back each profile row's small
// routing-mode subtitle ("All traffic" vs "Apps: N") — shares
// includedPreviewCache (declared below) with the existing under-the-
// Connect-button preview rather than fetching separately.
function routingSummaryText(apps) {
  if (!apps || apps.length === 0) return t('routingFullTunnel');
  return tf('routingAppsCount', apps.length);
}

function loadRoutingSummaryInto(name, el) {
  var cached = includedPreviewCache[name];
  if (cached !== undefined) {
    el.textContent = routingSummaryText(cached);
    return;
  }
  loadProfile(name).then(function(details) {
    var apps = details.IncludedApps || [];
    includedPreviewCache[name] = apps;
    if (el.isConnected) el.textContent = routingSummaryText(apps);
  }).catch(function() { /* leave blank — not worth an error popup for a subtitle */ });
}

// ---------- included-apps preview (under Connect/Disconnect) ----------

var includedPreviewCache = {}; // profile name -> IncludedApps array, cleared whenever that profile is saved

function refreshIncludedAppsPreview() {
  var row = $('included-apps-row');
  // Prefer the profile that's actually connected over merely-selected —
  // the per-row quick-connect button (toggleConnectRow) connects without
  // selecting a row, so this used to just stay empty after connecting
  // that way instead of showing what's actually being routed (found live
  // 2026-09-04).
  var name = state.connected ? state.connectedProfile : state.selected;
  if (!name) {
    row.style.display = 'none';
    row.innerHTML = '';
    return;
  }
  var cached = includedPreviewCache[name];
  if (cached !== undefined) {
    renderIncludedAppsPreview(cached);
    return;
  }
  loadProfile(name).then(function(details) {
    var apps = details.IncludedApps || [];
    includedPreviewCache[name] = apps;
    var stillCurrent = state.connected ? state.connectedProfile === name : state.selected === name;
    if (stillCurrent) renderIncludedAppsPreview(apps);
  }).catch(function() { /* leave whatever was showing */ });
}

function renderIncludedAppsPreview(apps) {
  var row = $('included-apps-row');
  row.innerHTML = '';
  row.style.display = 'block';
  if (apps.length === 0) {
    var note = document.createElement('div');
    note.className = 'included-apps-fulltunnel';
    note.textContent = t('noAppsFullTunnel');
    row.appendChild(note);
    return;
  }
  var label = document.createElement('div');
  label.className = 'included-apps-label';
  label.textContent = t('routedApps');
  row.appendChild(label);
  var icons = document.createElement('div');
  icons.className = 'included-apps-icons';
  apps.forEach(function(p) {
    var img = document.createElement('img');
    img.className = 'included-app-icon';
    img.title = p;
    loadAppIcon(p, img);
    icons.appendChild(img);
  });
  row.appendChild(icons);
}

function refreshStatus() {
  // Must return this promise, not just kick it off — btn-connect's click
  // handler chains .finally(...) after .then(refreshStatus) specifically
  // so state.connectBusy doesn't clear until state.connected has
  // actually been refreshed from the server. Without the return, the
  // outer promise resolved with getState() still in flight, clearing
  // connectBusy (and re-enabling the button) on stale state — a second
  // click landing in that gap still saw state.connected === false and
  // fired a real second Connect RPC, hitting the backend's genuine
  // "already connected" error. Confirmed live 2026-09-10 on the Windows
  // VM: three rapid clicks on Connect, one of them got through as a
  // real duplicate call despite the busy-flag guard.
  return getState().then(function(s) {
    state.connected = s.Connected;
    state.connectedProfile = s.ProfileName;
    state.handshakeOK = s.HandshakeOK;
    updateButtons();
    renderProfileList();
  }).catch(showListError);
}

function showListError(err) {
  var e = $('list-error');
  e.style.display = 'block';
  e.textContent = String(err);
}

$('btn-add').onclick = function() { openEdit(null); };
$('btn-edit').onclick = function() { if (state.selected) openEdit(state.selected); };
$('btn-delete').onclick = function() {
  if (!state.selected) return;
  var name = state.selected;
  showConfirm(tf('confirmDelete', name), function() {
    deleteProfile(name).then(function() {
      state.selected = null;
      refreshProfiles();
    }).catch(showListError);
  });
};

// showConfirm replaces the native confirm() popup with an in-theme modal —
// runs onOk if the user confirms, does nothing on Cancel/overlay click.
function showConfirm(message, onOk) {
  $('confirm-message').textContent = message;
  $('confirm-overlay').style.display = 'flex';
  var cleanup;
  var okHandler = function() { cleanup(); onOk(); };
  var cancelHandler = function() { cleanup(); };
  cleanup = function() {
    $('confirm-overlay').style.display = 'none';
    $('confirm-ok').removeEventListener('click', okHandler);
    $('confirm-cancel').removeEventListener('click', cancelHandler);
    $('confirm-overlay').removeEventListener('click', overlayHandler);
  };
  var overlayHandler = function(e) { if (e.target === $('confirm-overlay')) cancelHandler(); };
  $('confirm-ok').addEventListener('click', okHandler);
  $('confirm-cancel').addEventListener('click', cancelHandler);
  $('confirm-overlay').addEventListener('click', overlayHandler);
}

// showPrompt replaces the native prompt() popup with an in-theme modal —
// runs onOk(value) if the user confirms with a non-empty value, does
// nothing on Cancel/overlay click.
function showPrompt(message, defaultValue, onOk) {
  $('prompt-message').textContent = message;
  $('prompt-input').value = defaultValue || '';
  $('prompt-overlay').style.display = 'flex';
  $('prompt-input').focus();
  var cleanup;
  var okHandler = function() {
    var value = $('prompt-input').value.trim();
    if (!value) return;
    cleanup();
    onOk(value);
  };
  var cancelHandler = function() { cleanup(); };
  var keyHandler = function(e) { if (e.key === 'Enter') okHandler(); else if (e.key === 'Escape') cancelHandler(); };
  cleanup = function() {
    $('prompt-overlay').style.display = 'none';
    $('prompt-ok').removeEventListener('click', okHandler);
    $('prompt-cancel').removeEventListener('click', cancelHandler);
    $('prompt-overlay').removeEventListener('click', overlayHandler);
    $('prompt-input').removeEventListener('keydown', keyHandler);
  };
  var overlayHandler = function(e) { if (e.target === $('prompt-overlay')) cancelHandler(); };
  $('prompt-ok').addEventListener('click', okHandler);
  $('prompt-cancel').addEventListener('click', cancelHandler);
  $('prompt-overlay').addEventListener('click', overlayHandler);
  $('prompt-input').addEventListener('keydown', keyHandler);
}
$('btn-apps').onclick = function() {
  if (!state.selected) return;
  openEdit(state.selected, 'apps');
};
$('btn-domains').onclick = function() {
  if (!state.selected) return;
  openEdit(state.selected, 'domains');
};
$('btn-connect').onclick = function() {
  if (state.connectBusy) return; // already in flight — ignore a second click
  state.connectBusy = true;
  $('btn-connect').disabled = true;
  var p = state.connected ? disconnect() : connect(state.selected);
  p.then(refreshStatus).catch(function(err) { showListError(err); refreshStatus(); })
    .finally(function() { state.connectBusy = false; updateButtons(); });
};

// ---------- edit view ----------

var editTemplate = "[Interface]\nPrivateKey = \nAddress = \nDNS = \n\n[Peer]\nPublicKey = \nEndpoint = \nAllowedIPs = 0.0.0.0/0\n";

function openEdit(name, jumpTo) {
  $('edit-error').style.display = 'none';
  state.editingExisting = name !== null;
  state.editOriginalName = name; // null for Add, the pre-edit name for Edit — see saveCurrentProfile
  state.includedApps = [];
  state.includedDomainLists = [];
  if (name === null) {
    $('edit-title').textContent = t('addProfile');
    $('edit-name').value = '';
    $('edit-name').disabled = false;
    $('edit-config').value = editTemplate;
    $('edit-autoconnect').checked = false;
    state.editHotkey = '';
    renderHotkeyValue();
    finishOpenEdit(jumpTo);
  } else {
    $('edit-title').textContent = t('editProfile');
    // Editable, not disabled — renaming a profile is just "Save" with a
    // different name now (saveCurrentProfile detects the change and
    // calls renameProfile first). Used to be locked entirely.
    $('edit-name').disabled = false;
    loadProfile(name).then(function(details) {
      $('edit-name').value = name;
      $('edit-config').value = details.ConfigText;
      state.includedApps = details.IncludedApps || [];
      state.includedDomainLists = details.IncludedDomainLists || [];
      $('edit-autoconnect').checked = !!details.AutoConnect;
      state.editHotkey = details.Hotkey || '';
      renderHotkeyValue();
      finishOpenEdit(jumpTo);
    }).catch(function(err) { showListError(err); });
  }
}

function finishOpenEdit(jumpTo) {
  updateAppsCount();
  updateDomainsCount();
  show('view-edit');
  if (jumpTo === 'apps') openAppsPicker();
  else if (jumpTo === 'domains') openDomainsPicker();
}

// ---------- hotkey capture ----------
// Registered per-profile in the running Bridge by tray.go's
// refreshHotkeys, which reads this straight out of the saved config's
// Hotkey field — this widget just builds that "Ctrl+Alt+F1" string from
// real keydown events instead of asking the user to type it by hand.

function renderHotkeyValue() {
  $('btn-hotkey-capture').textContent = state.editHotkey || t('hotkeyNotSet');
}

var hotkeyCaptureHandler = null;

function stopHotkeyCapture() {
  if (hotkeyCaptureHandler) {
    document.removeEventListener('keydown', hotkeyCaptureHandler, true);
    hotkeyCaptureHandler = null;
  }
}

$('btn-hotkey-capture').onclick = function() {
  stopHotkeyCapture();
  $('btn-hotkey-capture').textContent = t('hotkeyPress');
  hotkeyCaptureHandler = function(e) {
    e.preventDefault();
    e.stopPropagation();
    if (e.key === 'Escape') {
      stopHotkeyCapture();
      renderHotkeyValue();
      return;
    }
    // Wait for a real (non-modifier) key while at least one modifier is
    // still held — a bare "Ctrl" keydown fires before the second key.
    if (e.key === 'Control' || e.key === 'Alt' || e.key === 'Shift' || e.key === 'Meta') return;
    if (!e.ctrlKey && !e.altKey && !e.shiftKey) {
      showEditError(t('hotkeyNeedsModifier'));
      return;
    }
    var keyName;
    if (/^F([1-9]|1[0-2])$/.test(e.key)) {
      keyName = e.key;
    } else if (/^[a-zA-Z]$/.test(e.key)) {
      keyName = e.key.toUpperCase();
    } else if (/^[0-9]$/.test(e.key)) {
      keyName = e.key;
    } else {
      showEditError(t('hotkeyNeedsModifier'));
      return;
    }
    var mods = [];
    if (e.ctrlKey) mods.push('Ctrl');
    if (e.altKey) mods.push('Alt');
    if (e.shiftKey) mods.push('Shift');
    state.editHotkey = mods.concat([keyName]).join('+');
    stopHotkeyCapture();
    renderHotkeyValue();
  };
  document.addEventListener('keydown', hotkeyCaptureHandler, true);
};

$('btn-hotkey-clear').onclick = function() {
  stopHotkeyCapture();
  state.editHotkey = '';
  renderHotkeyValue();
};

$('edit-back').onclick = function() { stopHotkeyCapture(); show('view-list'); };
$('btn-cancel').onclick = function() { stopHotkeyCapture(); show('view-list'); };
$('btn-open-apps').onclick = function() { stopHotkeyCapture(); openAppsPicker(); };
$('btn-open-domains').onclick = function() { stopHotkeyCapture(); openDomainsPicker(); };
$('btn-import').onclick = function() {
  importConfig().then(function(text) {
    if (text) $('edit-config').value = text;
  }).catch(showEditError);
};

// saveCurrentProfile handles both plain saves and renames — if
// editOriginalName (set by openEdit) differs from the current Name
// field, it moves the on-disk profile first (renameProfile: refuses a
// name collision or renaming the currently-connected profile) before
// writing the edited content under the new name, so a rename plus other
// edits in the same Save always lands as one coherent result rather than
// a renamed copy of the pre-edit content.
function saveCurrentProfile() {
  var name = $('edit-name').value.trim();
  if (!name) { return Promise.reject(t('nameRequired')); }
  var oldName = state.editOriginalName;
  var doSave = function() {
    return saveProfile(name, $('edit-config').value, state.includedApps, state.includedDomainLists, $('edit-autoconnect').checked, state.editHotkey).then(function() {
      delete includedPreviewCache[name];
      if (oldName && oldName !== name) delete includedPreviewCache[oldName];
    });
  };
  if (oldName && oldName !== name) {
    return renameProfile(oldName, name).then(doSave);
  }
  return doSave();
}

$('btn-save').onclick = function() {
  stopHotkeyCapture();
  $('btn-save').disabled = true;
  saveCurrentProfile().then(function() {
    $('btn-save').disabled = false;
    show('view-list');
    refreshProfiles();
  }).catch(function(err) {
    $('btn-save').disabled = false;
    showEditError(err);
  });
};

function showEditError(err) {
  var e = $('edit-error');
  e.style.display = 'block';
  e.textContent = String(err);
}

function updateAppsCount() { $('btn-open-apps').textContent = tf('appsSelected', state.includedApps.length); }

// ---------- apps picker view ----------

function openAppsPicker() {
  $('apps-search').value = '';
  state.appsFilter = '';
  show('view-apps');
  listInstalledApps().then(function(apps) {
    state.installedApps = apps || [];
    // Keep any included paths that aren't in the discovered list (e.g. an
    // app that's been uninstalled since the profile was last saved) —
    // same "don't silently lose the selection" rule the old picker had.
    var known = {};
    state.installedApps.forEach(function(a) { known[a.Path.toLowerCase()] = true; });
    state.includedApps.forEach(function(p) {
      if (!known[p.toLowerCase()]) {
        state.installedApps.push({ Name: p, Path: p });
      }
    });
    renderAppsList();
  }).catch(showEditError);
  refreshAppPresets();
}

// ---------- app presets ----------
// Named, reusable app-selection lists (apppresets.go on the Go side) —
// separate from tunnel profiles entirely, so the same "Firefox + Discord"
// set can be dropped into any profile's Apps selection instead of
// re-searching and re-checking the same rows every time.

function refreshAppPresets() {
  listAppPresets().then(function(names) {
    state.appPresets = names || [];
    var sel = $('apps-preset-select');
    sel.innerHTML = '';
    if (state.appPresets.length === 0) {
      var opt = document.createElement('option');
      opt.textContent = t('presetNone');
      opt.disabled = true;
      sel.appendChild(opt);
      return;
    }
    state.appPresets.forEach(function(name) {
      var opt = document.createElement('option');
      opt.value = name;
      opt.textContent = name;
      sel.appendChild(opt);
    });
  }).catch(showEditError);
}

$('btn-load-preset').onclick = function() {
  var name = $('apps-preset-select').value;
  if (!name) { showEditError(t('presetSelectFirst')); return; }
  loadAppPreset(name).then(function(apps) {
    state.includedApps = apps || [];
    renderAppsList();
    updateAppsCount();
  }).catch(showEditError);
};

$('btn-save-preset').onclick = function() {
  showPrompt(t('presetSaveMessage'), '', function(name) {
    saveAppPreset(name, state.includedApps).then(function() {
      refreshAppPresets();
    }).catch(showEditError);
  });
};

$('btn-delete-preset').onclick = function() {
  var name = $('apps-preset-select').value;
  if (!name) { showEditError(t('presetSelectFirst')); return; }
  showConfirm(tf('presetConfirmDelete', name), function() {
    deleteAppPreset(name).then(function() {
      refreshAppPresets();
    }).catch(showEditError);
  });
};

function renderAppsList() {
  var ul = $('apps-list');
  ul.innerHTML = '';
  var filter = state.appsFilter.toLowerCase();
  var includedSet = {};
  state.includedApps.forEach(function(p) { includedSet[p.toLowerCase()] = true; });

  state.installedApps
    .filter(function(a) {
      if (!filter) return true;
      return a.Name.toLowerCase().indexOf(filter) !== -1 || a.Path.toLowerCase().indexOf(filter) !== -1;
    })
    .forEach(function(a) {
      var li = document.createElement('li');
      var included = !!includedSet[a.Path.toLowerCase()];
      var cb = document.createElement('span');
      cb.className = included ? 'checkbox checked' : 'checkbox';
      cb.textContent = included ? '☑' : '☐';
      var icon = document.createElement('img');
      icon.className = 'app-icon';
      icon.width = 20;
      icon.height = 20;
      loadAppIcon(a.Path, icon);
      var text = document.createElement('span');
      text.className = 'app-name-wrap';
      text.innerHTML = '<div class="app-name"></div><div class="app-path"></div>';
      text.querySelector('.app-name').textContent = a.Name;
      text.querySelector('.app-path').textContent = a.Path;
      li.appendChild(cb);
      li.appendChild(icon);
      li.appendChild(text);
      // launchApp only exists on Linux (Windows matches an already-running
      // process's live traffic instead — there's nothing to "launch") —
      // feature-detected so this button simply doesn't render on Windows
      // rather than calling a binding that isn't there.
      if (window.launchApp) {
        var launchBtn = document.createElement('button');
        launchBtn.type = 'button';
        launchBtn.className = 'app-launch-btn';
        launchBtn.textContent = t('appsLaunch');
        launchBtn.onclick = function(e) {
          e.stopPropagation(); // don't also toggle the checkbox
          launchBtn.disabled = true;
          window.launchApp(a.Path).catch(function(err) {
            showListError(err);
          }).finally(function() { launchBtn.disabled = false; });
        };
        li.appendChild(launchBtn);
      }
      li.onclick = function() { toggleApp(a.Path); };
      ul.appendChild(li);
    });
}

var iconCache = {}; // path (lowercase) -> data URI, or null while pending/failed

function loadAppIcon(path, imgEl) {
  var key = path.toLowerCase();
  var cached = iconCache[key];
  if (cached) { imgEl.src = cached; return; }
  if (cached === null) return; // already tried, no icon available
  iconCache[key] = null;
  getAppIcon(path).then(function(dataUri) {
    iconCache[key] = dataUri;
    // The row may have been re-rendered (filter/toggle) since this
    // fetch started — only apply if this exact <img> is still attached.
    if (imgEl.isConnected) imgEl.src = dataUri;
  }).catch(function() { /* no icon for this app — leave the placeholder blank */ });
}

function toggleApp(path) {
  var lower = path.toLowerCase();
  var idx = state.includedApps.findIndex(function(p) { return p.toLowerCase() === lower; });
  if (idx === -1) {
    state.includedApps.push(path);
  } else {
    state.includedApps.splice(idx, 1);
  }
  renderAppsList();
  updateAppsCount();
}

$('apps-search').oninput = function() { state.appsFilter = $('apps-search').value; renderAppsList(); };
$('btn-select-all').onclick = function() {
  state.includedApps = state.installedApps.map(function(a) { return a.Path; });
  renderAppsList();
  updateAppsCount();
};
$('btn-clear-all').onclick = function() {
  state.includedApps = [];
  renderAppsList();
  updateAppsCount();
};
$('apps-back').onclick = function() {
  // Always save and return straight to the main list — regardless of
  // whether Apps was opened from there or from within the edit screen,
  // "Done" should never leave the user looking at the raw config text
  // they didn't ask to see.
  saveCurrentProfile().then(function() {
    show('view-list');
    refreshProfiles();
  }).catch(function(err) { show('view-edit'); showEditError(err); });
};
$('btn-browse-app').onclick = function() {
  browseForExe().then(function(path) {
    if (path) toggleApp(path);
  }).catch(showEditError);
};

// ---------- domain lists picker view ----------
// Named, reusable domain lists (domainlists_linux.go on the Go side) —
// unlike app presets (a one-time copy into includedApps), each list stays
// a live, independently toggleable membership: state.includedDomainLists
// holds just the *names* of the lists enabled for this profile, and the
// lists themselves (their actual domains) are edited in one shared place
// via view-domain-edit rather than duplicated per profile.

function updateDomainsCount() { $('btn-open-domains').textContent = tf('domainsSelected', state.includedDomainLists.length); }

function openDomainsPicker() {
  show('view-domains');
  refreshDomainLists();
}

function refreshDomainLists() {
  listDomainLists().then(function(names) {
    state.domainLists = names || [];
    renderDomainsList();
  }).catch(showEditError);
}

function renderDomainsList() {
  var ul = $('domains-list');
  ul.innerHTML = '';
  if (state.domainLists.length === 0) {
    var empty = document.createElement('li');
    empty.textContent = t('domainListNoLists');
    empty.style.cursor = 'default';
    ul.appendChild(empty);
    return;
  }
  var enabledSet = {};
  state.includedDomainLists.forEach(function(n) { enabledSet[n] = true; });
  state.domainLists.forEach(function(name) {
    var li = document.createElement('li');
    li.style.cursor = 'default';

    var label = document.createElement('label');
    label.className = 'switch';
    var cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = !!enabledSet[name];
    cb.onchange = function() { toggleDomainList(name); };
    var slider = document.createElement('span');
    slider.className = 'switch-slider';
    label.appendChild(cb);
    label.appendChild(slider);

    var text = document.createElement('span');
    text.className = 'app-name-wrap';
    text.textContent = name;

    var editBtn = document.createElement('button');
    editBtn.type = 'button';
    editBtn.className = 'app-launch-btn';
    editBtn.textContent = t('domainListEdit');
    editBtn.onclick = function() { openDomainListEditor(name); };

    var delBtn = document.createElement('button');
    delBtn.type = 'button';
    delBtn.className = 'app-launch-btn';
    delBtn.textContent = t('delete');
    delBtn.onclick = function() {
      showConfirm(tf('domainListConfirmDelete', name), function() {
        deleteDomainList(name).then(function() {
          state.includedDomainLists = state.includedDomainLists.filter(function(n) { return n !== name; });
          updateDomainsCount();
          refreshDomainLists();
        }).catch(showEditError);
      });
    };

    li.appendChild(label);
    li.appendChild(text);
    li.appendChild(editBtn);
    li.appendChild(delBtn);
    ul.appendChild(li);
  });
}

function toggleDomainList(name) {
  var idx = state.includedDomainLists.indexOf(name);
  if (idx === -1) {
    state.includedDomainLists.push(name);
  } else {
    state.includedDomainLists.splice(idx, 1);
  }
  updateDomainsCount();
}

$('domains-back').onclick = function() {
  // Same reasoning as apps-back: always save and return to the main
  // list, regardless of how Domains was opened.
  saveCurrentProfile().then(function() {
    show('view-list');
    refreshProfiles();
  }).catch(function(err) { show('view-edit'); showEditError(err); });
};

$('btn-new-domain-list').onclick = function() {
  showPrompt(t('domainListNamePrompt'), '', function(name) {
    openDomainListEditor(name, true);
  });
};

// ---------- domain list editor view ----------

function openDomainListEditor(name, isNew) {
  state.editingDomainList = name;
  $('domain-edit-title').textContent = name;
  $('domain-edit-text').value = '';
  show('view-domain-edit');
  if (isNew) return;
  loadDomainList(name).then(function(domains) {
    $('domain-edit-text').value = (domains || []).join('\n');
  }).catch(function(err) { show('view-domains'); showEditError(err); });
}

$('btn-domain-edit-save').onclick = function() {
  var name = state.editingDomainList;
  var domains = $('domain-edit-text').value.split('\n')
    .map(function(d) { return d.trim(); })
    .filter(function(d) { return d !== ''; });
  saveDomainList(name, domains).then(function() {
    show('view-domains');
    refreshDomainLists();
  }).catch(showEditError);
};

$('btn-domain-edit-cancel').onclick = function() {
  show('view-domains');
  refreshDomainLists();
};

// ---------- language switching ----------

function applyI18n() {
  document.querySelectorAll('[data-i18n]').forEach(function(el) {
    el.textContent = t(el.getAttribute('data-i18n'));
  });
  document.querySelectorAll('[data-i18n-placeholder]').forEach(function(el) {
    el.placeholder = t(el.getAttribute('data-i18n-placeholder'));
  });
  $('lang-en').classList.toggle('active', state.lang === 'en');
  $('lang-ru').classList.toggle('active', state.lang === 'ru');
  // Re-run the dynamic renderers so text they build themselves (status
  // line, "N selected…", list rows) picks up the new language too — the
  // data-i18n pass above only covers static markup.
  renderProfileList();
  updateAppsCount();
  updateDomainsCount();
  renderHotkeyValue();
  renderUpdateStatus();
  if ($('view-apps').classList.contains('active')) renderAppsList();
  if ($('view-domains').classList.contains('active')) renderDomainsList();
}

function setLang(lang) {
  state.lang = lang;
  try { localStorage.setItem('lang', lang); } catch (e) { /* private/blocked storage — just don't persist */ }
  applyI18n();
}

$('lang-en').onclick = function() { setLang('en'); };
$('lang-ru').onclick = function() { setLang('ru'); };

// ---------- theme picker ----------
// Everyone gets to pick their own accent color + light/dark, applied via
// CSS custom properties on <html> and remembered per-viewer in
// localStorage. This only recolors UI chrome (buttons, selection,
// checkmarks) — the brand mark itself (topbar logo, tray icon, taskbar
// icon) stays the fixed gradient/grey pair tied to the actual .ico/.png
// assets on disk, which can't be recolored at runtime.

var THEMES = {
  violet:  { c1: '#7a5cff', c2: '#ff3ea5' },
  ocean:   { c1: '#00c2ff', c2: '#2f6bff' },
  emerald: { c1: '#00e5b0', c2: '#12b76a' },
  sunset:  { c1: '#ffb347', c2: '#ff5f6d' },
  crimson: { c1: '#ff5c8a', c2: '#c2185b' },
  slate:   { c1: '#8a8fa3', c2: '#545a70' },
};

function applyTheme() {
  var root = document.documentElement;
  root.classList.toggle('light', state.theme.mode === 'light');
  var c1, c2;
  if (state.theme.accent === 'custom') {
    c1 = state.theme.customColor1 || '#7a5cff';
    c2 = state.theme.customColor2 || '#ff3ea5';
  } else {
    var preset = THEMES[state.theme.accent] || THEMES.violet;
    c1 = preset.c1;
    c2 = preset.c2;
  }
  root.style.setProperty('--accent', c1);
  root.style.setProperty('--accent-dark', c2);
  root.style.setProperty('--grad', 'linear-gradient(135deg, ' + c1 + ' 0%, ' + c2 + ' 100%)');
  applyBackgroundImage();
  renderThemePicker();
}

function applyBackgroundImage() {
  document.documentElement.style.setProperty('--bg-fit', state.theme.bgFit || 'contain');
  if (state.theme.bgImage) {
    document.body.style.backgroundImage = 'url(' + state.theme.bgImage + ')';
    document.body.classList.add('has-bg-image');
    $('theme-bg-fit-row').style.display = 'flex';
  } else {
    document.body.style.backgroundImage = '';
    document.body.classList.remove('has-bg-image');
    $('theme-bg-fit-row').style.display = 'none';
  }
  $('theme-bg-fit-contain').classList.toggle('active', (state.theme.bgFit || 'contain') === 'contain');
  $('theme-bg-fit-cover').classList.toggle('active', state.theme.bgFit === 'cover');
}

function setThemeBgFit(fit) {
  state.theme.bgFit = fit;
  try { localStorage.setItem('themeBgFit', fit); } catch (e) {}
  applyBackgroundImage();
}

function renderThemePicker() {
  $('theme-mode-dark').classList.toggle('active', state.theme.mode === 'dark');
  $('theme-mode-light').classList.toggle('active', state.theme.mode === 'light');
  document.querySelectorAll('.theme-swatch').forEach(function(el) {
    el.classList.toggle('active', el.getAttribute('data-accent') === state.theme.accent);
  });
  $('theme-custom-color1').value = state.theme.customColor1 || '#7a5cff';
  $('theme-custom-color2').value = state.theme.customColor2 || '#ff3ea5';
}

function setThemeMode(mode) {
  state.theme.mode = mode;
  try { localStorage.setItem('themeMode', mode); } catch (e) {}
  applyTheme();
}

function setThemeAccent(accent) {
  state.theme.accent = accent;
  try { localStorage.setItem('themeAccent', accent); } catch (e) {}
  applyTheme();
}

function setThemeCustomColors() {
  state.theme.accent = 'custom';
  state.theme.customColor1 = $('theme-custom-color1').value;
  state.theme.customColor2 = $('theme-custom-color2').value;
  try {
    localStorage.setItem('themeAccent', 'custom');
    localStorage.setItem('themeCustomColor1', state.theme.customColor1);
    localStorage.setItem('themeCustomColor2', state.theme.customColor2);
  } catch (e) {}
  applyTheme();
}

function openSettings() {
  $('settings-overlay').style.display = 'flex';
  $('settings-toggle-btn').classList.add('active');
  renderUpdateStatus();
  // getAutostartEnabled only exists on Windows (autostart_windows.go) —
  // the daemon already starts itself on Linux via systemd, this toggle
  // is purely about the GUI/tray front-end reappearing after login.
  if (window.getAutostartEnabled) {
    $('settings-autostart-row').style.display = 'flex';
    getAutostartEnabled().then(function(enabled) {
      $('settings-autostart-toggle').checked = enabled;
    }).catch(function() { /* best-effort — leave it unchecked */ });
  }
}
function closeSettings() {
  $('settings-overlay').style.display = 'none';
  $('settings-toggle-btn').classList.remove('active');
}
$('settings-toggle-btn').onclick = openSettings;
$('settings-close').onclick = closeSettings;
$('settings-overlay').addEventListener('click', function(e) {
  if (e.target === $('settings-overlay')) closeSettings();
});
$('settings-autostart-toggle').onchange = function() {
  var checked = $('settings-autostart-toggle').checked;
  setAutostartEnabled(checked).catch(function(err) {
    $('settings-autostart-toggle').checked = !checked; // revert on failure
    showListError(err);
  });
};
// pollForRestart waits for the daemon/service to actually come back up
// running a version different from previousVersion — see
// update_linux.go/update_windows.go's CheckForUpdateNow doc for why
// this has to be a poll on fresh connections rather than trusting that
// RPC's own reply: installing restarts the very process answering it,
// so the reply can be (and during testing, was) lost to that restart
// before ever reaching here. A connection failure mid-poll is the
// expected, normal shape of "it's restarting right now," not an error.
// Only calls restartApp() once the new version is confirmed running —
// never on a blind timer — so the GUI never relaunches itself into a
// half-installed or failed update.
function pollForRestart(previousVersion, deadline) {
  if (Date.now() > deadline) {
    $('update-status-text').textContent = t('updateStillInstalling');
    $('btn-check-update').disabled = false;
    return;
  }
  getRunningVersion().then(function(v) {
    if (v && v !== previousVersion) {
      $('update-status-text').textContent = t('updateRestarting');
      restartApp();
    } else {
      setTimeout(function() { pollForRestart(previousVersion, deadline); }, 1500);
    }
  }).catch(function() {
    setTimeout(function() { pollForRestart(previousVersion, deadline); }, 1500);
  });
}
$('btn-check-update').onclick = function() {
  $('btn-check-update').disabled = true;
  $('update-status-text').textContent = t('updateChecking');
  checkForUpdateNow().then(function(reply) {
    if (reply.Available) {
      $('update-status-text').textContent = tf('updateAvailable', reply.Latest);
      pollForRestart(reply.Current, Date.now() + 60000);
    } else {
      $('update-status-text').textContent = tf('updateUpToDate', reply.Current);
      $('btn-check-update').disabled = false;
    }
  }).catch(function(err) {
    $('update-status-text').textContent = tf('updateCheckFailed', String(err));
    $('btn-check-update').disabled = false;
  });
};
$('theme-mode-dark').onclick = function() { setThemeMode('dark'); };
$('theme-mode-light').onclick = function() { setThemeMode('light'); };
$('theme-custom-color1').oninput = setThemeCustomColors;
$('theme-custom-color2').oninput = setThemeCustomColors;
$('btn-choose-bg').onclick = function() {
  chooseBackgroundImage().then(function(dataUri) {
    if (!dataUri) return;
    state.theme.bgImage = dataUri;
    try { localStorage.setItem('themeBgImage', dataUri); } catch (e) {}
    applyBackgroundImage();
  }).catch(showListError);
};
$('btn-clear-bg').onclick = function() {
  state.theme.bgImage = '';
  try { localStorage.removeItem('themeBgImage'); } catch (e) {}
  applyBackgroundImage();
};
$('theme-bg-fit-contain').onclick = function() { setThemeBgFit('contain'); };
$('theme-bg-fit-cover').onclick = function() { setThemeBgFit('cover'); };
document.querySelectorAll('.theme-swatch').forEach(function(el) {
  el.onclick = function() { setThemeAccent(el.getAttribute('data-accent')); };
});

// ---------- startup ----------


applyTheme();
applyI18n();
refreshProfiles();
refreshStatus();
setInterval(refreshStatus, 3000);
</script>
</body>
</html>`
