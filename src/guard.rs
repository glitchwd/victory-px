use crate::metrics::{LogLine, RunStats};
use crate::system::HostMetrics;
use std::path::Path;

#[derive(Debug, Clone)]
pub struct GuardFinding {
    pub severity: &'static str, // "WARN" | "TIP"
    pub title: &'static str,
    pub hint: String,
}

pub fn evaluate_guard(
    wine_bin: &str,
    series_s: bool,
    gamescope: bool,
    has_gamescope: bool,
    metrics: &HostMetrics,
    stats: &RunStats,
    logs: &[LogLine],
) -> Vec<GuardFinding> {
    let mut out = Vec::new();

    // 1. Wine vs GE-Proton check
    if !wine_bin.is_empty() && !wine_bin.contains("Proton") {
        out.push(GuardFinding {
            severity: "TIP",
            title: "System Wine active",
            hint: "Use GE-Proton (--wine or launch-*.sh) for vkd3d-proton D3D12 support.".into(),
        });
    }

    // 2. Gamescope availability check
    if gamescope && !has_gamescope {
        out.push(GuardFinding {
            severity: "WARN",
            title: "Gamescope missing",
            hint: "Gamescope is enabled but `gamescope` was not found on PATH.".into(),
        });
    }

    // 3. WINEPREFIX Z: drive check (if WINEPREFIX is set)
    if let Ok(prefix) = std::env::var("WINEPREFIX") {
        let z = Path::new(&prefix).join("dosdevices/z:");
        if !z.exists() {
            out.push(GuardFinding {
                severity: "WARN",
                title: "Wine Z: drive missing",
                hint: "WINEPREFIX needs dosdevices/z: mapped to / for Linux paths.".into(),
            });
        }
    }

    // 4. High RAM usage + Series X check
    if let Some(used_pct) = (metrics.ram_used_mib * 100).checked_div(metrics.ram_total_mib) {
        if used_pct >= 88 && !series_s {
            out.push(GuardFinding {
                severity: "WARN",
                title: "High host RAM pressure",
                hint: format!("Host RAM at {used_pct}% — press [C] to switch to Series S."),
            });
        }
    }

    // 5. Log-driven troubleshooting rules
    let mut saw_dlss_missing = false;
    let mut saw_crash = false;
    let mut saw_dri2 = false;
    for l in logs {
        if l.text.contains("No NVIDIA files: running without DLSS") {
            saw_dlss_missing = true;
        }
        if l.text.contains("unhandled exception") || l.text.contains("BacktraceBreakpad") {
            saw_crash = true;
        }
        if l.text.contains("failed to create dri2 screen") {
            saw_dri2 = true;
        }
    }

    if saw_dlss_missing {
        out.push(GuardFinding {
            severity: "TIP",
            title: "NVIDIA DLSS files missing",
            hint: "Run `victory get-dlss` once in the fork dir to enable DLSS / FG.".into(),
        });
    }
    if saw_crash && stats.errors > 0 {
        out.push(GuardFinding {
            severity: "WARN",
            title: "Title crash detected",
            hint: "Try Series S ([C]), GE-Proton, or check free RAM/VRAM before relaunching.".into(),
        });
    }
    if saw_dri2 {
        out.push(GuardFinding {
            severity: "TIP",
            title: "EGL/DRI2 fallback warning",
            hint: "Ensure Proton WINEDLLOVERRIDES and GPU Vulkan ICD are active.".into(),
        });
    }

    out
}

pub fn classify_error_line(
    line: &str,
    series_s: bool,
    metrics: &HostMetrics,
    wine_bin: &str,
) -> Option<GuardFinding> {
    if line.contains("unhandled exception") || line.contains("BacktraceBreakpad") {
        let ram_hint = if metrics.ram_total_mib > 0 {
            let pct = (metrics.ram_used_mib * 100) / metrics.ram_total_mib;
            if pct >= 80 && !series_s {
                format!("Host RAM is at {pct}% on Series X — try Series S ([C]) and close heavy apps.")
            } else {
                "Try switching to Series S ([C]) or running with GE-Proton.".into()
            }
        } else {
            "Try switching to Series S ([C]) or running with GE-Proton.".into()
        };
        let wine_hint = if !wine_bin.is_empty() && !wine_bin.contains("Proton") {
            " Also switch from system Wine to GE-Proton."
        } else {
            ""
        };
        return Some(GuardFinding {
            severity: "WARN",
            title: "VIctory / Title Exception Crash",
            hint: format!("{ram_hint}{wine_hint}"),
        });
    }
    if line.contains("wineComputePipelineFault") || line.contains("VK_ERROR_DEVICE_LOST") {
        return Some(GuardFinding {
            severity: "WARN",
            title: "GPU / Shader Pipeline Fault",
            hint: "Clear shader cache, update GPU driver, or test another VIctory fork ([B]).".into(),
        });
    }
    if line.contains("unexpected argument") {
        return Some(GuardFinding {
            severity: "WARN",
            title: "Fork CLI Flag Mismatch",
            hint: "This VIctory build rejected a launch flag. Check fork name or update victory-px.".into(),
        });
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn guard_flags_high_ram_on_series_x() {
        let hm = HostMetrics {
            ram_used_mib: 29_000,
            ram_total_mib: 32_000,
            ..Default::default()
        };
        let findings = evaluate_guard(
            "/usr/bin/wine",
            false,
            false,
            true,
            &hm,
            &RunStats::default(),
            &[],
        );
        assert!(findings.iter().any(|f| f.title == "High host RAM pressure"));
        assert!(findings.iter().any(|f| f.title == "System Wine active"));
    }
}
