# Changelog

All notable changes to `victory-px` will be documented in this file.

## [v0.2.0] - 2026-11-28

### Added
- **Auto-Loadout Release Updater (`U`)**: Download and atomically replace `victory.exe` directly from the latest GitHub release for release-tracking forks (`VIctory-NVIDIA`, `VIctory-NVIDIA-RT`, `Vilocity`) inside **LOADOUT**.
- **Pre-update validation**: Enforces configuring the game executable/folder (`G` / `F`) and the fork build folder/binary (`N` / `E`) before downloading a release.
- **Fork deletion (`X` / `Delete` / `Backspace`)**: Remove saved fork entries directly from **LOADOUT** without manually deleting `~/.config/victory-px/forks/*.json`.
- **Versioning**: Default `releaseVersion` set to `v0.2.0` (overridable at build time via `-ldflags "-X main.releaseVersion=..."`).

## [v0.1.0] - 2026-11-27

### Added
- Initial release of `victory-px` TUI launcher with Gamescope, Proton, fork inventory (`LOADOUT`), and upstream/release status tracking.
