---
title: Reporting and benchmarks
---
# Reporting and benchmarks

- **3** opens the Lighthouse panel, including the **Saved Reports** list (`↑`/`↓` to select, `Enter` to open, `Del` to delete a report and its attached screenshot).
- **R** saves `lighthouse-*.html` (with an in-game screenshot `*.png` attached when available) and asks `xdg-open` to display it.
- **W** saves `report-*.md` (with an in-game screenshot `*.png` attached when available) with a short message and a full report outline.

Reports live in `${XDG_STATE_HOME:-~/.local/state}/victory-px/reports/`. New reports use mode `0600` and unique filenames. A missing desktop opener leaves the saved report available in this directory.

## Headless benchmark

```sh
./victory-px-rs/target/release/victory-px --fork my-build --exe /path/to/title.exe \
  --bench --duration 30
```

The command stops after the requested duration or when the child exits. It prints the results and writes both reports. It can run without a terminal UI or a desktop opener. The game and its graphics backend still require a working Linux graphics environment.

## What the reports contain

Both formats use recognized numeric telemetry, warning and error counts, launch settings, run duration, and coarse hardware details such as GPU vendor and memory capacity. The reports omit raw logs, paths, custom profile names, custom runtime labels, and host probe strings. Missing measurements appear as unavailable.

Performance compares measured FPS to 60 FPS. Stability is a heuristic based on logged warnings and errors. Headroom uses detected VRAM and shows unavailable when that probe yields no reading. Add scene, build revision, and repeat count when comparing two runs.

## Session logs

Victory-PX hides path-bearing child output in the monitor, including Windows,
Wine and Linux paths. Consecutive redactions with the same severity are shown
as one muted entry. In the Rust monitor, hold **Ctrl+P** to see the original
lines temporarily on terminals that report key releases. Release the keys or
leave the monitor to hide them again. This preview stays in the running
session and is excluded from reports and saved logs.

The legacy Go launcher's optional `--save-log` applies
the same redaction before writing new log entries. Wine and VIctory can also
write their own logs directly to disk. Those files are managed by the child
programs and are outside Victory-PX's log filter.
