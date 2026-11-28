#!/usr/bin/env bash
set -euo pipefail

fail() { printf 'Sley: %s\n' "$*" >&2; exit 1; }

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
[[ -t 0 && -t 1 ]] || fail 'Open a terminal in the repository and run ./launch-sley.sh.'
launcher="$root/victory-px-rs/target/release/victory-px"
[[ -x "$launcher" ]] || fail 'Build the launcher with: cargo build --release --locked --manifest-path victory-px-rs/Cargo.toml'

proton=${PX_PROTON_DIR:-$HOME/.steam/root/compatibilitytools.d/GE-Proton11-14-x86_64}
[[ -x "$proton/files/bin/wine" && -r "$proton/version" ]] \
    || fail 'GE-Proton 11-14 is missing. Set PX_PROTON_DIR to its installation directory.'
proton=$(realpath -- "$proton")
read -r build release _ < "$proton/version"
[[ $build =~ ^[0-9]+$ && $release =~ ^GE-Proton[0-9]+-[0-9]+(-viewtree)?$ ]] \
    || fail 'The selected GE-Proton version file is invalid.'

data_home=${XDG_DATA_HOME:-$HOME/.local/share}
export WINEPREFIX=${PX_PROTON_PREFIX:-$data_home/victory-px/prefix-${proton##*/}-$build/pfx}
[[ -d "$WINEPREFIX/drive_c" && -e "$WINEPREFIX/dosdevices/z:" ]] \
    || fail 'The Proton prefix is missing or incomplete. Set PX_PROTON_PREFIX to an initialized pfx directory.'
export WINEDLLOVERRIDES="d3d12,d3d12core,dxgi,d3d11,d3d10core,d3d9=n;d3dcompiler_47=n,b${WINEDLLOVERRIDES:+;$WINEDLLOVERRIDES}"

# Sley's README reports this larger card-buffer cap as its main measured gain.
# It was measured on a 12 GB card; override it if the 8 GB GPU runs out of VRAM.
export VICTORY_CARD_BUFFERS_MIN_KB=${VICTORY_CARD_BUFFERS_MIN_KB:-64}
export VICTORY_CARD_BUFFERS_MB=${VICTORY_CARD_BUFFERS_MB:-2048}

exec "$launcher" --fork sley --wine "$proton/files/bin/wine" \
    --runtime-label "$release" --series-s --gamescope "$@"
