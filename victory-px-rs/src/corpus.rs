use crate::forks::{state_root, NVIDIA_FORK, NVIDIA_RT_FORK, VILOCITY_FORK};
use crate::metrics::RunStats;
use crate::private_files;
use crate::runner::DisplayOptions;
use crate::system::HostMetrics;
use chrono::Utc;
use std::fs;
use std::path::PathBuf;
use std::time::Duration;

pub struct CorpusMeta<'a> {
    pub fork_name: &'a str,
    pub host_metrics: &'a HostMetrics,
    pub series_s: bool,
    pub gamescope: bool,
    pub display: &'a DisplayOptions,
    pub duration: Option<Duration>,
    pub state_label: &'a str,
}

fn public_fork(name: &str) -> &'static str {
    match name {
        NVIDIA_FORK => NVIDIA_FORK,
        NVIDIA_RT_FORK => NVIDIA_RT_FORK,
        VILOCITY_FORK => VILOCITY_FORK,
        _ => "custom or unselected",
    }
}

fn build_line(meta: &CorpusMeta<'_>) -> &'static str {
    match public_fork(meta.fork_name) {
        NVIDIA_FORK | NVIDIA_RT_FORK => "NVIDIA",
        _ => gpu_vendor(meta),
    }
}

fn gpu_vendor(meta: &CorpusMeta<'_>) -> &'static str {
    match meta.host_metrics.gpu_vendor {
        "AMD" => "AMD",
        "NVIDIA" => "NVIDIA",
        _ => "unknown",
    }
}

fn run_status(value: &str) -> &str {
    match value {
        "Idle" | "Running" | "Stopped" | "Finished" | "Completed" | "Interrupted" | "Failed" => {
            value
        }
        _ => "unknown",
    }
}

fn memory(value: Option<u64>) -> String {
    value
        .filter(|&v| v > 0)
        .map(|v| format!("{v} MiB"))
        .unwrap_or_else(|| "not recorded".into())
}

pub fn build_corpus_bundle(stats: &RunStats, meta: &CorpusMeta<'_>) -> String {
    let build_line = build_line(meta);
    let fork = public_fork(meta.fork_name);
    let console = if meta.series_s {
        "Series S (--series-s)"
    } else {
        "Series X"
    };
    let switches = if meta.gamescope {
        format!(
            "Gamescope; console={console}; game={}x{}; output={}x{}; fsr={}; fullscreen={}; fps-limit={}",
            meta.display.game_w,
            meta.display.game_h,
            meta.display.win_w,
            meta.display.win_h,
            meta.display.fsr,
            meta.display.fullscreen,
            meta.display.fps_limit
        )
    } else {
        format!("Wine direct; console={console}")
    };

    let mut ev_parts = Vec::new();
    if stats.fps.count > 0 {
        ev_parts.push(format!(
            "FPS min/avg/max {:.1}/{:.1}/{:.1} (n={}, last={:.1})",
            stats.fps.min,
            stats.fps.avg(),
            stats.fps.max,
            stats.fps.count,
            stats.fps.last.unwrap_or(0.0)
        ));
    }
    if stats.frame_time.count > 0 {
        ev_parts.push(format!(
            "ms/frame min/avg/max {:.2}/{:.2}/{:.2}",
            stats.frame_time.min,
            stats.frame_time.avg(),
            stats.frame_time.max
        ));
    }
    if let Some(d) = stats.dropped {
        ev_parts.push(format!("dropped/frame last {:.1}", d));
    }
    ev_parts.push(format!(
        "warnings={}, errors={}, status={}",
        stats.warnings,
        stats.errors,
        run_status(meta.state_label)
    ));
    let evidence_line = ev_parts.join("; ");

    let ram = memory(Some(meta.host_metrics.ram_total_mib));
    let vram = memory(meta.host_metrics.vram_total_mib);
    let hw_line = format!("GPU vendor: {}; VRAM: {vram}; RAM: {ram}", gpu_vendor(meta));
    let dur_str = meta
        .duration
        .map(|d| format!("{}s", d.as_secs()))
        .unwrap_or_else(|| "not recorded".into());

    let evidence_type = if stats.fps.count > 0 || stats.frame_time.count > 0 {
        "measured"
    } else {
        "no performance samples"
    };
    let mut short = format!(
        "**<One-line claim describing observed behaviour>**\n\n\
        Claim: <state the single claim clearly>\n\
        What is new: <new measurement or confirms/contradicts <reply header>>\n\
        Evidence type: {evidence_type}\n\
        Evidence: {evidence_line}\n\
        AI model and settings: <model name, client, effort>\n\
        Where it ran: local, victory-px\n\
        Human check: <record actual review>\n\
        Sources read: victory-px session metrics ({fork})\n\
        What was run: {hw_line} | build: {fork} | switches: {switches} | duration: {dur_str}\n\
        Build: {build_line}\n\
        Not checked / unsure about: <caveats, e.g. single run only>\n\
        How to reproduce: launch via victory-px with {switches}\n\
        Next step or open question: <next check or open question>\n\
        Attached: <corpus filename>\n"
    );
    if short.chars().count() > 2000 {
        short = short.chars().take(1997).collect();
        short.push_str("...");
    }

    format!(
        "# <One-line claim or question>\n\n\
        ## WORKING ON lines (copy to WORKING ON thread)\n\
        ```text\n\
        Started: <task or question in one line>\n\
        Build: {build_line}\n\
        Model: <real AI model or \"by hand\">\n\
        ```\n\n\
        ## Short message (<= 2,000 chars, copy to category thread)\n\
        ```markdown\n\
        {short}\
        ```\n\n\
        ## Full report\n\n\
        ### Header fields\n\
        - Reply header: **<One-line claim>**\n\
        - Thread(s) replied in: <{build_line} Diagnostics and Tools / Performance, FPS and Frame Time>\n\
        - Build line: {build_line}\n\
        - Code tree and commit: <record public build revision>\n\
        - Contributor handle: <handle>\n\
        - AI model: <exact model name and version, or \"by hand\">\n\
        - Where and how it ran: local, victory-px\n\
        - Human involvement: <record actual review>\n\n\
        ### Scope\n\
        <One or two sentences stating the question asked.>\n\n\
        ### Sources read\n\
        - victory-px session metrics ({fork})\n\n\
        ### What was run or measured\n\
        - Hardware: {hw_line}\n\
        - Build / fork: {fork}\n\
        - Launch switches: {switches}\n\
        - Run length: {dur_str}\n\
        - Scene / number of runs: <scene, 1 run>\n\n\
        ### Findings\n\
        #### F-01 <One-line claim>\n\
        - Platform scope: {build_line}\n\
        - Evidence: PROBED {hw_line}; {evidence_line}\n\
        - Status: open\n\
        - By: <handle>, <AI-assisted or by hand>; {hw_line}\n\
        - Notes: <caveats, single run, etc.>\n\n\
        ### Not checked / unsure\n\
        - Single run / unverified against other scenes.\n\n\
        ### Conflicts or overlap with known findings\n\
        - none\n\n\
        ### How to reproduce or check\n\
        - Launch via `victory-px` with: `{switches}`\n\n\
        ### Proposed next step or experiment\n\
        - none\n\n\
        ### Open questions raised\n\
        - none\n\n\
        ### Compliance checklist\n\
        - [ ] Review any details you add before sharing.\n"
    )
}

pub fn write_report_file(prefix: &str, content: &str) -> Result<PathBuf, String> {
    write_report_with_ext(prefix, "md", content)
}

pub fn write_report_with_ext(prefix: &str, ext: &str, content: &str) -> Result<PathBuf, String> {
    let root = state_root().ok_or("cannot resolve state directory")?;
    let dir = root.join("reports");
    private_files::directory(&root).map_err(|e| e.to_string())?;
    private_files::directory(&dir).map_err(|e| e.to_string())?;
    let ts = Utc::now().format("%Y%m%d-%H%M%S");
    let (path, mut file) = private_files::unique_file(&dir, &format!("{prefix}-{ts}"), ext)
        .map_err(|e| e.to_string())?;
    use std::io::Write;
    if let Err(error) = file.write_all(content.as_bytes()) {
        let _ = fs::remove_file(&path);
        return Err(error.to_string());
    }
    Ok(path)
}

fn score_color(score: u32) -> &'static str {
    if score >= 90 {
        "#0cce6b"
    } else if score >= 50 {
        "#ffa400"
    } else {
        "#ff4e42"
    }
}

fn gauge_svg(label: &str, score: Option<u32>) -> String {
    let col = score.map(score_color).unwrap_or("#9ca3af");
    let r = 44.0_f64;
    let circ = 2.0 * std::f64::consts::PI * r;
    let dash = circ * (score.unwrap_or(0).min(100) as f64 / 100.0);
    let score = score.map_or_else(|| "—".into(), |v| v.to_string());
    format!(
        r#"<div class="lh-gauge">
  <svg viewBox="0 0 120 120" width="110" height="110">
    <circle cx="60" cy="60" r="44" fill="none" stroke="{col}" stroke-opacity="0.16" stroke-width="8"/>
    <circle cx="60" cy="60" r="44" fill="none" stroke="{col}" stroke-width="8" stroke-linecap="round"
      stroke-dasharray="{dash:.1} {circ:.1}" transform="rotate(-90 60 60)"/>
    <text x="60" y="66" text-anchor="middle" fill="{col}" font-size="26" font-weight="700">{score}</text>
  </svg>
  <div class="lh-gauge-label">{label}</div>
</div>"#
    )
}

pub struct LighthouseScores {
    pub perf: Option<u32>,
    pub stab: Option<u32>,
    pub headroom: Option<u32>,
}

pub fn compute_lighthouse_scores(
    stats: &RunStats,
    has_run: bool,
    series_s: bool,
    vram_total_mib: Option<u64>,
) -> LighthouseScores {
    let perf = if stats.fps.count > 0 {
        Some(((stats.fps.avg() / 60.0) * 100.0).clamp(0.0, 100.0) as u32)
    } else {
        stats
            .fps
            .last
            .map(|fps| ((fps / 60.0) * 100.0).clamp(0.0, 100.0) as u32)
    };

    let stab = if has_run || stats.warnings > 0 || stats.errors > 0 {
        let stab_penalty = (stats.errors * 25 + stats.warnings * 3) as u32;
        Some(100u32.saturating_sub(stab_penalty))
    } else {
        None
    };

    let req_vram = if series_s { 6144 } else { 10240 };
    let headroom = vram_total_mib.filter(|&v| v > 0).map(|v| {
        if v >= req_vram {
            98
        } else if v >= 6144 {
            72
        } else {
            40
        }
    });
    LighthouseScores {
        perf,
        stab,
        headroom,
    }
}

pub fn build_lighthouse_html(stats: &RunStats, meta: &CorpusMeta<'_>) -> String {
    let build_line = build_line(meta);
    let fork = public_fork(meta.fork_name);
    let numeric = |measurement: Option<f64>, digits| {
        measurement.map_or_else(|| "—".into(), |v| format!("{v:.digits$}"))
    };
    let fps = &stats.fps;
    let ft = &stats.frame_time;
    let avg_fps = numeric((fps.count > 0).then(|| fps.avg()).or(fps.last), 1);
    let min_fps = numeric((fps.count > 0).then_some(fps.min), 1);
    let max_fps = numeric((fps.count > 0).then_some(fps.max), 1);
    let avg_ft = numeric((ft.count > 0).then(|| ft.avg()).or(ft.last), 2);
    let dropped = numeric(stats.dropped, 1);
    let warnings = stats.warnings;
    let errors = stats.errors;
    let vendor = gpu_vendor(meta);
    let vram = memory(meta.host_metrics.vram_total_mib);
    let status = run_status(meta.state_label);
    let scores = compute_lighthouse_scores(
        stats,
        meta.duration.is_some(),
        meta.series_s,
        meta.host_metrics.vram_total_mib,
    );

    let gauges = format!(
        "{}\n{}\n{}",
        gauge_svg("Performance", scores.perf),
        gauge_svg("Stability", scores.stab),
        gauge_svg("Headroom", scores.headroom)
    );

    let console = if meta.series_s {
        "Xbox Series S"
    } else {
        "Xbox Series X"
    };
    let backend = if meta.gamescope {
        format!(
            "Gamescope ({}x{} → {}x{}, FSR={})",
            meta.display.game_w,
            meta.display.game_h,
            meta.display.win_w,
            meta.display.win_h,
            meta.display.fsr
        )
    } else {
        "Wine direct".into()
    };
    let dur = meta
        .duration
        .map(|d| format!("{}s", d.as_secs()))
        .unwrap_or_else(|| "not recorded".into());
    let ram = memory(Some(meta.host_metrics.ram_total_mib));

    format!(
        r#"<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>victory-px // Lighthouse Report</title>
<style>
  :root {{ --bg:#111827; --card:#1f2937; --border:#374151; --text:#f3f4f6; --muted:#9ca3af; }}
  * {{ box-sizing:border-box; margin:0; padding:0; }}
  body {{ background:var(--bg); color:var(--text); font-family:system-ui,-apple-system,sans-serif; padding:2rem 1rem; }}
  .wrap {{ max-width:920px; margin:0 auto; }}
  .topbar {{ display:flex; justify-content:space-between; align-items:center; border-bottom:1px solid var(--border); padding-bottom:1rem; margin-bottom:1.5rem; flex-wrap:wrap; gap:1rem; }}
  .brand {{ font-size:1.25rem; font-weight:700; letter-spacing:-0.02em; }}
  .brand span {{ color:#ff2a85; }}
  .meta {{ font-size:0.85rem; color:var(--muted); font-family:ui-monospace,monospace; }}
  .gauges {{ display:flex; justify-content:space-around; background:var(--card); border:1px solid var(--border); border-radius:12px; padding:1.5rem; margin-bottom:1.5rem; flex-wrap:wrap; gap:1rem; }}
  .lh-gauge {{ text-align:center; }}
  .lh-gauge-label {{ margin-top:0.4rem; font-weight:600; font-size:0.95rem; }}
  .section {{ background:var(--card); border:1px solid var(--border); border-radius:12px; padding:1.25rem 1.5rem; margin-bottom:1.25rem; }}
  .section h2 {{ font-size:0.95rem; text-transform:uppercase; letter-spacing:0.06em; color:var(--muted); margin-bottom:1rem; }}
  .metrics {{ display:grid; grid-template-columns:repeat(auto-fit,minmax(190px,1fr)); gap:1rem; }}
  .metric {{ background:#111827; border:1px solid var(--border); border-radius:8px; padding:0.9rem; }}
  .metric-k {{ font-size:0.78rem; color:var(--muted); text-transform:uppercase; }}
  .metric-v {{ font-size:1.35rem; font-weight:700; margin-top:0.25rem; font-family:ui-monospace,monospace; }}
  table {{ width:100%; border-collapse:collapse; font-size:0.9rem; }}
  td {{ padding:0.55rem 0; border-bottom:1px solid var(--border); }}
  td:first-child {{ color:var(--muted); width:210px; }}
  tr:last-child td {{ border-bottom:none; }}
</style>
</head>
<body>
<div class="wrap">
  <div class="topbar">
    <div class="brand"><span>VI</span>ctoryᵖˣ // Lighthouse Run Report</div>
    <div class="meta">{build_line}</div>
  </div>
  <div class="gauges">{gauges}</div>
  <div class="section">
    <h2>Performance Metrics</h2>
    <div class="metrics">
      <div class="metric"><div class="metric-k">Avg FPS</div><div class="metric-v">{avg_fps}</div></div>
      <div class="metric"><div class="metric-k">Min / Max FPS</div><div class="metric-v">{min_fps} / {max_fps}</div></div>
      <div class="metric"><div class="metric-k">Avg Frame Time</div><div class="metric-v">{avg_ft} ms</div></div>
      <div class="metric"><div class="metric-k">Dropped / Frame</div><div class="metric-v">{dropped}</div></div>
      <div class="metric"><div class="metric-k">Warnings</div><div class="metric-v">{warnings}</div></div>
      <div class="metric"><div class="metric-k">Errors</div><div class="metric-v">{errors}</div></div>
    </div>
  </div>
  <div class="section">
    <h2>Environment &amp; Audit Details</h2>
    <table>
      <tr><td>Fork / Build</td><td>{fork} ({build_line})</td></tr>
      <tr><td>Console Profile</td><td>{console}</td></tr>
      <tr><td>Display Backend</td><td>{backend}</td></tr>
      <tr><td>GPU / VRAM</td><td>{vendor} / {vram}</td></tr>
      <tr><td>Host RAM</td><td>{ram}</td></tr>
      <tr><td>Run Duration</td><td>{dur} (status: {status})</td></tr>
    </table>
  </div>
  <div style="text-align:center;margin-top:1.5rem;font-size:0.85rem;color:var(--muted);">
    Generated by <a href="https://github.com/glitchwd/victory-px/tree/lighthouse" target="_blank" rel="noopener noreferrer" style="color:#00f0ff;text-decoration:none;font-weight:600;">glitchwd/victory-px</a> (branch <code>lighthouse</code>)
  </div>
</div>
</body>
</html>"#
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reports_only_serialize_public_fields() {
        let metrics = HostMetrics {
            gpu_vendor: "unexpected vendor",
            ..Default::default()
        };
        let meta = CorpusMeta {
            fork_name: "custom & private label",
            host_metrics: &metrics,
            series_s: false,
            gamescope: false,
            display: &DisplayOptions::default(),
            duration: None,
            state_label: "unexpected status",
        };
        let stats = RunStats::default();
        for report in [
            build_corpus_bundle(&stats, &meta),
            build_lighthouse_html(&stats, &meta),
        ] {
            for private in [meta.fork_name, meta.state_label, metrics.gpu_vendor] {
                assert!(!report.contains(private));
            }
            assert!(report.contains("custom or unselected"));
        }
        assert!(compute_lighthouse_scores(&stats, false, false, None)
            .headroom
            .is_none());
    }
}
