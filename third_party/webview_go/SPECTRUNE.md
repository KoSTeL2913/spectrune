# Spectrune WebKit runtime fix

Based on github.com/webview/webview_go at
v0.0.0-20240831120633-6173450d4dd6, with its MIT license retained.

The only upstream code change is the library candidate list in
libs/webview/include/webview.h (get_webkit_library). Try the runtime
SONAMEs libwebkit2gtk-4.1.so.0 and libwebkit2gtk-4.0.so.37 before the
unversioned development links.

Without this change, the GUI can send RPC requests and the daemon can
reply, but eval_impl silently skips delivering those replies to JavaScript
on runtime-only installations. Profiles appear empty and status never updates.

Keep this module in the repository and use the go.mod replacement for both
ordinary Go builds and packaged releases. Do not patch the Go module cache
or add system library symlinks. When updating this dependency, retain this
fix until the upstream loader supports runtime SONAMEs.

Validation: install the Linux package on a machine with libwebkit2gtk-4.1-0
and no libwebkit2gtk-4.1.so development link, then verify that the actual GUI
loads saved profiles and receives daemon state replies.
