#!/usr/bin/env bash
set -euo pipefail

fail() { printf 'Victory-PX NVIDIA-RT: %s\n' "$*" >&2; exit 1; }

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
[[ -x "$root/victory-px" ]] || fail 'Build Victory-PX first with: go build -trimpath -o victory-px .'

proton=${PX_PROTON_DIR:-$HOME/.local/share/Steam/compatibilitytools.d/GE-Proton11-14-x86_64}
if [[ ! -x "$proton/files/bin/wine" || ! -r "$proton/version" ]]; then
    proton=${PX_PROTON_DIR:-$HOME/.steam/root/compatibilitytools.d/GE-Proton11-14-x86_64}
fi
[[ -x "$proton/files/bin/wine" && -r "$proton/version" ]] || fail 'GE-Proton 11-14 is missing.'
proton=$(realpath -- "$proton")
read -r proton_build proton_release _ < "$proton/version"

data_home=${XDG_DATA_HOME:-$HOME/.local/share}
export WINEPREFIX=${PX_PROTON_PREFIX:-$data_home/victory-px/prefix-${proton##*/}-$proton_build/pfx}
[[ -d "$WINEPREFIX/drive_c" && -e "$WINEPREFIX/dosdevices/z:" ]] || fail 'The existing PX Proton prefix is missing or incomplete.'

# Use the prefix's Proton graphics DLLs when invoking Wine outside Steam.
export WINEDLLOVERRIDES="d3d12,d3d12core,dxgi,d3d11,d3d10core,d3d9=n;d3dcompiler_47=n,b${WINEDLLOVERRIDES:+;$WINEDLLOVERRIDES}"

exec "$root/victory-px" --fork nvidia-rt \
    --wine "$proton/files/bin/wine" --runtime-label "$proton_release" \
    --check-updates "$@"
