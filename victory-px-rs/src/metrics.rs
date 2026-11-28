use regex::Regex;
use std::sync::OnceLock;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Severity {
    Info,
    Warning,
    Error,
}

pub struct LogLine {
    pub text: String,
    pub severity: Severity,
    pub is_metric: bool,
    pub path_hidden: bool,
    preview: Option<String>,
}

impl LogLine {
    pub fn preview(&self) -> Option<&str> {
        self.preview.as_deref()
    }
}

#[derive(Debug, Clone, Default)]
pub struct MetricAgg {
    pub min: f64,
    pub max: f64,
    pub sum: f64,
    pub count: usize,
    pub last: Option<f64>,
}

impl MetricAgg {
    pub fn record(&mut self, v: f64) {
        if self.count == 0 || v < self.min {
            self.min = v;
        }
        if self.count == 0 || v > self.max {
            self.max = v;
        }
        self.sum += v;
        self.count += 1;
        self.last = Some(v);
    }

    pub fn avg(&self) -> f64 {
        if self.count == 0 {
            0.0
        } else {
            self.sum / self.count as f64
        }
    }
}

#[derive(Debug, Clone, Default)]
pub struct RunStats {
    pub fps: MetricAgg,
    pub frame_time: MetricAgg,
    pub draws: Option<f64>,
    pub dropped: Option<f64>,
    pub private_ram: Option<f64>,
    pub vram_used: Option<f64>,
    pub vram_budget: Option<f64>,
    pub warnings: usize,
    pub errors: usize,
}

struct Patterns {
    fps: Regex,
    frame_time: Regex,
    draws: Regex,
    dropped: Regex,
    private_ram: Regex,
    vram: Regex,
    level: Regex,
    units: Regex,
    path_hint: Regex,
}

fn patterns() -> &'static Patterns {
    static PATS: OnceLock<Patterns> = OnceLock::new();
    PATS.get_or_init(|| Patterns {
        fps: Regex::new(r"(?i)\b([0-9]+(?:\.[0-9]+)?)\s+fps\b").unwrap(),
        frame_time: Regex::new(r"(?i)\b([0-9]+(?:\.[0-9]+)?)\s+ms/frame\b").unwrap(),
        draws: Regex::new(r"(?i)\b([0-9]+(?:\.[0-9]+)?)\s+draws/frame\b").unwrap(),
        dropped: Regex::new(
            r"(?i)\bdropped\s+([0-9]+(?:\.[0-9]+)?)(?:/frame\b|\s+without a host pipeline\b)",
        )
        .unwrap(),
        private_ram: Regex::new(r"(?i)\bprivate\s+([0-9]+)\s+MiB\b").unwrap(),
        vram: Regex::new(r"(?i)\bdriver:\s+video\s+([0-9]+)\s+of\s+([0-9]+)\s+MiB\b").unwrap(),
        level: Regex::new(r"(?i)^(?:\S+T\S+\s+)?(WARN(?:ING)?|ERROR)(?:[ :\[]|$)").unwrap(),
        units: Regex::new(r"(?i)\b(?:[0-9]+(?:\.[0-9]+)?[ \t]+(?:ms|draws)/frame|dropped[ \t]+[0-9]+(?:\.[0-9]+)?/frame)\b").unwrap(),
        path_hint: Regex::new(r#"(?i)(?:[/\\]|\b[a-z]:[^\s:]|\b(?:path|root|storage|filename|directory|cwd|log)["']?\s*[:=]|%(?:2f|5c|3a))"#).unwrap(),
    })
}

fn has_log_path(raw: &str) -> bool {
    let p = patterns();
    // Keep the entire message private when an unquoted path could contain spaces.
    p.path_hint.is_match(&p.units.replace_all(raw, " "))
}

fn has_control_codes(raw: &str) -> bool {
    raw.chars().any(|ch| ch.is_control() && ch != '\t')
}

impl RunStats {
    pub fn record_for_monitor(&mut self, raw: String, allow_preview: bool) -> LogLine {
        let mut line = self.record(&raw);
        if allow_preview && line.path_hidden && !has_control_codes(&raw) {
            line.preview = Some(raw);
        }
        line
    }

    pub fn record(&mut self, raw: &str) -> LogLine {
        let p = patterns();
        let mut is_metric = false;

        if let Some(v) = p
            .fps
            .captures(raw)
            .and_then(|c| c.get(1))
            .and_then(|m| m.as_str().parse::<f64>().ok())
        {
            self.fps.record(v);
            is_metric = true;
        }
        if let Some(v) = p
            .frame_time
            .captures(raw)
            .and_then(|c| c.get(1))
            .and_then(|m| m.as_str().parse::<f64>().ok())
        {
            self.frame_time.record(v);
            is_metric = true;
        }
        if let Some(v) = p
            .draws
            .captures(raw)
            .and_then(|c| c.get(1))
            .and_then(|m| m.as_str().parse::<f64>().ok())
        {
            self.draws = Some(v);
            is_metric = true;
        }
        if let Some(v) = p
            .dropped
            .captures(raw)
            .and_then(|c| c.get(1))
            .and_then(|m| m.as_str().parse::<f64>().ok())
        {
            self.dropped = Some(v);
            is_metric = true;
        }
        if let Some(v) = p
            .private_ram
            .captures(raw)
            .and_then(|c| c.get(1))
            .and_then(|m| m.as_str().parse::<f64>().ok())
        {
            self.private_ram = Some(v);
            is_metric = true;
        }
        if let Some(caps) = p.vram.captures(raw) {
            if let (Ok(used), Ok(total)) = (
                caps.get(1).unwrap().as_str().parse::<f64>(),
                caps.get(2).unwrap().as_str().parse::<f64>(),
            ) {
                self.vram_used = Some(used);
                self.vram_budget = Some(total);
                is_metric = true;
            }
        }

        let severity = if let Some(caps) = p.level.captures(raw) {
            if caps.get(1).unwrap().as_str().eq_ignore_ascii_case("error") {
                self.errors += 1;
                Severity::Error
            } else {
                self.warnings += 1;
                Severity::Warning
            }
        } else if (raw.starts_with("thread '") && raw.contains("panicked at"))
            || raw.contains(":err:module:")
            || raw.contains("BacktraceBreakpad")
            || raw.contains("MiniDumpWriteDump")
            || raw.contains("unhandled exception")
        {
            self.errors += 1;
            Severity::Error
        } else {
            Severity::Info
        };

        let path_hidden = has_log_path(raw);
        let text = if path_hidden {
            "path hidden".into()
        } else if has_control_codes(raw) {
            "…".into()
        } else {
            raw.into()
        };
        LogLine {
            text,
            path_hidden,
            preview: None,
            severity,
            is_metric,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn logs_hide_paths_without_losing_numeric_metrics() {
        let mut stats = RunStats::default();
        for line in [
            r"INFO Log: C:\users\example\AppData\Local\Demo\play.log",
            r"INFO drive=G root=Z:\home\example\Folder with spaces hides_host_drive=false",
            r"INFO drive=T root=\\server\share\temp",
            r"INFO mapped /mnt/media/Folder with spaces/app.exe",
            r"INFO resolved C:relative.exe",
            r"INFO found ../data/title.exe",
            r"INFO file:%2Ftmp%2Ftitle.exe",
            r"INFO path=title.exe",
            r#"INFO {"path":"title.exe"}"#,
            r"INFO %2Fexample",
        ] {
            assert_eq!(stats.record(line).text, "path hidden");
        }
        let control = "INFO \x1b[31m /tmp/example";
        assert!(stats
            .record_for_monitor(control.into(), true)
            .preview()
            .is_none());
        let sample = "INFO /tmp/example";
        assert!(stats.record(sample).preview().is_none());
        assert_eq!(
            stats.record_for_monitor(sample.into(), true).preview(),
            Some(sample)
        );
        let telemetry = "INFO 42 fps 23.81 ms/frame 4 draws/frame";
        assert_eq!(stats.record(telemetry).text, telemetry);
        let mixed = stats.record(r"ERROR 60 fps path=C:\demo\game.exe");
        assert!(mixed.is_metric);
        assert_eq!(mixed.severity, Severity::Error);
        assert_eq!(stats.fps.last, Some(60.0));
        assert!(!mixed.text.contains("game.exe"));
    }
}
