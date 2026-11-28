mod corpus;
mod forks;
mod guard;
mod metrics;
mod private_files;
mod runner;
mod system;
mod ui;

use clap::Parser;
use corpus::{
    build_corpus_bundle, build_lighthouse_html, write_report_file, write_report_with_ext,
    CorpusMeta,
};
use crossterm::event::{
    self, DisableFocusChange, DisableMouseCapture, EnableFocusChange, EnableMouseCapture, Event,
    KeyCode, KeyEventKind, KeyModifiers, KeyboardEnhancementFlags, ModifierKeyCode, MouseEventKind,
    PopKeyboardEnhancementFlags, PushKeyboardEnhancementFlags,
};
use crossterm::terminal::{
    disable_raw_mode, enable_raw_mode, EnterAlternateScreen, LeaveAlternateScreen,
};
use crossterm::ExecutableCommand;
use forks::{
    delete_fork_build, load_fork_builds, load_game_path, save_fork_build, save_game_path, ForkBuild,
};
use metrics::RunStats;
use ratatui::backend::CrosstermBackend;
use ratatui::Terminal;
use runner::{
    find_in_path, find_proton_wine, spawn_victory, DisplayOptions, LaunchOptions, RunnerEvent,
    Session,
};
use std::collections::VecDeque;
use std::io;
use std::path::Path;
use std::process::{Command, Stdio};
use std::sync::{
    atomic::{AtomicBool, Ordering},
    mpsc, Arc,
};
use std::time::{Duration, Instant};
use system::{probe_host_info, sample_host_metrics};
use ui::{
    draw, ActiveTab, AnimMode, AppState, GameBrowserState, LogFilter, ModalKind, ThemePalette,
};

#[derive(Parser, Debug)]
#[command(
    name = "victory-px",
    version,
    about = "OpenCode-style TUI launcher for VIctory"
)]
struct Cli {
    #[arg(long, env = "VICTORY_DIR")]
    victory_dir: Option<String>,
    #[arg(long)]
    victory_bin: Option<String>,
    #[arg(long, env = "VICTORY_EXE")]
    exe: Option<String>,
    #[arg(long)]
    fork: Option<String>,
    #[arg(long)]
    save_fork: Option<String>,
    #[arg(long)]
    list_forks: bool,
    #[arg(long)]
    wine: Option<String>,
    #[arg(long, default_value = "")]
    runtime_label: String,
    #[arg(long)]
    series_s: bool,
    #[arg(long)]
    gamescope: bool,
    #[arg(long, default_value = "small")]
    display_preset: String,
    #[arg(long, default_value_t = 0)]
    fps_limit: u32,
    #[arg(long)]
    fsr: bool,
    #[arg(long)]
    fullscreen: bool,
    #[arg(long)]
    bench: bool,
    #[arg(long, default_value_t = 30)]
    duration: u64,
    #[arg(long)]
    run: bool,
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let cli = Cli::parse();

    if let Some(name) = cli.save_fork.as_deref() {
        let dir = cli
            .victory_dir
            .as_deref()
            .ok_or("--save-fork requires --victory-dir")?;
        save_fork_build(name, dir, cli.victory_bin.as_deref())?;
        println!("Saved fork {name}");
        return Ok(());
    }

    let mut forks = load_fork_builds();
    if cli.list_forks {
        for f in &forks {
            println!("{}", f.name);
        }
        return Ok(());
    }

    let mut active_idx = None;
    if let Some(name) = cli.fork.as_deref() {
        let pos = forks
            .iter()
            .position(|f| f.name == name)
            .ok_or_else(|| format!("unknown fork {name:?}; run --list-forks"))?;
        active_idx = Some(pos);
    } else if let Some(dir) = cli.victory_dir {
        let bin = cli
            .victory_bin
            .unwrap_or_else(|| format!("{dir}/{}", forks::DIAG_BINARY));
        forks.push(ForkBuild {
            name: String::new(),
            directory: dir,
            binary: bin,
        });
        active_idx = Some(forks.len() - 1);
    } else if !forks.is_empty() {
        active_idx = Some(0);
    }

    let game_exe = cli.exe.or_else(load_game_path).unwrap_or_default();
    let (wine_bin, runtime_label) = if let Some(w) = cli.wine {
        (w, cli.runtime_label)
    } else if let Some((pw, pl)) = find_proton_wine() {
        let lbl = if cli.runtime_label.is_empty() { pl } else { cli.runtime_label };
        (pw, lbl)
    } else {
        (find_in_path(&["wine", "wine64"]).unwrap_or_default(), cli.runtime_label)
    };

    let mut display = DisplayOptions::default();
    match cli.display_preset.as_str() {
        "upscale" => {
            display.cycle_preset();
        }
        "1080p" => {
            display.cycle_preset();
            display.cycle_preset();
        }
        _ => {}
    }
    if cli.fsr {
        display.fsr = true;
    }
    if cli.fullscreen {
        display.fullscreen = true;
    }
    display.fps_limit = cli.fps_limit;

    let host_info = probe_host_info();
    let host_metrics = sample_host_metrics(host_info.has_nvidia_smi);

    let theme = load_theme_prefs();
    let mut app = AppState {
        tab: if active_idx.is_none() || game_exe.is_empty() {
            ActiveTab::Forks
        } else {
            ActiveTab::Monitor
        },
        modal: ModalKind::None,
        palette: theme.palette,
        anim_mode: theme.anim_mode,
        anim_speed: theme.anim_speed,
        anim_tick: 0,
        show_error_popups: true,
        advisory_shown_this_run: false,
        active_advisory: None,
        browser: GameBrowserState::default(),
        wizard_dir: String::new(),
        wizard_bin: String::new(),
        wizard_name: String::new(),
        forks,
        active_fork_idx: active_idx,
        inventory_cursor: active_idx.unwrap_or(0),
        game_exe,
        wine_bin,
        runtime_label,
        series_s: cli.series_s,
        gamescope: cli.gamescope,
        display,
        running: false,
        status_text: "Idle".into(),
        notice: String::new(),
        host_info,
        host_metrics,
        stats: RunStats::default(),
        elapsed_secs: 0,
        fps_history: VecDeque::with_capacity(64),
        gpu_history: VecDeque::with_capacity(64),
        logs: Vec::new(),
        log_filter: LogFilter::All,
        log_scroll: 0,
    };

    let interrupted = Arc::new(AtomicBool::new(false));
    for signal in [libc::SIGINT, libc::SIGTERM, libc::SIGHUP] {
        signal_hook::flag::register(signal, Arc::clone(&interrupted))?;
    }
    if cli.run {
        return run_headless_cli(&mut app, &interrupted);
    }
    if cli.bench {
        return run_headless_bench(&mut app, cli.duration, &interrupted);
    }

    enable_raw_mode()?;
    let mut restore = TerminalRestore {
        keyboard_enhanced: false,
    };
    io::stdout().execute(EnterAlternateScreen)?;
    io::stdout().execute(EnableMouseCapture)?;
    io::stdout().execute(EnableFocusChange)?;
    if crossterm::terminal::supports_keyboard_enhancement().unwrap_or(false) {
        io::stdout().execute(PushKeyboardEnhancementFlags(
            KeyboardEnhancementFlags::DISAMBIGUATE_ESCAPE_CODES
                | KeyboardEnhancementFlags::REPORT_EVENT_TYPES
                | KeyboardEnhancementFlags::REPORT_ALL_KEYS_AS_ESCAPE_CODES,
        ))?;
        restore.keyboard_enhanced = true;
    }
    let backend = CrosstermBackend::new(io::stdout());
    let mut terminal = Terminal::new(backend)?;
    run_event_loop(
        &mut terminal,
        &mut app,
        &interrupted,
        restore.keyboard_enhanced,
    )
}

struct TerminalRestore {
    keyboard_enhanced: bool,
}

impl Drop for TerminalRestore {
    fn drop(&mut self) {
        if self.keyboard_enhanced {
            let _ = io::stdout().execute(PopKeyboardEnhancementFlags);
        }
        let _ = io::stdout().execute(DisableFocusChange);
        let _ = disable_raw_mode();
        let _ = io::stdout().execute(DisableMouseCapture);
        let _ = io::stdout().execute(LeaveAlternateScreen);
        let _ = io::stdout().execute(crossterm::cursor::Show);
    }
}

impl AppState {
    fn launch_options(&self) -> LaunchOptions<'_> {
        LaunchOptions {
            game_exe: &self.game_exe,
            wine_bin: &self.wine_bin,
            series_s: self.series_s,
            gamescope: self.gamescope,
            display: &self.display,
        }
    }
}

fn run_headless_cli(
    app: &mut AppState,
    interrupted: &AtomicBool,
) -> Result<(), Box<dyn std::error::Error>> {
    let b = app
        .active_fork()
        .cloned()
        .ok_or("select a fork with --fork NAME for --run")?;
    println!(
        "CLI RUN // fork={} | wine={} | game={}",
        b.label(),
        app.wine_bin,
        app.game_exe
    );
    let (tx, rx) = mpsc::sync_channel(1024);
    let mut child = spawn_victory(&b, app.launch_options(), tx)?;
    let start = Instant::now();
    while !interrupted.load(Ordering::Relaxed) {
        for RunnerEvent::Line(line) in rx.try_iter().take(1024) {
            app.stats.record(&line);
            println!("{line}");
        }
        if let Some(status) = child.try_wait()? {
            let drain_deadline = Instant::now() + Duration::from_millis(200);
            while let Ok(RunnerEvent::Line(line)) =
                rx.recv_timeout(drain_deadline.saturating_duration_since(Instant::now()))
            {
                app.stats.record(&line);
                println!("{line}");
            }
            println!(
                "\n--- CLI RUN EXITED ({}) in {:.1}s | WARN={} ERR={} ---",
                status.code().unwrap_or(-1),
                start.elapsed().as_secs_f64(),
                app.stats.warnings,
                app.stats.errors
            );
            let findings = guard::evaluate_guard(
                &app.wine_bin,
                app.series_s,
                app.gamescope,
                find_in_path(&["gamescope"]).is_some(),
                &app.host_metrics,
                &app.stats,
                &app.logs,
            );
            if !findings.is_empty() {
                println!("TROUBLESHOOTING GUARD:");
                for f in findings {
                    println!("  [{}] {}: {}", f.severity, f.title, f.hint);
                }
            }
            return if status.success() {
                Ok(())
            } else {
                Err(format!("Session failed ({status})").into())
            };
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    child.stop()?;
    println!("\n--- CLI RUN INTERRUPTED in {:.1}s ---", start.elapsed().as_secs_f64());
    Ok(())
}

fn run_headless_bench(
    app: &mut AppState,
    duration_secs: u64,
    interrupted: &AtomicBool,
) -> Result<(), Box<dyn std::error::Error>> {
    let b = app
        .active_fork()
        .cloned()
        .ok_or("select a fork with --fork NAME for --bench")?;
    let (tx, rx) = mpsc::sync_channel(1024);
    let mut child = spawn_victory(&b, app.launch_options(), tx)?;
    let start = Instant::now();
    let target = Duration::from_secs(duration_secs.max(1));
    println!(
        "Running repeatable benchmark on {} for {}s...",
        b.label(),
        duration_secs.max(1)
    );

    let mut failed = false;
    while start.elapsed() < target && !interrupted.load(Ordering::Relaxed) {
        for RunnerEvent::Line(line) in rx.try_iter().take(1024) {
            app.stats.record(&line);
        }
        if let Some(status) = child.try_wait()? {
            failed = !status.success();
            app.status_text = if failed { "Failed" } else { "Finished" }.into();
            break;
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    child.stop()?;
    let duration = start.elapsed();
    let cancelled = interrupted.load(Ordering::Relaxed);
    if cancelled {
        app.status_text = "Interrupted".into();
    }
    let drain_deadline = Instant::now() + Duration::from_millis(200);
    while let Ok(RunnerEvent::Line(line)) =
        rx.recv_timeout(drain_deadline.saturating_duration_since(Instant::now()))
    {
        app.stats.record(&line);
        if Instant::now() >= drain_deadline {
            break;
        }
    }
    if app.status_text == "Idle" {
        app.status_text = "Completed".into();
    }
    app.host_metrics = sample_host_metrics(app.host_info.has_nvidia_smi);

    let meta = CorpusMeta {
        fork_name: &b.name,
        host_metrics: &app.host_metrics,
        series_s: app.series_s,
        gamescope: app.gamescope,
        display: &app.display,
        duration: Some(duration),
        state_label: &app.status_text,
    };
    let html = build_lighthouse_html(&app.stats, &meta);
    let html_path = write_report_with_ext("lighthouse", "html", &html)?;
    let corpus = build_corpus_bundle(&app.stats, &meta);
    let md_path = write_report_file("report", &corpus)?;
    let scores = corpus::compute_lighthouse_scores(
        &app.stats,
        true,
        app.series_s,
        app.host_metrics.vram_total_mib,
    );
    let avg_fps = if app.stats.fps.count > 0 {
        app.stats.fps.avg()
    } else {
        app.stats.fps.last.unwrap_or(0.0)
    };
    let avg_ft = if app.stats.frame_time.count > 0 {
        app.stats.frame_time.avg()
    } else {
        app.stats.frame_time.last.unwrap_or(0.0)
    };

    println!("\n=== VICTORY-PX LIGHTHOUSE REPORT ===");
    println!(
        "Performance: {}/100 | Stability: {}/100 | Headroom: {}/100",
        scores
            .perf
            .map(|v| v.to_string())
            .unwrap_or_else(|| "--".into()),
        scores
            .stab
            .map(|v| v.to_string())
            .unwrap_or_else(|| "--".into()),
        scores
            .headroom
            .map_or_else(|| "--".into(), |v| v.to_string())
    );
    println!(
        "Avg FPS: {:.1} (min {:.1} / max {:.1}) | Avg Frame Time: {:.2} ms",
        avg_fps, app.stats.fps.min, app.stats.fps.max, avg_ft
    );
    println!(
        "Warnings: {} | Errors: {} | Status: {}",
        app.stats.warnings, app.stats.errors, app.status_text
    );
    println!("Reports saved in the Victory-PX state directory:");
    println!(
        "Lighthouse HTML: {}",
        html_path.file_name().unwrap_or_default().to_string_lossy()
    );
    println!(
        "Report Markdown: {}",
        md_path.file_name().unwrap_or_default().to_string_lossy()
    );
    if cancelled {
        return Err("Benchmark interrupted".into());
    }
    if failed {
        return Err("Benchmark child did not exit successfully".into());
    }
    Ok(())
}

fn open_in_os(
    path: &Path,
    label: &'static str,
    results: mpsc::Sender<String>,
) -> Result<(), String> {
    let path = std::path::absolute(path).map_err(|e| format!("Invalid path: {e}"))?;
    let mut opener = Command::new("xdg-open")
        .arg(path)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|e| format!("Could not open {label} with xdg-open: {e}"))?;
    std::thread::spawn(move || {
        let message = match opener.wait() {
            Ok(status) if status.success() => format!("Opened {label}"),
            Ok(status) => format!("Could not open {label}: xdg-open {status}"),
            Err(error) => format!("Could not open {label}: {error}"),
        };
        let _ = results.send(message);
    });
    Ok(())
}

fn open_folder_in_fm(path: &str, results: mpsc::Sender<String>) -> Result<(), String> {
    if path.is_empty() {
        return Err("no path set".into());
    }
    let p = Path::new(path);
    let target = if p.is_dir() {
        p
    } else {
        p.parent().unwrap_or(p)
    };
    open_in_os(target, "folder", results)
}

fn run_event_loop(
    terminal: &mut Terminal<CrosstermBackend<io::Stdout>>,
    app: &mut AppState,
    interrupted: &AtomicBool,
    can_preview: bool,
) -> Result<(), Box<dyn std::error::Error>> {
    let (open_tx, open_rx) = mpsc::channel();
    let mut child: Option<Session> = None;
    let mut output = None;
    let mut started_at: Option<Instant> = None;
    let mut finished_duration = None;
    let mut run_settings = None;
    let mut preview_until: Option<Instant> = None;
    let nvidia = app.host_info.has_nvidia_smi;
    let (metrics_tx, metrics_rx) = mpsc::sync_channel(1);
    std::thread::spawn(move || {
        while metrics_tx.send(sample_host_metrics(nvidia)).is_ok() {
            std::thread::sleep(Duration::from_secs(2));
        }
    });

    while !interrupted.load(Ordering::Relaxed) {
        for message in open_rx.try_iter() {
            app.notice = message;
        }
        if let Some(c) = child.as_mut() {
            if let Some(status) = c.try_wait()? {
                child = None;
                finished_duration = started_at.map(|t| t.elapsed());
                app.elapsed_secs = finished_duration.map_or(0, |d| d.as_secs());
                app.running = false;
                app.status_text = if status.success() {
                    "Finished"
                } else {
                    app.notice = format!("Session failed ({status})");
                    "Failed"
                }
                .into();
            }
        }

        if let Some(rx) = &output {
            for RunnerEvent::Line(line) in mpsc::Receiver::try_iter(rx).take(1024) {
                if app.show_error_popups && !app.advisory_shown_this_run {
                    if let Some(finding) = guard::classify_error_line(
                        &line,
                        app.series_s,
                        &app.host_metrics,
                        &app.wine_bin,
                    ) {
                        app.active_advisory = Some(finding);
                        app.advisory_shown_this_run = true;
                        app.modal = ModalKind::ErrorAdvisory;
                    }
                }
                let parsed = app.stats.record_for_monitor(line, can_preview);
                if let Some(fps) = app.stats.fps.last {
                    if parsed.is_metric {
                        app.push_fps(fps);
                    }
                }
                app.logs.push(parsed);
                if app.logs.len() > 400 {
                    app.logs.remove(0);
                }
            }
        }

        if let Ok(metrics) = metrics_rx.try_recv() {
            app.host_metrics = metrics;
            if let Some(util) = app.host_metrics.gpu_util {
                app.push_gpu(util);
            }
        }

        if app.running {
            if let Some(st) = started_at {
                app.elapsed_secs = st.elapsed().as_secs();
            }
        }
        app.anim_tick = app.anim_tick.wrapping_add(1);
        let show_paths = preview_until.is_some_and(|until| Instant::now() < until);
        let frame_area = terminal
            .draw(|f| draw(f, app, can_preview, show_paths))?
            .area;

        if event::poll(Duration::from_millis(80))? {
            match event::read()? {
                Event::FocusLost | Event::Resize(_, _) => preview_until = None,
                Event::Mouse(mouse) => {
                    preview_until = None;
                    if mouse.kind == MouseEventKind::Down(crossterm::event::MouseButton::Left) {
                        match ui::fork_hit(frame_area, app, mouse.column, mouse.row) {
                            Some(ui::ForkClick::Card(index)) => {
                                app.inventory_cursor = index;
                                equip_or_add(app);
                            }
                            Some(ui::ForkClick::Game) => {
                                app.browser.open(&app.game_exe);
                                app.modal = ModalKind::GameBrowser;
                            }
                            Some(ui::ForkClick::Display) => app.modal = ModalKind::Gamescope,
                            Some(ui::ForkClick::Console) if !app.running => {
                                app.series_s = !app.series_s
                            }
                            _ => {}
                        }
                    }
                }
                Event::Key(key) => {
                    if let Some(reveal) = preview_key(key) {
                        preview_until = (reveal
                            && can_preview
                            && app.tab == ActiveTab::Monitor
                            && app.modal == ModalKind::None
                            && !ui::compact(frame_area))
                        .then(|| Instant::now() + Duration::from_secs(2));
                        continue;
                    }
                    preview_until = None;
                    if key.kind != KeyEventKind::Press {
                        continue;
                    }
                    if is_quit(key, app.modal) {
                        if let Some(mut session) = child.take() {
                            session.stop()?;
                        }
                        break;
                    }
                    if app.running
                        && matches!(key.code, KeyCode::Char('s' | 'S'))
                        && !key.modifiers.intersects(
                            KeyModifiers::CONTROL | KeyModifiers::ALT | KeyModifiers::SUPER,
                        )
                    {
                        if let Some(mut session) = child.take() {
                            session.stop()?;
                        }
                        finished_duration = started_at.map(|t| t.elapsed());
                        app.elapsed_secs = finished_duration.map_or(0, |d| d.as_secs());
                        app.running = false;
                        app.status_text = "Stopped".into();
                        continue;
                    }
                    if ui::compact(frame_area)
                        && !matches!(key.code, KeyCode::Esc | KeyCode::Char('s' | 'S'))
                    {
                        continue;
                    }
                    if key
                        .modifiers
                        .intersects(KeyModifiers::CONTROL | KeyModifiers::ALT | KeyModifiers::SUPER)
                    {
                        continue;
                    }
                    if app.modal == ModalKind::ErrorAdvisory {
                        match key.code {
                            KeyCode::Esc | KeyCode::Enter => {
                                app.modal = ModalKind::None;
                            }
                            KeyCode::Char('d') | KeyCode::Char('D') => {
                                app.show_error_popups = false;
                                app.modal = ModalKind::None;
                                app.notice = "Error popups disabled (re-enable in [T] Theme & FX)".into();
                            }
                            _ => {}
                        }
                        continue;
                    }
                    if app.modal == ModalKind::ThemeFX {
                        match key.code {
                            KeyCode::Esc | KeyCode::Char('t') | KeyCode::Char('T') => {
                                app.modal = ModalKind::None;
                            }
                            KeyCode::Char('p') | KeyCode::Char('P') => {
                                app.palette = app.palette.next();
                                save_theme_prefs(&app);
                            }
                            KeyCode::Char('a') | KeyCode::Char('A') => {
                                app.anim_mode = app.anim_mode.next();
                                save_theme_prefs(&app);
                            }
                            KeyCode::Char('v') | KeyCode::Char('V') => {
                                app.anim_speed = (app.anim_speed + 1) % 3;
                                save_theme_prefs(&app);
                            }
                            KeyCode::Char('e') | KeyCode::Char('E') => {
                                app.show_error_popups = !app.show_error_popups;
                            }
                            _ => {}
                        }
                        continue;
                    }
                    if app.modal == ModalKind::Gamescope {
                        match key.code {
                            KeyCode::Esc | KeyCode::Char('g') | KeyCode::Char('G') => {
                                app.modal = ModalKind::None;
                            }
                            KeyCode::Char(' ') | KeyCode::Enter if !app.running => {
                                app.gamescope = !app.gamescope;
                            }
                            KeyCode::Char('p') | KeyCode::Char('P') if !app.running => {
                                app.display.cycle_preset();
                            }
                            KeyCode::Char('a') | KeyCode::Char('A') if !app.running => {
                                app.display.fsr = !app.display.fsr;
                            }
                            KeyCode::Char('f') | KeyCode::Char('F') if !app.running => {
                                app.display.fullscreen = !app.display.fullscreen;
                            }
                            KeyCode::Char('l') | KeyCode::Char('L') if !app.running => {
                                app.display.cycle_fps();
                            }
                            _ => {}
                        }
                        continue;
                    }
                    if matches!(
                        app.modal,
                        ModalKind::GameBrowser
                            | ModalKind::ForkWizardDir
                            | ModalKind::ForkWizardBin
                    ) {
                        match key.code {
                            KeyCode::Esc => {
                                app.modal = ModalKind::None;
                            }
                            KeyCode::Char('o') | KeyCode::Char('O')
                                if app.modal == ModalKind::GameBrowser =>
                            {
                                app.modal = ModalKind::None;
                            }
                            KeyCode::Up => {
                                app.browser.cursor = app.browser.cursor.saturating_sub(1);
                            }
                            KeyCode::Down => {
                                if !app.browser.entries.is_empty() {
                                    app.browser.cursor =
                                        (app.browser.cursor + 1).min(app.browser.entries.len() - 1);
                                }
                            }
                            KeyCode::Backspace if app.modal == ModalKind::ForkWizardBin => {
                                app.browser.load_dir(app.wizard_dir.clone().into());
                                app.modal = ModalKind::ForkWizardDir;
                            }
                            KeyCode::Backspace => {
                                if let Some(parent) =
                                    app.browser.cwd.parent().map(|p| p.to_path_buf())
                                {
                                    app.browser.load_dir(parent);
                                }
                            }
                            KeyCode::Char('h') | KeyCode::Char('H') => {
                                if let Ok(home) = std::env::var("HOME") {
                                    app.browser.load_dir(std::path::PathBuf::from(home));
                                }
                            }
                            KeyCode::Char('d') | KeyCode::Char('D') => {
                                if let Ok(home) = std::env::var("HOME") {
                                    let dl = std::path::PathBuf::from(home).join("Downloads");
                                    if dl.is_dir() {
                                        app.browser.load_dir(dl);
                                    }
                                }
                            }
                            KeyCode::Char('.') => {
                                if let Ok(cwd) = std::env::current_dir() {
                                    app.browser.load_dir(cwd);
                                }
                            }
                            KeyCode::Char(' ') if app.modal == ModalKind::ForkWizardDir => {
                                let dir = app.browser.cwd.clone();
                                app.wizard_dir = dir.to_string_lossy().into_owned();
                                let found = app.browser.scan_victory_exes(&dir);
                                if found == 1 {
                                    app.wizard_bin =
                                        app.browser.entries[0].path.to_string_lossy().into_owned();
                                    app.wizard_name = dir
                                        .file_name()
                                        .and_then(|s| s.to_str())
                                        .filter(|name| forks::valid_name(name))
                                        .unwrap_or("fork")
                                        .to_string();
                                    app.modal = ModalKind::ForkWizardName;
                                } else {
                                    if found == 0 {
                                        app.browser.load_dir(dir);
                                        app.notice = "No victory.exe found in this folder — enter the fork dir first".into();
                                    }
                                    app.modal = ModalKind::ForkWizardBin;
                                }
                            }
                            KeyCode::Enter => {
                                if let Some(entry) =
                                    app.browser.entries.get(app.browser.cursor).cloned()
                                {
                                    if entry.is_dir {
                                        app.browser.load_dir(entry.path);
                                    } else {
                                        let chosen = entry.path.to_string_lossy().into_owned();
                                        match app.modal {
                                            ModalKind::GameBrowser => {
                                                app.notice = match save_game_path(&chosen) {
                                                    Ok(()) => "Selected game executable".into(),
                                                    Err(error) => format!(
                                                        "Selected for this session only: {error}"
                                                    ),
                                                };
                                                app.game_exe = chosen;
                                                app.modal = ModalKind::None;
                                            }
                                            ModalKind::ForkWizardBin => {
                                                app.wizard_bin = chosen;
                                                app.wizard_name.clear();
                                                app.modal = ModalKind::ForkWizardName;
                                            }
                                            _ => {}
                                        }
                                    }
                                }
                            }
                            _ => {}
                        }
                        continue;
                    }
                    if app.modal == ModalKind::ForkWizardName {
                        match key.code {
                            KeyCode::Esc => {
                                app.modal = ModalKind::None;
                            }
                            KeyCode::Backspace => {
                                app.wizard_name.pop();
                            }
                            KeyCode::Char(ch)
                                if ch.is_ascii_alphanumeric() || matches!(ch, '.' | '_' | '-') =>
                            {
                                if app.wizard_name.len() < 40 {
                                    app.wizard_name.push(ch);
                                }
                            }
                            KeyCode::Enter => {
                                let name = app.wizard_name.trim().to_string();
                                if !name.is_empty() {
                                    match save_fork_build(
                                        &name,
                                        &app.wizard_dir,
                                        Some(&app.wizard_bin),
                                    ) {
                                        Ok(_) => {
                                            app.forks = load_fork_builds();
                                            if let Some(pos) =
                                                app.forks.iter().position(|f| f.name == name)
                                            {
                                                app.active_fork_idx = Some(pos);
                                                app.inventory_cursor = pos;
                                            }
                                            app.notice = format!("Saved fork {name}");
                                            app.modal = ModalKind::None;
                                        }
                                        Err(e) => {
                                            app.notice = e;
                                        }
                                    }
                                }
                            }
                            _ => {}
                        }
                        continue;
                    }
                    match key.code {
                        KeyCode::Tab
                        | KeyCode::Char('1')
                        | KeyCode::Char('2')
                        | KeyCode::Char('3') => {
                            app.tab = match key.code {
                                KeyCode::Char('1') => ActiveTab::Monitor,
                                KeyCode::Char('2') => ActiveTab::Display,
                                KeyCode::Char('3') => ActiveTab::Lighthouse,
                                _ => match app.tab {
                                    ActiveTab::Monitor => ActiveTab::Display,
                                    ActiveTab::Display => ActiveTab::Lighthouse,
                                    ActiveTab::Lighthouse => ActiveTab::Forks,
                                    ActiveTab::Forks => ActiveTab::Monitor,
                                },
                            };
                        }
                        KeyCode::Char('b') | KeyCode::Char('B') => {
                            app.tab = ActiveTab::Forks;
                        }
                        KeyCode::Esc if app.tab == ActiveTab::Forks => {
                            app.tab = ActiveTab::Monitor;
                        }
                        KeyCode::Left if app.tab == ActiveTab::Forks => {
                            app.inventory_cursor = app.inventory_cursor.saturating_sub(1);
                        }
                        KeyCode::Right if app.tab == ActiveTab::Forks => {
                            app.inventory_cursor = (app.inventory_cursor + 1).min(app.forks.len());
                        }
                        KeyCode::Up if app.tab == ActiveTab::Forks => {
                            app.inventory_cursor = app
                                .inventory_cursor
                                .saturating_sub(ui::fork_columns(frame_area));
                        }
                        KeyCode::Down if app.tab == ActiveTab::Forks => {
                            app.inventory_cursor = (app.inventory_cursor
                                + ui::fork_columns(frame_area))
                            .min(app.forks.len());
                        }
                        KeyCode::Enter if app.tab == ActiveTab::Forks => {
                            equip_or_add(app);
                        }
                        KeyCode::Char('n') | KeyCode::Char('N')
                            if app.tab == ActiveTab::Forks && !app.running =>
                        {
                            open_fork_wizard(app);
                        }
                        KeyCode::Char('x') | KeyCode::Char('X') if app.tab == ActiveTab::Forks => {
                            if let Some(b) = app.forks.get(app.inventory_cursor) {
                                match open_folder_in_fm(&b.directory, open_tx.clone()) {
                                    Ok(()) => app.notice = "Opening fork folder…".into(),
                                    Err(e) => app.notice = e,
                                }
                            }
                        }
                        KeyCode::Delete if app.tab == ActiveTab::Forks && !app.running => {
                            if let Some(b) = app.forks.get(app.inventory_cursor).cloned() {
                                if !b.name.is_empty() && delete_fork_build(&b.name).is_ok() {
                                    app.forks = load_fork_builds();
                                    app.active_fork_idx =
                                        if app.forks.is_empty() { None } else { Some(0) };
                                    app.inventory_cursor =
                                        app.inventory_cursor.min(app.forks.len());
                                    app.notice = format!("Removed fork {}", b.label());
                                }
                            }
                        }
                        KeyCode::Char('v') | KeyCode::Char('V') => {
                            match open_folder_in_fm(&app.game_exe, open_tx.clone()) {
                                Ok(()) => app.notice = "Opening game folder…".into(),
                                Err(e) => app.notice = e,
                            }
                        }
                        KeyCode::Char('c') | KeyCode::Char('C') if !app.running => {
                            app.series_s = !app.series_s;
                        }
                        KeyCode::Char('t') | KeyCode::Char('T') => {
                            app.modal = ModalKind::ThemeFX;
                        }
                        KeyCode::Char('g') | KeyCode::Char('G') => {
                            app.modal = ModalKind::Gamescope;
                        }
                        KeyCode::Char('o') | KeyCode::Char('O') => {
                            app.browser.open(&app.game_exe);
                            app.modal = ModalKind::GameBrowser;
                        }
                        KeyCode::Char('p') | KeyCode::Char('P')
                            if app.tab == ActiveTab::Display && !app.running =>
                        {
                            app.display.cycle_preset();
                        }
                        KeyCode::Char('a') | KeyCode::Char('A')
                            if app.tab == ActiveTab::Display && !app.running =>
                        {
                            app.display.fsr = !app.display.fsr;
                        }
                        KeyCode::Char('l') | KeyCode::Char('L')
                            if app.tab == ActiveTab::Display && !app.running =>
                        {
                            app.display.cycle_fps();
                        }
                        KeyCode::Enter if app.tab == ActiveTab::Display && !app.running => {
                            app.display.fullscreen = !app.display.fullscreen;
                        }
                        KeyCode::Char('f') | KeyCode::Char('F') => {
                            app.log_filter = app.log_filter.next();
                            app.log_scroll = 0;
                        }
                        KeyCode::Up if app.tab == ActiveTab::Monitor => {
                            app.log_scroll =
                                (app.log_scroll + 4).min(app.logs.len().saturating_sub(1));
                        }
                        KeyCode::Down if app.tab == ActiveTab::Monitor => {
                            app.log_scroll = app.log_scroll.saturating_sub(4);
                        }
                        KeyCode::Char('s') | KeyCode::Char('S') => {
                            if let Some(b) = app.active_fork().cloned() {
                                let (tx, rx) = mpsc::sync_channel(1024);
                                match spawn_victory(&b, app.launch_options(), tx) {
                                    Ok(c) => {
                                        output = Some(rx);
                                        app.stats = RunStats::default();
                                        app.logs.clear();
                                        app.fps_history.clear();
                                        app.advisory_shown_this_run = false;
                                        app.active_advisory = None;
                                        app.log_scroll = 0;
                                        app.elapsed_secs = 0;
                                        finished_duration = None;
                                        run_settings = Some((
                                            b.name,
                                            app.series_s,
                                            app.gamescope,
                                            app.display.clone(),
                                        ));
                                        child = Some(c);
                                        started_at = Some(Instant::now());
                                        app.running = true;
                                        app.status_text = "Running".into();
                                        app.notice.clear();
                                    }
                                    Err(e) => app.notice = e,
                                }
                            } else {
                                app.notice = "Select a fork first (press B).".into();
                            }
                        }
                        KeyCode::Char('r') | KeyCode::Char('R') => {
                            let current_fork =
                                app.active_fork().map(|b| b.name.as_str()).unwrap_or("");
                            let (fork_name, series_s, gamescope, display) =
                                run_settings.as_ref().map_or(
                                    (current_fork, app.series_s, app.gamescope, &app.display),
                                    |(name, series, scope, display)| {
                                        (name.as_str(), *series, *scope, display)
                                    },
                                );
                            let meta = CorpusMeta {
                                fork_name,
                                series_s,
                                gamescope,
                                display,
                                host_metrics: &app.host_metrics,
                                duration: finished_duration
                                    .or_else(|| started_at.map(|t| t.elapsed())),
                                state_label: &app.status_text,
                            };
                            let html = build_lighthouse_html(&app.stats, &meta);
                            match write_report_with_ext("lighthouse", "html", &html) {
                                Ok(p) => {
                                    app.notice = match open_in_os(
                                        &p,
                                        "Lighthouse report",
                                        open_tx.clone(),
                                    ) {
                                        Ok(()) => "Saved Lighthouse report; opening…".into(),
                                        Err(error) => format!("Saved Lighthouse report. {error}"),
                                    };
                                }
                                Err(e) => app.notice = format!("Lighthouse export failed: {e}"),
                            }
                        }
                        KeyCode::Char('w') | KeyCode::Char('W') => {
                            let current_fork =
                                app.active_fork().map(|b| b.name.as_str()).unwrap_or("");
                            let (fork_name, series_s, gamescope, display) =
                                run_settings.as_ref().map_or(
                                    (current_fork, app.series_s, app.gamescope, &app.display),
                                    |(name, series, scope, display)| {
                                        (name.as_str(), *series, *scope, display)
                                    },
                                );
                            let meta = CorpusMeta {
                                fork_name,
                                series_s,
                                gamescope,
                                display,
                                host_metrics: &app.host_metrics,
                                duration: finished_duration
                                    .or_else(|| started_at.map(|t| t.elapsed())),
                                state_label: &app.status_text,
                            };
                            let bundle = build_corpus_bundle(&app.stats, &meta);
                            match write_report_file("report", &bundle) {
                                Ok(p) => {
                                    app.notice = format!(
                                        "Saved {}",
                                        p.file_name().unwrap_or_default().to_string_lossy()
                                    )
                                }
                                Err(e) => app.notice = format!("Report export failed: {e}"),
                            }
                        }
                        _ => {}
                    }
                }
                _ => {}
            }
        }
    }
    Ok(())
}

fn preview_key(key: event::KeyEvent) -> Option<bool> {
    match (key.code, key.kind) {
        (KeyCode::Char('p' | 'P'), KeyEventKind::Release)
        | (
            KeyCode::Modifier(ModifierKeyCode::LeftControl | ModifierKeyCode::RightControl),
            KeyEventKind::Release,
        ) => Some(false),
        (KeyCode::Char('p' | 'P'), KeyEventKind::Press | KeyEventKind::Repeat)
            if key.modifiers == KeyModifiers::CONTROL =>
        {
            Some(true)
        }
        (KeyCode::Modifier(ModifierKeyCode::LeftControl | ModifierKeyCode::RightControl), _) => {
            None
        }
        _ => None,
    }
}

fn is_quit(key: event::KeyEvent, modal: ModalKind) -> bool {
    let modifiers = key.modifiers;
    (modifiers.contains(KeyModifiers::CONTROL)
        && !modifiers.intersects(KeyModifiers::ALT | KeyModifiers::SUPER)
        && matches!(key.code, KeyCode::Char('c' | 'C')))
        || (modal != ModalKind::ForkWizardName
            && !modifiers
                .intersects(KeyModifiers::CONTROL | KeyModifiers::ALT | KeyModifiers::SUPER)
            && matches!(key.code, KeyCode::Char('q' | 'Q')))
}

fn equip_or_add(app: &mut AppState) {
    if app.running {
        app.notice = "Stop the session (S) before switching or adding forks.".into();
    } else if app.inventory_cursor == app.forks.len() {
        open_fork_wizard(app);
    } else if app.inventory_cursor < app.forks.len() {
        app.active_fork_idx = Some(app.inventory_cursor);
        app.tab = ActiveTab::Monitor;
        app.notice = format!(
            "Equipped {}",
            app.active_fork().map(|b| b.label()).unwrap_or("")
        );
    }
}

fn open_fork_wizard(app: &mut AppState) {
    app.wizard_dir.clear();
    app.wizard_bin.clear();
    app.wizard_name.clear();
    app.browser.open("");
    app.modal = ModalKind::ForkWizardDir;
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quit_and_console_keys_are_distinct() {
        let key = |ch, modifiers| event::KeyEvent::new(KeyCode::Char(ch), modifiers);
        for modal in [
            ModalKind::None,
            ModalKind::Gamescope,
            ModalKind::GameBrowser,
            ModalKind::ForkWizardDir,
            ModalKind::ForkWizardBin,
            ModalKind::ForkWizardName,
            ModalKind::ThemeFX,
        ] {
            assert!(is_quit(key('c', KeyModifiers::CONTROL), modal));
            assert!(!is_quit(key('C', KeyModifiers::SHIFT), modal));
            assert!(!is_quit(
                key('c', KeyModifiers::CONTROL | KeyModifiers::ALT),
                modal
            ));
        }
        assert!(is_quit(key('q', KeyModifiers::NONE), ModalKind::Gamescope));
        assert!(!is_quit(
            key('q', KeyModifiers::NONE),
            ModalKind::ForkWizardName
        ));
    }
}


#[derive(serde::Serialize, serde::Deserialize)]
#[serde(default)]
struct ThemePrefs {
    palette: ThemePalette,
    anim_mode: AnimMode,
    anim_speed: u8,
}

impl Default for ThemePrefs {
    fn default() -> Self {
        Self {
            palette: ThemePalette::ViceCity,
            anim_mode: AnimMode::Static,
            anim_speed: 1,
        }
    }
}

fn theme_prefs_path() -> Option<std::path::PathBuf> {
    if let Ok(xdg) = std::env::var("XDG_CONFIG_HOME") {
        if !xdg.trim().is_empty() {
            return Some(std::path::PathBuf::from(xdg).join("victory-px").join("theme.json"));
        }
    }
    std::env::var_os("HOME").map(|h| {
        std::path::PathBuf::from(h)
            .join(".config")
            .join("victory-px")
            .join("theme.json")
    })
}

fn load_theme_prefs() -> ThemePrefs {
    let Some(path) = theme_prefs_path() else {
        return ThemePrefs::default();
    };
    let Ok(raw) = std::fs::read_to_string(path) else {
        return ThemePrefs::default();
    };
    serde_json::from_str(&raw).unwrap_or_default()
}

fn save_theme_prefs(app: &AppState) {
    let Some(path) = theme_prefs_path() else {
        return;
    };
    if let Some(parent) = path.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    let prefs = ThemePrefs {
        palette: app.palette,
        anim_mode: app.anim_mode,
        anim_speed: app.anim_speed,
    };
    if let Ok(json) = serde_json::to_string_pretty(&prefs) {
        let _ = std::fs::write(path, json);
    }
}
