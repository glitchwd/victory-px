# victory-px

Victory-PX is an experimental Linux launcher for comparing how VIctory builds
run under Wine, with or without Gamescope. It started as a tool for personal
compatibility tests. You can start a session, watch frame rate and GPU usage,
and inspect logs from the same terminal.

## Setup

Download the Linux x86-64 archive and `SHA256SUMS` from [Releases](../../releases).
Save both in the same directory, then run `sha256sum -c SHA256SUMS` before
extracting the archive. To build from source, use a supported Go release
(Go 1.21 or newer).

You also need Wine, a terminal, your own Windows build of VIctory, and a
title executable supported by that build.
Follow VIctory's build instructions for its dependencies and supported titles.

```sh
go build -trimpath -o victory-px .
./victory-px --victory-dir /path/to/VIctory --exe /path/to/title.exe
```

Run the build command from the Victory-PX source directory. It builds the
Linux launcher. Compile VIctory separately to obtain the Windows `victory.exe`
that Wine runs. VIctory's build instructions may require Windows and MSVC;
building it on Linux requires a Windows cross-compilation toolchain.

The default binary is `target/x86_64-pc-windows-msvc/diag/victory.exe` under
`--victory-dir`. For a different build, pass `--victory-bin /path/to/victory.exe`.
For example, a Windows diagnostics build may be placed under `target/diag/`:

```sh
./victory-px \
  --victory-dir /path/to/VIctory \
  --victory-bin /path/to/VIctory/target/diag/victory.exe \
  --exe /path/to/title.exe \
  --series-s --gamescope --display-preset upscale
```

`--victory-dir` selects VIctory's working directory, `--victory-bin` selects
the Windows launcher binary, and `--exe` selects the title executable.
After updating VIctory or creating a new Git worktree, compile that revision
and select its resulting binary. A worktree contains source files; build
outputs must be produced separately. Selecting an older `victory.exe` runs
that older build even when the working directory contains newer sources.
An error stating `VIctory binary: stat ... no such file or directory` means
you need to build VIctory or point `--victory-bin` to an existing build.

Use Linux paths. Wine uses its standard `Z:` drive mapped to `/` for the
title and shader archive. Set `WINEPREFIX` to use a different Wine prefix.
VIctory runs from the supplied working directory and reads its own configuration
there. The companion does not require or generate `victory.toml`.

`VICTORY_DIR` and `VICTORY_EXE` are environment defaults for the corresponding
flags. `--help` lists the options. `--version` prints the Go module version,
or `(devel)` for a local build. Choose Wine with `--wine` if needed. The child
inherits your environment and Wine configuration.

## Build compatibility

Belobug is the build used for the initial compatibility experiments. To try
another fork, point `--victory-dir` and `--victory-bin` at your build. Its CLI
must accept `run <title> --execute --console <profile>`. The optional shader
translation action also requires `translate <archive>`. Log metrics depend on
the build's output format. The GitHub update check tracks Belobug's
`a-publier` branch.

When sharing a result, include the build or commit, Wine version, GPU, and
Gamescope preset (or Wine direct).

## Controls

| Key | Action |
| --- | --- |
| S | Run the title or stop the current session |
| T | Ask VIctory to translate `shaders.rpf` beside the title executable |
| C / D | Change the console profile or open Display settings |
| F / [ / ] | Change the log filter or scroll |
| R / Y | Save the session summary or copy it to the clipboard |
| Tab / Shift+Tab | Cycle through Monitor, Display and PortWatch forwards or backwards |
| U | Check Belobug on GitHub. In PortWatch, also refresh public headlines |
| Q / Ctrl+C | Quit and stop the current session |

Clipboard copy uses `wl-copy` on Wayland or
`xclip` / `xsel` on X11. The monitor reads Linux host RAM and, when available,
GPU 0 via `nvidia-smi`. VIctory statistics depend on its log format and build
options. Missing values are shown as dashes, and running values older than
12 seconds are marked stale.

Stop and quit terminate the launched Linux process group. Wine services that
are shared or detached, processes that create a new group, and a forced kill
of the companion are outside that cleanup. A dedicated Wine prefix keeps
the session separate from your other Wine applications.

## Display settings

Wine direct is the default. Press D, then G to switch between Wine direct
and Gamescope for the next run. In Display settings, P cycles through
these presets:

| Preset | Game resolution | Starting output size | Filter |
| --- | --- | --- | --- |
| Small | 1280x720 | 1280x720 | Linear |
| Upscale | 1280x720 | 1920x1080 | FSR |
| 1080p | 1920x1080 | 1920x1080 | Linear |

I and O cycle the game resolution and output size independently. A toggles
FSR, L cycles the frame rate limit (unlimited, 30, 60), and Enter switches
the starting mode between windowed and fullscreen. For custom sizes or a
different frame rate limit, use the command line:

```sh
./victory-px --victory-dir /path/to/VIctory --exe /path/to/title.exe \
  --gamescope --display-preset upscale --game-size 1280x800 \
  --window-size 1920x1200 --fullscreen --fps-limit 30
```

Options apply to the next launch. A smaller game resolution can help when
VIctory uses it and rendering is limited by the GPU. The effective FPS
limit can differ from the requested value depending on the refresh rate.
The cap requires a Gamescope build with `--framerate-limit`.

While the Gamescope window has focus, press **Super+F** to enter or leave
fullscreen. In Wine direct, window shortcuts are handled by the game or
desktop. See the [Gamescope controls](https://github.com/ValveSoftware/gamescope#keyboard-shortcuts).

## Local files and network access

Reports go to `${XDG_STATE_HOME:-~/.local/state}/victory-px/reports/`.
Reports and clipboard summaries include the operation, status, selected launch
options, and recognized numeric metrics. They exclude raw output, paths,
environment values, timestamps, and host identifiers.

Use `--save-log` to retain raw child output under
`${XDG_STATE_HOME:-~/.local/state}/victory-px/logs/`. Report and log files are
created with mode `0600`. Each raw log stops at 32 MiB. It can contain private
data, so review it before sharing.

Press U to check the public Belobug `a-publier` branch and latest stable
GitHub release. Use `--check-updates` to check at startup and every 15 minutes
while the companion is open. The Monitor shows the update status. PortWatch
shows the public commit, release, and time of the last successful check.

The GitHub requests use fixed public endpoints and no credentials. The
comparison with the checked-out Git revision runs locally with Git lazy
fetching disabled. The checker does not fetch Git objects, install a release,
or verify which commit the existing `victory.exe` was built from. Without
`--check-updates`, network requests start only on demand. Pressing U in
PortWatch also fetches its public feed from `raw.githubusercontent.com`.
