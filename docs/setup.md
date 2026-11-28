---
title: Setup and launch
---
# Setup and launch

These commands target the Rust launcher on the `lighthouse` branch. Run them from the repository root.

## Requirements

- Linux x86-64 and a current stable Rust toolchain with Cargo.
- Wine or Wine64, or the Wine executable from a compatible Proton distribution. Use an initialized prefix with a `Z:` drive mapped to `/`.
- A compiled VIctory Windows binary and a supported title executable.
- Optional: Gamescope for the nested display, `xdg-open` for opening reports and folders, and `nvidia-smi` for NVIDIA telemetry. AMD telemetry uses driver data from sysfs when available.

## Build and launch

```sh
cargo build --release --locked --manifest-path victory-px-rs/Cargo.toml
./victory-px-rs/target/release/victory-px --help
./victory-px-rs/target/release/victory-px
```

A direct launch can use explicit paths to the build and title:

```sh
./victory-px-rs/target/release/victory-px \
  --victory-dir /path/to/VIctory \
  --victory-bin /path/to/victory.exe \
  --exe /path/to/title.exe
```

If `--victory-bin` is omitted, the default is `target/x86_64-pc-windows-msvc/diag/victory.exe` inside `--victory-dir`. `VICTORY_DIR` and `VICTORY_EXE` supply defaults for their corresponding flags. Set `WINEPREFIX` to choose a prefix and `--wine` to select a runtime executable. An isolated prefix makes it easier to avoid shared Wine services.

The launcher automatically detects a Steam GE-Proton installation when `--wine` is omitted and applies `WINEDLLOVERRIDES` for DXVK/VKD3D-Proton, falling back to system Wine if no GE-Proton is found. Set `WINEPREFIX` explicitly (for example to an initialized prefix under `${XDG_DATA_HOME:-~/.local/share}/victory-px/`), because leaving `WINEPREFIX` unset uses `~/.wine`.

The root `launch-*.sh` helpers use the Rust release binary. First save the matching fork profile and configure the existing Proton installation and prefix. Override discovery with `PX_PROTON_DIR` and `PX_PROTON_PREFIX` as needed. The Go package at the repository root is retained as legacy source.

The launcher stops its Linux process group when you stop the session or quit. Wine services shared with other programs or processes that detach into another group may remain running.
