---
title: Fork inventory
---
# Fork inventory

Press **B** to open the loadout. Use the arrow keys and **Enter** or click a card. **N** adds a profile. In the wizard, open the desired directory, press **Space** to scan it, select a binary if prompted, then name and save the profile. **Esc** cancels. **X** opens the highlighted fork folder. **Delete** removes the highlighted saved profile while stopped.

Fork profiles are organized into tiered slots (`Covert`, `Classified`, `Restricted`, and `Mil-Spec` for custom builds) so you can quickly distinguish and switch between different VIctory builds.

Press **O** to browse for the game's executable. **Enter** opens a directory or selects an executable, and **Backspace** goes up a directory. **V** opens the selected game's folder in the desktop file manager. Stop the current session before switching or adding a fork.

## CLI profiles

```sh
px=./victory-px-rs/target/release/victory-px
"$px" --save-fork my-build --victory-dir /path/to/VIctory --victory-bin /path/to/victory.exe
"$px" --list-forks
"$px" --fork my-build --exe /path/to/title.exe
```

Profiles live in `${XDG_CONFIG_HOME:-~/.config}/victory-px/forks/`. Names use 1 to 40 ASCII letters, digits, dots, underscores or hyphens and start with a letter or digit. The game selection is stored in `game.json` in the app's config directory. These files contain local paths and use mode `0600`.

Profiles launch the configured `victory.exe` with the active title executable and selected console profile (`run --execute`, or fork-specific launch subcommands when configured).
