---
title: Display and Gamescope
---
# Display and Gamescope

Wine direct is the default. Press **2** for Display or **G** to open Gamescope settings on any tab. **Space** or **Enter** switches the backend in the modal. Changes apply to the next run.

| Preset | Game resolution | Output size | Filter |
| --- | --- | --- | --- |
| `small` | 1280 × 720 | 1280 × 720 | Linear |
| `upscale` | 1280 × 720 | 1920 × 1080 | FSR |
| `1080p` | 1920 × 1080 | 1920 × 1080 | Linear |

While stopped, use **P** for presets, **A** for FSR and **L** for the frame cap (unlimited, 30 or 60). Use **F** in the modal or **Enter** on the Display tab for fullscreen. **Esc** or **G** closes the modal.

```sh
./victory-px-rs/target/release/victory-px --fork my-build --exe /path/to/title.exe \
  --gamescope --display-preset upscale --fullscreen --fps-limit 30
```

A frame cap requires a Gamescope build that supports `--framerate-limit`.
