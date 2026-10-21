package main

// ImportedConfig is importConfig's return value (webui_linux.go,
// webui_windows.go) — the picked file's raw text plus a filename-derived
// name suggestion, so the "name this tunnel" prompt (btn-import's handler
// below) has something sensible to prefill instead of an empty field.
type ImportedConfig struct {
	SuggestedName string
	Text          string
}

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
    --grad: linear-gradient(135deg, #8b3dff 0%, #ff2d55 100%);
    --accent: #8b3dff;
    --accent-dark: #ff2d55;
    --ink: #f1f0f8;
    --muted: #9b98b0;
    --faint: #6d6a82;
    --page-base: #0d0c14;
    --surface: #17161f;
    --surface-2: #1e1d29;
    --surface-3: #282637;
    --border: rgba(255, 255, 255, .07);
    --border-strong: rgba(255, 255, 255, .14);
    --input-bg: #111019;
    --ok: #2ee6a0;
    --ok-ink: #45e9ad;
    --warn: #f5b83d;
    --bad: #ff5d6c;
    --err-fg: #ffa3b4;
    --err-bg: rgba(255, 93, 108, .1);
    --err-border: rgba(255, 93, 108, .28);
    --scroll-thumb: rgba(255, 255, 255, .12);
    --scroll-thumb-hover: rgba(255, 255, 255, .22);
    --radius: 14px;
    --shadow: 0 18px 40px -18px rgba(0, 0, 0, .7);
    --aurora: .2;
    /* Older names, still used by a few inline styles below. */
    --card: var(--surface);
    --chip-bg: var(--surface-2);
    --chip-hover: var(--surface-3);
  }
  :root.light {
    color-scheme: light;
    --ink: #17161f;
    --muted: #686679;
    --faint: #9997aa;
    --page-base: #f3f3f8;
    --surface: #ffffff;
    --surface-2: #f5f4fa;
    --surface-3: #ebe9f4;
    --border: rgba(24, 20, 50, .08);
    --border-strong: rgba(24, 20, 50, .16);
    --input-bg: #fafafd;
    --ok: #12b77a;
    --ok-ink: #0d9463;
    --warn: #d8960f;
    --bad: #e5384a;
    --err-fg: #c81e45;
    --err-bg: #fdeef2;
    --err-border: #f7cfda;
    --scroll-thumb: rgba(24, 20, 50, .14);
    --scroll-thumb-hover: rgba(24, 20, 50, .26);
    --shadow: 0 16px 36px -18px rgba(56, 41, 120, .3);
    --aurora: .13;
  }
  * { box-sizing: border-box; }
  html, body { height: 100%; }
  body {
    margin: 0; padding: 0; color: var(--ink);
    /* The accent tints the page itself (two soft glows), so picking a
       theme color recolors the whole window, not just the buttons. */
    background:
      radial-gradient(120% 70% at 0% 0%, color-mix(in srgb, var(--accent) calc(var(--aurora) * 100%), transparent), transparent 60%),
      radial-gradient(90% 60% at 100% 100%, color-mix(in srgb, var(--accent-dark) calc(var(--aurora) * 70%), transparent), transparent 60%),
      var(--page-base);
    font-family: "Segoe UI Variable Text", "Segoe UI", system-ui, "Ubuntu", "Cantarell", sans-serif;
    font-size: 14px; line-height: 1.35;
    display: flex; justify-content: center;
    -webkit-font-smoothing: antialiased;
  }
  /* Fills the window: one flex column whose single growing element per
     view (the profile/app list, or a textarea — see .flex-grow) takes
     whatever height is left, so resizing or maximizing the window never
     leaves dead space. Capped in width so a maximized window keeps
     readable line lengths. */
  .card {
    width: 100%; max-width: 760px; padding: 14px 18px 16px;
    display: flex; flex-direction: column; min-height: 0;
  }
  svg.i { width: 18px; height: 18px; flex-shrink: 0; fill: none; stroke: currentColor; stroke-width: 2; stroke-linecap: round; stroke-linejoin: round; }
  svg.i.sm { width: 15px; height: 15px; }

  /* ---------- top bar ---------- */
  .topbar { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; flex-shrink: 0; }
  .brand { display: flex; align-items: center; gap: 9px; }
  .brand-title {
    font-size: 17px; font-weight: 700; letter-spacing: .2px;
  }
  .topbar-actions { display: flex; align-items: center; gap: 6px; }
  .icon-btn {
    width: 36px; height: 36px; padding: 0; display: inline-flex; align-items: center; justify-content: center;
    border-radius: 11px; background: transparent; border-color: transparent; color: var(--muted);
  }
  .icon-btn:hover { background: var(--surface-2); color: var(--ink); }
  .icon-btn.active { background: var(--surface-2); color: var(--ink); border-color: var(--border-strong); }
  .icon-btn svg.i { transition: transform .4s ease; }
  #settings-toggle-btn:hover svg.i { transform: rotate(60deg); }

  /* ---------- panels ---------- */
  .panel {
    background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius);
    box-shadow: var(--shadow);
  }

  /* ---------- hero (connection status) ---------- */
  .hero {
    position: relative; overflow: hidden; flex-shrink: 0;
    padding: 24px 18px 20px 30px; margin-bottom: 18px;
  }
  .hero::before {
    /* Accent wash behind the status, only while a tunnel is up. */
    content: ''; position: absolute; inset: 0; pointer-events: none;
    background: radial-gradient(90% 120% at 0% 50%, color-mix(in srgb, var(--accent) 22%, transparent), transparent 70%);
    opacity: 0; transition: opacity .5s ease;
  }
  .hero.on::before { opacity: 1; }
  .hero-main { position: relative; display: flex; align-items: center; gap: 30px; }
  .orb {
    position: relative; width: 78px; height: 78px; flex-shrink: 0; border-radius: 50%;
    display: grid; place-items: center; cursor: pointer;
    background: var(--surface-2); border: 1px solid var(--border);
    transition: transform .2s ease, box-shadow .4s ease, background .4s ease;
  }
  .orb:hover { transform: scale(1.04); border-color: color-mix(in srgb, var(--accent) 60%, transparent); }
  .orb:active { transform: scale(.97); }
  .orb.busy { pointer-events: none; }
  .orb-ring {
    position: absolute; inset: -4px; border-radius: 50%; opacity: 0; transition: opacity .4s ease;
    background: conic-gradient(from 0deg, var(--accent), var(--accent-dark), transparent 70%, var(--accent));
    -webkit-mask: radial-gradient(farthest-side, transparent calc(100% - 3px), #000 calc(100% - 3px));
            mask: radial-gradient(farthest-side, transparent calc(100% - 3px), #000 calc(100% - 3px));
  }
  .hero.on .orb { box-shadow: 0 0 46px -6px color-mix(in srgb, var(--accent) 70%, transparent); background: color-mix(in srgb, var(--accent) 12%, var(--surface)); }
  .hero.on .orb-ring { opacity: 1; }
  /* Swirling dust around the logo — drawn by the orb-dust script. */
  .orb-dust { position: absolute; left: 50%; top: 50%; width: 200px; height: 200px; transform: translate(-50%, -50%); pointer-events: none; }
  /* Spins only while connecting — a constantly spinning ring cost as much
     as all the dust; the dust alone keeps the logo alive. */
  .hero.connecting .orb-ring { opacity: .9; animation: orb-spin 1.1s linear infinite; }
  @keyframes orb-spin { to { transform: rotate(360deg); } }
  .brand-icon { width: 46px; height: 46px; position: relative; }
  .brand-icon svg { position: absolute; inset: 0; width: 46px; height: 46px; transition: opacity .35s ease; }
  .hero.on .brand-icon svg { filter: drop-shadow(0 0 8px color-mix(in srgb, var(--accent) 70%, transparent)); }
  .hero-text { min-width: 0; flex: 1; }
  .hero-kicker { font-size: 11.5px; font-weight: 600; letter-spacing: .9px; text-transform: uppercase; color: var(--faint); margin-bottom: 4px; }
  /* #status-text is the hero's big title — the dot before it is the
     same green/amber/red/grey state the tray icon shows. */
  .status-text { font-size: 22px; font-weight: 700; letter-spacing: -.2px; display: flex; align-items: center; gap: 9px; }
  .status-text::before { content: ''; width: 10px; height: 10px; border-radius: 50%; background: var(--faint); flex-shrink: 0; transition: background .3s ease, box-shadow .3s ease; }
  .status-text.connected::before { background: var(--ok); box-shadow: 0 0 0 4px color-mix(in srgb, var(--ok) 22%, transparent); }
  .status-text.connecting::before { background: var(--warn); box-shadow: 0 0 0 4px color-mix(in srgb, var(--warn) 22%, transparent); animation: blink 1s ease-in-out infinite; }
  .status-text.disconnected.error::before { background: var(--bad); box-shadow: 0 0 0 4px color-mix(in srgb, var(--bad) 22%, transparent); }
  .hero-sub { color: var(--muted); font-size: 13px; margin-top: 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .hero-sub.error { color: var(--bad); }
  /* Live speed, right of the "Status" label, while connected. */
  .hero-kicker-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 4px; }
  .hero-kicker-row .hero-kicker { margin: 0; }
  .hero-speed { display: none; gap: 12px; font-size: 12.5px; font-weight: 600; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .hero.on .hero-speed { display: flex; }
  .hero-speed span { display: inline-flex; align-items: center; gap: 4px; }
  .hero-speed svg.i { width: 13px; height: 13px; stroke-width: 2.5; }
  .hero-speed .sp-down svg.i { color: var(--accent); }
  .hero-speed .sp-up svg.i { color: var(--accent-dark); }
  .hero-speed small { font-size: 11px; color: var(--muted); font-weight: 600; margin-left: 2px; }
  /* Connect celebration: a flash of the accent spreading from the logo. */
  .hero::after {
    content: ''; position: absolute; left: 69px; top: 63px; width: 10px; height: 10px; border-radius: 50%;
    background: radial-gradient(closest-side, color-mix(in srgb, var(--accent) 55%, transparent), transparent);
    transform: translate(-50%, -50%) scale(0); opacity: 0; pointer-events: none;
  }
  .hero.flash::after { animation: hero-flash 1.1s cubic-bezier(.2, .7, .3, 1); }
  @keyframes hero-flash { 0% { opacity: 1; transform: translate(-50%, -50%) scale(0); } 100% { opacity: 0; transform: translate(-50%, -50%) scale(140); } }
  .status-text.connected {
    background: linear-gradient(90deg, var(--ink) 30%, color-mix(in srgb, var(--accent) 55%, var(--ink)));
    -webkit-background-clip: text; background-clip: text; color: transparent;
  }
  /* One chip per connected tunnel: name, state, the apps it routes. */
  .included-apps { position: relative; margin-top: 14px; display: flex; flex-wrap: wrap; gap: 8px; }
  .tunnel-block {
    display: flex; align-items: center; gap: 9px; min-width: 0; max-width: 100%;
    padding: 6px 10px 6px 8px; border-radius: 11px;
    background: color-mix(in srgb, var(--surface-2) 80%, transparent); border: 1px solid var(--border);
  }
  .tunnel-head { font-size: 12.5px; font-weight: 600; display: flex; align-items: center; gap: 6px; white-space: nowrap; }
  .tunnel-head::before { content: ''; width: 7px; height: 7px; border-radius: 50%; background: var(--ok); }
  .tunnel-head.connecting::before { background: var(--warn); animation: blink 1s ease-in-out infinite; }
  @keyframes blink { 50% { opacity: .3; } }
  .included-apps-icons { display: flex; flex-wrap: wrap; gap: 4px; }
  .included-app-icon { width: 20px; height: 20px; border-radius: 5px; object-fit: contain; }
  .included-app-icon:not([src]) { visibility: hidden; }
  .included-apps-fulltunnel { color: var(--muted); font-size: 12px; white-space: nowrap; }

  /* ---------- section header ---------- */
  .section-head { display: flex; align-items: center; justify-content: space-between; margin: 0 2px 10px; flex-shrink: 0; }
  .section-title { font-size: 12px; font-weight: 700; letter-spacing: .9px; text-transform: uppercase; color: var(--muted); display: flex; align-items: center; gap: 8px; }
  .count-badge { font-size: 11px; font-weight: 700; letter-spacing: 0; color: var(--muted); background: var(--surface-2); border: 1px solid var(--border); border-radius: 999px; padding: 1px 8px; }

  .view { display: none; }
  .view.active { display: flex; flex-direction: column; flex: 1; min-height: 0; animation: view-in .22s ease; }
  @keyframes view-in { from { opacity: 0; transform: translateY(6px); } }
  .flex-grow { flex: 1; min-height: 0; }

  /* ---------- profile cards ---------- */
  .profile-list {
    list-style: none; margin: 0 -4px 12px; padding: 2px 4px 4px; overflow-y: auto;
    display: flex; flex-direction: column; gap: 8px; border-radius: var(--radius);
    outline: 2px dashed transparent; outline-offset: -2px; transition: outline-color .15s ease, background .15s ease;
  }
  .profile-list.drag-over { outline-color: var(--accent); background: color-mix(in srgb, var(--accent) 8%, transparent); }
  .profile-row {
    display: flex; align-items: center; gap: 12px; padding: 11px 12px 11px 11px; flex-shrink: 0;
    background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius);
    cursor: pointer; transition: border-color .15s ease, background .25s ease, box-shadow .15s ease, transform .15s ease;
  }
  .profile-row:hover { border-color: var(--border-strong); transform: translateY(-1px); }
  .profile-row { position: relative; overflow: hidden; }
  .profile-row > * { position: relative; }
  /* A soft light that follows the cursor across a card (--mx/--my are
     set by the profile list's mousemove handler). */
  .profile-row::before {
    content: ''; position: absolute; inset: 0; pointer-events: none; opacity: 0; transition: opacity .2s ease;
    background: radial-gradient(220px circle at var(--mx, 50%) var(--my, 50%), color-mix(in srgb, var(--accent) 16%, transparent), transparent 70%);
  }
  .profile-row:hover::before { opacity: 1; }
  .profile-row.enter { animation: card-in .4s cubic-bezier(.2, .8, .3, 1) backwards; }
  @keyframes card-in { from { opacity: 0; transform: translateY(10px) scale(.98); } }
  .profile-row.selected { border-color: color-mix(in srgb, var(--accent) 65%, transparent); box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 16%, transparent); }
  .profile-row.is-on { background: linear-gradient(110deg, color-mix(in srgb, var(--accent) 13%, var(--surface)), var(--surface) 70%); }
  .profile-avatar {
    width: 40px; height: 40px; border-radius: 12px; flex-shrink: 0; display: grid; place-items: center;
    font-weight: 700; font-size: 16px; color: var(--muted); background: var(--surface-2); border: 1px solid var(--border);
    transition: background .3s ease, color .3s ease;
  }
  .profile-row.is-on .profile-avatar { background: var(--grad); color: #fff; border-color: transparent; box-shadow: 0 6px 16px -6px color-mix(in srgb, var(--accent) 80%, transparent); }
  .profile-row-info { min-width: 0; flex: 1 1 0; }
  .profile-row-name { font-weight: 600; font-size: 14.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .profile-row-meta { display: flex; align-items: center; gap: 8px; margin-top: 3px; min-width: 0; }
  .profile-row-status { font-size: 12px; font-weight: 600; white-space: nowrap; }
  .profile-row-status:empty { display: none; }
  .profile-row-status.connected { color: var(--ok-ink); }
  .profile-row-status.connecting { color: var(--warn); }
  .profile-row-status.error { color: var(--bad); }
  .profile-row-status:not(:empty) + .profile-row-routing::before { content: '·'; margin-right: 8px; color: var(--faint); }
  .profile-row-routing { font-size: 12px; color: var(--muted); display: flex; align-items: center; gap: 6px; min-width: 0; white-space: nowrap; }
  .mini-icons { display: inline-flex; }
  .mini-icons img { width: 16px; height: 16px; border-radius: 4px; object-fit: contain; margin-right: 2px; }
  .mini-icons img:not([src]) { visibility: hidden; }
  /* The connect switch — always visible (it used to appear only on
     hover, so first-time users didn't know where Connect was). */
  .profile-row-connect { flex-shrink: 0; padding: 6px; margin: -6px; cursor: pointer; }
  .profile-row-connect.busy { opacity: .55; pointer-events: none; }
  .profile-row-connect-btn {
    display: block; position: relative; width: 46px; height: 26px; border-radius: 999px;
    background: var(--surface-3); border: 1px solid var(--border-strong);
    transition: background .25s ease, border-color .25s ease;
  }
  .profile-row-connect-btn::after {
    content: ''; position: absolute; top: 2px; left: 2px; width: 20px; height: 20px; border-radius: 50%;
    background: #fff; box-shadow: 0 1px 3px rgba(0, 0, 0, .35); transition: transform .25s cubic-bezier(.3, 1.4, .5, 1);
  }
  :root:not(.light) .profile-row-connect-btn::after { background: #d9d7e6; }
  .profile-row-connect:hover .profile-row-connect-btn { border-color: var(--accent); }
  .profile-row-connect.active .profile-row-connect-btn { background: var(--grad); border-color: transparent; }
  .profile-row-connect.active .profile-row-connect-btn::after { transform: translateX(20px); background: #fff; }
  .profile-row-connect.pending .profile-row-connect-btn::after { animation: blink 1s ease-in-out infinite; }

  .empty-state {
    list-style: none; flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
    text-align: center; padding: 26px 20px; border: 1.5px dashed var(--border-strong); border-radius: var(--radius); color: var(--muted);
  }
  .empty-state .empty-icon { width: 54px; height: 54px; border-radius: 16px; display: grid; place-items: center; background: var(--grad); color: #fff; margin-bottom: 12px; box-shadow: 0 10px 24px -10px var(--accent); }
  .empty-state .empty-icon svg.i { width: 26px; height: 26px; }
  .empty-title { color: var(--ink); font-weight: 700; font-size: 15px; margin-bottom: 4px; }
  .empty-text { font-size: 12.5px; max-width: 300px; }

  /* ---------- buttons ---------- */
  .row { display: flex; gap: 8px; margin-bottom: 8px; flex-wrap: wrap; flex-shrink: 0; }
  /* Applies only inside a [data-reorder] row — plain .row buttons elsewhere
     (Save/Cancel dialogs, theme swatches, etc.) are never draggable, so
     this never fights an ordinary click. */
  [data-reorder] > button:active { cursor: grabbing; }
  [data-reorder] > button.dragging { opacity: .6; box-shadow: 0 0 0 2px var(--accent); position: relative; z-index: 1; }
  button {
    font-family: inherit; display: inline-flex; align-items: center; justify-content: center; gap: 7px;
    padding: 8px 14px; border: 1px solid var(--border); border-radius: 10px; background: var(--surface-2);
    color: var(--ink); cursor: pointer; font-size: 13px; font-weight: 500;
    transition: background .15s ease, border-color .15s ease, color .15s ease, filter .15s ease, transform .1s ease, opacity .15s ease;
  }
  button:hover { background: var(--surface-3); border-color: var(--border-strong); }
  button:active { transform: scale(.98); }
  button:disabled { opacity: .4; cursor: default; }
  button:disabled:hover { background: var(--surface-2); border-color: var(--border); }
  button:disabled:active { transform: none; }
  button:focus-visible, input:focus-visible, select:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  button.primary { background: var(--grad); color: #fff; border-color: transparent; font-weight: 600; box-shadow: 0 8px 20px -10px var(--accent); }
  button.primary:hover { filter: brightness(1.1); }
  button.primary.danger { background: linear-gradient(135deg, #ff5c6a 0%, #c2185b 100%); box-shadow: 0 8px 20px -10px #ff5c6a; }
  button.ghost { background: transparent; border-color: transparent; color: var(--muted); }
  button.ghost:hover { background: var(--surface-2); color: var(--ink); }
  button.small { padding: 6px 11px; font-size: 12.5px; border-radius: 9px; }
  .action-bar { display: grid; grid-template-columns: repeat(4, 1fr); gap: 6px; margin: 0; padding: 6px; }
  .action-bar > button { background: transparent; border-color: transparent; color: var(--muted); padding: 9px 6px; font-size: 12.5px; flex-direction: column; gap: 4px; }
  .action-bar > button:hover { background: var(--surface-2); color: var(--ink); }
  .action-bar > button.danger-hover:hover { color: var(--bad); background: color-mix(in srgb, var(--bad) 10%, transparent); }
  .action-bar > button:disabled:hover { background: transparent; color: var(--muted); }
  .action-bar > button span { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%; }

  /* ---------- sub-view header ---------- */
  .view-head { display: flex; align-items: center; gap: 10px; margin-bottom: 14px; flex-shrink: 0; }
  .view-head h1 { flex: 1; min-width: 0; margin: 0; font-size: 18px; font-weight: 700; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .backlink { padding: 7px 12px 7px 9px; }
  h1 { font-size: 18px; margin: 0 0 12px; font-weight: 700; flex-shrink: 0; }

  /* ---------- forms ---------- */
  label { display: block; margin: 0 0 6px; font-weight: 600; font-size: 12.5px; color: var(--muted); }
  input[type=text], textarea {
    width: 100%; padding: 10px 12px; border: 1px solid var(--border-strong); border-radius: 10px;
    font-family: inherit; font-size: 13.5px; background: var(--input-bg); color: var(--ink);
    transition: border-color .15s ease, box-shadow .15s ease;
  }
  textarea { font-family: "Cascadia Mono", Consolas, "Ubuntu Mono", monospace; font-size: 12.5px; line-height: 1.5; height: 220px; resize: vertical; }
  input[type=text]:focus, textarea:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 20%, transparent); }
  textarea.flex-grow { height: auto; min-height: 110px; resize: none; }
  select {
    font-family: inherit; padding: 8px 10px; border: 1px solid var(--border-strong); border-radius: 10px;
    background: var(--input-bg); color: var(--ink); font-size: 13px; flex: 1; min-width: 0;
  }
  select:focus { outline: none; border-color: var(--accent); }
  .field { margin-bottom: 14px; flex-shrink: 0; }
  .field-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 6px; }
  .field-head label { margin: 0; }
  .group { padding: 4px 14px; margin-bottom: 14px; flex-shrink: 0; box-shadow: none; }
  .group-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 0; min-height: 50px; }
  .group-row + .group-row { border-top: 1px solid var(--border); }
  .group-row-label { font-size: 13.5px; font-weight: 500; display: flex; align-items: center; gap: 10px; color: var(--ink); margin: 0; }
  .group-row-label svg.i { color: var(--muted); }
  .kbd-btn { font-family: "Cascadia Mono", Consolas, monospace; font-size: 12.5px; min-width: 96px; }
  .footer-bar { display: flex; justify-content: flex-end; gap: 8px; margin: 2px 0 0; padding-top: 12px; border-top: 1px solid var(--border); }
  .footer-bar button { min-width: 110px; }
  .hint { color: var(--muted); font-size: 12.5px; margin: 0 0 12px; flex-shrink: 0; overflow-wrap: anywhere; line-height: 1.45; }
  #update-status-text { flex-shrink: 1; min-width: 0; }
  /* The error banners only — .error is also a state class on status
     labels (.status-text, .profile-row-status, .hero-sub). */
  #list-error, #edit-error, #settings-service-status.error { color: var(--err-fg); background: var(--err-bg); border: 1px solid var(--err-border); border-radius: 10px; padding: 9px 12px; margin: 0 0 12px; white-space: pre-wrap; font-size: 12.5px; flex-shrink: 0; }

  /* ---------- search + app list ---------- */
  .search { position: relative; margin-bottom: 10px; flex-shrink: 0; }
  .search svg.i { position: absolute; left: 12px; top: 50%; transform: translateY(-50%); color: var(--faint); pointer-events: none; }
  .search input[type=text] { padding-left: 38px; }
  .toolbar { display: flex; align-items: center; gap: 6px; margin-bottom: 10px; flex-shrink: 0; flex-wrap: wrap; }
  .toolbar .sep { width: 1px; align-self: stretch; background: var(--border-strong); margin: 4px 4px; }
  .toolbar select { flex: 1 1 120px; }
  .toolbar .icon-btn { width: 34px; height: 34px; background: var(--surface-2); border-color: var(--border); color: var(--ink); }
  .toolbar .icon-btn:hover { background: var(--surface-3); }
  .app-list { list-style: none; margin: 0 0 12px; padding: 4px; overflow-y: auto; }
  .app-list li { padding: 8px 10px; border-radius: 10px; cursor: pointer; display: flex; align-items: center; gap: 12px; transition: background .12s ease; }
  .app-list li + li { margin-top: 2px; }
  .app-list li:hover { background: var(--surface-2); }
  .app-list li.checked { background: color-mix(in srgb, var(--accent) 11%, transparent); }
  .app-list li.empty-row { cursor: default; color: var(--muted); justify-content: center; padding: 22px 10px; text-align: center; }
  .app-list li.empty-row:hover { background: transparent; }
  .app-icon { width: 28px; height: 28px; flex-shrink: 0; object-fit: contain; border-radius: 6px; }
  .app-icon:not([src]) { visibility: hidden; }
  .app-name { font-weight: 600; font-size: 13.5px; }
  .app-path { color: var(--faint); font-size: 11.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; direction: rtl; text-align: left; }
  .checkbox {
    width: 20px; height: 20px; border-radius: 6px; flex-shrink: 0; position: relative;
    border: 1.5px solid var(--border-strong); background: var(--input-bg); transition: background .15s ease, border-color .15s ease;
  }
  .checkbox.checked { background: var(--grad); border-color: transparent; }
  .checkbox.checked::after {
    content: ''; position: absolute; left: 6px; top: 2.5px; width: 5px; height: 10px;
    border: solid #fff; border-width: 0 2.2px 2.2px 0; transform: rotate(45deg);
  }
  .app-list li > .app-name-wrap { flex-grow: 1; min-width: 0; }
  .app-launch-btn { flex-shrink: 0; font-size: 12px; padding: 5px 10px; border-radius: 8px; }
  .app-launch-btn:hover { background: var(--accent); color: #fff; border-color: transparent; }
  .app-launch-btn.danger-hover:hover { background: var(--bad); }
  .list-footer { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-shrink: 0; }
  .list-footer .hint { margin: 0; }

  .icon-close { cursor: pointer; color: var(--muted); }
  .seg { display: inline-flex; padding: 3px; gap: 2px; border-radius: 11px; background: var(--surface-2); border: 1px solid var(--border); }
  .seg button { padding: 5px 13px; font-size: 12.5px; border-radius: 8px; background: transparent; border-color: transparent; color: var(--muted); }
  .seg button:hover { background: transparent; color: var(--ink); }
  .seg button.active { background: var(--surface); color: var(--ink); border-color: var(--border); box-shadow: 0 2px 6px -2px rgba(0, 0, 0, .3); font-weight: 600; }
  ::-webkit-scrollbar { width: 8px; height: 8px; }
  ::-webkit-scrollbar-track { background: transparent; }
  ::-webkit-scrollbar-thumb { background: var(--scroll-thumb); border-radius: 8px; border: 2px solid transparent; background-clip: padding-box; }
  ::-webkit-scrollbar-thumb:hover { background: var(--scroll-thumb-hover); background-clip: padding-box; }

  /* ---------- settings ---------- */
  .theme-panel-row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .theme-panel-row + .theme-panel-row { margin-top: 12px; }
  .theme-panel-label { color: var(--ink); font-size: 13.5px; font-weight: 500; flex: 1; min-width: 120px; }
  .theme-mode-btn.active { background: var(--surface); }
  .theme-swatch {
    width: 26px; height: 26px; padding: 0; border-radius: 50%; border: 2px solid transparent;
    cursor: pointer; box-shadow: inset 0 0 0 1px rgba(255, 255, 255, .15);
  }
  .theme-swatch:hover { transform: scale(1.12); }
  .theme-swatch.active { border-color: var(--page-base); box-shadow: 0 0 0 2px var(--ink); }
  input[type=color] {
    width: 34px; height: 28px; padding: 2px; border: 1px solid var(--border-strong); border-radius: 8px;
    background: var(--surface-2); cursor: pointer;
  }
  .settings-section + .settings-section { margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--border); }
  .settings-section-title { font-size: 11px; font-weight: 700; letter-spacing: 1px; text-transform: uppercase; color: var(--faint); margin-bottom: 10px; }
  .swatches { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
  .about-row { display: flex; align-items: center; gap: 12px; }
  .app-tile { display: block; flex-shrink: 0; border-radius: 22%; box-shadow: 0 4px 14px -4px rgba(139, 61, 255, .5); }
  .about-text { flex: 1; min-width: 0; }
  .about-name { font-weight: 700; font-size: 14px; }

  /* Background photo: covers the whole window, and every panel turns
     into frosted glass over it. --bg-fit is contain (whole photo,
     letterboxed on --page-base) or cover (fills, crops) — the user's
     choice. */
  body.has-bg-image { background-size: var(--bg-fit, contain); background-position: center; background-repeat: no-repeat; background-attachment: fixed; }
  body.has-bg-image .panel,
  body.has-bg-image .profile-row {
    background: color-mix(in srgb, var(--surface) 62%, transparent); backdrop-filter: blur(16px);
  }
  body.has-bg-image .profile-row.is-on { background: color-mix(in srgb, var(--accent) 22%, color-mix(in srgb, var(--surface) 55%, transparent)); }
  .switch { position: relative; display: inline-block; width: 42px; height: 24px; flex-shrink: 0; margin: 0; }
  .switch input { opacity: 0; width: 0; height: 0; position: absolute; }
  .switch-slider {
    position: absolute; inset: 0; background: var(--surface-3); border: 1px solid var(--border-strong);
    border-radius: 24px; cursor: pointer; transition: background .2s ease, border-color .2s ease;
  }
  .switch-slider::before {
    content: ''; position: absolute; width: 18px; height: 18px; left: 2px; top: 2px;
    background: #fff; border-radius: 50%; transition: transform .22s cubic-bezier(.3, 1.4, .5, 1);
    box-shadow: 0 1px 3px rgba(0, 0, 0, .35);
  }
  :root:not(.light) .switch-slider::before { background: #d9d7e6; }
  .switch input:checked + .switch-slider { background: var(--grad); border-color: transparent; }
  .switch input:checked + .switch-slider::before { transform: translateX(18px); background: #fff; }
  .version-tag { display: none; }

  /* ---------- dialogs ---------- */
  .confirm-overlay {
    position: fixed; inset: 0; background: rgba(6, 5, 12, .6); backdrop-filter: blur(4px);
    display: flex; align-items: center; justify-content: center; z-index: 1000; padding: 16px;
    animation: fade-in .18s ease;
  }
  .confirm-box, .settings-box {
    background: var(--surface); border: 1px solid var(--border-strong); border-radius: 18px;
    box-shadow: 0 30px 60px -20px rgba(0, 0, 0, .6); animation: pop-in .22s cubic-bezier(.2, 1.2, .4, 1);
  }
  .confirm-box { padding: 20px; width: 340px; max-width: 100%; }
  .settings-box { padding: 18px 20px 20px; width: 420px; max-width: 100%; max-height: 100%; overflow-y: auto; }
  .settings-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 14px; }
  .settings-head h2 { margin: 0; font-size: 18px; }
  @keyframes fade-in { from { opacity: 0; } }
  @keyframes pop-in { from { opacity: 0; transform: translateY(8px) scale(.97); } }
  .confirm-message { font-size: 14px; line-height: 1.5; font-weight: 500; }
  .dialog-buttons { display: flex; justify-content: flex-end; gap: 8px; margin-top: 18px; }
  .dialog-buttons button { min-width: 96px; }
  /* Indeterminate progress bar for update-installing-overlay — there's no
     real byte/step progress to report (download size and dpkg/msiexec
     both run opaque to the GUI), so this only needs to read as "still
     working," not track an actual percentage. */
  .progress-track {
    margin-top: 16px; height: 5px; border-radius: 3px; overflow: hidden;
    background: var(--surface-3);
  }
  .progress-fill {
    height: 100%; width: 40%; border-radius: 3px; background: var(--grad);
    animation: progress-indeterminate 1.3s ease-in-out infinite;
  }
  @keyframes progress-indeterminate {
    0%   { transform: translateX(-100%); }
    100% { transform: translateX(250%); }
  }
  /* ---------- space background (dark theme) ---------- */
  /* A starfield canvas (the starfield script) over slowly drifting
     accent-colored nebulae, behind everything; panels turn into frosted
     glass so it shows through. Off in the light theme, under a
     background photo, or when switched off in Settings. */
  .space, .nebula { position: fixed; inset: 0; pointer-events: none; z-index: 0; display: none; }
  body.space-on .space, body.space-on .nebula { display: block; }
  .card { position: relative; z-index: 1; }
  body.space-on {
    background: radial-gradient(140% 100% at 50% 0%, #0f0b22 0%, #07060e 55%, #040309 100%);
  }
  .nebula { overflow: hidden; }
  .nebula::before, .nebula::after, .nebula span {
    content: ''; position: absolute; border-radius: 50%;
  }
  .nebula::before {
    width: 95vmax; height: 70vmax; left: -35vmax; top: -30vmax;
    background: radial-gradient(closest-side, color-mix(in srgb, var(--accent) 26%, transparent), color-mix(in srgb, var(--accent) 7%, transparent) 60%, transparent);
  }
  .nebula::after {
    width: 90vmax; height: 75vmax; right: -40vmax; bottom: -35vmax;
    background: radial-gradient(closest-side, color-mix(in srgb, var(--accent-dark) 22%, transparent), color-mix(in srgb, var(--accent-dark) 5%, transparent) 60%, transparent);
  }
  .nebula span.n-blue {
    width: 60vmax; height: 40vmax; right: -15vmax; top: 10vmax;
    background: radial-gradient(closest-side, rgba(60, 110, 255, .13), rgba(60, 110, 255, .03) 60%, transparent);
  }
  /* A faint Milky Way band across the window. */
  .nebula span.n-band {
    width: 160vmax; height: 26vmax; left: -30vmax; top: 30%;
    transform: rotate(-24deg); border-radius: 50%;
    background: radial-gradient(closest-side, rgba(220, 210, 255, .07), rgba(220, 210, 255, .02) 55%, transparent);
  }
  body.space-on .panel,
  body.space-on .profile-row {
    /* No backdrop-filter here: re-blurring the animated sky behind every
       panel each frame is what made this expensive. */
    background: color-mix(in srgb, var(--surface) 86%, transparent);
    border-color: rgba(255, 255, 255, .09);
  }
  body.space-on .profile-row.is-on {
    background: linear-gradient(110deg, color-mix(in srgb, var(--accent) 20%, color-mix(in srgb, var(--surface) 86%, transparent)), color-mix(in srgb, var(--surface) 86%, transparent) 70%);
  }
  body.space-on .profile-row:hover { border-color: rgba(255, 255, 255, .18); }
  body.space-on .profile-row.selected { border-color: color-mix(in srgb, var(--accent) 65%, transparent); }
  /* Stars over a background photo: just the starfield canvas, no nebulae
     or dark sky, so the photo stays the background. The panels drop
     has-bg-image's backdrop blur for the same reason as space-on above
     and get more opaque instead, so the stars pass behind them. */
  body.stars-on .space { display: block; }
  body.stars-on .panel,
  body.stars-on .profile-row {
    background: color-mix(in srgb, var(--surface) 86%, transparent); backdrop-filter: none;
  }
  body.stars-on .profile-row.is-on { background: color-mix(in srgb, var(--accent) 22%, color-mix(in srgb, var(--surface) 84%, transparent)); }
  /* Only the endless animations — pausing the one-shot entrance ones
     would freeze them on their first, invisible frame. */
  body.paused .orb-ring, body.paused .status-text::before,
  body.paused .tunnel-head::before, body.paused .profile-row-connect-btn::after { animation-play-state: paused; }
  @media (prefers-reduced-motion: reduce) {
    *, *::before, *::after { animation: none !important; transition: none !important; }
  }
</style>
</head>
<body>

<!-- Line icons (lucide-style strokes), referenced via <use href="#i-…">. -->
<svg width="0" height="0" style="position:absolute" aria-hidden="true">
  <symbol id="i-gear" viewBox="0 0 24 24"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></symbol>
  <symbol id="i-plus" viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></symbol>
  <symbol id="i-pencil" viewBox="0 0 24 24"><path d="M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z"/><path d="m15 5 4 4"/></symbol>
  <symbol id="i-apps" viewBox="0 0 24 24"><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></symbol>
  <symbol id="i-globe" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="M2 12h20M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></symbol>
  <symbol id="i-trash" viewBox="0 0 24 24"><path d="M3 6h18M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M10 11v6M14 11v6"/></symbol>
  <symbol id="i-back" viewBox="0 0 24 24"><path d="m15 18-6-6 6-6"/></symbol>
  <symbol id="i-chev" viewBox="0 0 24 24"><path d="m9 18 6-6-6-6"/></symbol>
  <symbol id="i-check" viewBox="0 0 24 24"><path d="M20 6 9 17l-5-5"/></symbol>
  <symbol id="i-x" viewBox="0 0 24 24"><path d="M18 6 6 18M6 6l12 12"/></symbol>
  <symbol id="i-search" viewBox="0 0 24 24"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></symbol>
  <symbol id="i-file" viewBox="0 0 24 24"><path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4M12 18v-6M9 15l3-3 3 3"/></symbol>
  <symbol id="i-key" viewBox="0 0 24 24"><rect x="2" y="6" width="20" height="12" rx="2"/><path d="M6 10h.01M10 10h.01M14 10h.01M18 10h.01M8 14h8"/></symbol>
  <symbol id="i-bolt" viewBox="0 0 24 24"><path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z"/></symbol>
  <symbol id="i-tag" viewBox="0 0 24 24"><path d="M12.6 2.6A2 2 0 0 0 11.2 2H4a2 2 0 0 0-2 2v7.2a2 2 0 0 0 .6 1.4l8.7 8.7a2.4 2.4 0 0 0 3.4 0l6.6-6.6a2.4 2.4 0 0 0 0-3.4z"/><circle cx="7.5" cy="7.5" r=".5" fill="currentColor"/></symbol>
  <symbol id="i-download" viewBox="0 0 24 24"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/></symbol>
  <symbol id="i-save" viewBox="0 0 24 24"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><path d="M17 21v-8H7v8M7 3v5h8"/></symbol>
  <symbol id="i-refresh" viewBox="0 0 24 24"><path d="M21 12a9 9 0 0 1-15 6.7L3 16M3 12a9 9 0 0 1 15-6.7L21 8M21 3v5h-5M3 21v-5h5"/></symbol>
  <symbol id="i-down" viewBox="0 0 24 24"><path d="M12 5v14M19 12l-7 7-7-7"/></symbol>
  <symbol id="i-up" viewBox="0 0 24 24"><path d="M12 19V5M5 12l7-7 7 7"/></symbol>
  <symbol id="i-image" viewBox="0 0 24 24"><rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="9" cy="9" r="2"/><path d="m21 15-3.1-3.1a2 2 0 0 0-2.8 0L6 21"/></symbol>
  <!-- The Spectrune rune (logo variant "Космос", 2026-10-19): fill comes
       from the <use>; s-rune-shine is the glass highlight laid over it. -->
  <symbol id="s-rune" viewBox="0 0 256 256"><polygon points="160,40 196,40 120,104 176,132 96,216 60,216 132,148 76,120"/></symbol>
  <symbol id="s-rune-shine" viewBox="0 0 256 256"><polygon points="160,40 196,40 120,104 176,132 96,216 60,216 132,148 76,120" fill="url(#runeShine)"/></symbol>
  <linearGradient id="runeGrad" gradientUnits="userSpaceOnUse" x1="40" y1="30" x2="216" y2="226">
    <stop offset="0" stop-color="#b06bff"/><stop offset=".45" stop-color="#8b3dff"/><stop offset="1" stop-color="#ff2d55"/>
  </linearGradient>
  <linearGradient id="runeGrey" gradientUnits="userSpaceOnUse" x1="40" y1="30" x2="216" y2="226">
    <stop offset="0" stop-color="#c3c7cc"/><stop offset=".5" stop-color="#9aa0a6"/><stop offset="1" stop-color="#7a8086"/>
  </linearGradient>
  <linearGradient id="runeShine" gradientUnits="userSpaceOnUse" x1="60" y1="40" x2="150" y2="150">
    <stop offset="0" stop-color="#fff" stop-opacity=".5"/><stop offset=".5" stop-color="#fff" stop-opacity=".05"/><stop offset="1" stop-color="#fff" stop-opacity="0"/>
  </linearGradient>
</svg>

<div class="nebula"><span class="n-blue"></span><span class="n-band"></span></div>
<canvas class="space" id="space"></canvas>
<div class="card">
<div class="topbar">
  <div class="brand">
    <img class="app-tile" src="data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCAyNTYgMjU2IiB3aWR0aD0iMjU2IiBoZWlnaHQ9IjI1NiI+PGRlZnM+PHJhZGlhbEdyYWRpZW50IGlkPSJ0aWxlIiBjeD0iLjMiIGN5PSIuMiIgcj0iMSI+PHN0b3Agb2Zmc2V0PSIwIiBzdG9wLWNvbG9yPSIjMjQxYTNmIi8+PHN0b3Agb2Zmc2V0PSIuNiIgc3RvcC1jb2xvcj0iIzExMGQxZiIvPjxzdG9wIG9mZnNldD0iMSIgc3RvcC1jb2xvcj0iIzA3MDYwZCIvPjwvcmFkaWFsR3JhZGllbnQ+PGZpbHRlciBpZD0iZ2xvdyIgeD0iLTUwJSIgeT0iLTUwJSIgd2lkdGg9IjIwMCUiIGhlaWdodD0iMjAwJSI+PGZlR2F1c3NpYW5CbHVyIHN0ZERldmlhdGlvbj0iOSIvPjwvZmlsdGVyPjxsaW5lYXJHcmFkaWVudCBpZD0iZyIgeDE9IjQwIiB5MT0iMzAiIHgyPSIyMTYiIHkyPSIyMjYiIGdyYWRpZW50VW5pdHM9InVzZXJTcGFjZU9uVXNlIj48c3RvcCBvZmZzZXQ9IjAiIHN0b3AtY29sb3I9IiNiMDZiZmYiLz48c3RvcCBvZmZzZXQ9Ii40NSIgc3RvcC1jb2xvcj0iIzhiM2RmZiIvPjxzdG9wIG9mZnNldD0iMSIgc3RvcC1jb2xvcj0iI2ZmMmQ1NSIvPjwvbGluZWFyR3JhZGllbnQ+PHJhZGlhbEdyYWRpZW50IGlkPSJuZWIiIGN4PSIuNzUiIGN5PSIuOCIgcj0iLjciPjxzdG9wIG9mZnNldD0iMCIgc3RvcC1jb2xvcj0iI2ZmMmQ1NSIgc3RvcC1vcGFjaXR5PSIuMzUiLz48c3RvcCBvZmZzZXQ9IjEiIHN0b3AtY29sb3I9IiNmZjJkNTUiIHN0b3Atb3BhY2l0eT0iMCIvPjwvcmFkaWFsR3JhZGllbnQ+PGxpbmVhckdyYWRpZW50IGlkPSJnbCIgeDE9IjYwIiB5MT0iNDAiIHgyPSIxNTAiIHkyPSIxNTAiIGdyYWRpZW50VW5pdHM9InVzZXJTcGFjZU9uVXNlIj48c3RvcCBvZmZzZXQ9IjAiIHN0b3AtY29sb3I9IiNmZmYiIHN0b3Atb3BhY2l0eT0iLjU1Ii8+PHN0b3Agb2Zmc2V0PSIuNSIgc3RvcC1jb2xvcj0iI2ZmZiIgc3RvcC1vcGFjaXR5PSIuMDUiLz48c3RvcCBvZmZzZXQ9IjEiIHN0b3AtY29sb3I9IiNmZmYiIHN0b3Atb3BhY2l0eT0iMCIvPjwvbGluZWFyR3JhZGllbnQ+PC9kZWZzPjxyZWN0IHdpZHRoPSIyNTYiIGhlaWdodD0iMjU2IiByeD0iNTgiIGZpbGw9InVybCgjdGlsZSkiLz48cmVjdCB3aWR0aD0iMjU2IiBoZWlnaHQ9IjI1NiIgcng9IjU4IiBmaWxsPSJ1cmwoI25lYikiLz48Y2lyY2xlIGN4PSI2Ni4yIiBjeT0iMTM4LjQiIHI9IjEuMDQiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNjQiLz48Y2lyY2xlIGN4PSIxNTcuNyIgY3k9IjI1LjUiIHI9IjAuNjIiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNzkiLz48Y2lyY2xlIGN4PSI3MS4yIiBjeT0iNjUuMyIgcj0iMS43OSIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC41NiIvPjxjaXJjbGUgY3g9IjIwNy40IiBjeT0iMTIyLjQiIHI9IjEuMzciIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMzUiLz48Y2lyY2xlIGN4PSIxNTkuOCIgY3k9IjIxNC45IiByPSIxLjIzIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjczIi8+PGNpcmNsZSBjeD0iMTY4LjUiIGN5PSIyNS4xIiByPSIxLjUxIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjYzIi8+PGNpcmNsZSBjeD0iODEuMSIgY3k9IjE3LjMiIHI9IjEuNjQiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNTYiLz48Y2lyY2xlIGN4PSIxNzkuNiIgY3k9IjIxNy40IiByPSIxLjQ2IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjg1Ii8+PGNpcmNsZSBjeD0iMTAzLjIiIGN5PSIxOTkuMCIgcj0iMS4xMyIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC44NiIvPjxjaXJjbGUgY3g9IjIxNy40IiBjeT0iMzMuMCIgcj0iMC43NiIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC4zOSIvPjxjaXJjbGUgY3g9IjIzNy45IiBjeT0iMTEyLjkiIHI9IjEuMzUiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNDUiLz48Y2lyY2xlIGN4PSIxMjkuNyIgY3k9IjEwMS4xIiByPSIxLjAyIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjYzIi8+PGNpcmNsZSBjeD0iMTQ3LjkiIGN5PSIyMjMuNCIgcj0iMS40MiIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC44NSIvPjxjaXJjbGUgY3g9IjIxMi4xIiBjeT0iMjQzLjkiIHI9IjEuNDEiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMzYiLz48Y2lyY2xlIGN4PSIyMTMuMSIgY3k9IjIzNy43IiByPSIxLjY5IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjYyIi8+PGNpcmNsZSBjeD0iMTc4LjUiIGN5PSI1OS44IiByPSIxLjYwIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjYyIi8+PGNpcmNsZSBjeD0iNzcuMiIgY3k9IjI1LjAiIHI9IjEuNjIiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuODkiLz48Y2lyY2xlIGN4PSIzMC45IiBjeT0iMTk4LjkiIHI9IjEuMDkiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMzUiLz48Y2lyY2xlIGN4PSI3OS40IiBjeT0iMTkxLjQiIHI9IjEuNjUiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMjgiLz48Y2lyY2xlIGN4PSIxNTUuMCIgY3k9IjIwLjYiIHI9IjEuNDYiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNDciLz48Y2lyY2xlIGN4PSIyMTcuOSIgY3k9IjI0MS40IiByPSIxLjIxIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjkwIi8+PGNpcmNsZSBjeD0iODMuMSIgY3k9IjI4LjIiIHI9IjEuMzIiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMjciLz48Y2lyY2xlIGN4PSI1Ni42IiBjeT0iMTA2LjMiIHI9IjEuMzMiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMzUiLz48Y2lyY2xlIGN4PSIyMC4wIiBjeT0iMjE0LjgiIHI9IjAuOTgiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuODciLz48Y2lyY2xlIGN4PSIyMjEuNiIgY3k9Ijk5LjIiIHI9IjEuMTUiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNTkiLz48Y2lyY2xlIGN4PSIxNjIuMCIgY3k9IjE1MC42IiByPSIxLjI3IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjY1Ii8+PGNpcmNsZSBjeD0iMjMyLjAiIGN5PSIxMjkuNyIgcj0iMS4xMiIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC43MiIvPjxjaXJjbGUgY3g9IjY2LjEiIGN5PSI4MS4xIiByPSIxLjc3IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjU5Ii8+PGNpcmNsZSBjeD0iMTM5LjQiIGN5PSIxMi43IiByPSIxLjEwIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjYzIi8+PGNpcmNsZSBjeD0iMTQuNyIgY3k9IjE1NS4zIiByPSIxLjM2IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjI5Ii8+PGNpcmNsZSBjeD0iMTU4LjEiIGN5PSIxMjAuMCIgcj0iMS40MiIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC40OCIvPjxjaXJjbGUgY3g9IjE3Ni44IiBjeT0iMTg0LjIiIHI9IjAuNjMiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMjkiLz48Y2lyY2xlIGN4PSIxNjkuNSIgY3k9IjIzNy4zIiByPSIwLjkwIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjU1Ii8+PGNpcmNsZSBjeD0iMTQ5LjkiIGN5PSI4NS41IiByPSIxLjA0IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjQ1Ii8+PGNpcmNsZSBjeD0iOTcuMSIgY3k9IjE1MC42IiByPSIwLjk2IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjUwIi8+PGNpcmNsZSBjeD0iMTkyLjMiIGN5PSIxNi40IiByPSIxLjI4IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjczIi8+PGNpcmNsZSBjeD0iODMuMiIgY3k9IjYyLjUiIHI9IjEuNTYiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNDEiLz48Y2lyY2xlIGN4PSI1NC4yIiBjeT0iMTEyLjciIHI9IjEuNDQiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMzIiLz48Y2lyY2xlIGN4PSI4Ni4wIiBjeT0iODguOCIgcj0iMS42MCIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC41MyIvPjxjaXJjbGUgY3g9IjIxMS45IiBjeT0iNTAuMCIgcj0iMS4wMCIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC42NyIvPjxyZWN0IHg9IjEiIHk9IjEiIHdpZHRoPSIyNTQiIGhlaWdodD0iMjU0IiByeD0iNTciIGZpbGw9Im5vbmUiIHN0cm9rZT0icmdiYSgyNTUsMjU1LDI1NSwuMDkpIiBzdHJva2Utd2lkdGg9IjIiLz48cG9seWdvbiBwb2ludHM9IjE2MCw0MCAxOTYsNDAgMTIwLDEwNCAxNzYsMTMyIDk2LDIxNiA2MCwyMTYgMTMyLDE0OCA3NiwxMjAiIGZpbGw9InVybCgjZykiIGZpbHRlcj0idXJsKCNnbG93KSIgb3BhY2l0eT0iLjkiLz48cG9seWdvbiBwb2ludHM9IjE2MCw0MCAxOTYsNDAgMTIwLDEwNCAxNzYsMTMyIDk2LDIxNiA2MCwyMTYgMTMyLDE0OCA3NiwxMjAiIGZpbGw9InVybCgjZykiIHN0cm9rZT0icmdiYSgyNTUsMjU1LDI1NSwuMzUpIiBzdHJva2Utd2lkdGg9IjIiIHN0cm9rZS1saW5lam9pbj0icm91bmQiLz48cG9seWdvbiBwb2ludHM9IjE2MCw0MCAxOTYsNDAgMTIwLDEwNCAxNzYsMTMyIDk2LDIxNiA2MCwyMTYgMTMyLDE0OCA3NiwxMjAiIGZpbGw9InVybCgjZ2wpIi8+PC9zdmc+" width="28" height="28" alt="">
    <span class="brand-title">Spectrune</span>
  </div>
  <div class="topbar-actions">
    <button id="settings-toggle-btn" class="icon-btn" data-i18n-title="settingsTitle" title="Settings"><svg class="i"><use href="#i-gear"/></svg></button>
  </div>
</div>

<div id="settings-overlay" class="confirm-overlay" style="display:none">
  <div class="settings-box">
    <div class="settings-head">
      <h2 data-i18n="settingsTitle">Settings</h2>
      <button class="icon-btn icon-close" id="settings-close"><svg class="i"><use href="#i-x"/></svg></button>
    </div>
    <div id="settings-service-status" class="hint" style="display:none"></div>

    <div class="settings-section">
      <div class="settings-section-title" data-i18n="settingsAppearance">Appearance</div>
      <div class="theme-panel-row">
        <span class="theme-panel-label" data-i18n="themeMode">Mode</span>
        <div class="seg">
          <button id="theme-mode-dark" class="theme-mode-btn" data-i18n="themeDark">Dark</button>
          <button id="theme-mode-light" class="theme-mode-btn" data-i18n="themeLight">Light</button>
        </div>
      </div>
      <div class="theme-panel-row">
        <span class="theme-panel-label" data-i18n="themeAccent">Accent color</span>
        <div class="swatches">
          <button class="theme-swatch" data-accent="violet" style="background:linear-gradient(135deg,#8b3dff,#ff2d55)"></button>
          <button class="theme-swatch" data-accent="ocean" style="background:linear-gradient(135deg,#00c2ff,#2f6bff)"></button>
          <button class="theme-swatch" data-accent="emerald" style="background:linear-gradient(135deg,#00e5b0,#12b76a)"></button>
          <button class="theme-swatch" data-accent="sunset" style="background:linear-gradient(135deg,#ffb347,#ff5f6d)"></button>
          <button class="theme-swatch" data-accent="crimson" style="background:linear-gradient(135deg,#ff5c8a,#c2185b)"></button>
          <button class="theme-swatch" data-accent="slate" style="background:linear-gradient(135deg,#8a8fa3,#545a70)"></button>
        </div>
      </div>
      <div class="theme-panel-row">
        <span class="theme-panel-label" data-i18n="themeCustomColors">Custom colors (full RGB)</span>
        <input type="color" id="theme-custom-color1" value="#8b3dff">
        <input type="color" id="theme-custom-color2" value="#ff2d55">
      </div>
      <div class="theme-panel-row">
        <span class="theme-panel-label" data-i18n="themeAnim">Animations</span>
        <label class="switch">
          <input type="checkbox" id="theme-anim-toggle">
          <span class="switch-slider"></span>
        </label>
      </div>
      <div class="theme-panel-row" id="theme-space-row">
        <span class="theme-panel-label" data-i18n="themeSpace">Space background</span>
        <label class="switch">
          <input type="checkbox" id="theme-space-toggle">
          <span class="switch-slider"></span>
        </label>
      </div>
      <div class="theme-panel-row">
        <span class="theme-panel-label" data-i18n="themeBackground">Background photo</span>
        <button id="btn-choose-bg" class="small"><svg class="i sm"><use href="#i-image"/></svg><span data-i18n="themeChooseBg">Choose photo…</span></button>
        <button id="btn-clear-bg" class="small ghost" data-i18n="themeClearBg">Clear</button>
      </div>
      <div class="theme-panel-row" id="theme-bg-fit-row" style="display:none">
        <span class="theme-panel-label" data-i18n="themeBgFit">Fit</span>
        <div class="seg">
          <button id="theme-bg-fit-contain" class="theme-mode-btn" data-i18n="themeBgFitContain">Whole photo</button>
          <button id="theme-bg-fit-cover" class="theme-mode-btn" data-i18n="themeBgFitCover">Fill (crop)</button>
        </div>
      </div>
    </div>

    <div class="settings-section">
      <div class="settings-section-title" data-i18n="settingsSystem">System</div>
      <div class="theme-panel-row">
        <span class="theme-panel-label" data-i18n="settingsLanguage">Language</span>
        <div class="seg lang-switch">
          <button id="lang-en">English</button>
          <button id="lang-ru">Русский</button>
        </div>
      </div>
      <div class="theme-panel-row" id="settings-autostart-row" style="display:none">
        <span class="theme-panel-label" data-i18n="settingsAutostart">Launch on Windows startup</span>
        <label class="switch">
          <input type="checkbox" id="settings-autostart-toggle">
          <span class="switch-slider"></span>
        </label>
      </div>
    </div>

    <div class="settings-section">
      <div class="about-row">
        <img class="app-tile" src="data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCAyNTYgMjU2IiB3aWR0aD0iMjU2IiBoZWlnaHQ9IjI1NiI+PGRlZnM+PHJhZGlhbEdyYWRpZW50IGlkPSJ0aWxlIiBjeD0iLjMiIGN5PSIuMiIgcj0iMSI+PHN0b3Agb2Zmc2V0PSIwIiBzdG9wLWNvbG9yPSIjMjQxYTNmIi8+PHN0b3Agb2Zmc2V0PSIuNiIgc3RvcC1jb2xvcj0iIzExMGQxZiIvPjxzdG9wIG9mZnNldD0iMSIgc3RvcC1jb2xvcj0iIzA3MDYwZCIvPjwvcmFkaWFsR3JhZGllbnQ+PGZpbHRlciBpZD0iZ2xvdyIgeD0iLTUwJSIgeT0iLTUwJSIgd2lkdGg9IjIwMCUiIGhlaWdodD0iMjAwJSI+PGZlR2F1c3NpYW5CbHVyIHN0ZERldmlhdGlvbj0iOSIvPjwvZmlsdGVyPjxsaW5lYXJHcmFkaWVudCBpZD0iZyIgeDE9IjQwIiB5MT0iMzAiIHgyPSIyMTYiIHkyPSIyMjYiIGdyYWRpZW50VW5pdHM9InVzZXJTcGFjZU9uVXNlIj48c3RvcCBvZmZzZXQ9IjAiIHN0b3AtY29sb3I9IiNiMDZiZmYiLz48c3RvcCBvZmZzZXQ9Ii40NSIgc3RvcC1jb2xvcj0iIzhiM2RmZiIvPjxzdG9wIG9mZnNldD0iMSIgc3RvcC1jb2xvcj0iI2ZmMmQ1NSIvPjwvbGluZWFyR3JhZGllbnQ+PHJhZGlhbEdyYWRpZW50IGlkPSJuZWIiIGN4PSIuNzUiIGN5PSIuOCIgcj0iLjciPjxzdG9wIG9mZnNldD0iMCIgc3RvcC1jb2xvcj0iI2ZmMmQ1NSIgc3RvcC1vcGFjaXR5PSIuMzUiLz48c3RvcCBvZmZzZXQ9IjEiIHN0b3AtY29sb3I9IiNmZjJkNTUiIHN0b3Atb3BhY2l0eT0iMCIvPjwvcmFkaWFsR3JhZGllbnQ+PGxpbmVhckdyYWRpZW50IGlkPSJnbCIgeDE9IjYwIiB5MT0iNDAiIHgyPSIxNTAiIHkyPSIxNTAiIGdyYWRpZW50VW5pdHM9InVzZXJTcGFjZU9uVXNlIj48c3RvcCBvZmZzZXQ9IjAiIHN0b3AtY29sb3I9IiNmZmYiIHN0b3Atb3BhY2l0eT0iLjU1Ii8+PHN0b3Agb2Zmc2V0PSIuNSIgc3RvcC1jb2xvcj0iI2ZmZiIgc3RvcC1vcGFjaXR5PSIuMDUiLz48c3RvcCBvZmZzZXQ9IjEiIHN0b3AtY29sb3I9IiNmZmYiIHN0b3Atb3BhY2l0eT0iMCIvPjwvbGluZWFyR3JhZGllbnQ+PC9kZWZzPjxyZWN0IHdpZHRoPSIyNTYiIGhlaWdodD0iMjU2IiByeD0iNTgiIGZpbGw9InVybCgjdGlsZSkiLz48cmVjdCB3aWR0aD0iMjU2IiBoZWlnaHQ9IjI1NiIgcng9IjU4IiBmaWxsPSJ1cmwoI25lYikiLz48Y2lyY2xlIGN4PSI2Ni4yIiBjeT0iMTM4LjQiIHI9IjEuMDQiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNjQiLz48Y2lyY2xlIGN4PSIxNTcuNyIgY3k9IjI1LjUiIHI9IjAuNjIiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNzkiLz48Y2lyY2xlIGN4PSI3MS4yIiBjeT0iNjUuMyIgcj0iMS43OSIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC41NiIvPjxjaXJjbGUgY3g9IjIwNy40IiBjeT0iMTIyLjQiIHI9IjEuMzciIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMzUiLz48Y2lyY2xlIGN4PSIxNTkuOCIgY3k9IjIxNC45IiByPSIxLjIzIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjczIi8+PGNpcmNsZSBjeD0iMTY4LjUiIGN5PSIyNS4xIiByPSIxLjUxIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjYzIi8+PGNpcmNsZSBjeD0iODEuMSIgY3k9IjE3LjMiIHI9IjEuNjQiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNTYiLz48Y2lyY2xlIGN4PSIxNzkuNiIgY3k9IjIxNy40IiByPSIxLjQ2IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjg1Ii8+PGNpcmNsZSBjeD0iMTAzLjIiIGN5PSIxOTkuMCIgcj0iMS4xMyIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC44NiIvPjxjaXJjbGUgY3g9IjIxNy40IiBjeT0iMzMuMCIgcj0iMC43NiIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC4zOSIvPjxjaXJjbGUgY3g9IjIzNy45IiBjeT0iMTEyLjkiIHI9IjEuMzUiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNDUiLz48Y2lyY2xlIGN4PSIxMjkuNyIgY3k9IjEwMS4xIiByPSIxLjAyIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjYzIi8+PGNpcmNsZSBjeD0iMTQ3LjkiIGN5PSIyMjMuNCIgcj0iMS40MiIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC44NSIvPjxjaXJjbGUgY3g9IjIxMi4xIiBjeT0iMjQzLjkiIHI9IjEuNDEiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMzYiLz48Y2lyY2xlIGN4PSIyMTMuMSIgY3k9IjIzNy43IiByPSIxLjY5IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjYyIi8+PGNpcmNsZSBjeD0iMTc4LjUiIGN5PSI1OS44IiByPSIxLjYwIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjYyIi8+PGNpcmNsZSBjeD0iNzcuMiIgY3k9IjI1LjAiIHI9IjEuNjIiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuODkiLz48Y2lyY2xlIGN4PSIzMC45IiBjeT0iMTk4LjkiIHI9IjEuMDkiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMzUiLz48Y2lyY2xlIGN4PSI3OS40IiBjeT0iMTkxLjQiIHI9IjEuNjUiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMjgiLz48Y2lyY2xlIGN4PSIxNTUuMCIgY3k9IjIwLjYiIHI9IjEuNDYiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNDciLz48Y2lyY2xlIGN4PSIyMTcuOSIgY3k9IjI0MS40IiByPSIxLjIxIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjkwIi8+PGNpcmNsZSBjeD0iODMuMSIgY3k9IjI4LjIiIHI9IjEuMzIiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMjciLz48Y2lyY2xlIGN4PSI1Ni42IiBjeT0iMTA2LjMiIHI9IjEuMzMiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMzUiLz48Y2lyY2xlIGN4PSIyMC4wIiBjeT0iMjE0LjgiIHI9IjAuOTgiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuODciLz48Y2lyY2xlIGN4PSIyMjEuNiIgY3k9Ijk5LjIiIHI9IjEuMTUiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNTkiLz48Y2lyY2xlIGN4PSIxNjIuMCIgY3k9IjE1MC42IiByPSIxLjI3IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjY1Ii8+PGNpcmNsZSBjeD0iMjMyLjAiIGN5PSIxMjkuNyIgcj0iMS4xMiIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC43MiIvPjxjaXJjbGUgY3g9IjY2LjEiIGN5PSI4MS4xIiByPSIxLjc3IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjU5Ii8+PGNpcmNsZSBjeD0iMTM5LjQiIGN5PSIxMi43IiByPSIxLjEwIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjYzIi8+PGNpcmNsZSBjeD0iMTQuNyIgY3k9IjE1NS4zIiByPSIxLjM2IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjI5Ii8+PGNpcmNsZSBjeD0iMTU4LjEiIGN5PSIxMjAuMCIgcj0iMS40MiIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC40OCIvPjxjaXJjbGUgY3g9IjE3Ni44IiBjeT0iMTg0LjIiIHI9IjAuNjMiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMjkiLz48Y2lyY2xlIGN4PSIxNjkuNSIgY3k9IjIzNy4zIiByPSIwLjkwIiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjU1Ii8+PGNpcmNsZSBjeD0iMTQ5LjkiIGN5PSI4NS41IiByPSIxLjA0IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjQ1Ii8+PGNpcmNsZSBjeD0iOTcuMSIgY3k9IjE1MC42IiByPSIwLjk2IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjUwIi8+PGNpcmNsZSBjeD0iMTkyLjMiIGN5PSIxNi40IiByPSIxLjI4IiBmaWxsPSIjZmZmIiBvcGFjaXR5PSIwLjczIi8+PGNpcmNsZSBjeD0iODMuMiIgY3k9IjYyLjUiIHI9IjEuNTYiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuNDEiLz48Y2lyY2xlIGN4PSI1NC4yIiBjeT0iMTEyLjciIHI9IjEuNDQiIGZpbGw9IiNmZmYiIG9wYWNpdHk9IjAuMzIiLz48Y2lyY2xlIGN4PSI4Ni4wIiBjeT0iODguOCIgcj0iMS42MCIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC41MyIvPjxjaXJjbGUgY3g9IjIxMS45IiBjeT0iNTAuMCIgcj0iMS4wMCIgZmlsbD0iI2ZmZiIgb3BhY2l0eT0iMC42NyIvPjxyZWN0IHg9IjEiIHk9IjEiIHdpZHRoPSIyNTQiIGhlaWdodD0iMjU0IiByeD0iNTciIGZpbGw9Im5vbmUiIHN0cm9rZT0icmdiYSgyNTUsMjU1LDI1NSwuMDkpIiBzdHJva2Utd2lkdGg9IjIiLz48cG9seWdvbiBwb2ludHM9IjE2MCw0MCAxOTYsNDAgMTIwLDEwNCAxNzYsMTMyIDk2LDIxNiA2MCwyMTYgMTMyLDE0OCA3NiwxMjAiIGZpbGw9InVybCgjZykiIGZpbHRlcj0idXJsKCNnbG93KSIgb3BhY2l0eT0iLjkiLz48cG9seWdvbiBwb2ludHM9IjE2MCw0MCAxOTYsNDAgMTIwLDEwNCAxNzYsMTMyIDk2LDIxNiA2MCwyMTYgMTMyLDE0OCA3NiwxMjAiIGZpbGw9InVybCgjZykiIHN0cm9rZT0icmdiYSgyNTUsMjU1LDI1NSwuMzUpIiBzdHJva2Utd2lkdGg9IjIiIHN0cm9rZS1saW5lam9pbj0icm91bmQiLz48cG9seWdvbiBwb2ludHM9IjE2MCw0MCAxOTYsNDAgMTIwLDEwNCAxNzYsMTMyIDk2LDIxNiA2MCwyMTYgMTMyLDE0OCA3NiwxMjAiIGZpbGw9InVybCgjZ2wpIi8+PC9zdmc+" width="40" height="40" alt="">
        <div class="about-text">
          <div class="about-name">Spectrune</div>
          <span id="update-status-text" class="hint" style="margin:0"></span>
        </div>
        <button id="btn-check-update" class="small"><svg class="i sm"><use href="#i-refresh"/></svg><span data-i18n="updateCheckButton">Check for updates</span></button>
      </div>
    </div>
  </div>
</div>

<div id="view-list" class="view active">
  <div id="list-error" class="error" style="display:none"></div>
  <div class="hero panel" id="hero">
    <div class="hero-main">
      <div class="orb" id="hero-orb">
        <canvas class="orb-dust" id="orb-dust"></canvas>
        <span class="orb-ring"></span>
        <span class="brand-icon">
          <svg id="brand-icon-active" viewBox="0 0 256 256" style="opacity:0"><use href="#s-rune" fill="url(#runeGrad)"/><use href="#s-rune-shine"/></svg>
          <svg id="brand-icon-inactive" viewBox="0 0 256 256" style="opacity:1"><use href="#s-rune" fill="url(#runeGrey)"/><use href="#s-rune-shine"/></svg>
        </span>
      </div>
      <div class="hero-text" id="status-line">
        <div class="hero-kicker-row">
          <div class="hero-kicker" data-i18n="heroKicker">Status</div>
          <div class="hero-speed">
            <span class="sp-down" data-i18n-title="statDown"><svg class="i"><use href="#i-down"/></svg><b id="stat-down"></b></span>
            <span class="sp-up" data-i18n-title="statUp"><svg class="i"><use href="#i-up"/></svg><b id="stat-up"></b></span>
          </div>
        </div>
        <div id="status-text" class="status-text disconnected"></div>
        <div id="hero-sub" class="hero-sub"></div>
      </div>
    </div>
    <div id="included-apps-row" class="included-apps" style="display:none"></div>
  </div>
  <div class="section-head">
    <div class="section-title"><span data-i18n="profilesTitle">Profiles</span><span id="profiles-count" class="count-badge">0</span></div>
    <button id="btn-add" class="small primary"><svg class="i sm"><use href="#i-plus"/></svg><span data-i18n="addShort">Add</span></button>
  </div>
  <ul id="profile-list" class="profile-list flex-grow"></ul>
  <div class="row action-bar panel" data-reorder="list-actions">
    <button id="btn-edit" disabled><svg class="i"><use href="#i-pencil"/></svg><span data-i18n="actEdit">Edit</span></button>
    <button id="btn-apps" disabled><svg class="i"><use href="#i-apps"/></svg><span data-i18n="actApps">Apps</span></button>
    <button id="btn-domains" disabled><svg class="i"><use href="#i-globe"/></svg><span data-i18n="actDomains">Domains</span></button>
    <button id="btn-delete" class="danger-hover" disabled><svg class="i"><use href="#i-trash"/></svg><span data-i18n="delete">Delete</span></button>
  </div>
</div>

<div id="view-edit" class="view">
  <div class="view-head">
    <button class="backlink ghost" id="edit-back"><svg class="i"><use href="#i-back"/></svg><span data-i18n="back">Back</span></button>
    <h1 id="edit-title">Add profile</h1>
  </div>
  <div id="edit-error" class="error" style="display:none"></div>
  <div class="field">
    <label for="edit-name" data-i18n="name">Name</label>
    <input type="text" id="edit-name" data-i18n-placeholder="namePlaceholder">
  </div>
  <div class="group panel">
    <div class="group-row">
      <span class="group-row-label"><svg class="i"><use href="#i-bolt"/></svg><span data-i18n="autoConnect">Connect automatically on startup</span></span>
      <label class="switch">
        <input type="checkbox" id="edit-autoconnect">
        <span class="switch-slider"></span>
      </label>
    </div>
    <div class="group-row">
      <span class="group-row-label"><svg class="i"><use href="#i-key"/></svg><span data-i18n="hotkey">Hotkey</span></span>
      <div class="row" style="margin:0;flex-wrap:nowrap">
        <button id="btn-hotkey-capture" class="small kbd-btn" type="button"></button>
        <button id="btn-hotkey-clear" class="small ghost" type="button" data-i18n="hotkeyClear">Clear</button>
      </div>
    </div>
  </div>
  <div class="field-head">
    <label for="edit-config" data-i18n="configLabel">Configuration</label>
    <button id="btn-import" class="small ghost"><svg class="i sm"><use href="#i-file"/></svg><span data-i18n="importFile">Import from file…</span></button>
  </div>
  <textarea id="edit-config" class="flex-grow" spellcheck="false" style="margin-bottom:12px"></textarea>
  <div class="footer-bar" data-reorder="edit-save-cancel">
    <button id="btn-cancel" class="ghost" data-i18n="cancel">Cancel</button>
    <button id="btn-save" class="primary" data-i18n="save">Save</button>
  </div>
</div>

<div id="view-apps" class="view">
  <div class="view-head">
    <button class="backlink ghost" id="btn-apps-cancel"><svg class="i"><use href="#i-back"/></svg><span data-i18n="cancel">Cancel</span></button>
    <h1 data-i18n="applications">Applications</h1>
    <button class="primary" id="apps-back"><svg class="i sm"><use href="#i-check"/></svg><span data-i18n="done">Done</span></button>
  </div>
  <p class="hint" data-i18n="appsHint">Select the applications whose traffic should be routed through this tunnel. Click a row to toggle it, or use "Add…" to browse for one not listed here.</p>
  <div class="search">
    <svg class="i sm"><use href="#i-search"/></svg>
    <input type="text" id="apps-search" data-i18n-placeholder="searchPlaceholder" placeholder="Search installed applications…">
  </div>
  <div class="toolbar">
    <div class="row" data-reorder="apps-select" style="margin:0">
      <button id="btn-select-all" class="small" data-i18n="selectAll">Select all</button>
      <button id="btn-clear-all" class="small" data-i18n="clearSelection">Clear selection</button>
    </div>
    <span class="sep"></span>
    <select id="apps-preset-select" data-i18n-title="presetsTitle"></select>
    <button id="btn-load-preset" class="icon-btn" data-i18n-title="presetLoad"><svg class="i sm"><use href="#i-download"/></svg></button>
    <button id="btn-save-preset" class="icon-btn" data-i18n-title="presetSaveAs"><svg class="i sm"><use href="#i-save"/></svg></button>
    <button id="btn-delete-preset" class="icon-btn" data-i18n-title="presetDelete"><svg class="i sm"><use href="#i-trash"/></svg></button>
  </div>
  <div id="apps-taken-note" class="hint" style="display:none"></div>
  <ul id="apps-list" class="app-list panel flex-grow"></ul>
  <div class="list-footer">
    <span id="apps-selected-count" class="hint"></span>
    <button id="btn-browse-app" class="small"><svg class="i sm"><use href="#i-plus"/></svg><span data-i18n="browseApp">Other app…</span></button>
  </div>
</div>

<div id="view-domains" class="view">
  <div class="view-head">
    <h1 data-i18n="domainLists">Domain lists</h1>
    <button class="primary" id="domains-back"><svg class="i sm"><use href="#i-check"/></svg><span data-i18n="done">Done</span></button>
  </div>
  <p class="hint" data-i18n="domainsHint">Toggle a list on to route every domain in it through this tunnel, regardless of which application accesses it. Edit a list's domains with "Edit".</p>
  <ul id="domains-list" class="app-list panel flex-grow"></ul>
  <div class="list-footer">
    <span></span>
    <button id="btn-new-domain-list" class="small"><svg class="i sm"><use href="#i-plus"/></svg><span data-i18n="domainsNewList">New list…</span></button>
  </div>
</div>

<div id="view-domain-edit" class="view">
  <div class="view-head">
    <button class="backlink ghost" id="domain-edit-back"><svg class="i"><use href="#i-back"/></svg><span data-i18n="back">Back</span></button>
    <h1 id="domain-edit-title"></h1>
  </div>
  <p class="hint" data-i18n="domainEditHint">One domain per line (e.g. discord.com) — subdomains are matched automatically.</p>
  <textarea id="domain-edit-text" class="flex-grow" spellcheck="false" style="margin-bottom:12px"></textarea>
  <div class="footer-bar" data-reorder="domain-edit-save-cancel">
    <button id="btn-domain-edit-cancel" class="ghost" data-i18n="cancel">Cancel</button>
    <button id="btn-domain-edit-save" class="primary" data-i18n="save">Save</button>
  </div>
</div>

</div>

<div class="version-tag">v%%VERSION%%</div>

<!--
  Shown by checkBackgroundUpdate's background poll (below) the moment an
  automatic self-update starts downloading/installing — not just after a
  manual "Check for updates" click. Without this, the only visible sign
  of an automatic update used to be refreshStatus's raw IPC connection
  error the instant dpkg/msiexec restarts the daemon/service, which
  looked like a real failure (reported 2026-09-14). z-index 1000 (same
  as confirm-overlay) covers that error text underneath for the whole
  outage window.
-->
<div id="update-installing-overlay" class="confirm-overlay" style="display:none">
  <div class="confirm-box">
    <div class="confirm-message" data-i18n="updateInstallingOverlay">Installing an update. Spectrune will restart automatically…</div>
    <div class="progress-track"><div class="progress-fill"></div></div>
  </div>
</div>

<div id="confirm-overlay" class="confirm-overlay" style="display:none">
  <div class="confirm-box">
    <div id="confirm-message" class="confirm-message"></div>
    <div class="dialog-buttons">
      <button id="confirm-cancel" class="ghost" data-i18n="cancel">Cancel</button>
      <button id="confirm-ok" class="primary danger" data-i18n="delete">Delete</button>
    </div>
  </div>
</div>

<div id="prompt-overlay" class="confirm-overlay" style="display:none">
  <div class="confirm-box">
    <div id="prompt-message" class="confirm-message"></div>
    <input type="text" id="prompt-input" style="margin-top:12px">
    <div class="dialog-buttons">
      <button id="prompt-cancel" class="ghost" data-i18n="cancel">Cancel</button>
      <button id="prompt-ok" class="primary" data-i18n="save">Save</button>
    </div>
  </div>
</div>
<script>
'use strict';

var I18N = {
  en: {
    add: 'Add…', edit: 'Edit…', apps: 'Apps…', domains: 'Domains…', delete: 'Delete',
    connectedShort: 'Connected', connectingShort: 'Connecting…', errorShort: 'Error',
    routingFullTunnel: 'All traffic', routingAppsCount: 'Apps: %d',
    connect: 'Connect', disconnect: 'Disconnect',
    confirmDelete: 'Delete profile "%s"?',
    back: 'Back', addProfile: 'Add profile', editProfile: 'Edit profile',
    name: 'Name', configLabel: "Configuration",
    importFile: 'Import from file…',
    importNamePrompt: 'Name for this tunnel:', importNameHint: 'Visible only to you',
    save: 'Save', cancel: 'Cancel', nameRequired: 'A name is required.',
    done: 'Done', applications: 'Applications',
    appsHint: 'Select the applications whose traffic should be routed through this tunnel. Click a row to toggle it, or use "Other app…" to browse for one not listed here.',
    searchPlaceholder: 'Search installed applications…',
    selectAll: 'Select all', clearSelection: 'Clear selection',
    appsTakenNote: 'Apps routed by connected profile(s) %s are hidden.',
    settingsTitle: 'Settings', settingsLanguage: 'Language',
    settingsAutostart: 'Launch on Windows startup',
    updateVersionLine: 'Version %%VERSION%%', updateCheckButton: 'Check for updates',
    updateChecking: 'Checking…', updateUpToDate: 'Up to date (%s)',
    updateAvailable: 'Updating to %s…', updateCheckFailed: 'Update check failed: %s',
    updateInstallingOverlay: 'Installing an update. Spectrune will restart automatically…',
    updateSplashTitle: 'Spectrune — updating', updateSplashChecking: 'Checking for updates…',
    updateSplashInstalling: 'Installing update %s…', updateSplashUpToDate: "You're on the latest version (%s)",
    updateSplashFailed: "Couldn't check for updates: %s",
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
    themeSpace: 'Space background', themeStars: 'Stars over the photo', themeAnim: 'Animations',
    domainLists: 'Domain lists',
    domainsHint: 'Toggle a list on to route every domain in it through this tunnel, regardless of which application accesses it. Edit a list\'s domains with "Edit".',
    domainsNewList: 'New list…', domainEditHint: 'One domain per line (e.g. discord.com) — subdomains are matched automatically.',
    domainListNamePrompt: 'Name for this domain list:', domainListConfirmDelete: 'Delete domain list "%s"?',
    domainListEdit: 'Edit', domainListNoLists: 'No domain lists yet — use "New list…" to create one.',
    serviceNotRunning: 'The Spectrune service isn\'t running — nothing will connect until it is. Start it now?',
    serviceStarting: 'Starting the service…', serviceStartFailed: 'Could not start the service. Try starting it manually as an administrator.',
    startNow: 'Start',
    heroKicker: 'Status', profilesTitle: 'Profiles', addShort: 'Add',
    actEdit: 'Edit', actApps: 'Apps', actDomains: 'Domains',
    statusOff: 'Not connected', statusError: 'Connection failed',
    heroSubOff: 'Switch on a profile below, or click the logo',
    heroSubError: "The server didn't respond. Check the profile and try again.",
    heroSubOn: 'Traffic goes through %s',
    orbConnect: 'Connect "%s"', orbDisconnect: 'Disconnect all',
    emptyTitle: 'No profiles yet', emptyText: 'Drop a .conf file here, or click "Add" to paste a configuration.',
    namePlaceholder: 'e.g. Netherlands',
    browseApp: 'Other app…', presetsTitle: 'App presets',
    appsSelectedCount: 'Selected: %d', appsNoMatch: 'Nothing found',
    settingsAppearance: 'Appearance', settingsSystem: 'System',
    statDown: 'Download', statUp: 'Upload',
    unitB: 'B/s', unitKB: 'KB/s', unitMB: 'MB/s', unitGB: 'GB/s',
  },
  ru: {
    add: 'Добавить…', edit: 'Изменить…', apps: 'Приложения…', domains: 'Домены…', delete: 'Удалить',
    connectedShort: 'Подключено', connectingShort: 'Подключение…', errorShort: 'Ошибка',
    routingFullTunnel: 'Весь трафик', routingAppsCount: 'Приложений: %d',
    connect: 'Подключить', disconnect: 'Отключить',
    confirmDelete: 'Удалить профиль «%s»?',
    back: 'Назад', addProfile: 'Новый профиль', editProfile: 'Изменение профиля',
    name: 'Название', configLabel: 'Конфигурация',
    importFile: 'Импорт из файла…',
    importNamePrompt: 'Название туннеля:', importNameHint: 'Отображается только для вас',
    save: 'Сохранить', cancel: 'Отмена', nameRequired: 'Укажите название.',
    done: 'Готово', applications: 'Приложения',
    appsHint: 'Выберите приложения, трафик которых должен идти через этот туннель. Нажмите на строку, чтобы переключить, или «Другое приложение…», чтобы указать путь вручную.',
    searchPlaceholder: 'Поиск установленных приложений…',
    selectAll: 'Выбрать все', clearSelection: 'Очистить выбор',
    appsTakenNote: 'Скрыты приложения, которые уже идут через подключённые профили: %s.',
    settingsTitle: 'Настройки', settingsLanguage: 'Язык',
    settingsAutostart: 'Запускать при старте Windows',
    updateVersionLine: 'Версия %%VERSION%%', updateCheckButton: 'Проверить обновления',
    updateChecking: 'Проверка…', updateUpToDate: 'Актуальная версия (%s)',
    updateAvailable: 'Обновление до %s…', updateCheckFailed: 'Ошибка проверки: %s',
    updateInstallingOverlay: 'Устанавливается обновление. Spectrune перезапустится автоматически…',
    updateSplashTitle: 'Spectrune — обновление', updateSplashChecking: 'Проверка обновлений…',
    updateSplashInstalling: 'Устанавливается обновление %s…', updateSplashUpToDate: 'У вас последняя версия (%s)',
    updateSplashFailed: 'Не удалось проверить обновления: %s',
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
    themeSpace: 'Космический фон', themeStars: 'Звёзды поверх фото', themeAnim: 'Анимации',
    domainLists: 'Списки доменов',
    domainsHint: 'Включите список, чтобы направить все его домены через этот туннель — независимо от того, какое приложение к ним обращается. Кнопка «Изменить» редактирует домены списка.',
    domainsNewList: 'Новый список…', domainEditHint: 'По одному домену на строку (например, discord.com) — поддомены учитываются автоматически.',
    domainListNamePrompt: 'Название списка доменов:', domainListConfirmDelete: 'Удалить список доменов «%s»?',
    domainListEdit: 'Изменить', domainListNoLists: 'Списков доменов пока нет — нажмите «Новый список…», чтобы создать.',
    serviceNotRunning: 'Служба Spectrune не запущена — без неё ничего не подключится. Запустить сейчас?',
    serviceStarting: 'Запуск службы…', serviceStartFailed: 'Не удалось запустить службу. Попробуйте запустить её вручную от имени администратора.',
    startNow: 'Запустить',
    heroKicker: 'Состояние', profilesTitle: 'Профили', addShort: 'Добавить',
    actEdit: 'Изменить', actApps: 'Приложения', actDomains: 'Домены',
    statusOff: 'Не подключено', statusError: 'Ошибка подключения',
    heroSubOff: 'Включите профиль ниже или нажмите на логотип',
    heroSubError: 'Сервер не ответил. Проверьте профиль и попробуйте ещё раз.',
    heroSubOn: 'Трафик идёт через %s',
    orbConnect: 'Подключить «%s»', orbDisconnect: 'Отключить всё',
    emptyTitle: 'Пока нет профилей', emptyText: 'Перетащите сюда .conf файл или нажмите «Добавить», чтобы вставить конфигурацию.',
    namePlaceholder: 'Например, Нидерланды',
    browseApp: 'Другое приложение…', presetsTitle: 'Наборы приложений',
    appsSelectedCount: 'Выбрано: %d', appsNoMatch: 'Ничего не найдено',
    settingsAppearance: 'Оформление', settingsSystem: 'Система',
    statDown: 'Загрузка', statUp: 'Отдача',
    unitB: 'Б/с', unitKB: 'КБ/с', unitMB: 'МБ/с', unitGB: 'ГБ/с',
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

// How long to wait for a handshake before giving up and reporting an
// error instead of sitting on "Connecting…" forever — see refreshStatus.
var CONNECT_TIMEOUT_MS = 20000;

var state = {
  profiles: [],
  selected: null,
  connectBusy: false, // a connect()/disconnect() call is in flight — keep
                       // every row's connect zone disabled regardless of
                       // what the 3s status poll thinks, so a slow
                       // Connect can't be double-clicked into "already
                       // connected"
  // active maps every connected profile's name to whether its WireGuard
  // handshake has completed; activeOrder is the same names in connect
  // order. Several profiles can be connected at once — see
  // applyStateReply for how a backend that only reports one (Linux) maps
  // onto this.
  active: {},
  activeOrder: [],
  stats: {},          // profile name -> {rx, tx, since} from the last getState
  stateSeen: false,   // set after the first getState, so opening the window
                      // on an already-up tunnel doesn't play the connect burst
  // connectStartedAt/errorProfiles back the "give up and say so" timeout
  // in refreshStatus below, per profile — a dead/blocked server (e.g. a
  // router whitelist that only lets specific traffic out) used to leave
  // the UI on "Connecting…" forever with no way to tell the two apart,
  // and left the bridge's own default-route override in place
  // indefinitely too. Reported 2026-09-14.
  connectStartedAt: {},
  errorProfiles: {}, // which rows get the error label — see refreshStatus
  // The profile whose connect() call is still in flight. The service only
  // lists a tunnel once Connect has done its setup, which takes seconds
  // on some machines, so until then this keeps the profile shown as
  // "Connecting…" instead of the UI sitting on "Not connected".
  connectingName: null,
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
  takenApps: {}, // see loadTakenApps
  lang: (function() { try { return localStorage.getItem('lang') || 'en'; } catch (e) { return 'en'; } })(),
  theme: (function() {
    var fallback = { mode: 'dark', accent: 'violet', customColor1: '#8b3dff', customColor2: '#ff2d55', bgImage: '', bgFit: 'contain', space: true, anim: true };
    try {
      return {
        mode: localStorage.getItem('themeMode') || fallback.mode,
        accent: localStorage.getItem('themeAccent') || fallback.accent,
        customColor1: localStorage.getItem('themeCustomColor1') || fallback.customColor1,
        customColor2: localStorage.getItem('themeCustomColor2') || fallback.customColor2,
        bgImage: localStorage.getItem('themeBgImage') || fallback.bgImage,
        bgFit: localStorage.getItem('themeBgFit') || fallback.bgFit,
        space: localStorage.getItem('themeSpace') !== '0',
        anim: localStorage.getItem('themeAnim') !== '0',
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

function markConnecting() {
  var n = state.connectingName;
  if (n === null || isActive(n)) return;
  state.active[n] = false;
  state.activeOrder.push(n);
}

function isActive(name) {
  return Object.prototype.hasOwnProperty.call(state.active, name);
}

// applyStateReply copies a getState() reply into state.active. Tunnels
// lists every connected profile (Windows); a backend without it (Linux)
// only ever has the one described by Connected/ProfileName/HandshakeOK.
function applyStateReply(s) {
  var tunnels = s.Tunnels;
  if (!tunnels) {
    tunnels = s.Connected && s.ProfileName ? [{Name: s.ProfileName, HandshakeOK: s.HandshakeOK,
      RxBytes: s.RxBytes, TxBytes: s.TxBytes, Since: s.Since}] : [];
  }
  var was = state.active;
  state.active = {};
  state.activeOrder = [];
  state.stats = {};
  var justUp = false;
  tunnels.forEach(function(ts) {
    state.active[ts.Name] = !!ts.HandshakeOK;
    state.activeOrder.push(ts.Name);
    state.stats[ts.Name] = { rx: ts.RxBytes || 0, tx: ts.TxBytes || 0, since: ts.Since || 0 };
    if (ts.HandshakeOK && !was[ts.Name]) justUp = true;
  });
  markConnecting();
  if (justUp && state.stateSeen) celebrateConnect();
  state.stateSeen = true;
  sampleSpeed();
}

// ---------- live speed (hero) ----------
// Speed is the change in the connected tunnels' byte totals between two
// getState polls, lightly smoothed so the numbers don't flicker.
var speedLast = null; // {t, rx, tx}
var speedNow = null;  // {down, up} bytes/s

function sampleSpeed() {
  var rx = 0, tx = 0, up = false;
  state.activeOrder.forEach(function(n) {
    if (!state.active[n]) return;
    up = true;
    rx += state.stats[n].rx;
    tx += state.stats[n].tx;
  });
  var now = Date.now();
  if (!up) { speedLast = null; speedNow = null; renderSpeed(); return; }
  if (speedLast && rx >= speedLast.rx && tx >= speedLast.tx && now > speedLast.t) {
    var dt = (now - speedLast.t) / 1000;
    var down = (rx - speedLast.rx) / dt, upB = (tx - speedLast.tx) / dt;
    if (speedNow) { down = speedNow.down * .6 + down * .4; upB = speedNow.up * .6 + upB * .4; }
    speedNow = { down: down, up: upB };
  }
  speedLast = { t: now, rx: rx, tx: tx };
  renderSpeed();
}

function formatSpeed(bps) {
  var units = ['unitB', 'unitKB', 'unitMB', 'unitGB'], i = 0;
  while (bps >= 1000 && i < units.length - 1) { bps /= 1024; i++; }
  var num = bps < 10 && i > 0 ? bps.toFixed(1) : String(Math.round(bps));
  return num.replace('.', state.lang === 'ru' ? ',' : '.') + '<small>' + t(units[i]) + '</small>';
}

function renderSpeed() {
  var sp = speedNow || { down: 0, up: 0 };
  $('stat-down').innerHTML = formatSpeed(sp.down);
  $('stat-up').innerHTML = formatSpeed(sp.up);
}

// setAnimationsPaused stops every animation while the window can't be
// seen — hidden to the tray, minimized, or (on Linux) any time WebKit
// reports the page hidden. Windows' WebView2 keeps rendering a hidden
// window, so webui_windows.go calls window.setWindowVisible from the
// window's show/hide/minimize messages.
var windowHidden = false;
var reducedMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
function setAnimationsPaused() {
  var hidden = windowHidden || document.hidden;
  var off = !state.theme.anim || reducedMotion;
  document.body.classList.toggle('paused', hidden || off);
  var mode = hidden ? 'stop' : off ? 'still' : 'run';
  orbDust.setMode(mode);
  starfield.setMode(mode);
}
window.setWindowVisible = function(visible) { windowHidden = !visible; setAnimationsPaused(); };
document.addEventListener('visibilitychange', setAnimationsPaused);

// celebrateConnect plays when a tunnel's handshake completes: the logo's
// dust bursts outward, the stars jump to warp, and the hero flashes.
function celebrateConnect() {
  if (document.body.classList.contains('paused')) return; // animations off or window hidden
  orbDust.burst();
  starfield.warp();
  var hero = $('hero');
  hero.classList.remove('flash');
  void hero.offsetWidth; // restart the animation
  hero.classList.add('flash');
}

// disconnectOne disconnects just name, leaving other connected profiles
// alone. A backend that can only run one profile (no disconnectProfile
// binding — Linux) just disconnects.
function disconnectOne(name) {
  return window.disconnectProfile ? disconnectProfile(name) : disconnect();
}

function setRowStatus(statusEl, name) {
  if (isActive(name)) {
    // Short, unparametrized labels — not the tf('connected'/'connecting',
    // name) versions the old bottom status bar used, since this row
    // already shows the name right above; reusing those templates here
    // literally printed "%s" (nothing filled it in — found live
    // 2026-09-04).
    var ok = state.active[name];
    statusEl.textContent = ok ? t('connectedShort') : t('connectingShort');
    statusEl.className = 'profile-row-status ' + (ok ? 'connected' : 'connecting');
  } else if (state.errorProfiles[name]) {
    // See refreshStatus's connect-timeout branch.
    statusEl.textContent = t('errorShort');
    statusEl.className = 'profile-row-status error';
  } else {
    statusEl.textContent = '';
    statusEl.className = 'profile-row-status';
  }
}

function renderProfileList() {
  var ul = $('profile-list');
  ul.innerHTML = '';
  $('profiles-count').textContent = state.profiles.length;
  // With profiles but nothing picked yet, pick the first one, so the
  // action bar under the list is never a row of disabled buttons and the
  // hero's logo button has something to connect.
  if (state.selected === null && state.profiles.length > 0) state.selected = state.profiles[0];
  if (state.profiles.length === 0) {
    var li = document.createElement('li');
    li.className = 'empty-state';
    li.innerHTML = '<div class="empty-icon"><svg class="i"><use href="#i-file"/></svg></div>' +
      '<div class="empty-title"></div><div class="empty-text"></div>';
    li.querySelector('.empty-title').textContent = t('emptyTitle');
    li.querySelector('.empty-text').textContent = t('emptyText');
    ul.appendChild(li);
  }
  state.profiles.forEach(function(name) {
    var li = document.createElement('li');
    li.className = 'profile-row enter';
    li.style.animationDelay = (ul.children.length * 45) + 'ms';
    li.dataset.name = name; // looked up by updateProfileRowStates below
    li.addEventListener('animationend', function() { li.classList.remove('enter'); });
    li.onmousemove = function(e) {
      var r = li.getBoundingClientRect();
      li.style.setProperty('--mx', (e.clientX - r.left) + 'px');
      li.style.setProperty('--my', (e.clientY - r.top) + 'px');
    };
    li.onclick = function() { state.selected = name; updateProfileRowStates(); };
    li.ondblclick = function(e) {
      if (e.target.closest('.profile-row-connect')) return;
      openEdit(name);
    };

    var avatar = document.createElement('div');
    avatar.className = 'profile-avatar';
    avatar.textContent = (Array.from(name.trim())[0] || '?').toUpperCase();

    var info = document.createElement('div');
    info.className = 'profile-row-info';
    var nameEl = document.createElement('div');
    nameEl.className = 'profile-row-name';
    nameEl.textContent = name;
    var meta = document.createElement('div');
    meta.className = 'profile-row-meta';
    var statusEl = document.createElement('span');
    statusEl.className = 'profile-row-status';
    var routingEl = document.createElement('span');
    routingEl.className = 'profile-row-routing';
    loadRoutingSummaryInto(name, routingEl);
    meta.appendChild(statusEl);
    meta.appendChild(routingEl);
    info.appendChild(nameEl);
    info.appendChild(meta);

    // The connect switch. Its own click zone, so toggling it doesn't
    // also count as a row click; state classes are set by
    // updateProfileRowStates.
    var connectZone = document.createElement('div');
    connectZone.className = 'profile-row-connect';
    var connectBtnInner = document.createElement('span');
    connectBtnInner.className = 'profile-row-connect-btn';
    connectZone.appendChild(connectBtnInner);
    connectZone.onclick = function(e) {
      e.stopPropagation();
      toggleConnectRow(name);
    };

    li.appendChild(avatar);
    li.appendChild(info);
    li.appendChild(connectZone);
    ul.appendChild(li);
  });
  updateProfileRowStates();
}

// updateProfileRowStates patches the already-rendered <li> elements in
// place (selected highlight, per-row status label, connect zone's
// active/busy class and button text) instead of calling renderProfileList
// and rebuilding the whole list from scratch. This matters a lot more
// than it looks: refreshStatus's 3s poll used to call renderProfileList
// unconditionally, which clears and rebuilds the whole <ul> — including
// whichever row the mouse happened to be hovering. WebView2
// (Windows) doesn't retroactively apply :hover to a brand-new element
// under an unmoved cursor the way GTK WebKit (Linux) does, so every 3s
// tick made the hovered row's connect button flicker out and back —
// reported live 2026-09-15, Windows-only, "flickers on long hover".
// Only call full renderProfileList() when state.profiles itself changes
// (add/delete/rename) or the language switches — anything that's purely
// a connection-state or selection change should come through here.
function updateProfileRowStates() {
  Array.prototype.forEach.call($('profile-list').children, function(li) {
    var name = li.dataset.name;
    if (name === undefined) return; // the "no profiles" empty placeholder
    li.classList.toggle('selected', name === state.selected);
    var rowActive = isActive(name);
    li.classList.toggle('is-on', rowActive && state.active[name]);
    setRowStatus(li.querySelector('.profile-row-status'), name);
    var connectZone = li.querySelector('.profile-row-connect');
    connectZone.classList.toggle('active', rowActive);
    connectZone.classList.toggle('pending', rowActive && !state.active[name]);
    connectZone.classList.toggle('busy', state.connectBusy);
    connectZone.title = rowActive ? t('disconnect') : t('connect');
  });
  updateButtons();
  refreshIncludedAppsPreview();
}

// toggleConnectRow is the row's right-half connect/disconnect zone (see
// its CSS doc — this is now the only way to connect, there's no separate
// bottom Connect button any more) — disconnect this row's profile if
// it's connected, otherwise connect it alongside whatever else already
// is (or, on a backend that runs only one profile at a time — no
// disconnectProfile binding — switch to it). connectBusy guards against a
// double-click race the same way the old bottom button's handler did
// (fixed live 2026-09-04) — this is the only trigger left, so it has to
// carry that guard itself now.
function toggleConnectRow(name) {
  if (state.connectBusy) return;
  state.connectBusy = true;
  updateProfileRowStates();
  var p;
  if (isActive(name)) {
    p = disconnectOne(name);
  } else {
    delete state.errorProfiles[name];
    delete state.connectStartedAt[name];
    if (!window.disconnectProfile && state.activeOrder.length > 0) {
      p = disconnect().then(function() { return connect(name); });
    } else {
      p = connect(name);
    }
    state.connectingName = name;
    markConnecting();
    updateProfileRowStates();
    updateButtons();
    p = p.then(function() { state.connectingName = null; },
      function(err) { state.connectingName = null; throw err; });
  }
  p.then(refreshStatus).catch(function(err) { showListError(err); refreshStatus(); })
    .finally(function() { state.connectBusy = false; updateProfileRowStates(); });
}

// The hero's logo is a one-click power button: with anything connected
// it disconnects everything, otherwise it connects the selected profile.
$('hero-orb').onclick = function() {
  if (state.connectBusy) return;
  if (state.activeOrder.length === 0) {
    if (state.selected) toggleConnectRow(state.selected);
    return;
  }
  state.connectBusy = true;
  updateProfileRowStates();
  state.activeOrder.slice().reduce(function(p, n) {
    return p.then(function() { return disconnectOne(n); });
  }, Promise.resolve()).then(refreshStatus).catch(function(err) { showListError(err); refreshStatus(); })
    .finally(function() { state.connectBusy = false; updateProfileRowStates(); });
};

function updateButtons() {
  var has = state.selected !== null;
  $('btn-edit').disabled = !has;
  $('btn-apps').disabled = !has;
  $('btn-domains').disabled = !has;
  $('btn-delete').disabled = !has;
  // Readout only — see profile-row-connect for the actual per-row
  // Connect/Disconnect action, there's no button down here any more.
  var up = state.activeOrder.filter(function(n) { return state.active[n]; });
  var pending = state.activeOrder.filter(function(n) { return !state.active[n]; });
  var anyError = Object.keys(state.errorProfiles).length > 0;
  // The hero at the top of the list: a big state title, one line of
  // detail under it, and the logo orb glowing/spinning to match.
  var sub = $('hero-sub');
  sub.classList.remove('error');
  if (up.length > 0) {
    $('status-text').textContent = t('connectedShort');
    $('status-text').className = 'status-text connected';
    sub.textContent = tf('heroSubOn', up.join(', '));
  } else if (pending.length > 0) {
    // Adapter/local bridge is up but the WireGuard handshake with the
    // peer hasn't completed (or never will, for a bad config/dead
    // server) — see StateReply.HandshakeOK's doc in service.go. Used to
    // just say "Connected" here regardless, which was actively
    // misleading: confirmed live 2026-09-04 with a profile whose tunnel
    // never actually passed traffic.
    $('status-text').textContent = t('connectingShort');
    $('status-text').className = 'status-text connecting';
    sub.textContent = pending.join(', ');
  } else if (anyError) {
    $('status-text').textContent = t('statusError');
    $('status-text').className = 'status-text disconnected error';
    sub.textContent = t('heroSubError');
    sub.classList.add('error');
  } else {
    $('status-text').textContent = t('statusOff');
    $('status-text').className = 'status-text disconnected';
    sub.textContent = state.profiles.length ? t('heroSubOff') : '';
  }
  $('hero').classList.toggle('on', up.length > 0);
  $('hero').classList.toggle('connecting', up.length === 0 && pending.length > 0);
  orbDust.refresh();
  $('hero-orb').classList.toggle('busy', state.connectBusy);
  $('hero-orb').title = state.activeOrder.length > 0 ? t('orbDisconnect') :
    (state.selected ? tf('orbConnect', state.selected) : '');
  // Same colored-when-connected / grey-when-disconnected swap as the tray
  // icon (tray.go) and the Linux awg-gui counterpart's AppIndicator —
  // colored only once the handshake actually confirms the tunnel works.
  var reallyConnected = up.length > 0;
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
    state.profilesLoadFailed = false;
    // Clears any stale error from a prior failed poll (e.g. "connecting
    // to Spectrune service..." while the service was briefly down) —
    // nothing previously hid #list-error once shown, so a transient
    // outage that recovered on its own (SCM auto-restarting the
    // service, see installService's recovery actions) still left a
    // scary banner up forever, looking like every profile had vanished
    // even though this exact successful call just proved otherwise.
    $('list-error').style.display = 'none';
  }).catch(function(err) {
    // Flagged so refreshStatus's 3s poll retries this once the service
    // is reachable again — refreshProfiles itself is only otherwise
    // called once at page load plus a handful of specific user actions
    // (see call sites), never on a timer, so a load that fails at the
    // exact moment the service happens to be briefly down (a crash, or
    // literally anything stopping it) previously left the profile list
    // empty *forever*, not just until the service recovered — reported
    // live 2026-09-21 as "profile disappeared" when nothing had
    // actually been lost, the list had just never successfully
    // reloaded since that one failed attempt.
    state.profilesLoadFailed = true;
    showListError(err);
  });
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

// fillRoutingSummary renders a card's routing line: "All traffic", or the
// first few routed apps' icons plus the count.
function fillRoutingSummary(el, apps) {
  el.innerHTML = '';
  if (apps.length > 0) {
    var icons = document.createElement('span');
    icons.className = 'mini-icons';
    apps.slice(0, 4).forEach(function(p) {
      var img = document.createElement('img');
      img.title = p;
      loadAppIcon(p, img);
      icons.appendChild(img);
    });
    el.appendChild(icons);
  }
  el.appendChild(document.createTextNode(routingSummaryText(apps)));
}

function loadRoutingSummaryInto(name, el) {
  var cached = includedPreviewCache[name];
  if (cached !== undefined) {
    fillRoutingSummary(el, cached);
    return;
  }
  loadProfile(name).then(function(details) {
    var apps = details.IncludedApps || [];
    includedPreviewCache[name] = apps;
    if (el.isConnected) fillRoutingSummary(el, apps);
  }).catch(function() { /* leave blank — not worth an error popup for a subtitle */ });
}

// ---------- included-apps preview (under Connect/Disconnect) ----------

var includedPreviewCache = {}; // profile name -> IncludedApps array, cleared whenever that profile is saved
var includedPreviewLoading = {}; // profile name -> true while its loadProfile is in flight

// includedAppsPreviewRenderedFor is the signature (see
// refreshIncludedAppsPreview) of what the row last rendered, or undefined
// to force a re-render.
var includedAppsPreviewRenderedFor = undefined;

// refreshIncludedAppsPreview fills the strip at the bottom of the hero:
// one chip per connected profile with its state and routed apps (or "all
// traffic"). Hidden while nothing is connected — each profile card
// already shows its own routing.
function refreshIncludedAppsPreview() {
  var row = $('included-apps-row');
  var names = state.activeOrder.slice();
  // Called from updateProfileRowStates's 3s poll — fetch whatever isn't
  // cached yet (and retry a failed fetch next tick), then only rebuild
  // the DOM when something visible actually changed, so icons don't
  // flicker/reload every tick (same reason as the v2.0.13.0 hover fix).
  names.forEach(function(n) {
    if (includedPreviewCache[n] !== undefined || includedPreviewLoading[n]) return;
    includedPreviewLoading[n] = true;
    loadProfile(n).then(function(details) {
      includedPreviewCache[n] = details.IncludedApps || [];
      refreshIncludedAppsPreview();
    }).catch(function() { /* retried on the next poll tick */ })
      .finally(function() { delete includedPreviewLoading[n]; });
  });
  var sig = JSON.stringify([state.lang, names.map(function(n) {
    return [n, state.active[n], includedPreviewCache[n] || null];
  })]);
  if (sig === includedAppsPreviewRenderedFor) return;
  includedAppsPreviewRenderedFor = sig;
  row.innerHTML = '';
  // With one tunnel the subtitle already names it and its card shows its
  // apps — the chips only earn their space with two or more.
  row.style.display = names.length > 1 ? 'flex' : 'none';
  names.forEach(function(n) {
    var block = document.createElement('div');
    block.className = 'tunnel-block';
    var head = document.createElement('div');
    var ok = state.active[n];
    head.className = 'tunnel-head' + (ok ? '' : ' connecting');
    head.textContent = n;
    head.title = ok ? t('connectedShort') : t('connectingShort');
    block.appendChild(head);
    var apps = includedPreviewCache[n];
    if (apps !== undefined) block.appendChild(includedAppsIcons(apps));
    row.appendChild(block);
  });
}

// includedAppsIcons builds a row of app icons, or the "all traffic" note
// for a profile with no app selection.
function includedAppsIcons(apps) {
  if (apps.length === 0) {
    var note = document.createElement('div');
    note.className = 'included-apps-fulltunnel';
    note.textContent = t('routingFullTunnel');
    return note;
  }
  var icons = document.createElement('div');
  icons.className = 'included-apps-icons';
  apps.forEach(function(p) {
    var img = document.createElement('img');
    img.className = 'included-app-icon';
    img.title = p;
    loadAppIcon(p, img);
    icons.appendChild(img);
  });
  return icons;
}

function refreshStatus() {
  // Must return this promise, not just kick it off — toggleConnectRow's
  // click handler chains .finally(...) after .then(refreshStatus) specifically
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
    // getState succeeding proves the service is reachable right now —
    // if the last refreshProfiles() attempt failed (service was down at
    // the time, or any other transient IPC error), retry it here so the
    // profile list actually recovers within one poll tick instead of
    // staying empty until some unrelated user action happens to call
    // refreshProfiles() again. See its own doc for the full story.
    if (state.profilesLoadFailed) refreshProfiles();

    applyStateReply(s);

    // A connected profile without a WireGuard handshake yet is normal for
    // the first few seconds of every connect, but a server that's
    // actually unreachable (wrong endpoint, or — reported 2026-09-14 — a
    // router that whitelists only specific traffic and blocks the rest)
    // leaves it there forever with no way to distinguish "still trying"
    // from "never going to work", and leaves the bridge's default-route
    // override sitting on a dead tunnel the whole time. Give each
    // profile a fixed window, then give up on it: disconnect just that
    // one and flag its row instead of hanging on "Connecting…".
    var now = Date.now();
    var timedOut = [];
    Object.keys(state.connectStartedAt).forEach(function(n) {
      if (!isActive(n) || state.active[n]) delete state.connectStartedAt[n];
    });
    state.activeOrder.forEach(function(n) {
      if (state.active[n]) return;
      if (!state.connectStartedAt[n]) state.connectStartedAt[n] = now;
      else if (now - state.connectStartedAt[n] >= CONNECT_TIMEOUT_MS) timedOut.push(n);
    });
    if (timedOut.length === 0) {
      updateProfileRowStates();
      return;
    }
    timedOut.forEach(function(n) {
      delete state.connectStartedAt[n];
      state.errorProfiles[n] = true;
    });
    state.connectBusy = true;
    timedOut.reduce(function(p, n) {
      return p.then(function() { return disconnectOne(n); });
    }, Promise.resolve()).then(function() {
      return getState();
    }).then(applyStateReply).catch(showListError).finally(function() {
      state.connectBusy = false;
      updateProfileRowStates();
    });
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
// okLabel/danger let a caller override the OK button's default "Delete"
// text and red styling — every prior caller here was a real delete
// confirmation, so that stayed the default rather than becoming a
// required argument everywhere. Always reset on entry (rather than only
// when okLabel is given) since #confirm-ok is one shared, reused DOM
// element — otherwise a non-danger confirm would leave "Delete"/red
// showing for the next real delete confirmation after it.
function showConfirm(message, onOk, okLabel) {
  $('confirm-message').textContent = message;
  var okBtn = $('confirm-ok');
  okBtn.textContent = okLabel || t('delete');
  okBtn.classList.toggle('danger', !okLabel);
  // applyI18n() re-applies every [data-i18n] element's text on a
  // language switch — leaving this attribute set while a custom label
  // is showing would silently revert it back to "Delete" if the user
  // switches language with this dialog still open.
  if (okLabel) {
    okBtn.removeAttribute('data-i18n');
  } else {
    okBtn.setAttribute('data-i18n', 'delete');
  }
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
// nothing on Cancel/overlay click. An optional hint renders as a smaller
// muted line below the message (its own line rather than appended in
// parentheses — a long RU hint appended inline used to wrap mid-sentence).
function showPrompt(message, defaultValue, onOk, hint) {
  var msgEl = $('prompt-message');
  msgEl.innerHTML = '';
  msgEl.appendChild(document.createTextNode(message));
  if (hint) {
    var hintEl = document.createElement('div');
    hintEl.style.cssText = 'font-weight:400;font-size:11.5px;color:var(--muted);margin-top:3px';
    hintEl.textContent = hint;
    msgEl.appendChild(hintEl);
  }
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
  // Opened straight from the main list, not from within an in-progress
  // edit — Cancel should drop the user back there, not into the edit
  // screen this jump only visits as an implementation detail.
  state.appsOrigin = 'list';
  openEdit(state.selected, 'apps');
};
$('btn-domains').onclick = function() {
  if (!state.selected) return;
  openEdit(state.selected, 'domains');
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
$('btn-import').onclick = function() {
  importConfig().then(function(result) {
    if (!result || !result.Text) return;
    $('edit-config').value = result.Text;
    // Only offer to name+save for a brand-new profile — silently renaming
    // an existing one just because its config text got replaced would be
    // surprising, so Edit keeps the old behavior (text only, manual Save).
    if (!state.editingExisting) {
      showPrompt(t('importNamePrompt'), result.SuggestedName || '', function(name) {
        $('edit-name').value = name;
        saveCurrentProfile().then(function() {
          show('view-list');
          refreshProfiles();
        }).catch(showEditError);
      }, t('importNameHint'));
    }
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

function updateAppsCount() {
  $('apps-selected-count').textContent = tf('appsSelectedCount', state.includedApps.length);
}

// ---------- apps picker view ----------

function openAppsPicker() {
  $('apps-search').value = '';
  state.appsFilter = '';
  // Snapshot so "Cancel" can discard whatever gets toggled/select-all'd/
  // cleared/preset-loaded in this view without touching the profile at
  // all — "Done" is the only path that's supposed to commit apps changes.
  state.includedAppsSnapshot = state.includedApps.slice();
  show('view-apps');
  state.takenApps = {};
  loadTakenApps().then(renderAppsList);
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

// loadTakenApps fills state.takenApps (lowercase path -> profile name)
// with the apps other connected profiles route, which renderAppsList
// hides: one app can only go through one connected tunnel at a time (the
// service refuses to connect a profile whose apps overlap a connected
// one's).
function loadTakenApps() {
  // A backend that runs one profile at a time (no disconnectProfile —
  // Linux) switches profiles instead, so nothing is ever shared.
  if (!window.disconnectProfile) return Promise.resolve();
  var others = state.activeOrder.filter(function(n) { return n !== state.editOriginalName; });
  return Promise.all(others.map(function(n) {
    return loadProfile(n).then(function(details) {
      (details.IncludedApps || []).forEach(function(p) { state.takenApps[p.toLowerCase()] = n; });
    }).catch(function() {});
  }));
}

// isTakenApp reports whether path is hidden from this profile's picker
// (routed by another connected profile and not already selected here).
function isTakenApp(path, includedSet) {
  var key = path.toLowerCase();
  return state.takenApps[key] !== undefined && !includedSet[key];
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

  var takenBy = {};
  state.installedApps.forEach(function(a) {
    if (isTakenApp(a.Path, includedSet)) takenBy[state.takenApps[a.Path.toLowerCase()]] = true;
  });
  var takenNames = Object.keys(takenBy);
  $('apps-taken-note').style.display = takenNames.length ? '' : 'none';
  $('apps-taken-note').textContent = takenNames.length ? tf('appsTakenNote', takenNames.join(', ')) : '';

  state.installedApps
    .filter(function(a) {
      if (isTakenApp(a.Path, includedSet)) return false;
      if (!filter) return true;
      return a.Name.toLowerCase().indexOf(filter) !== -1 || a.Path.toLowerCase().indexOf(filter) !== -1;
    })
    .forEach(function(a) {
      var li = document.createElement('li');
      var included = !!includedSet[a.Path.toLowerCase()];
      if (included) li.className = 'checked';
      var cb = document.createElement('span');
      cb.className = included ? 'checkbox checked' : 'checkbox';
      var icon = document.createElement('img');
      icon.className = 'app-icon';
      icon.width = 28;
      icon.height = 28;
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
  if (!ul.children.length && state.installedApps.length) {
    var none = document.createElement('li');
    none.className = 'empty-row';
    none.textContent = t('appsNoMatch');
    ul.appendChild(none);
  }
  $('apps-selected-count').textContent = tf('appsSelectedCount', state.includedApps.length);
}

// iconCache maps a lowercase path to the promise of its data URI, shared
// by every <img> that asks for it. It used to hold null while a fetch
// was pending, and any second <img> for the same path (the list being
// re-rendered by a click/search mid-load, or the main screen's routed-
// apps row, which never re-renders) just returned — leaving that icon
// blank for good.
var iconCache = {};

// Generic app glyph for an exe whose icon can't be extracted, instead of
// a blank/broken image.
var FALLBACK_APP_ICON = 'data:image/svg+xml,' + encodeURIComponent(
  '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">' +
  '<rect x="1.5" y="2.5" width="13" height="11" rx="2" fill="#8a94a6"/>' +
  '<rect x="1.5" y="2.5" width="13" height="3" rx="1.5" fill="#5d6678"/>' +
  '<rect x="3.5" y="7.5" width="9" height="4" rx="1" fill="#e6e9ef"/></svg>');

function loadAppIcon(path, imgEl) {
  var key = path.toLowerCase();
  if (!iconCache[key]) {
    iconCache[key] = getAppIcon(path).catch(function() { return FALLBACK_APP_ICON; });
  }
  iconCache[key].then(function(dataUri) {
    imgEl.src = dataUri;
  });
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
  var includedSet = {};
  state.includedApps.forEach(function(p) { includedSet[p.toLowerCase()] = true; });
  state.includedApps = state.installedApps
    .filter(function(a) { return !isTakenApp(a.Path, includedSet); })
    .map(function(a) { return a.Path; });
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
$('btn-apps-cancel').onclick = function() {
  // Unlike "Done", this never saves anything — just throws away whatever
  // got toggled/select-all'd/cleared/preset-loaded since the view opened,
  // and returns to wherever this view was opened from: the main list if
  // opened via its "Apps…" button, or the edit screen if opened via
  // "Apps: N selected…" while already mid-edit (see appsOrigin above).
  state.includedApps = (state.includedAppsSnapshot || state.includedApps).slice();
  updateAppsCount();
  show(state.appsOrigin === 'list' ? 'view-list' : 'view-edit');
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
    empty.className = 'empty-row';
    empty.textContent = t('domainListNoLists');
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
    delBtn.className = 'app-launch-btn danger-hover';
    delBtn.textContent = t('delete');
    delBtn.onclick = function() {
      showConfirm(tf('domainListConfirmDelete', name), function() {
        deleteDomainList(name).then(function() {
          state.includedDomainLists = state.includedDomainLists.filter(function(n) { return n !== name; });
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
// Back leaves the editor without saving, same as Cancel — it never had a
// handler before, so it did nothing.
$('domain-edit-back').onclick = $('btn-domain-edit-cancel').onclick;

// ---------- language switching ----------

function applyI18n() {
  document.querySelectorAll('[data-i18n]').forEach(function(el) {
    el.textContent = t(el.getAttribute('data-i18n'));
  });
  document.querySelectorAll('[data-i18n-placeholder]').forEach(function(el) {
    el.placeholder = t(el.getAttribute('data-i18n-placeholder'));
  });
  document.querySelectorAll('[data-i18n-title]').forEach(function(el) {
    el.title = t(el.getAttribute('data-i18n-title'));
  });
  $('lang-en').classList.toggle('active', state.lang === 'en');
  $('lang-ru').classList.toggle('active', state.lang === 'ru');
  // Re-run the dynamic renderers so text they build themselves (status
  // line, "N selected…", list rows) picks up the new language too — the
  // data-i18n pass above only covers static markup.
  renderProfileList();
  updateAppsCount();
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
  violet:  { c1: '#8b3dff', c2: '#ff2d55' },
  ocean:   { c1: '#00c2ff', c2: '#2f6bff' },
  emerald: { c1: '#00e5b0', c2: '#12b76a' },
  sunset:  { c1: '#ffb347', c2: '#ff5f6d' },
  crimson: { c1: '#ff5c8a', c2: '#c2185b' },
  slate:   { c1: '#8a8fa3', c2: '#545a70' },
};

// ---------- orb dust ----------
// Glowing particles orbiting the hero logo, with short fading trails.
// Their speed and brightness follow the hero's state: bright and calm
// when connected, fast while connecting, a faint slow grey drift when
// off. Easing between states (cur -> target) keeps a change smooth.
// animTicker drives the canvas animations from one ~24 fps timer, so the
// dust and the starfield redraw together and the page is re-composited
// once per tick rather than once for each. Each handler returns whether
// it still wants ticks; with none left the timer stops entirely (nothing
// moving, nothing hidden still running at 60 Hz).
var ANIM_SLOT_MS = 42;
var animTicker = (function() {
  var handlers = [], timer = null;
  function tick() {
    var now = performance.now(), any = false;
    handlers.forEach(function(f) { if (f(now)) any = true; });
    if (!any) { clearInterval(timer); timer = null; }
  }
  return {
    add: function(f) { handlers.push(f); },
    wake: function() { if (!timer) timer = setInterval(tick, ANIM_SLOT_MS); },
  };
})();

var orbDust = (function() {
  var canvas = $('orb-dust');
  var ctx = canvas.getContext('2d');
  var SIZE = 200, R_MIN = 41, R_MAX = 66;
  var burst = 0; // 1 right after a connect, easing back to 0
  var mode = 'run'; // see setMode
  var dpr = 1, colors = [[139, 61, 255], [255, 45, 85]];
  var cur = { speed: .25, glow: .35, tint: 0 };
  var parts = [];
  for (var i = 0; i < 54; i++) {
    parts.push({
      a: Math.random() * Math.PI * 2,
      r: R_MIN + Math.pow(Math.random(), 1.6) * (R_MAX - R_MIN),
      v: .006 + Math.random() * .012,         // radians per frame at speed 1
      wob: Math.random() * Math.PI * 2,       // phase of the in/out drift
      size: .5 + Math.random() * 1.7,
      tw: Math.random() * Math.PI * 2,        // twinkle phase
      mix: Math.random(),                     // where on the c1 -> c2 gradient
    });
  }
  function hexToRgb(h) {
    var m = /^#?([0-9a-f]{6})$/i.exec(h || '');
    if (!m) return [139, 61, 255];
    var n = parseInt(m[1], 16);
    return [n >> 16 & 255, n >> 8 & 255, n & 255];
  }
  function resize() {
    dpr = window.devicePixelRatio || 1;
    canvas.width = SIZE * dpr;
    canvas.height = SIZE * dpr;
  }
  // One step per animTicker tick (the per-step amounts below are sized
  // for its ~24 fps).
  animTicker.add(function() {
    if (mode !== 'run') return false;
    step(false);
    return true;
  });
  // drawStill paints the dust unmoving, in the current state's colors —
  // a few steps so it shows at full strength.
  function drawStill() {
    for (var k = 0; k < 6; k++) step(true);
  }
  function step(frozen) {
    var hero = $('hero');
    var target = hero.classList.contains('on') ? { speed: 1, glow: 1, tint: 1 }
      : hero.classList.contains('connecting') ? { speed: 3, glow: .8, tint: 1 }
      : { speed: .22, glow: .28, tint: 0 };
    ['speed', 'glow', 'tint'].forEach(function(k) { cur[k] = frozen ? target[k] : cur[k] + (target[k] - cur[k]) * .1; });
    burst *= .885;
    if (canvas.width !== Math.round(SIZE * (window.devicePixelRatio || 1))) resize();
    var light = document.documentElement.classList.contains('light');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    // Fade the previous frame instead of clearing it — that's the trail.
    ctx.globalCompositeOperation = 'destination-out';
    ctx.fillStyle = 'rgba(0,0,0,' + (.36 + .18 * (1 - cur.speed / 3)) + ')';
    ctx.fillRect(0, 0, SIZE, SIZE);
    ctx.globalCompositeOperation = light ? 'source-over' : 'lighter';
    var c = SIZE / 2, t = Date.now() / 1000;
    parts.forEach(function(p) {
      if (!frozen) p.a += p.v * 2.5 * (cur.speed + burst * 7);
      var r = p.r + Math.sin(t * .8 + p.wob) * 2.5 + burst * (18 + p.size * 16);
      var x = c + Math.cos(p.a) * r, y = c + Math.sin(p.a) * r;
      // Dimmer toward the outer edge so the cloud has no hard rim.
      var edge = 1 - Math.max(0, (r - R_MIN) / (R_MAX - R_MIN + 4));
      if (burst > .01) edge = Math.max(edge, Math.min(1, 1 - (r - R_MAX) / 50));
      var a = Math.min(1, (cur.glow + burst * .8) * Math.max(0, edge) * (.45 + .55 * Math.abs(Math.sin(t * 1.7 + p.tw))));
      var col = [0, 1, 2].map(function(k) {
        var accent = colors[0][k] + (colors[1][k] - colors[0][k]) * p.mix;
        var grey = light ? 120 : 170;
        return Math.round(grey + (accent - grey) * cur.tint);
      });
      ctx.fillStyle = 'rgba(' + col.join(',') + ',' + a.toFixed(3) + ')';
      ctx.beginPath();
      ctx.arc(x, y, p.size, 0, Math.PI * 2);
      ctx.fill();
    });
  }
  resize();
  animTicker.wake();
  return {
    setColors: function(c1, c2) { colors = [hexToRgb(c1), hexToRgb(c2)]; },
    burst: function() { burst = 1; },
    // setMode: 'run' animates, 'still' shows the dust frozen (animations
    // off in Settings), 'stop' draws nothing new (window hidden).
    setMode: function(m) {
      mode = m;
      if (m === 'run') animTicker.wake();
      else if (m === 'still') drawStill();
    },
    // refresh redraws the still dust after the connection state changed.
    refresh: function() { if (mode === 'still') drawStill(); },
  };
})();

function applyTheme() {
  var root = document.documentElement;
  root.classList.toggle('light', state.theme.mode === 'light');
  var c1, c2;
  if (state.theme.accent === 'custom') {
    c1 = state.theme.customColor1 || '#8b3dff';
    c2 = state.theme.customColor2 || '#ff2d55';
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
  // Windows only (feature-detected — no such binding on Linux, where the
  // window has no native title bar to color in the first place) and only
  // takes visible effect on Windows 11: colors the actual OS title bar to
  // match the app's accent instead of leaving it whatever stock color
  // Windows uses.
  if (window.setTitleBarColor) window.setTitleBarColor(c1);
  orbDust.setColors(c1, c2);
  starfield.setAccent(c1);
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
  // The space background is a dark-theme look. Under a background photo
  // the same switch keeps only the moving stars, over the photo, in
  // either theme.
  var spaceOn = state.theme.space && state.theme.mode !== 'light' && !state.theme.bgImage;
  var starsOn = state.theme.space && !!state.theme.bgImage;
  document.body.classList.toggle('space-on', spaceOn);
  document.body.classList.toggle('stars-on', starsOn);
  $('theme-space-toggle').checked = state.theme.space;
  $('theme-anim-toggle').checked = state.theme.anim;
  $('theme-space-row').style.display = state.theme.mode === 'light' && !state.theme.bgImage ? 'none' : 'flex';
  var label = $('theme-space-row').querySelector('.theme-panel-label');
  label.dataset.i18n = state.theme.bgImage ? 'themeStars' : 'themeSpace';
  label.textContent = t(label.dataset.i18n);
  starfield.setEnabled(spaceOn || starsOn, starsOn);
}

// ---------- starfield ----------
// Three depths of stars drifting slowly left (a little faster while a
// tunnel is up), a few twinkling, plus an occasional shooting star.
// Cheap by design: each depth is drawn once into its own off-screen
// canvas and then only blitted at an offset, so a frame is three image
// copies plus a few dozen dots — at ~15 fps, and none at all while the
// window is hidden (see setAnimationsPaused).
var starfield = (function() {
  var canvas = $('space');
  var ctx = canvas.getContext('2d');
  var w = 0, h = 0, dpr = 1;
  var layers = [];   // [{img, speed, off}]
  var twinklers = []; // a few stars drawn live, so they can twinkle
  var shooting = null, nextShot = 0;
  var enabled = false, mode = 'run', last = 0, tickN = 0, speed = 1;
  var warpV = 0; // >0 right after a connect: stars streak out from the centre
  var accent = '#8b3dff';
  var bright = false; // over a background photo: bigger stars with a halo
  var LAYERS = [ // depth: star count per 100k px², radius, alpha, drift px/s
    { density: 14, r: .45, a: .5, v: 3 },
    { density: 5, r: .8, a: .75, v: 7 },
    { density: 1.3, r: 1.2, a: .95, v: 14 },
  ];
  function starColor() {
    var t = Math.random();
    return t < .1 ? accent : t < .35 ? 'rgb(170,195,255)' : '#fff';
  }
  function build() {
    dpr = Math.min(window.devicePixelRatio || 1, 2);
    w = window.innerWidth; h = window.innerHeight;
    canvas.width = Math.round(w * dpr); canvas.height = Math.round(h * dpr);
    layers = LAYERS.map(function(L) {
      var img = document.createElement('canvas');
      img.width = canvas.width; img.height = canvas.height;
      var c = img.getContext('2d');
      c.scale(dpr, dpr);
      var n = Math.round(w * h / 100000 * L.density);
      for (var i = 0; i < n; i++) {
        var x = Math.random() * w, y = Math.random() * h;
        var r = L.r * (.7 + Math.random() * .6) * (bright ? 1.5 : 1);
        var a = L.a * (.5 + Math.random() * .5);
        c.fillStyle = starColor();
        if (bright) {
          // A soft halo so the star still reads over a light photo.
          c.globalAlpha = a * .22;
          c.beginPath(); c.arc(x, y, r * 2.2, 0, Math.PI * 2); c.fill();
          a = Math.min(1, a + .25);
        }
        c.globalAlpha = a;
        c.beginPath();
        c.arc(x, y, r, 0, Math.PI * 2);
        c.fill();
      }
      return { img: img, v: L.v, off: 0 };
    });
    twinklers = [];
    for (var i = 0; i < 28; i++) {
      twinklers.push({ x: Math.random() * w, y: Math.random() * h, r: (.9 + Math.random() * .9) * (bright ? 1.4 : 1),
        tw: Math.random() * Math.PI * 2, tws: .8 + Math.random() * 2, c: starColor() });
    }
  }
  // Every other animTicker tick (~12 fps) for the slow drift; every tick
  // while a warp or shooting star is moving fast.
  animTicker.add(function(now) {
    if (!enabled || mode !== 'run') return false;
    tickN++;
    if (!(warpV > .02 || shooting) && tickN % 2) return true;
    var dt = last ? Math.min(now - last, 250) : 0;
    last = now;
    paint(dt, false, now);
    return true;
  });
  // kick restarts the drift, or paints one unmoving frame when
  // animations are off in Settings.
  function kick() {
    if (!enabled) return;
    if (mode === 'run') { last = 0; animTicker.wake(); }
    else if (mode === 'still') paint(0, true);
  }
  function paint(dt, still, now) {
    var reduced = still;
    now = now || performance.now();
    var target = $('hero').classList.contains('on') ? 2.2 : 1;
    speed += (target - speed) * .05;
    warpV *= .84;
    var t = now / 1000;
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    ctx.globalAlpha = 1;
    layers.forEach(function(L) {
      if (!reduced) L.off = (L.off + L.v * speed * dt / 1000 * dpr) % canvas.width;
      var x = -Math.round(L.off);
      ctx.drawImage(L.img, x, 0);
      ctx.drawImage(L.img, x + canvas.width, 0);
    });
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    var cx = w / 2, cy = h / 3;
    twinklers.forEach(function(s) {
      var x = ((s.x - layers[2].off / dpr) % w + w) % w;
      ctx.globalAlpha = still ? .85 : .55 + .45 * Math.sin(t * s.tws + s.tw);
      ctx.fillStyle = s.c;
      ctx.beginPath(); ctx.arc(x, s.y, s.r, 0, Math.PI * 2); ctx.fill();
      if (!still && warpV > .02) {
        // Warp: streaks pointing out from the centre.
        var dx = x - cx, dy = s.y - cy, d = Math.sqrt(dx * dx + dy * dy) || 1;
        var len = warpV * (40 + d * .5);
        ctx.strokeStyle = s.c; ctx.lineWidth = s.r;
        ctx.globalAlpha = Math.min(1, warpV * 1.2);
        ctx.beginPath(); ctx.moveTo(x, s.y); ctx.lineTo(x + dx / d * len, s.y + dy / d * len); ctx.stroke();
      }
    });
    if (!still && warpV > .02) {
      // Extra streaks from random points, so the jump reads as a burst.
      ctx.globalAlpha = warpV;
      ctx.strokeStyle = '#fff';
      ctx.lineWidth = 1;
      for (var k = 0; k < 60; k++) {
        var ang = k * 2.39996, rad = 40 + (k * 37) % Math.max(w, h);
        var sx = cx + Math.cos(ang) * rad, sy = cy + Math.sin(ang) * rad;
        var len = warpV * (30 + rad * .45);
        ctx.beginPath(); ctx.moveTo(sx, sy); ctx.lineTo(sx + Math.cos(ang) * len, sy + Math.sin(ang) * len); ctx.stroke();
      }
    }
    if (!reduced && !shooting && now > nextShot) {
      shooting = { x: w * (.3 + Math.random() * .7), y: -20, vx: -(.5 + Math.random() * .3), vy: .32 + Math.random() * .15, life: 0 };
      nextShot = now + 7000 + Math.random() * 10000;
    }
    if (shooting) {
      var sh = shooting;
      sh.life += dt;
      sh.x += sh.vx * dt; sh.y += sh.vy * dt;
      var fade = Math.min(1, sh.life / 150) * Math.max(0, 1 - sh.life / 1100);
      var tx = sh.x - sh.vx * 180, ty = sh.y - sh.vy * 180;
      var g = ctx.createLinearGradient(sh.x, sh.y, tx, ty);
      g.addColorStop(0, 'rgba(255,255,255,' + fade + ')');
      g.addColorStop(1, 'rgba(255,255,255,0)');
      ctx.globalAlpha = 1;
      ctx.strokeStyle = g;
      ctx.lineWidth = 1.4;
      ctx.beginPath(); ctx.moveTo(sh.x, sh.y); ctx.lineTo(tx, ty); ctx.stroke();
      if (sh.life > 1100 || sh.y > h + 40) shooting = null;
    }
    ctx.globalAlpha = 1;
  }
  var resizeTimer = null;
  window.addEventListener('resize', function() {
    if (!enabled) return;
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(function() { build(); kick(); }, 150);
  });
  return {
    warp: function() { warpV = 1; },
    setAccent: function(c) { if (c !== accent) { accent = c; if (layers.length) { build(); kick(); } } },
    setEnabled: function(on, overPhoto) {
      enabled = on;
      if (!on) return;
      if (!layers.length || w !== window.innerWidth || h !== window.innerHeight || bright !== !!overPhoto) {
        bright = !!overPhoto;
        build();
      }
      if (!nextShot) nextShot = performance.now() + 3000;
      kick();
    },
    setMode: function(m) { mode = m; kick(); }, // same modes as orbDust.setMode
  };
})();

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
  $('theme-custom-color1').value = state.theme.customColor1 || '#8b3dff';
  $('theme-custom-color2').value = state.theme.customColor2 || '#ff2d55';
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
  // Reveal the row only once the real value is known, rather than
  // showing it unchecked first and flipping it a moment later — that
  // async gap was visible as a brief flicker every time Settings opened
  // (reported live 2026-09-15: "мигнул и всё" for a toggle that actually
  // was enabled the whole time).
  if (window.getAutostartEnabled) {
    // Hidden first, every open — not just the first — otherwise the
    // second+ open would flash whatever value was left over from last
    // time before this refreshes it.
    $('settings-autostart-row').style.display = 'none';
    getAutostartEnabled().then(function(enabled) {
      $('settings-autostart-toggle').checked = enabled;
    }).catch(function() { /* best-effort — leave it unchecked */ })
      .finally(function() { $('settings-autostart-row').style.display = 'flex'; });
  }
  checkServiceRunning();
}

// checkServiceRunning only exists on Windows (isServiceRunning/
// startServiceElevated, webui_windows.go) — the daemon there is a
// separate, non-elevated-from-this-GUI process reached only over the
// named pipe (see ipc.go), so unlike Linux (systemd restarts it on its
// own and this GUI process IS the same trust level as the daemon) there
// was previously no way to notice it's down, let alone start it,
// short of the user opening Services.msc themselves. A stopped service
// otherwise just shows up as every RPC call failing with an unhelpful
// connection error, with no obvious next step. Checked every time
// Settings opens, same as the autostart toggle above — if it's still
// down after a Cancel, opening Settings again asks again rather than
// silently giving up on it for the rest of the session.
function checkServiceRunning() {
  if (!window.isServiceRunning) return;
  $('settings-service-status').style.display = 'none';
  isServiceRunning().then(function(running) {
    if (running) return;
    showConfirm(t('serviceNotRunning'), function() {
      // Shown inside the settings panel itself, not via showListError's
      // #list-error — that sits in the list view underneath
      // settings-overlay, invisible while Settings is still open, which
      // is exactly when this status/error is relevant.
      var statusEl = $('settings-service-status');
      statusEl.className = 'hint';
      statusEl.textContent = t('serviceStarting');
      statusEl.style.display = 'block';
      startServiceElevated().then(function(started) {
        if (!started) {
          statusEl.className = 'error';
          statusEl.textContent = t('serviceStartFailed');
          return;
        }
        statusEl.style.display = 'none';
        // The service being down this whole time already left #list-error
        // showing a stale "connecting to Spectrune service..." banner
        // underneath Settings — that's now resolved, but nothing else
        // clears it once shown (refreshStatus/refreshProfiles below only
        // ever set it again on a fresh failure, never hide it on
        // success), so it would otherwise keep showing even after this
        // visibly worked.
        $('list-error').style.display = 'none';
        refreshStatus();
        refreshProfiles();
      }).catch(function() {
        statusEl.className = 'error';
        statusEl.textContent = t('serviceStartFailed');
      });
    }, t('startNow'));
  }).catch(function() { /* can't tell — don't bother the user over it */ });
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
// checkBackgroundUpdate polls Bridge.UpdateInProgress (via
// getUpdateInProgress) so ANY self-update — the silent per-launch/once-
// an-hour background check, or a manual "Check for updates" click —
// actually closes this window during the install instead of leaving it
// sitting open on top of the daemon/service restarting underneath it
// (reported live 2026-09-14: the window plainly did not close). Shows
// the overlay briefly first so the message is actually visible, then
// calls closeForUpdate — which spawns a small watcher
// (update_linux.go's waitAndReopen / update_windows.go's Scheduled-Task
// equivalent) that relaunches the GUI once the new version is confirmed
// running, and only then terminates this window. Piggybacks on the
// same 3s cadence as refreshStatus below. This is the single restart
// mechanism for every update path — an earlier version of this file
// had a second, independent Go-side poll-and-restart loop for the
// silent path specifically, removed 2026-09-14 after it and this one
// raced each other into spawning two replacement windows for the same
// update (confirmed live: two overlapping Spectrune windows after one
// install). backgroundUpdateHandled latches once triggered so a still-
// installing update doesn't re-trigger this every tick; reset if
// closeForUpdate itself fails, so the app doesn't get stuck behind a
// permanent overlay over nothing actually happening.
var backgroundUpdateHandled = false;
function triggerCloseForUpdate(showOverlayFirst) {
  if (backgroundUpdateHandled) return;
  backgroundUpdateHandled = true;
  // The delay (if any) is passed *into* closeForUpdate rather than
  // applied here via setTimeout before calling it — closeForUpdate's Go
  // side spawns its relaunch watcher immediately, before sleeping for
  // this delay, purely so the overlay is visible for a moment before
  // the window actually closes. Delaying the call itself would delay
  // that spawn too, and on a fast install that spawn can lose its race
  // against dpkg/msiexec replacing this exe on disk — see
  // webui_linux.go's closeForUpdate doc for what that looked like live.
  // !showOverlayFirst means checkBackgroundUpdate's fallback path called
  // this, i.e. the new version is already confirmed running — passed
  // through as alreadyDone so the Go side can relaunch immediately via
  // the plain, always-supported /gui rather than a poll-and-wait watcher.
  if (showOverlayFirst) $('update-installing-overlay').style.display = 'flex';
  // Windows only: style the update splash (updatesplash_windows.go) that
  // stays on screen for the rest of the install, to match this window.
  var styled = showOverlayFirst ? styleUpdateSplash() : Promise.resolve();
  styled.then(function() {
    return closeForUpdate(showOverlayFirst ? 1500 : 0, !showOverlayFirst);
  }).catch(function() {
    $('update-installing-overlay').style.display = 'none';
    backgroundUpdateHandled = false;
  });
}
// styleUpdateSplash passes this window's language and theme to the
// Windows update window (updatesplash_windows.go); no-op elsewhere.
function styleUpdateSplash() {
  if (!window.setUpdateSplashStyle) return Promise.resolve();
  var cs = getComputedStyle(document.documentElement);
  return window.setUpdateSplashStyle(t('updateSplashTitle'), t('updateSplashChecking'),
    t('updateSplashInstalling'), t('updateSplashUpToDate'), t('updateSplashFailed'),
    document.documentElement.classList.contains('light'),
    cs.getPropertyValue('--grad').trim()).catch(function() {});
}
function checkBackgroundUpdate() {
  if (backgroundUpdateHandled) return;
  getUpdateInProgress().then(function(installing) {
    if (installing) {
      triggerCloseForUpdate(true);
      return;
    }
    // Fallback: a small package on a fast connection can finish the
    // whole download+install+restart cycle in well under one 3s poll
    // interval, landing entirely between two ticks — this poll would
    // then see updateInProgress==false the whole time and never once
    // catch it (confirmed live 2026-09-14: the window was left sitting
    // on a stale connection-error banner from the brief outage, having
    // missed its only chance to notice). Comparing the daemon/service's
    // actual running version against what this page loaded with catches
    // that case too, independent of the in-progress flag's timing.
    return getRunningVersion().then(function(v) {
      if (v && v !== appVersionStr) {
        triggerCloseForUpdate(false);
      }
    });
  }).catch(function() {
    // Daemon unreachable or an old build without these RPCs — nothing
    // useful to show yet; refreshStatus's own error path still covers
    // a genuine, non-update outage.
  });
}
$('btn-check-update').onclick = function() {
  $('btn-check-update').disabled = true;
  // Windows: hand the whole check (and install, if any) to the update
  // window, which reopens Spectrune when it's done. Falls back to the
  // in-window check below if it can't start.
  if (window.startUpdateCheck) {
    backgroundUpdateHandled = true; // this window is closing either way
    styleUpdateSplash().then(function() { return startUpdateCheck(); }).catch(function() {
      backgroundUpdateHandled = false;
      checkUpdateInWindow();
    });
    return;
  }
  checkUpdateInWindow();
};
function checkUpdateInWindow() {
  $('update-status-text').textContent = t('updateChecking');
  checkForUpdateNow().then(function(reply) {
    if (reply.Available) {
      // Just the immediate "found one" feedback — checkBackgroundUpdate's
      // own 3s poll (already running unconditionally) is what notices
      // Bridge.UpdateInProgress flip true right after this and takes
      // over closing/relaunching the window, same as the silent path.
      $('update-status-text').textContent = tf('updateAvailable', reply.Latest);
    } else {
      $('update-status-text').textContent = tf('updateUpToDate', reply.Current);
      $('btn-check-update').disabled = false;
    }
  }).catch(function(err) {
    $('update-status-text').textContent = tf('updateCheckFailed', String(err));
    $('btn-check-update').disabled = false;
  });
}
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
$('theme-anim-toggle').onchange = function() {
  state.theme.anim = $('theme-anim-toggle').checked;
  try { localStorage.setItem('themeAnim', state.theme.anim ? '1' : '0'); } catch (e) {}
  setAnimationsPaused();
};
$('theme-space-toggle').onchange = function() {
  state.theme.space = $('theme-space-toggle').checked;
  try { localStorage.setItem('themeSpace', state.theme.space ? '1' : '0'); } catch (e) {}
  applyBackgroundImage();
};
document.querySelectorAll('.theme-swatch').forEach(function(el) {
  el.onclick = function() { setThemeAccent(el.getAttribute('data-accent')); };
});

// ---------- reorderable button rows ----------
// Lets the user drag buttons within a [data-reorder] row into whatever
// order they prefer (e.g. put "Delete" first instead of last), remembered
// per row via localStorage so it's a one-time arrangement, not something
// re-done every launch. Deliberately opt-in per row (via the data-reorder
// attribute) rather than global — dialog button rows like Save/Cancel
// confirms keep a fixed, predictable order since those matter for safety,
// while pure action-toolbar rows (list actions, apps select-all row, etc.)
// are pure preference and safe to let the user rearrange.

function initReorderableRows() {
  document.querySelectorAll('[data-reorder]').forEach(setUpReorderableRow);
}

// Pointer Events rather than native HTML5 draggable/dragstart/dragover —
// WebKitGTK's HTML5 drag-and-drop support (the webview backend used on
// Linux) doesn't reliably fire dragstart for in-page reordering, so the
// first cut of this (native DnD) silently did nothing for most users.
// Pointer Events are just plain mouse tracking under the hood and work
// identically across WebKitGTK, WebView2 and every other backend this
// app embeds.
function setUpReorderableRow(row) {
  var key = 'buttonOrder:' + row.getAttribute('data-reorder');

  try {
    var saved = JSON.parse(localStorage.getItem(key) || 'null');
    if (Array.isArray(saved)) {
      saved.forEach(function(id) {
        var el = id && row.querySelector('#' + CSS.escape(id));
        if (el) row.appendChild(el);
      });
    }
  } catch (e) {}

  function persistOrder() {
    var ids = Array.prototype.map.call(row.children, function(el) { return el.id; }).filter(Boolean);
    try { localStorage.setItem(key, JSON.stringify(ids)); } catch (e) {}
  }

  // Where dragEl would land among its *other* siblings for pointer x —
  // the sibling immediately after the insertion point, or null for "at
  // the end". Ignores dragEl itself so it doesn't compare against its
  // own current position.
  function siblingAfterFor(dragEl, x) {
    var others = Array.prototype.filter.call(row.children, function(el) { return el !== dragEl; });
    for (var i = 0; i < others.length; i++) {
      var box = others[i].getBoundingClientRect();
      if (x < box.left + box.width / 2) return others[i];
    }
    return null;
  }

  Array.prototype.forEach.call(row.children, function(btn) {
    btn.style.touchAction = 'none';
    var startX = 0, startY = 0, moved = false;

    btn.addEventListener('pointerdown', function(e) {
      if (e.button !== 0) return;
      startX = e.clientX;
      startY = e.clientY;
      moved = false;
      btn.setPointerCapture(e.pointerId);
    });

    btn.addEventListener('pointermove', function(e) {
      if (!btn.hasPointerCapture(e.pointerId)) return;
      if (!moved) {
        // Small threshold so an ordinary click never gets misread as a
        // drag because of a pixel of mouse jitter between press/release.
        if (Math.abs(e.clientX - startX) < 4 && Math.abs(e.clientY - startY) < 4) return;
        moved = true;
        btn.classList.add('dragging');
      }
      var after = siblingAfterFor(btn, e.clientX);
      if (after !== btn.nextSibling) row.insertBefore(btn, after);
    });

    function endDrag(e) {
      if (!btn.hasPointerCapture(e.pointerId)) return;
      btn.releasePointerCapture(e.pointerId);
      if (moved) {
        btn.classList.remove('dragging');
        persistOrder();
        // The pointerup that ends a drag would otherwise also fire this
        // button's own click handler (e.g. re-opening "Add…" right after
        // the user finished dragging it) — swallow exactly that one.
        var suppressClick = function(ev) { ev.stopPropagation(); };
        btn.addEventListener('click', suppressClick, { capture: true, once: true });
      }
      moved = false;
    }
    btn.addEventListener('pointerup', endDrag);
    btn.addEventListener('pointercancel', endDrag);
  });
}

// ---------- startup ----------


applyTheme();
setAnimationsPaused();
applyI18n();
initReorderableRows();
refreshProfiles();
refreshStatus();
setInterval(refreshStatus, 1000);
setInterval(checkBackgroundUpdate, 3000);
// Windows: tell the update window (updatesplash_windows.go) this page has
// loaded, so it can step aside without exposing a blank window.
// Not requestAnimationFrame: the window starts off screen (runGUI), where
// Chromium may treat it as occluded and never run animation frames.
if (window.uiReady) {
  if (document.readyState === 'complete') setTimeout(uiReady, 50);
  else window.addEventListener('load', function() { setTimeout(uiReady, 50); });
}
</script>
</body>
</html>`
