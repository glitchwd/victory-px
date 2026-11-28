# victory-px

This guide covers the Rust launcher on the `lighthouse` branch.

> OpenCode-inspired Rust (`ratatui`) TUI launcher, fork loadout, and Lighthouse-style benchmark monitor for **VIctory** builds on Linux.

[![Rust](https://img.shields.io/badge/Rust-2021-orange?style=flat-square&logo=rust)](victory-px-rs/Cargo.toml)
[![Platform Linux](https://img.shields.io/badge/Platform-Linux%20x86__64-00F0FF?style=flat-square&logo=linux&logoColor=black)](#quick-start)
[![Docs](https://img.shields.io/badge/Docs-lighthouse-FF2A85?style=flat-square)](docs/index.html)

---

## Highlights

- **Tiered Fork Loadout (`B`)** — Tiered ASCII inventory (`Covert`, `Classified`, `Restricted`, `Mil-Spec`), in-TUI 3-step fork wizard (`N`) with automatic `victory.exe` scan, folder opener (`X`/`V`), and hidden paths in the inventory.
- **Lighthouse Reports (`3` / `R` / `--bench`)** — Live in-app **Performance / Stability / Headroom** gauges, repeatable headless benchmarks (`--bench --duration 30`), and standalone Chrome Lighthouse-style HTML exports.
- **Quick Modals** — Gamescope & display config (`G`), integrated `.exe` / folder browser (`O`), and animated **Vice City / GTA VI** logo FX (`T`).
- **Hardware Vitals** — Live host RAM, CPU, kernel, and GPU load/temp/VRAM for **NVIDIA** (`nvidia-smi`) and **AMD** (`amdgpu` sysfs).
- **Corpus Export (`W`)** — Markdown bundle with metrics and launch settings for research threads.

---

## Quick Start

Requires **Linux x86_64**, **Rust / Cargo**, **Wine** (or Proton), and a compiled `victory.exe`.

```sh
cargo build --release --locked --manifest-path victory-px-rs/Cargo.toml
./victory-px-rs/target/release/victory-px
```

### Register a fork from CLI (optional)

```sh
./victory-px-rs/target/release/victory-px --save-fork my-fork \
  --victory-dir /path/to/VIctory \
  --victory-bin /path/to/VIctory/victory.exe
```

### Repeatable Headless Benchmark

```sh
./victory-px-rs/target/release/victory-px --fork my-fork --exe /path/to/title.exe --bench --duration 30
```

Outputs a Lighthouse HTML report (`lighthouse-*.html`) and a Markdown bundle (`corpus-*.md`) under `${XDG_STATE_HOME:-~/.local/state}/victory-px/reports/`.

---

## Keybindings

| Key | Action |
| --- | --- |
| `1` / `2` / `3` | Switch between **Monitor**, **Display**, and **Lighthouse** |
| `B` | Open **Forks Loadout** (tiered ASCII inventory) |
| `N` | Add a new fork in-TUI (`Space` confirms dir, auto-scans `victory.exe`) |
| `O` | Open integrated **Game Browser** (`.exe` / folder) |
| `G` | Open **Gamescope** quick config modal |
| `C` | Toggle console profile (`Series X` / `Series S`) |
| `S` | Start or stop the active session |
| `R` / `W` | Export **Lighthouse HTML** / **Corpus Markdown** |
| `T` | Open **Theme & Logo FX** modal |
| `X` / `V` | Open highlighted fork folder / game folder in file manager |
| `Q` / `Ctrl+C` | Stop session and quit (`Ctrl+C` works while typing a profile name) |

---

## Documentation

- [Setup & Launch](docs/setup.md)
- [Fork Inventory](docs/inventory.md)
- [Display & Gamescope](docs/display.md)
- [Reporting & Corpus](docs/reporting.md)
