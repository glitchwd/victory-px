use crate::corpus::compute_lighthouse_scores;
use crate::forks::{ForkBuild, ForkTier};
use crate::guard::{evaluate_guard, GuardFinding};
use crate::metrics::{LogLine, RunStats, Severity};
use crate::runner::DisplayOptions;
use crate::system::{HostInfo, HostMetrics};
use ratatui::layout::{Constraint, Direction, Layout, Rect};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Block, BorderType, Borders, Clear, Paragraph, Sparkline, Wrap};
use ratatui::Frame;
use std::collections::VecDeque;
use std::fs;
use std::path::{Path, PathBuf};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ActiveTab {
    Monitor,
    Display,
    Lighthouse,
    Forks,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ModalKind {
    None,
    Gamescope,
    GameBrowser,
    ForkWizardDir,
    ForkWizardBin,
    ForkWizardName,
    ThemeFX,
    ErrorAdvisory,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ThemePalette {
    ViceCity,
    CyberNeon,
    Bloodline,
    Stealth,
}

impl ThemePalette {
    pub fn next(self) -> Self {
        match self {
            Self::ViceCity => Self::CyberNeon,
            Self::CyberNeon => Self::Bloodline,
            Self::Bloodline => Self::Stealth,
            Self::Stealth => Self::ViceCity,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Self::ViceCity => "Vice City (GTA VI)",
            Self::CyberNeon => "Cyber Neon",
            Self::Bloodline => "Bloodline (Crimson)",
            Self::Stealth => "Stealth (Mono)",
        }
    }

    pub fn colors(self) -> [Color; 5] {
        match self {
            Self::ViceCity => [
                Color::Rgb(255, 42, 133),
                Color::Rgb(255, 94, 126),
                Color::Rgb(255, 159, 67),
                Color::Rgb(199, 125, 255),
                Color::Rgb(123, 44, 191),
            ],
            Self::CyberNeon => [
                Color::Rgb(0, 255, 159),
                Color::Rgb(0, 240, 255),
                Color::Rgb(56, 189, 248),
                Color::Rgb(99, 102, 241),
                Color::Rgb(168, 85, 247),
            ],
            Self::Bloodline => [
                Color::Rgb(255, 51, 102),
                Color::Rgb(239, 68, 68),
                Color::Rgb(249, 115, 22),
                Color::Rgb(251, 191, 36),
                Color::Rgb(185, 28, 28),
            ],
            Self::Stealth => [
                Color::Rgb(248, 250, 252),
                Color::Rgb(203, 213, 225),
                Color::Rgb(148, 163, 184),
                Color::Rgb(100, 116, 139),
                Color::Rgb(71, 85, 105),
            ],
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AnimMode {
    Wave,
    Pulse,
    Static,
}

impl AnimMode {
    pub fn next(self) -> Self {
        match self {
            Self::Wave => Self::Pulse,
            Self::Pulse => Self::Static,
            Self::Static => Self::Wave,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Self::Wave => "Wave (Scrolling)",
            Self::Pulse => "Pulse (Breathing)",
            Self::Static => "Static",
        }
    }
}

#[derive(Debug, Clone)]
pub struct BrowserEntry {
    pub name: String,
    pub path: PathBuf,
    pub is_dir: bool,
}

#[derive(Debug, Clone, Default)]
pub struct GameBrowserState {
    pub cwd: PathBuf,
    pub entries: Vec<BrowserEntry>,
    pub cursor: usize,
}

impl GameBrowserState {
    pub fn open(&mut self, current_exe: &str) {
        let start = if !current_exe.is_empty() {
            Path::new(current_exe)
                .parent()
                .map(Path::to_path_buf)
                .filter(|p| p.is_dir())
        } else {
            None
        }
        .or_else(|| std::env::var("HOME").ok().map(PathBuf::from))
        .unwrap_or_else(|| PathBuf::from("/"));
        self.load_dir(start);
    }

    pub fn load_dir(&mut self, dir: PathBuf) {
        self.cwd = dir;
        self.entries.clear();
        self.cursor = 0;

        let mut dirs = Vec::new();
        let mut exes = Vec::new();
        if let Ok(rd) = fs::read_dir(&self.cwd) {
            for e in rd.flatten() {
                let p = e.path();
                let name = e.file_name().to_string_lossy().into_owned();
                if name.starts_with('.') {
                    continue;
                }
                if p.is_dir() {
                    dirs.push(BrowserEntry {
                        name,
                        path: p,
                        is_dir: true,
                    });
                } else if p
                    .extension()
                    .and_then(|s| s.to_str())
                    .is_some_and(|ext| ext.eq_ignore_ascii_case("exe"))
                {
                    exes.push(BrowserEntry {
                        name,
                        path: p,
                        is_dir: false,
                    });
                }
            }
        }
        dirs.sort_by_cached_key(|item| item.name.to_lowercase());
        exes.sort_by_cached_key(|item| item.name.to_lowercase());
        self.entries.extend(dirs);
        self.entries.extend(exes);
    }

    pub fn scan_victory_exes(&mut self, root: &Path) -> usize {
        self.cwd = root.to_path_buf();
        self.entries.clear();
        self.cursor = 0;
        let mut found = Vec::new();
        collect_exes(root, root, 0, true, &mut found);
        found.sort_by_key(|e| {
            let lower = e.name.to_lowercase();
            let prio = if lower == "victory.exe" || lower.ends_with("/victory.exe") {
                0
            } else if !lower.contains("win10") {
                1
            } else {
                2
            };
            (prio, lower)
        });
        let count = found.len();
        self.entries = found;
        count
    }
}

fn collect_exes(
    root: &Path,
    dir: &Path,
    depth: usize,
    victory_only: bool,
    out: &mut Vec<BrowserEntry>,
) {
    if depth > 5 {
        return;
    }
    let Ok(rd) = fs::read_dir(dir) else {
        return;
    };
    for e in rd.flatten() {
        let p = e.path();
        let name = e.file_name().to_string_lossy().into_owned();
        if name.starts_with('.') || name == "deps" || name == "incremental" || name == "build" {
            continue;
        }
        if e.file_type().is_ok_and(|kind| kind.is_dir()) {
            collect_exes(root, &p, depth + 1, victory_only, out);
        } else if p
            .extension()
            .and_then(|s| s.to_str())
            .is_some_and(|ext| ext.eq_ignore_ascii_case("exe"))
        {
            if victory_only && !name.to_lowercase().contains("victory") {
                continue;
            }
            let rel = p
                .strip_prefix(root)
                .map(|r| r.to_string_lossy().into_owned())
                .unwrap_or(name);
            out.push(BrowserEntry {
                name: rel,
                path: p,
                is_dir: false,
            });
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum LogFilter {
    All,
    WarnErr,
    Metrics,
}

impl LogFilter {
    pub fn next(self) -> Self {
        match self {
            Self::All => Self::WarnErr,
            Self::WarnErr => Self::Metrics,
            Self::Metrics => Self::All,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Self::All => "ALL",
            Self::WarnErr => "WARN/ERR",
            Self::Metrics => "METRICS",
        }
    }
}

pub struct AppState {
    pub tab: ActiveTab,
    pub modal: ModalKind,
    pub palette: ThemePalette,
    pub anim_mode: AnimMode,
    pub anim_speed: u8,
    pub anim_tick: u64,
    pub show_error_popups: bool,
    pub advisory_shown_this_run: bool,
    pub active_advisory: Option<GuardFinding>,
    pub browser: GameBrowserState,
    pub wizard_dir: String,
    pub wizard_bin: String,
    pub wizard_name: String,
    pub forks: Vec<ForkBuild>,
    pub active_fork_idx: Option<usize>,
    pub inventory_cursor: usize,
    pub game_exe: String,
    pub wine_bin: String,
    pub runtime_label: String,
    pub series_s: bool,
    pub gamescope: bool,
    pub display: DisplayOptions,
    pub running: bool,
    pub status_text: String,
    pub notice: String,
    pub host_info: HostInfo,
    pub host_metrics: HostMetrics,
    pub stats: RunStats,
    pub elapsed_secs: u64,
    pub fps_history: VecDeque<u64>,
    pub gpu_history: VecDeque<u64>,
    pub logs: Vec<LogLine>,
    pub log_filter: LogFilter,
    pub log_scroll: usize,
}

impl AppState {
    pub fn active_fork(&self) -> Option<&ForkBuild> {
        self.active_fork_idx.and_then(|i| self.forks.get(i))
    }

    pub fn push_fps(&mut self, v: f64) {
        if self.fps_history.len() >= 64 {
            self.fps_history.pop_front();
        }
        self.fps_history.push_back(v.max(0.0).round() as u64);
    }

    pub fn push_gpu(&mut self, v: u32) {
        if self.gpu_history.len() >= 64 {
            self.gpu_history.pop_front();
        }
        self.gpu_history.push_back(v as u64);
    }
}

// High-contrast h4x0r / cyberpunk palette
const BG_PANEL: Color = Color::Rgb(10, 14, 23);
const BG_BAR: Color = Color::Rgb(17, 24, 39);
const BORDER_DIM: Color = Color::Rgb(51, 65, 85);
const TEXT_MAIN: Color = Color::Rgb(248, 250, 252);
const TEXT_MUTED: Color = Color::Rgb(148, 163, 184);
const NEON_GREEN: Color = Color::Rgb(0, 255, 159);
const CYAN: Color = Color::Rgb(0, 240, 255);
const BLUE: Color = Color::Rgb(56, 189, 248);
const PURPLE: Color = Color::Rgb(168, 85, 247);
const MAGENTA: Color = Color::Rgb(255, 42, 133);
const AMBER: Color = Color::Rgb(255, 184, 0);
const RED: Color = Color::Rgb(255, 51, 102);

fn tier_color(tier: ForkTier) -> Color {
    match tier {
        ForkTier::Covert => AMBER,
        ForkTier::Classified => MAGENTA,
        ForkTier::Restricted => PURPLE,
        ForkTier::MilSpec => BLUE,
    }
}

fn tier_name(tier: ForkTier) -> &'static str {
    match tier {
        ForkTier::Covert => "COVERT",
        ForkTier::Classified => "CLASSIFIED",
        ForkTier::Restricted => "RESTRICTED",
        ForkTier::MilSpec => "MIL-SPEC",
    }
}

fn gauge_bar(pct: u64, width: usize) -> String {
    let filled = ((pct.min(100) * width as u64) / 100) as usize;
    format!(
        "[{}{}]",
        "█".repeat(filled),
        "·".repeat(width.saturating_sub(filled))
    )
}

fn redact_path(raw: &str) -> String {
    if raw.is_empty() {
        return "none".into();
    }
    if std::env::var_os("HOME").is_some_and(|home| Path::new(raw).starts_with(home)) {
        "~/…".into()
    } else {
        "[path hidden]".into()
    }
}

pub fn draw(f: &mut Frame, app: &AppState, can_preview: bool, show_paths: bool) {
    let area = f.area();
    if compact(area) {
        f.render_widget(
            Paragraph::new("Resize to at least 48 × 24. Ctrl+C quits.").wrap(Wrap { trim: true }),
            area,
        );
        return;
    }
    let [banner, sidebar, stage, footer] = screen_layout(area);
    draw_top_banner(f, banner, app);

    if sidebar.width > 0 {
        draw_sidebar(f, sidebar, app);
    }

    let [fork_bar, stage_main] = Layout::vertical([
        Constraint::Length(3),
        Constraint::Min(6),
    ])
    .areas(stage);

    draw_active_fork_banner(f, fork_bar, app);

    match app.tab {
        ActiveTab::Monitor => draw_monitor(f, stage_main, app, can_preview, show_paths),
        ActiveTab::Display => draw_display(f, stage_main, app),
        ActiveTab::Lighthouse => draw_lighthouse(f, stage_main, app),
        ActiveTab::Forks => draw_forks_inventory(f, stage_main, app),
    }

    draw_bottom_bar(f, footer, app);

    match app.modal {
        ModalKind::None => {}
        ModalKind::Gamescope => draw_gamescope_modal(f, area, app),
        ModalKind::GameBrowser | ModalKind::ForkWizardDir | ModalKind::ForkWizardBin => {
            draw_browser_modal(f, area, app)
        }
        ModalKind::ForkWizardName => draw_fork_name_modal(f, area, app),
        ModalKind::ThemeFX => draw_theme_modal(f, area, app),
        ModalKind::ErrorAdvisory => draw_error_advisory_modal(f, area, app),
    }
}

pub fn compact(area: Rect) -> bool {
    area.width < 48 || area.height < 24
}

fn screen_layout(area: Rect) -> [Rect; 4] {
    let [banner, body, footer] = Layout::vertical([
        Constraint::Length(7),
        Constraint::Min(10),
        Constraint::Length(1),
    ])
    .areas(area);
    let sidebar_width = if area.width >= 92 { 30 } else { 0 };
    let [sidebar, stage] =
        Layout::horizontal([Constraint::Length(sidebar_width), Constraint::Fill(1)]).areas(body);
    [banner, sidebar, stage, footer]
}

fn inventory_layout(area: Rect) -> [Rect; 3] {
    Layout::vertical([
        Constraint::Min(6),
        Constraint::Length(4),
        Constraint::Length(5),
    ])
    .areas(area)
}

fn inventory_cols(stage: Rect) -> usize {
    (stage.width / 22).clamp(1, 4) as usize
}

pub fn fork_columns(area: Rect) -> usize {
    inventory_cols(screen_layout(area)[2])
}

fn inventory_cards(grid: Rect, cols: usize) -> Vec<Rect> {
    let rows = usize::from((grid.height / 5).max(1));
    Layout::vertical(vec![Constraint::Length(5); rows])
        .split(grid)
        .iter()
        .flat_map(|row| {
            Layout::horizontal(vec![Constraint::Ratio(1, cols as u32); cols])
                .split(*row)
                .to_vec()
        })
        .collect()
}

fn essential_cards(area: Rect) -> [Rect; 3] {
    Layout::horizontal([Constraint::Ratio(1, 3); 3]).areas(area)
}

#[derive(Clone, Copy)]
pub enum ForkClick {
    Card(usize),
    Game,
    Display,
    Console,
}

pub fn fork_hit(area: Rect, app: &AppState, x: u16, y: u16) -> Option<ForkClick> {
    let point = ratatui::layout::Position::new(x, y);
    if compact(area)
        || !area.contains(point)
        || app.tab != ActiveTab::Forks
        || app.modal != ModalKind::None
    {
        return None;
    }
    let stage = screen_layout(area)[2];
    let [grid, _, essential] = inventory_layout(stage);
    let cards = inventory_cards(grid, inventory_cols(stage));
    let start = app.inventory_cursor / cards.len() * cards.len();
    if let Some(offset) = cards.iter().position(|rect| rect.contains(point)) {
        let index = start + offset;
        return (index <= app.forks.len()).then_some(ForkClick::Card(index));
    }
    essential_cards(essential)
        .iter()
        .position(|rect| rect.contains(point))
        .map(|i| [ForkClick::Game, ForkClick::Display, ForkClick::Console][i])
}

fn hardware_headroom(app: &AppState) -> (&'static str, Color, String) {
    let vram_mib = app.host_metrics.vram_total_mib.unwrap_or(0);
    let ram_gib = app.host_metrics.ram_total_mib as f64 / 1024.0;
    let req_vram = if app.series_s { 6144 } else { 10240 };
    let req_ram = if app.series_s { 10.0 } else { 16.0 };

    let vram_ok = vram_mib >= req_vram;
    let ram_ok = ram_gib >= req_ram;

    let (badge, col) = match (vram_ok, ram_ok) {
        _ if vram_mib == 0 || ram_gib == 0.0 => ("FIT: UNKNOWN", TEXT_MUTED),
        (true, true) => ("FIT: OK", NEON_GREEN),
        (false, true) if vram_mib >= 6144 => ("FIT: TIGHT VRAM", AMBER),
        _ => ("FIT: LOW SPEC", RED),
    };
    let detail = format!(
        "VRAM {}G · RAM {:.0}G",
        if vram_mib > 0 {
            format!("{:.0}", vram_mib as f64 / 1024.0)
        } else {
            "?".into()
        },
        ram_gib
    );
    (badge, col, detail)
}

fn draw_top_banner(f: &mut Frame, area: Rect, app: &AppState) {
    let cols = Layout::default()
        .direction(Direction::Horizontal)
        .constraints([Constraint::Min(64), Constraint::Length(46)])
        .split(area);

    // Left: Animated "VI" logo using active ThemePalette & AnimMode, muted dark grayscale "CTORY"
    let base = app.palette.colors();
    let step = (app.anim_tick / (4 - app.anim_speed.min(2) as u64)) as usize;
    let row_color = |row: usize| -> Color {
        match app.anim_mode {
            AnimMode::Static => base[row],
            AnimMode::Wave => base[(row + step) % 5],
            AnimMode::Pulse => {
                let phase = (step % 8) as f32;
                let factor = 0.65 + 0.35 * (1.0 - ((phase - 4.0).abs() / 4.0));
                if let Color::Rgb(r, g, b) = base[row] {
                    Color::Rgb(
                        (r as f32 * factor) as u8,
                        (g as f32 * factor) as u8,
                        (b as f32 * factor) as u8,
                    )
                } else {
                    base[row]
                }
            }
        }
    };
    let vi1 = Style::default()
        .fg(row_color(0))
        .add_modifier(Modifier::BOLD);
    let vi2 = Style::default()
        .fg(row_color(1))
        .add_modifier(Modifier::BOLD);
    let vi3 = Style::default()
        .fg(row_color(2))
        .add_modifier(Modifier::BOLD);
    let vi4 = Style::default()
        .fg(row_color(3))
        .add_modifier(Modifier::BOLD);
    let vi5 = Style::default()
        .fg(row_color(4))
        .add_modifier(Modifier::BOLD);

    let g1 = Style::default().fg(Color::Rgb(84, 92, 108));
    let g2 = Style::default().fg(Color::Rgb(70, 78, 92));
    let g3 = Style::default().fg(Color::Rgb(58, 65, 78));
    let g4 = Style::default().fg(Color::Rgb(46, 52, 64));
    let g5 = Style::default().fg(Color::Rgb(36, 41, 52));

    let logo_lines = vec![
        Line::from(vec![
            Span::styled("██▄   ▄██ ██", vi1),
            Span::raw("   "),
            Span::styled("▄████ ████████ ▄████▄  ██████▄ ▀██   ██▀", g1),
            Span::styled(" ᵖˣ", Style::default().fg(CYAN)),
        ]),
        Line::from(vec![
            Span::styled(" ███▄███  ██", vi2),
            Span::raw("  "),
            Span::styled("███      ███   ███  ███ ███  ███  ███ ███ ", g2),
        ]),
        Line::from(vec![
            Span::styled("  █████   ██", vi3),
            Span::raw("  "),
            Span::styled("███      ███   ███  ███ ██████▀    █████  ", g3),
        ]),
        Line::from(vec![
            Span::styled("   ███    ██", vi4),
            Span::raw("  "),
            Span::styled("███▄     ███   ███  ███ ███  ██▄    ███   ", g4),
        ]),
        Line::from(vec![
            Span::styled("    ▀     ██", vi5),
            Span::raw("   "),
            Span::styled("▀████   ███    ▀████▀  ███  ███    ███   ", g5),
        ]),
        Line::from(vec![
            Span::styled(
                " victory-px",
                Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD),
            ),
            Span::styled(
                "@lighthouse",
                Style::default().fg(NEON_GREEN).add_modifier(Modifier::BOLD),
            ),
            Span::styled("    //    ", Style::default().fg(BORDER_DIM)),
            Span::styled("ARCH: x86_64", Style::default().fg(CYAN)),
            Span::styled("    //    ", Style::default().fg(BORDER_DIM)),
            Span::styled("PROTON EXECUTOR", Style::default().fg(TEXT_MUTED)),
            Span::styled("    //    ", Style::default().fg(BORDER_DIM)),
            Span::styled(
                concat!("v", env!("CARGO_PKG_VERSION")),
                Style::default().fg(AMBER).add_modifier(Modifier::BOLD),
            ),
        ]),
    ];
    f.render_widget(Paragraph::new(logo_lines), cols[0]);

    // Right: High-contrast Session Status Card
    let (state_col, state_dot) = if app.running {
        (NEON_GREEN, "● RUNNING")
    } else {
        (AMBER, "◆ STANDBY")
    };
    let gpu_short = if app.host_info.gpu.is_empty() {
        app.host_info.os_name.as_str()
    } else {
        app.host_info.gpu.as_str()
    };
    let status_block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Double)
        .border_style(Style::default().fg(if app.running { NEON_GREEN } else { CYAN }))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            " SYSTEM ",
            Style::default()
                .bg(CYAN)
                .fg(Color::Black)
                .add_modifier(Modifier::BOLD),
        ));
    let (fit_badge, fit_col, fit_detail) = hardware_headroom(app);
    let status_lines = vec![
        Line::from(vec![
            Span::styled(
                format!(" {state_dot}"),
                Style::default().fg(state_col).add_modifier(Modifier::BOLD),
            ),
            Span::styled(" │ ", Style::default().fg(BORDER_DIM)),
            Span::styled("[C] ", Style::default().fg(TEXT_MUTED)),
            Span::styled(
                if app.series_s { "SERIES S" } else { "SERIES X" },
                Style::default()
                    .bg(BLUE)
                    .fg(Color::Black)
                    .add_modifier(Modifier::BOLD),
            ),
        ]),
        Line::from(Span::styled(
            format!(" {gpu_short}"),
            Style::default().fg(TEXT_MAIN),
        )),
        Line::from(vec![
            Span::styled(
                format!(" {fit_badge}"),
                Style::default().fg(fit_col).add_modifier(Modifier::BOLD),
            ),
            Span::styled(format!("  ({fit_detail})"), Style::default().fg(TEXT_MUTED)),
        ]),
        Line::from(Span::styled(
            format!(
                " KERNEL {}",
                if app.host_info.kernel.is_empty() {
                    "-"
                } else {
                    &app.host_info.kernel
                }
            ),
            Style::default().fg(TEXT_MUTED),
        )),
    ];
    f.render_widget(Paragraph::new(status_lines).block(status_block), cols[1]);
}

fn draw_active_fork_banner(f: &mut Frame, area: Rect, app: &AppState) {
    let (col, tier_lbl, fork_lbl, glyph) = if let Some(b) = app.active_fork() {
        (tier_color(b.tier()), tier_name(b.tier()), b.label().to_string(), b.glyph())
    } else {
        (TEXT_MUTED, "NONE", "No fork selected — press [B] to equip".to_string(), "")
    };
    let block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Rounded)
        .border_style(Style::default().fg(col))
        .style(Style::default().bg(BG_PANEL));
    let line = Line::from(vec![
        Span::styled(" ★ ACTIVE FORK ", Style::default().bg(col).fg(Color::Black).add_modifier(Modifier::BOLD)),
        Span::styled(format!("  {fork_lbl}  "), Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD)),
        Span::styled(format!("[{tier_lbl}] "), Style::default().fg(col).add_modifier(Modifier::BOLD)),
        Span::styled(format!("{glyph}  "), Style::default().fg(TEXT_MUTED)),
        Span::styled("│  [B] Switch Loadout", Style::default().fg(TEXT_MUTED)),
    ]);
    f.render_widget(Paragraph::new(line).block(block), area);
}

fn draw_sidebar(f: &mut Frame, area: Rect, app: &AppState) {
    let block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Rounded)
        .border_style(Style::default().fg(BLUE))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            " WORKSPACE ",
            Style::default()
                .bg(BLUE)
                .fg(Color::Black)
                .add_modifier(Modifier::BOLD),
        ));
    let inner = block.inner(area);
    f.render_widget(block, area);

    let nav_item = |key: &str, label: &str, active: bool, accent: Color| -> Line<'static> {
        if active {
            Line::from(vec![
                Span::styled(
                    "▌ ",
                    Style::default().fg(accent).add_modifier(Modifier::BOLD),
                ),
                Span::styled(
                    format!("[{key}] "),
                    Style::default().fg(accent).add_modifier(Modifier::BOLD),
                ),
                Span::styled(
                    label.to_string(),
                    Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD),
                ),
            ])
        } else {
            Line::from(vec![
                Span::raw("  "),
                Span::styled(format!("[{key}] "), Style::default().fg(TEXT_MUTED)),
                Span::styled(label.to_string(), Style::default().fg(TEXT_MUTED)),
            ])
        }
    };

    let fork_name = app
        .active_fork()
        .map(|b| format!("★ {}", b.label()))
        .unwrap_or_else(|| "None (press B)".into());
    let fork_col = app
        .active_fork()
        .map(|b| tier_color(b.tier()))
        .unwrap_or(TEXT_MUTED);
    let game_name = if app.game_exe.is_empty() {
        "not selected".into()
    } else {
        Path::new(&app.game_exe)
            .file_name()
            .and_then(|s| s.to_str())
            .unwrap_or(&app.game_exe)
            .to_string()
    };

    let hm = &app.host_metrics;
    let gpu_pct = hm.gpu_util.unwrap_or(0) as u64;
    let ram_pct = hm
        .ram_used_mib
        .saturating_mul(100)
        .checked_div(hm.ram_total_mib)
        .unwrap_or(0);

    let lines = vec![
        Line::from(Span::styled(
            "NAVIGATION",
            Style::default().fg(TEXT_MUTED).add_modifier(Modifier::BOLD),
        )),
        nav_item("1", "Monitor", app.tab == ActiveTab::Monitor, BLUE),
        nav_item("2", "Display", app.tab == ActiveTab::Display, PURPLE),
        nav_item(
            "3",
            "Lighthouse",
            app.tab == ActiveTab::Lighthouse,
            NEON_GREEN,
        ),
        nav_item("B", "Forks Loadout", app.tab == ActiveTab::Forks, AMBER),
        Line::from(""),
        Line::from(Span::styled(
            "ACTIVE LOADOUT",
            Style::default().fg(TEXT_MUTED).add_modifier(Modifier::BOLD),
        )),
        Line::from(Span::styled(
            fork_name,
            Style::default().fg(fork_col).add_modifier(Modifier::BOLD),
        )),
        Line::from(vec![
            Span::styled("Target  ", Style::default().fg(TEXT_MUTED)),
            Span::styled(game_name, Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(vec![
            Span::styled("Mode    ", Style::default().fg(TEXT_MUTED)),
            Span::styled(
                if app.gamescope {
                    "Gamescope"
                } else {
                    "Wine direct"
                },
                Style::default().fg(CYAN),
            ),
        ]),
        Line::from(""),
        Line::from(Span::styled(
            "HOST VITALS",
            Style::default().fg(TEXT_MUTED).add_modifier(Modifier::BOLD),
        )),
        Line::from(vec![
            Span::styled("GPU ", Style::default().fg(TEXT_MUTED)),
            Span::styled(gauge_bar(gpu_pct, 12), Style::default().fg(PURPLE)),
            Span::styled(format!(" {:>3}%", gpu_pct), Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(vec![
            Span::styled("RAM ", Style::default().fg(TEXT_MUTED)),
            Span::styled(gauge_bar(ram_pct, 12), Style::default().fg(BLUE)),
            Span::styled(format!(" {:>3}%", ram_pct), Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(Span::styled(
            format!(
                "Temp {}°C · VRAM {}/{}M",
                hm.gpu_temp
                    .map(|v| v.to_string())
                    .unwrap_or_else(|| "-".into()),
                hm.vram_used_mib
                    .map(|v| v.to_string())
                    .unwrap_or_else(|| "-".into()),
                hm.vram_total_mib
                    .map(|v| v.to_string())
                    .unwrap_or_else(|| "-".into()),
            ),
            Style::default().fg(TEXT_MUTED),
        )),
        Line::from(""),
        Line::from(Span::styled(
            "ACTIONS",
            Style::default().fg(TEXT_MUTED).add_modifier(Modifier::BOLD),
        )),
        Line::from(vec![
            Span::styled(
                "[S] ",
                Style::default().fg(CYAN).add_modifier(Modifier::BOLD),
            ),
            Span::styled(
                if app.running {
                    "Stop session"
                } else {
                    "Launch session"
                },
                Style::default().fg(TEXT_MAIN),
            ),
        ]),
        Line::from(vec![
            Span::styled(
                "[G] ",
                Style::default().fg(PURPLE).add_modifier(Modifier::BOLD),
            ),
            Span::styled("Gamescope Modal", Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(vec![
            Span::styled(
                "[O] ",
                Style::default().fg(BLUE).add_modifier(Modifier::BOLD),
            ),
            Span::styled("Game Browser", Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(vec![
            Span::styled(
                "[W] ",
                Style::default().fg(AMBER).add_modifier(Modifier::BOLD),
            ),
            Span::styled("Export Report MD", Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(vec![
            Span::styled(
                "[R] ",
                Style::default().fg(NEON_GREEN).add_modifier(Modifier::BOLD),
            ),
            Span::styled("Lighthouse HTML", Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(vec![
            Span::styled(
                "[T] ",
                Style::default().fg(MAGENTA).add_modifier(Modifier::BOLD),
            ),
            Span::styled("Theme & FX", Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(vec![
            Span::styled("[C] ", Style::default().fg(BLUE)),
            Span::styled("Toggle Series X/S", Style::default().fg(TEXT_MUTED)),
        ]),
        Line::from(vec![
            Span::styled("[Q] ", Style::default().fg(RED)),
            Span::styled("Quit", Style::default().fg(TEXT_MUTED)),
        ]),
    ];

    f.render_widget(Paragraph::new(lines), inner);
}

fn draw_monitor(f: &mut Frame, area: Rect, app: &AppState, can_preview: bool, show_paths: bool) {
    let rows = Layout::default()
        .direction(Direction::Vertical)
        .constraints([Constraint::Length(9), Constraint::Min(5)])
        .split(area);

    let cards = Layout::default()
        .direction(Direction::Horizontal)
        .constraints([Constraint::Percentage(55), Constraint::Percentage(45)])
        .split(rows[0]);

    // FPS Card
    let fps_block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Rounded)
        .border_style(Style::default().fg(BLUE))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            " PERFORMANCE // FPS & FRAMETIME ",
            Style::default().fg(BLUE).add_modifier(Modifier::BOLD),
        ));
    let fps_in = fps_block.inner(cards[0]);
    f.render_widget(fps_block, cards[0]);

    let fps_split = Layout::default()
        .direction(Direction::Vertical)
        .constraints([Constraint::Length(3), Constraint::Length(1), Constraint::Min(3)])
        .split(fps_in);
    if app.fps_history.is_empty() {
        let w = fps_split[0].width.saturating_sub(2) as usize;
        let baseline = "▁".repeat(w);
        f.render_widget(
            Paragraph::new(vec![
                Line::from(Span::styled(
                    "  TIMELINE // STANDBY (press S to stream FPS)",
                    Style::default().fg(TEXT_MUTED),
                )),
                Line::from(Span::styled(
                    format!(" {baseline}"),
                    Style::default().fg(BORDER_DIM),
                )),
                Line::from(Span::styled(
                    " 0s                  -30s                 -60s",
                    Style::default().fg(BORDER_DIM),
                )),
            ]),
            fps_split[0],
        );
    } else {
        let fps_data: Vec<u64> = app.fps_history.iter().copied().collect();
        f.render_widget(
            Sparkline::default()
                .data(&fps_data)
                .style(Style::default().fg(CYAN)),
            fps_split[0],
        );
    }
    let fps_last = app
        .stats
        .fps
        .last
        .map(|v| format!("{v:.1}"))
        .unwrap_or_else(|| "-".into());
    let fps_avg = if app.stats.fps.count > 0 {
        format!("{:.1}", app.stats.fps.avg())
    } else {
        "-".into()
    };
    let ft_last = app
        .stats
        .frame_time
        .last
        .map(|v| format!("{v:.2}"))
        .unwrap_or_else(|| "-".into());
    let mm = app.elapsed_secs / 60;
    let ss = app.elapsed_secs % 60;
    let stats_lines = vec![
        Line::from(vec![
            Span::styled("FPS ", Style::default().fg(TEXT_MUTED)),
            Span::styled(
                fps_last,
                Style::default().fg(CYAN).add_modifier(Modifier::BOLD),
            ),
            Span::styled(
                format!(" (avg {fps_avg})  "),
                Style::default().fg(TEXT_MUTED),
            ),
            Span::styled("ms/f ", Style::default().fg(TEXT_MUTED)),
            Span::styled(ft_last, Style::default().fg(TEXT_MAIN)),
            Span::styled(format!("  [{mm:02}:{ss:02}]"), Style::default().fg(BLUE)),
        ]),
        Line::from(vec![
            Span::styled(
                format!("WARN {}  ", app.stats.warnings),
                Style::default().fg(AMBER),
            ),
            Span::styled(
                format!("ERR {}  ", app.stats.errors),
                Style::default().fg(RED),
            ),
            Span::styled(
                format!(
                    "Drops {}",
                    app.stats
                        .dropped
                        .map(|v| format!("{v:.0}"))
                        .unwrap_or_else(|| "-".into())
                ),
                Style::default().fg(TEXT_MUTED),
            ),
        ]),
    ];
    f.render_widget(Paragraph::new(stats_lines), fps_split[2]);

    // GPU Card
    let gpu_block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Rounded)
        .border_style(Style::default().fg(PURPLE))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            " HOST // GPU LOAD ",
            Style::default().fg(PURPLE).add_modifier(Modifier::BOLD),
        ));
    let gpu_in = gpu_block.inner(cards[1]);
    f.render_widget(gpu_block, cards[1]);

    let gpu_split = Layout::default()
        .direction(Direction::Vertical)
        .constraints([Constraint::Length(3), Constraint::Length(1), Constraint::Min(3)])
        .split(gpu_in);
    let gpu_all_zero = app.gpu_history.iter().all(|&v| v == 0);
    if app.gpu_history.is_empty() || gpu_all_zero {
        let w = gpu_split[0].width.saturating_sub(2) as usize;
        let baseline = "▁".repeat(w);
        f.render_widget(
            Paragraph::new(vec![
                Line::from(Span::styled(
                    "  GPU UTILIZATION TIMELINE (0-100%)",
                    Style::default().fg(TEXT_MUTED),
                )),
                Line::from(Span::styled(
                    format!(" {baseline}"),
                    Style::default().fg(PURPLE),
                )),
                Line::from(Span::styled(
                    " 0%                  50%                 100%",
                    Style::default().fg(BORDER_DIM),
                )),
            ]),
            gpu_split[0],
        );
    } else {
        let gpu_data: Vec<u64> = app.gpu_history.iter().copied().collect();
        f.render_widget(
            Sparkline::default()
                .data(&gpu_data)
                .max(100)
                .style(Style::default().fg(PURPLE)),
            gpu_split[0],
        );
    }
    let hm = &app.host_metrics;
    f.render_widget(
        Paragraph::new(vec![
            Line::from(format!(
                "{} {}% · {}°C",
                if hm.gpu_vendor.is_empty() {
                    "GPU"
                } else {
                    hm.gpu_vendor
                },
                hm.gpu_util
                    .map(|v| v.to_string())
                    .unwrap_or_else(|| "-".into()),
                hm.gpu_temp
                    .map(|v| v.to_string())
                    .unwrap_or_else(|| "-".into()),
            )),
            Line::from(Span::styled(
                format!(
                    "VIctory VRAM {}/{} MiB",
                    app.stats
                        .vram_used
                        .map(|v| format!("{v:.0}"))
                        .unwrap_or_else(|| "-".into()),
                    app.stats
                        .vram_budget
                        .map(|v| format!("{v:.0}"))
                        .unwrap_or_else(|| "-".into()),
                ),
                Style::default().fg(TEXT_MUTED),
            )),
        ]),
        gpu_split[2],
    );

    // Log Stream
    let visible: Vec<&LogLine> = app
        .logs
        .iter()
        .filter(|l| match app.log_filter {
            LogFilter::All => true,
            LogFilter::WarnErr => l.severity != Severity::Info,
            LogFilter::Metrics => l.is_metric,
        })
        .collect();
    let mut log_block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Rounded)
        .border_style(Style::default().fg(NEON_GREEN))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            format!(
                " LOG STREAM // {} ({} lines) ",
                app.log_filter.label(),
                visible.len()
            ),
            Style::default()
                .bg(NEON_GREEN)
                .fg(Color::Black)
                .add_modifier(Modifier::BOLD),
        ));
    if can_preview && visible.iter().any(|line| line.preview().is_some()) {
        let hint = if show_paths {
            " release Ctrl+P "
        } else {
            " hold Ctrl+P "
        };
        log_block = log_block.title_bottom(
            Line::from(Span::styled(hint, Style::default().fg(TEXT_MUTED))).right_aligned(),
        );
    }
    let mut entries: Vec<(&LogLine, usize)> = Vec::with_capacity(visible.len());
    for line in visible {
        if !show_paths && line.path_hidden {
            if let Some((previous, count)) = entries.last_mut() {
                if previous.path_hidden && previous.severity == line.severity {
                    *count += 1;
                    continue;
                }
            }
        }
        entries.push((line, 1));
    }
    let height = usize::from(log_block.inner(rows[1]).height);
    let end = entries
        .len()
        .saturating_sub(app.log_scroll.min(entries.len().saturating_sub(1)));
    let start = end.saturating_sub(height);
    let log_lines: Vec<Line> = entries[start..end]
        .iter()
        .map(|(l, count)| {
            let (tag, col) = match l.severity {
                Severity::Error => ("ERR ", RED),
                Severity::Warning => ("WRN ", AMBER),
                Severity::Info => ("INF ", TEXT_MUTED),
            };
            let preview = if show_paths { l.preview() } else { None };
            let text = if *count > 1 {
                format!("{} ×{count}", l.text)
            } else {
                preview.unwrap_or(&l.text).to_owned()
            };
            let text_color = if l.path_hidden && preview.is_none() {
                TEXT_MUTED
            } else if l.severity == Severity::Info {
                TEXT_MAIN
            } else {
                col
            };
            Line::from(vec![
                Span::styled(tag, Style::default().fg(col).add_modifier(Modifier::BOLD)),
                Span::styled(text, Style::default().fg(text_color)),
            ])
        })
        .collect();
    f.render_widget(
        Paragraph::new(log_lines)
            .block(log_block)
            .wrap(Wrap { trim: false }),
        rows[1],
    );
}

fn draw_display(f: &mut Frame, area: Rect, app: &AppState) {
    let block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Rounded)
        .border_style(Style::default().fg(PURPLE))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            " DISPLAY & GAMESCOPE ",
            Style::default().fg(PURPLE).add_modifier(Modifier::BOLD),
        ));
    let d = &app.display;
    let toggle = |on: bool| -> Span<'static> {
        if on {
            Span::styled(
                " ON ",
                Style::default()
                    .bg(CYAN)
                    .fg(Color::Black)
                    .add_modifier(Modifier::BOLD),
            )
        } else {
            Span::styled(" OFF ", Style::default().bg(BORDER_DIM).fg(TEXT_MUTED))
        }
    };
    let lines = vec![
        Line::from(""),
        Line::from(vec![
            Span::styled("  [G] Backend     ", Style::default().fg(TEXT_MUTED)),
            Span::styled(
                if app.gamescope {
                    "Gamescope"
                } else {
                    "Wine direct"
                },
                Style::default().fg(CYAN).add_modifier(Modifier::BOLD),
            ),
        ]),
        Line::from(vec![
            Span::styled("  [P] Preset      ", Style::default().fg(TEXT_MUTED)),
            Span::styled(
                d.preset_name(),
                Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD),
            ),
            Span::styled(
                format!("  ({}x{} -> {}x{})", d.game_w, d.game_h, d.win_w, d.win_h),
                Style::default().fg(TEXT_MUTED),
            ),
        ]),
        Line::from(vec![
            Span::styled("  [A] FSR         ", Style::default().fg(TEXT_MUTED)),
            toggle(d.fsr),
        ]),
        Line::from(vec![
            Span::styled("  [Enter] Fullscr ", Style::default().fg(TEXT_MUTED)),
            toggle(d.fullscreen),
        ]),
        Line::from(vec![
            Span::styled("  [L] FPS Cap     ", Style::default().fg(TEXT_MUTED)),
            Span::styled(
                if d.fps_limit == 0 {
                    "Unlimited".into()
                } else {
                    format!("{} fps", d.fps_limit)
                },
                Style::default().fg(AMBER),
            ),
        ]),
        Line::from(""),
        Line::from(Span::styled(
            "  Settings apply on next launch (press S).",
            Style::default().fg(TEXT_MUTED),
        )),
    ];
    f.render_widget(Paragraph::new(lines).block(block), area);
}

fn lh_score_color(score: u32) -> Color {
    if score >= 90 {
        Color::Rgb(12, 206, 107) // Lighthouse green
    } else if score >= 50 {
        Color::Rgb(255, 164, 0) // Lighthouse amber
    } else {
        Color::Rgb(255, 78, 66) // Lighthouse red
    }
}

fn draw_lighthouse(f: &mut Frame, area: Rect, app: &AppState) {
    let rows = Layout::default()
        .direction(Direction::Vertical)
        .constraints([
            Constraint::Length(7), // 3 Lighthouse Gauges
            Constraint::Length(7), // Metrics Grid
            Constraint::Min(5),    // Diagnostics & Audits
        ])
        .split(area);

    let has_run = app.running || app.status_text != "Idle";
    let scores = compute_lighthouse_scores(
        &app.stats,
        has_run,
        app.series_s,
        app.host_metrics.vram_total_mib,
    );

    // 1. Three circular-style ASCII Lighthouse Gauges
    let gauge_cols = Layout::default()
        .direction(Direction::Horizontal)
        .constraints([
            Constraint::Ratio(1, 3),
            Constraint::Ratio(1, 3),
            Constraint::Ratio(1, 3),
        ])
        .split(rows[0]);

    for (i, (title, score_opt)) in [
        ("PERFORMANCE", scores.perf),
        ("STABILITY", scores.stab),
        ("HEADROOM", scores.headroom),
    ]
    .into_iter()
    .enumerate()
    {
        let (col, score_str, rating, filled_ratio) = match score_opt {
            Some(score) => (
                lh_score_color(score),
                format!("{:>3}", score),
                if score >= 90 {
                    "GOOD"
                } else if score >= 50 {
                    "NEEDS WORK"
                } else {
                    "POOR"
                },
                score.min(100) as usize,
            ),
            None => (TEXT_MUTED, " --".into(), "NO RUN YET", 0),
        };
        let block = Block::default()
            .borders(Borders::ALL)
            .border_type(BorderType::Rounded)
            .border_style(Style::default().fg(col))
            .style(Style::default().bg(BG_PANEL))
            .title(Span::styled(
                format!(" {title} "),
                Style::default().fg(col).add_modifier(Modifier::BOLD),
            ));
        let bar_w = gauge_cols[i].width.saturating_sub(6) as usize;
        let filled = (filled_ratio * bar_w) / 100;
        let bar = format!(
            "{}{}",
            "━".repeat(filled),
            "─".repeat(bar_w.saturating_sub(filled))
        );
        let lines = vec![
            Line::from(vec![
                Span::styled(
                    format!("  ╭─ {score_str} ─╮  "),
                    Style::default().fg(col).add_modifier(Modifier::BOLD),
                ),
                Span::styled(
                    rating,
                    Style::default().fg(col).add_modifier(Modifier::BOLD),
                ),
            ]),
            Line::from(Span::styled(
                "  ╰───────╯  / 100",
                Style::default().fg(TEXT_MUTED),
            )),
            Line::from(Span::styled(format!("  {bar}"), Style::default().fg(col))),
        ];
        f.render_widget(Paragraph::new(lines).block(block), gauge_cols[i]);
    }

    // 2. Metrics Grid (empty "-" until real metrics arrive)
    let avg_fps_str = if app.stats.fps.count > 0 {
        format!("{:.1}", app.stats.fps.avg())
    } else if let Some(v) = app.stats.fps.last {
        format!("{v:.1}")
    } else {
        "-".into()
    };
    let minmax_fps_str = if app.stats.fps.count > 0 {
        format!("{:.1} / {:.1}", app.stats.fps.min, app.stats.fps.max)
    } else {
        "- / -".into()
    };
    let avg_ft_str = if app.stats.frame_time.count > 0 {
        format!("{:.2} ms", app.stats.frame_time.avg())
    } else if let Some(v) = app.stats.frame_time.last {
        format!("{v:.2} ms")
    } else {
        "-".into()
    };
    let drops_str = app
        .stats
        .dropped
        .map(|v| format!("{v:.1}"))
        .unwrap_or_else(|| "-".into());
    let perf_col = scores.perf.map(lh_score_color).unwrap_or(TEXT_MUTED);

    let metrics_block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Rounded)
        .border_style(Style::default().fg(BLUE))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            " LIGHTHOUSE // METRICS ",
            Style::default()
                .bg(BLUE)
                .fg(Color::Black)
                .add_modifier(Modifier::BOLD),
        ));
    let m_lines = vec![
        Line::from(vec![
            Span::styled("  ● Avg FPS        ", Style::default().fg(perf_col)),
            Span::styled(
                format!("{avg_fps_str:<10}"),
                Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD),
            ),
            Span::styled("  ● Min / Max FPS  ", Style::default().fg(TEXT_MUTED)),
            Span::styled(minmax_fps_str, Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(vec![
            Span::styled("  ● Avg Frame Time ", Style::default().fg(CYAN)),
            Span::styled(
                format!("{avg_ft_str:<10}"),
                Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD),
            ),
            Span::styled("  ● Dropped/Frame  ", Style::default().fg(TEXT_MUTED)),
            Span::styled(drops_str, Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(vec![
            Span::styled("  ● Warnings       ", Style::default().fg(AMBER)),
            Span::styled(
                if has_run {
                    format!("{:<10}", app.stats.warnings)
                } else {
                    "-         ".into()
                },
                Style::default().fg(AMBER).add_modifier(Modifier::BOLD),
            ),
            Span::styled("  ● Errors         ", Style::default().fg(RED)),
            Span::styled(
                if has_run {
                    format!("{}", app.stats.errors)
                } else {
                    "-".into()
                },
                Style::default().fg(RED).add_modifier(Modifier::BOLD),
            ),
        ]),
        Line::from(Span::styled(
            "  Press [S] to launch a real session or [R] to export HTML report",
            Style::default().fg(TEXT_MUTED),
        )),
    ];
    f.render_widget(Paragraph::new(m_lines).block(metrics_block), rows[1]);

    // 3. Diagnostics & Audits
    let audit_block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Rounded)
        .border_style(Style::default().fg(BORDER_DIM))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            " AUDITS & DIAGNOSTICS ",
            Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD),
        ));

    let audit_row = |pass: bool, warn: bool, label: &str, detail: String| -> Line<'static> {
        let (icon, col) = if pass {
            ("✓ PASS ", lh_score_color(95))
        } else if warn {
            ("! WARN ", lh_score_color(70))
        } else {
            ("✗ FAIL ", lh_score_color(20))
        };
        Line::from(vec![
            Span::styled(
                format!("  {icon}"),
                Style::default().fg(col).add_modifier(Modifier::BOLD),
            ),
            Span::styled(format!("{label:<22} "), Style::default().fg(TEXT_MAIN)),
            Span::styled(detail, Style::default().fg(TEXT_MUTED)),
        ])
    };

    let fork_lbl = app
        .active_fork()
        .map(|b| b.label().to_string())
        .unwrap_or_else(|| "None".into());
    let vram_mib = app.host_metrics.vram_total_mib.unwrap_or(0);
    let req_vram = if app.series_s { 6144 } else { 10240 };
    let mut audits = vec![
        audit_row(
            app.stats.errors == 0,
            false,
            "Error-free session",
            format!("{} errors observed", app.stats.errors),
        ),
        audit_row(
            vram_mib >= req_vram,
            vram_mib >= 6144,
            "VRAM headroom",
            format!(
                "{} MiB available (profile target {} MiB)",
                vram_mib, req_vram
            ),
        ),
        audit_row(
            app.active_fork().is_some(),
            false,
            "Active VIctory build",
            fork_lbl,
        ),
        audit_row(
            !app.game_exe.is_empty(),
            false,
            "Target executable",
            redact_path(&app.game_exe),
        ),
        audit_row(
            true,
            false,
            "Display backend",
            if app.gamescope {
                format!("Gamescope ({})", app.display.preset_name())
            } else {
                "Wine direct".into()
            },
        ),
    ];
    let guard_findings = evaluate_guard(
        &app.wine_bin,
        app.series_s,
        app.gamescope,
        true,
        &app.host_metrics,
        &app.stats,
        &app.logs,
    );
    if !guard_findings.is_empty() {
        audits.push(Line::from(""));
        audits.push(Line::from(Span::styled(
            "  TROUBLESHOOTING GUARD",
            Style::default().fg(AMBER).add_modifier(Modifier::BOLD),
        )));
        for g in guard_findings.into_iter().take(4) {
            let col = if g.severity == "WARN" { AMBER } else { CYAN };
            audits.push(Line::from(vec![
                Span::styled(
                    format!("  [{}] ", g.severity),
                    Style::default().fg(col).add_modifier(Modifier::BOLD),
                ),
                Span::styled(format!("{}: ", g.title), Style::default().fg(TEXT_MAIN)),
                Span::styled(g.hint, Style::default().fg(TEXT_MUTED)),
            ]));
        }
    }
    f.render_widget(Paragraph::new(audits).block(audit_block), rows[2]);
}

fn draw_forks_inventory(f: &mut Frame, area: Rect, app: &AppState) {
    let [grid, inspect, essential] = inventory_layout(area);
    let cards = inventory_cards(grid, inventory_cols(area));
    let per_page = cards.len();
    let total_slots = app.forks.len() + 1;
    let page = app.inventory_cursor / per_page;
    let start = page * per_page;

    for (offset, rect) in cards.into_iter().enumerate() {
        let idx = start + offset;
        let selected = idx == app.inventory_cursor;
        let bar_w = rect.width.saturating_sub(4) as usize;

        let (border_c, rarity_c, title, sub) = if idx < app.forks.len() {
            let b = &app.forks[idx];
            let equipped = app.active_fork_idx == Some(idx);
            let rc = tier_color(b.tier());
            let bc = if selected { CYAN } else { rc };
            let prefix = if equipped { "★ " } else { "  " };
            let status = if equipped { "ACTIVE" } else { "READY" };
            (
                bc,
                rc,
                format!("{prefix}{}", b.label()),
                format!("{} {status}", b.glyph()),
            )
        } else if idx == app.forks.len() {
            (
                if selected { CYAN } else { BORDER_DIM },
                CYAN,
                "+ Add build".into(),
                "[Enter/N] Add in TUI".into(),
            )
        } else {
            (BORDER_DIM, BORDER_DIM, String::new(), String::new())
        };

        let block = Block::default()
            .borders(Borders::ALL)
            .border_type(if selected {
                BorderType::Double
            } else {
                BorderType::Rounded
            })
            .border_style(Style::default().fg(border_c));

        let bar_char = if idx < app.forks.len() {
            "▓"
        } else if idx == app.forks.len() {
            "▒"
        } else {
            "░"
        };
        let content = vec![
            Line::from(Span::styled(
                format!(" {}", bar_char.repeat(bar_w)),
                Style::default().fg(rarity_c),
            )),
            Line::from(Span::styled(
                title,
                Style::default().fg(TEXT_MAIN).add_modifier(if selected {
                    Modifier::BOLD
                } else {
                    Modifier::empty()
                }),
            )),
            Line::from(Span::styled(sub, Style::default().fg(TEXT_MUTED))),
        ];
        f.render_widget(Paragraph::new(content).block(block), rect);
    }

    let inspect_block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Rounded)
        .border_style(Style::default().fg(BORDER_DIM))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            format!(
                " LOADOUT INSPECT // PAGE {}/{} ",
                page + 1,
                total_slots.div_ceil(per_page).max(1)
            ),
            Style::default().fg(AMBER).add_modifier(Modifier::BOLD),
        ));
    let inspect_lines = if let Some(b) = app.forks.get(app.inventory_cursor) {
        let eq = if app.active_fork_idx == Some(app.inventory_cursor) {
            "★ ACTIVE"
        } else {
            "READY"
        };
        vec![
            Line::from(vec![
                Span::styled(
                    format!("{}  ", b.label()),
                    Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD),
                ),
                Span::styled(
                    format!("{}  ", tier_name(b.tier())),
                    Style::default().fg(tier_color(b.tier())),
                ),
                Span::styled(eq, Style::default().fg(CYAN)),
            ]),
            Line::from(Span::styled(
                format!(
                    "{}  •  {}   [X] Open Folder",
                    redact_path(&b.directory),
                    redact_path(&b.binary)
                ),
                Style::default().fg(TEXT_MUTED),
            )),
        ]
    } else {
        vec![
            Line::from("Register a fork: victory-px --save-fork <name> --victory-dir <dir>"),
            Line::from(Span::styled(
                "Arrows Navigate · Enter Equip · Esc Back",
                Style::default().fg(TEXT_MUTED),
            )),
        ]
    };
    f.render_widget(Paragraph::new(inspect_lines).block(inspect_block), inspect);

    // Bottom: Essential Loadout Slots (CS:GO style cards, zero full host paths)
    let ess_cols = essential_cards(essential);

    let game_name = if app.game_exe.is_empty() {
        "Select .exe".into()
    } else {
        Path::new(&app.game_exe)
            .file_name()
            .and_then(|s| s.to_str())
            .unwrap_or("title.exe")
            .to_string()
    };
    let wine_name = if !app.runtime_label.is_empty() {
        app.runtime_label
            .chars()
            .filter(|c| !c.is_control())
            .collect()
    } else if app.wine_bin.is_empty() {
        "missing".into()
    } else {
        Path::new(&app.wine_bin)
            .file_name()
            .and_then(|s| s.to_str())
            .unwrap_or("wine")
            .to_string()
    };

    let slot_cards = [
        (
            NEON_GREEN,
            "[O] TARGET .EXE",
            game_name,
            if app.game_exe.is_empty() {
                "PRESS O TO BROWSE".into()
            } else {
                "★ ARMED · [V] Open Dir".into()
            },
        ),
        (
            PURPLE,
            "[G] DISPLAY",
            if app.gamescope {
                format!("Gamescope ({})", app.display.preset_name())
            } else {
                "Wine direct".into()
            },
            if app.gamescope {
                format!(
                    "{}x{} · FSR {}",
                    app.display.game_w,
                    app.display.game_h,
                    if app.display.fsr { "ON" } else { "OFF" }
                )
            } else {
                "PRESS G TO CONFIG".into()
            },
        ),
        (
            BLUE,
            "[C] RUNTIME",
            if app.series_s {
                "Xbox Series S".into()
            } else {
                "Xbox Series X".into()
            },
            format!("runner: {wine_name}"),
        ),
    ];

    for (i, (col, tag, title, sub)) in slot_cards.into_iter().enumerate() {
        let bar_w = ess_cols[i].width.saturating_sub(4) as usize;
        let block = Block::default()
            .borders(Borders::ALL)
            .border_type(BorderType::Rounded)
            .border_style(Style::default().fg(col))
            .style(Style::default().bg(BG_PANEL))
            .title(Span::styled(
                format!(" {tag} "),
                Style::default()
                    .bg(col)
                    .fg(Color::Black)
                    .add_modifier(Modifier::BOLD),
            ));
        let lines = vec![
            Line::from(Span::styled(
                format!(" {}", "▓".repeat(bar_w)),
                Style::default().fg(col),
            )),
            Line::from(Span::styled(
                format!(" {title}"),
                Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD),
            )),
            Line::from(Span::styled(
                format!(" {sub}"),
                Style::default().fg(TEXT_MUTED),
            )),
        ];
        f.render_widget(Paragraph::new(lines).block(block), ess_cols[i]);
    }
}

fn draw_bottom_bar(f: &mut Frame, area: Rect, app: &AppState) {
    let notice = if !app.notice.is_empty() {
        Span::styled(
            format!(" ▸ {} ", app.notice),
            Style::default().fg(NEON_GREEN).add_modifier(Modifier::BOLD),
        )
    } else {
        Span::styled(
            " [1] Monitor  [2] Display  [B] Loadout  [G] Gamescope  [O] Game Dir  [S] Run/Stop  [W] Report  [Q] Quit ",
            Style::default().fg(TEXT_MAIN),
        )
    };
    f.render_widget(
        Paragraph::new(Line::from(notice)).style(Style::default().bg(BG_BAR)),
        area,
    );
}

fn centered_rect(width_pct: u16, height: u16, area: Rect) -> Rect {
    let w = ((u32::from(area.width) * u32::from(width_pct) / 100) as u16)
        .max(46)
        .min(area.width.saturating_sub(2));
    let h = height.min(area.height.saturating_sub(2));
    let x = area.x + (area.width.saturating_sub(w)) / 2;
    let y = area.y + (area.height.saturating_sub(h)) / 2;
    Rect::new(x, y, w, h)
}

fn draw_gamescope_modal(f: &mut Frame, area: Rect, app: &AppState) {
    let popup = centered_rect(58, 13, area);
    f.render_widget(Clear, popup);

    let block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Double)
        .border_style(Style::default().fg(PURPLE))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            " GAMESCOPE // QUICK CONFIG ",
            Style::default()
                .bg(PURPLE)
                .fg(Color::Black)
                .add_modifier(Modifier::BOLD),
        ));

    let d = &app.display;
    let badge = |on: bool| -> Span<'static> {
        if on {
            Span::styled(
                " ON ",
                Style::default()
                    .bg(NEON_GREEN)
                    .fg(Color::Black)
                    .add_modifier(Modifier::BOLD),
            )
        } else {
            Span::styled(" OFF ", Style::default().bg(BORDER_DIM).fg(TEXT_MUTED))
        }
    };
    let lines = vec![
        Line::from(""),
        Line::from(vec![
            Span::styled("  [Space] Backend     ", Style::default().fg(TEXT_MUTED)),
            Span::styled(
                if app.gamescope {
                    "GAMESCOPE"
                } else {
                    "WINE DIRECT"
                },
                Style::default()
                    .fg(if app.gamescope { NEON_GREEN } else { AMBER })
                    .add_modifier(Modifier::BOLD),
            ),
        ]),
        Line::from(vec![
            Span::styled("  [P]     Preset      ", Style::default().fg(TEXT_MUTED)),
            Span::styled(
                d.preset_name().to_uppercase(),
                Style::default().fg(CYAN).add_modifier(Modifier::BOLD),
            ),
            Span::styled(
                format!("  ({}x{} -> {}x{})", d.game_w, d.game_h, d.win_w, d.win_h),
                Style::default().fg(TEXT_MUTED),
            ),
        ]),
        Line::from(vec![
            Span::styled("  [A]     FSR         ", Style::default().fg(TEXT_MUTED)),
            badge(d.fsr),
        ]),
        Line::from(vec![
            Span::styled("  [F]     Fullscreen  ", Style::default().fg(TEXT_MUTED)),
            badge(d.fullscreen),
        ]),
        Line::from(vec![
            Span::styled("  [L]     FPS Cap     ", Style::default().fg(TEXT_MUTED)),
            Span::styled(
                if d.fps_limit == 0 {
                    "Unlimited".into()
                } else {
                    format!("{} fps", d.fps_limit)
                },
                Style::default().fg(AMBER).add_modifier(Modifier::BOLD),
            ),
        ]),
        Line::from(""),
        Line::from(Span::styled(
            "  [Space/P/A/F/L] Toggle   [Esc / G] Close",
            Style::default().fg(TEXT_MUTED),
        )),
    ];
    f.render_widget(Paragraph::new(lines).block(block), popup);
}

fn draw_browser_modal(f: &mut Frame, area: Rect, app: &AppState) {
    let popup = centered_rect(72, 20, area);
    f.render_widget(Clear, popup);

    let (title_txt, hint_txt) = match app.modal {
        ModalKind::ForkWizardDir => (
            " ADD FORK (1/3) // CHOOSE FORK DIRECTORY ",
            " [↑/↓] Nav  [Enter] Open  [Space] Use Dir  [D] Downloads  [.] CWD  [Esc] Cancel",
        ),
        ModalKind::ForkWizardBin => (
            " ADD FORK (2/3) // SELECT victory.exe ",
            " [↑/↓] Nav  [Enter] Select .exe  [Backspace] Previous step  [Esc] Cancel",
        ),
        _ => (
            " GAME BROWSER // SELECT .EXE OR FOLDER ",
            " [↑/↓] Nav  [Enter] Open/Select  [D] Downloads  [H] Home  [.] CWD  [Esc] Close",
        ),
    };

    let block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Double)
        .border_style(Style::default().fg(CYAN))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            title_txt,
            Style::default()
                .bg(CYAN)
                .fg(Color::Black)
                .add_modifier(Modifier::BOLD),
        ));
    let inner = block.inner(popup);
    f.render_widget(block, popup);

    let chunks = Layout::default()
        .direction(Direction::Vertical)
        .constraints([
            Constraint::Length(2),
            Constraint::Min(4),
            Constraint::Length(1),
        ])
        .split(inner);

    f.render_widget(
        Paragraph::new(vec![
            Line::from(vec![
                Span::styled(
                    " DIR ",
                    Style::default()
                        .bg(BLUE)
                        .fg(Color::Black)
                        .add_modifier(Modifier::BOLD),
                ),
                Span::styled(
                    format!(" {}", redact_path(&app.browser.cwd.to_string_lossy())),
                    Style::default().fg(TEXT_MAIN),
                ),
            ]),
            Line::from(Span::styled(
                " ─────────────────────────────────────────────────────",
                Style::default().fg(BORDER_DIM),
            )),
        ]),
        chunks[0],
    );

    let list_h = chunks[1].height as usize;
    let total = app.browser.entries.len();
    let start = app.browser.cursor.saturating_sub(list_h.saturating_sub(1));
    let end = (start + list_h).min(total);
    let mut lines = Vec::new();
    if total == 0 {
        lines.push(Line::from(Span::styled(
            "  (no folders or .exe files here)",
            Style::default().fg(TEXT_MUTED),
        )));
    } else {
        for (i, entry) in app.browser.entries[start..end].iter().enumerate() {
            let idx = start + i;
            let selected = idx == app.browser.cursor;
            let (tag, col) = if entry.is_dir {
                ("[DIR] ", BLUE)
            } else {
                ("[EXE] ", NEON_GREEN)
            };
            let prefix = if selected { "▌ " } else { "  " };
            let name_style = if selected {
                Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD)
            } else {
                Style::default().fg(TEXT_MUTED)
            };
            let mut spans = vec![
                Span::styled(prefix, Style::default().fg(CYAN)),
                Span::styled(tag, Style::default().fg(col).add_modifier(Modifier::BOLD)),
                Span::styled(entry.name.clone(), name_style),
            ];
            if app.modal == ModalKind::ForkWizardBin && !entry.is_dir {
                let lower = entry.name.to_lowercase();
                if lower == "victory.exe" || lower.ends_with("/victory.exe") {
                    spans.push(Span::styled(
                        "  ★ RECOMMENDED (Linux/Proton)",
                        Style::default().fg(NEON_GREEN).add_modifier(Modifier::BOLD),
                    ));
                } else if lower.contains("win10") {
                    spans.push(Span::styled(
                        "  (Windows 10 Agility SDK)",
                        Style::default().fg(TEXT_MUTED),
                    ));
                }
            }
            lines.push(Line::from(spans));
        }
    }
    f.render_widget(Paragraph::new(lines), chunks[1]);

    f.render_widget(
        Paragraph::new(Line::from(Span::styled(
            hint_txt,
            Style::default().fg(TEXT_MUTED),
        ))),
        chunks[2],
    );
}

fn draw_fork_name_modal(f: &mut Frame, area: Rect, app: &AppState) {
    let popup = centered_rect(54, 9, area);
    f.render_widget(Clear, popup);

    let block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Double)
        .border_style(Style::default().fg(NEON_GREEN))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            " ADD FORK (3/3) // NAME PROFILE ",
            Style::default()
                .bg(NEON_GREEN)
                .fg(Color::Black)
                .add_modifier(Modifier::BOLD),
        ));
    let lines = vec![
        Line::from(""),
        Line::from(vec![
            Span::styled("  DIR  ", Style::default().fg(TEXT_MUTED)),
            Span::styled(redact_path(&app.wizard_dir), Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(vec![
            Span::styled("  BIN  ", Style::default().fg(TEXT_MUTED)),
            Span::styled(redact_path(&app.wizard_bin), Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(""),
        Line::from(vec![
            Span::styled(
                "  NAME: ",
                Style::default().fg(CYAN).add_modifier(Modifier::BOLD),
            ),
            Span::styled(
                format!("{}_", app.wizard_name),
                Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD),
            ),
        ]),
        Line::from(""),
        Line::from(Span::styled(
            "  [Enter] Save Fork   [Esc] Cancel",
            Style::default().fg(TEXT_MUTED),
        )),
    ];
    f.render_widget(Paragraph::new(lines).block(block), popup);
}

fn draw_theme_modal(f: &mut Frame, area: Rect, app: &AppState) {
    let popup = centered_rect(54, 11, area);
    f.render_widget(Clear, popup);

    let block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Double)
        .border_style(Style::default().fg(MAGENTA))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            " THEME & LOGO FX ",
            Style::default()
                .bg(MAGENTA)
                .fg(Color::Black)
                .add_modifier(Modifier::BOLD),
        ));
    let speed_lbl = match app.anim_speed {
        0 => "Slow",
        1 => "Normal",
        _ => "Fast",
    };
    let swatches: Vec<Span> = app
        .palette
        .colors()
        .into_iter()
        .map(|c| Span::styled("██", Style::default().fg(c)))
        .collect();
    let mut palette_line = vec![
        Span::styled("  [P] Palette    ", Style::default().fg(TEXT_MUTED)),
        Span::styled(
            format!("{}  ", app.palette.label()),
            Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD),
        ),
    ];
    palette_line.extend(swatches);

    let lines = vec![
        Line::from(""),
        Line::from(palette_line),
        Line::from(vec![
            Span::styled("  [A] Animation  ", Style::default().fg(TEXT_MUTED)),
            Span::styled(
                app.anim_mode.label(),
                Style::default().fg(CYAN).add_modifier(Modifier::BOLD),
            ),
        ]),
        Line::from(vec![
            Span::styled("  [V] Speed         ", Style::default().fg(TEXT_MUTED)),
            Span::styled(
                speed_lbl,
                Style::default().fg(AMBER).add_modifier(Modifier::BOLD),
            ),
        ]),
        Line::from(vec![
            Span::styled("  [E] Error Popups  ", Style::default().fg(TEXT_MUTED)),
            Span::styled(
                if app.show_error_popups { "ON" } else { "OFF" },
                Style::default()
                    .fg(if app.show_error_popups {
                        NEON_GREEN
                    } else {
                        TEXT_MUTED
                    })
                    .add_modifier(Modifier::BOLD),
            ),
        ]),
        Line::from(""),
        Line::from(Span::styled(
            "  [P] Palette  [A] Anim  [V] Speed  [E] Error Popups  [Esc/T] Close",
            Style::default().fg(TEXT_MUTED),
        )),
    ];
    f.render_widget(Paragraph::new(lines).block(block), popup);
}

fn draw_error_advisory_modal(f: &mut Frame, area: Rect, app: &AppState) {
    let popup = centered_rect(64, 11, area);
    f.render_widget(Clear, popup);

    let block = Block::default()
        .borders(Borders::ALL)
        .border_type(BorderType::Double)
        .border_style(Style::default().fg(RED))
        .style(Style::default().bg(BG_PANEL))
        .title(Span::styled(
            " TROUBLESHOOTING GUARD // ERROR DETECTED ",
            Style::default()
                .bg(RED)
                .fg(Color::Black)
                .add_modifier(Modifier::BOLD),
        ));

    let (title, hint) = match &app.active_advisory {
        Some(g) => (g.title, g.hint.as_str()),
        None => (
            "Error detected in session",
            "Check the Log Stream and Lighthouse tab ([3]).",
        ),
    };
    let lines = vec![
        Line::from(""),
        Line::from(vec![
            Span::styled(
                "  PROBLEM:  ",
                Style::default().fg(RED).add_modifier(Modifier::BOLD),
            ),
            Span::styled(
                title,
                Style::default().fg(TEXT_MAIN).add_modifier(Modifier::BOLD),
            ),
        ]),
        Line::from(""),
        Line::from(vec![
            Span::styled(
                "  FIX:      ",
                Style::default().fg(NEON_GREEN).add_modifier(Modifier::BOLD),
            ),
            Span::styled(hint, Style::default().fg(TEXT_MAIN)),
        ]),
        Line::from(""),
        Line::from(Span::styled(
            "  [Enter / Esc] Dismiss    [D] Disable Error Popups",
            Style::default().fg(TEXT_MUTED),
        )),
    ];
    f.render_widget(
        Paragraph::new(lines).wrap(Wrap { trim: false }).block(block),
        popup,
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn redaction_never_echoes_path_components() {
        for raw in ["/media/example-person/project", "relative/file"] {
            assert_eq!(redact_path(raw), "[path hidden]");
        }
    }

    #[test]
    fn loadout_geometry_stays_inside_terminal() {
        for width in [48, 72, 91, 92, 139, 160] {
            let screen = Rect::new(0, 0, width, 24);
            let stage = screen_layout(screen)[2];
            let [grid, _, actions] = inventory_layout(stage);
            let cols = fork_columns(screen);
            let cards = inventory_cards(grid, cols);
            assert!(cards.len() >= cols);
            for rect in cards.into_iter().chain(essential_cards(actions)) {
                assert_eq!(rect.intersection(stage), rect);
                assert!(!rect.is_empty());
            }
        }
        assert!(compact(Rect::new(0, 0, 47, 24)));
        assert!(compact(Rect::new(0, 0, 48, 23)));
    }
}
