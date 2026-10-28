# Webbite Brick for Desktop

<img width="2560" height="1440" alt="Image" src="https://github.com/user-attachments/assets/eaf74082-5190-47f7-a515-9cab83cb7944" />

## About

This is the repo of Webbite Brick for Desktop, a graphical desktop app for
[Brick][rb].

The app runs in the system tray and keeps a local folder in two-way sync with
Brick — it uploads local-only files, downloads remote-only files, and
propagates deletions in either direction — deleting a file or folder locally
moves it to trash on Brick, and a file or folder trashed on Brick is removed
locally. When both sides edit the same file, the online version wins.

After the initial pass it watches the folder for filesystem changes and polls
Brick periodically, so both sides stay in sync for as long as the app is
running.

The app does not need the [Brick CLI][cli], but it is compatible with it: both
share the same configuration and sync state, and only one of them syncs at a
time.

[rb]: https://webbite.io
[cli]: https://github.com/webbite-io/brick-cli

## Installation

Brick for Desktop is currently available for Linux, as an AppImage, and for
macOS as a .DMG app. A version for Microsoft Windows is currently in
development.

### Linux

#### Quick Install

Install the latest release on Linux like so:

```bash
curl -fsSL https://webbite.io/desktop/appimage/install.sh | bash
```

The AppImage will be installed to `~/.local/bin` by default, along with a
desktop entry and icons, so the app shows up in your application menu.

Running the same command again upgrades an existing installation in place.

#### Custom Installation Directory

To install the latest release to a custom directory, do like so:

```bash
curl -fsSL https://webbite.io/desktop/appimage/install.sh | bash -s -- --prefix $HOME/bin
```

#### Uninstall

To remove the app, its desktop entry and icons, do like so:

```bash
curl -fsSL https://webbite.io/desktop/appimage/install.sh | bash -s -- --uninstall
```

### macOS

To download the latest version, go to [Releases][macos] and download the latest
`.dmg` file that has the name `brick-ui-{version}-macos-universal.dmg`. Once
downloaded, double-click the file to open the "drag-to-install" window and
follow the instructions.

When the app is added to the _Applications_ folder on your Mac, you can easily
start it by pressing Cmd-Space in Finder and search for _Webbite Brick_.

[macos]: https://github.com/webbite-io/brick-wails/releases

## Documentation

Read more about how to use Brick for Desktop at <https://docs.webbite.io/en/desktop/>
