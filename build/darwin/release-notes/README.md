# macOS release notes

What Sparkle's "A new version is available" window shows, one file per
release: `<version>.html`, named exactly like the tag (`1.0.3.html` for tag
`1.0.3`).

Each file is an HTML fragment — no `<html>`, `<head>` or `<body>` — that
`build/darwin/release.sh` writes inline into the appcast item's
`<description>`, after a small stylesheet that picks up the system font and
light/dark appearance. Keep it short and plain: Sparkle renders it in a small
webview with JavaScript off. For example:

```html
<h2>What's new</h2>
<ul>
  <li>Sync resumes on its own once Brick is reachable again.</li>
</ul>
<h2>Fixes</h2>
<ul>
  <li>An expired session no longer logs you out after a network hiccup.</li>
</ul>
```

The file can't contain `]]>`, which would end the appcast's CDATA section.

`make release` refuses to build a release whose notes file is missing
(a `--skip-notarize` test build gets placeholder notes instead), and
`make release-to-github` refuses an appcast with placeholder notes.

To change the notes of a release that's already out, edit its file here (so
the repo stays the record), make the same change to the `<description>` in
`dist/appcast.xml`, and upload that to webbite.io again. Only the DMG is
signed, not the appcast, so the update stays valid; rebuilding with
`make release` would instead make a new DMG that has to be uploaded too.
