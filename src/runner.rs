use crate::forks::{ForkBuild, NVIDIA_RT_FORK};
use std::env;
use std::fs;
use std::io::{self, BufRead, BufReader, Read};
use std::os::unix::{fs::PermissionsExt, process::CommandExt};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, ExitStatus, Stdio};
use std::sync::mpsc::SyncSender;
use std::thread;

#[derive(Debug, Clone)]
pub struct DisplayOptions {
    pub preset_idx: usize,
    pub game_w: u32,
    pub game_h: u32,
    pub win_w: u32,
    pub win_h: u32,
    pub fsr: bool,
    pub fullscreen: bool,
    pub fps_limit: u32,
}

impl Default for DisplayOptions {
    fn default() -> Self {
        Self {
            preset_idx: 0,
            game_w: 1280,
            game_h: 720,
            win_w: 1280,
            win_h: 720,
            fsr: false,
            fullscreen: false,
            fps_limit: 0,
        }
    }
}

impl DisplayOptions {
    pub fn preset_name(&self) -> &'static str {
        match self.preset_idx {
            1 => "upscale",
            2 => "1080p",
            _ => "small",
        }
    }

    pub fn cycle_preset(&mut self) {
        self.preset_idx = (self.preset_idx + 1) % 3;
        match self.preset_idx {
            0 => {
                self.game_w = 1280;
                self.game_h = 720;
                self.win_w = 1280;
                self.win_h = 720;
                self.fsr = false;
            }
            1 => {
                self.game_w = 1280;
                self.game_h = 720;
                self.win_w = 1920;
                self.win_h = 1080;
                self.fsr = true;
            }
            _ => {
                self.game_w = 1920;
                self.game_h = 1080;
                self.win_w = 1920;
                self.win_h = 1080;
                self.fsr = false;
            }
        }
    }

    pub fn cycle_fps(&mut self) {
        self.fps_limit = match self.fps_limit {
            0 => 30,
            30 => 60,
            _ => 0,
        };
    }
}

pub enum RunnerEvent {
    Line(String),
}

pub fn wine_path(linux_path: &str) -> String {
    let clean = linux_path.replace('/', "\\");
    if clean.starts_with('\\') {
        format!("Z:{clean}")
    } else {
        format!("Z:\\{clean}")
    }
}

pub fn find_in_path(bins: &[&str]) -> Option<String> {
    let path_var = std::env::var_os("PATH")?;
    for dir in std::env::split_paths(&path_var) {
        for bin in bins {
            let candidate = dir.join(bin);
            if executable(&candidate) {
                return std::path::absolute(candidate)
                    .ok()
                    .map(|p| p.to_string_lossy().into_owned());
            }
        }
    }
    None
}

fn executable(path: &Path) -> bool {
    path.metadata()
        .is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
}

pub struct Session {
    child: Child,
    stopped: bool,
}

impl Session {
    pub fn try_wait(&mut self) -> io::Result<Option<ExitStatus>> {
        // Observe the exit without reaping the leader, so its process group
        // cannot be reused before we stop any remaining descendants.
        let mut info = std::mem::MaybeUninit::<libc::siginfo_t>::zeroed();
        let result = unsafe {
            libc::waitid(
                libc::P_PID,
                self.child.id(),
                info.as_mut_ptr(),
                libc::WEXITED | libc::WNOHANG | libc::WNOWAIT,
            )
        };
        if result == -1 {
            return Err(io::Error::last_os_error());
        }
        if unsafe { info.assume_init().si_pid() } == 0 {
            Ok(None)
        } else {
            self.stop().map(Some)
        }
    }

    pub fn stop(&mut self) -> io::Result<ExitStatus> {
        if self.stopped {
            return self.child.wait();
        }
        // The child is the leader of a dedicated Linux process group.
        let result = unsafe { libc::kill(-(self.child.id() as libc::pid_t), libc::SIGKILL) };
        if result == -1 {
            let error = io::Error::last_os_error();
            if error.raw_os_error() != Some(libc::ESRCH) {
                return Err(error);
            }
        }
        self.stopped = true;
        self.child.wait()
    }
}

impl Drop for Session {
    fn drop(&mut self) {
        let _ = self.stop();
    }
}

pub struct LaunchOptions<'a> {
    pub game_exe: &'a str,
    pub wine_bin: &'a str,
    pub series_s: bool,
    pub gamescope: bool,
    pub display: &'a DisplayOptions,
}

pub fn find_proton_wine() -> Option<(String, String)> {
    let home = env::var("HOME").ok()?;
    let explicit_prefix = env::var_os("WINEPREFIX").is_some_and(|value| !value.is_empty());
    for base in [
        format!("{home}/.local/share/Steam/compatibilitytools.d"),
        format!("{home}/.steam/root/compatibilitytools.d"),
    ] {
        let Ok(entries) = std::fs::read_dir(&base) else {
            continue;
        };
        let mut candidates: Vec<std::path::PathBuf> = entries
            .flatten()
            .map(|e| e.path())
            .filter(|p| {
                p.file_name()
                    .and_then(|s| s.to_str())
                    .is_some_and(|n| n.starts_with("GE-Proton"))
                    && executable(&p.join("files/bin/wine"))
            })
            .collect();
        candidates.sort();
        for candidate in candidates.into_iter().rev() {
            let Ok(install) = candidate.canonicalize() else {
                continue;
            };
            let Some((build, label)) = proton_version(&install) else {
                continue;
            };
            if explicit_prefix || existing_proton_prefix(&install, &build).is_some() {
                let wine = install
                    .join("files/bin/wine")
                    .to_string_lossy()
                    .into_owned();
                return Some((wine, label));
            }
        }
    }
    None
}

fn proton_version(install: &Path) -> Option<(String, String)> {
    let version = fs::read_to_string(install.join("version")).ok()?;
    let mut fields = version.split_whitespace();
    let build = fields.next()?;
    let release = fields.next()?;
    (build.bytes().all(|b| b.is_ascii_digit()) && release.starts_with("GE-Proton"))
        .then(|| (build.to_owned(), release.to_owned()))
}

fn existing_proton_prefix(install: &Path, build: &str) -> Option<PathBuf> {
    let data_home = env::var_os("XDG_DATA_HOME")
        .map(PathBuf::from)
        .filter(|path| path.is_absolute())
        .or_else(|| env::var_os("HOME").map(|home| PathBuf::from(home).join(".local/share")))?;
    let name = install.file_name()?.to_str()?;
    let prefix = data_home.join(format!("victory-px/prefix-{name}-{build}/pfx"));
    (prefix.join("drive_c").is_dir()
        && prefix
            .join("dosdevices/z:")
            .canonicalize()
            .is_ok_and(|target| target == Path::new("/")))
    .then_some(prefix)
}

pub fn spawn_victory(
    fork: &ForkBuild,
    options: LaunchOptions<'_>,
    tx: SyncSender<RunnerEvent>,
) -> Result<Session, String> {
    let LaunchOptions {
        game_exe,
        wine_bin,
        series_s,
        gamescope,
        display,
    } = options;
    let victory_dir = fs::canonicalize(&fork.directory)
        .ok()
        .filter(|p| p.is_dir())
        .ok_or("VIctory working directory is missing")?;
    let victory_bin = fs::canonicalize(&fork.binary)
        .ok()
        .filter(|p| p.is_file())
        .ok_or("VIctory binary is missing; set --victory-bin")?;
    // Adjacent game files belong to the selected folder even if its exe is a symlink.
    let game_exe = std::path::absolute(game_exe)
        .ok()
        .filter(|p| p.is_file())
        .ok_or("select an existing game executable first")?;
    let wine = if wine_bin.is_empty() {
        None
    } else if wine_bin.contains('/') {
        fs::canonicalize(wine_bin)
            .ok()
            .filter(|p| executable(p))
            .map(|p| p.to_string_lossy().into_owned())
    } else {
        find_in_path(&[wine_bin])
    }
    .ok_or("Wine is missing or not executable; set --wine or install Wine")?;

    let proton = Path::new(&wine)
        .parent()
        .and_then(Path::parent)
        .and_then(Path::parent)
        .and_then(|install| proton_version(install).map(|(build, _)| (install, build)));
    let auto_prefix = match &proton {
        Some((install, build))
            if env::var_os("WINEPREFIX").is_none_or(|value| value.is_empty()) =>
        {
            Some(existing_proton_prefix(install, build).ok_or(
                "GE-Proton needs an initialized prefix; set WINEPREFIX to its pfx directory",
            )?)
        }
        _ => None,
    };
    let is_proton = proton.is_some();
    let is_nvidia_rt = fork.name == NVIDIA_RT_FORK;
    let console = if series_s { "series-s" } else { "series-x" };

    let mut args: Vec<String> = Vec::new();
    let program: String;

    if gamescope {
        let gs = find_in_path(&["gamescope"])
            .ok_or_else(|| "gamescope not found on PATH".to_string())?;
        program = gs;
        args.extend([
            "-w".into(),
            display.game_w.to_string(),
            "-h".into(),
            display.game_h.to_string(),
            "-W".into(),
            display.win_w.to_string(),
            "-H".into(),
            display.win_h.to_string(),
        ]);
        if display.fsr && !is_nvidia_rt {
            args.extend(["-F".into(), "fsr".into()]);
        }
        if display.fullscreen {
            args.push("-f".into());
        }
        if display.fps_limit > 0 {
            args.extend(["--framerate-limit".into(), display.fps_limit.to_string()]);
        }
        args.push("--".into());
        args.push(wine);
    } else {
        program = wine;
    }

    args.push(victory_bin.to_string_lossy().into_owned());
    let win_exe = wine_path(&game_exe.to_string_lossy());
    if is_nvidia_rt {
        args.push("play".into());
        if series_s {
            args.push("--series-s".into());
        }
        args.extend(nvidia_rt_launch_args());
        args.push(win_exe);
    } else {
        args.extend([
            "run".into(),
            win_exe,
            "--execute".into(),
            "--console".into(),
            console.into(),
        ]);
    }

    let work_dir = if is_nvidia_rt {
        game_exe.parent().unwrap_or(&victory_dir)
    } else {
        &victory_dir
    };

    let mut cmd = Command::new(&program);
    cmd.args(&args)
        .current_dir(work_dir)
        .process_group(0)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    if let Some(prefix) = auto_prefix {
        cmd.env("WINEPREFIX", prefix);
    }
    if is_proton && env::var_os("WINEDLLOVERRIDES").is_none() {
        cmd.env(
            "WINEDLLOVERRIDES",
            "d3d12,d3d12core,dxgi,d3d11,d3d10core,d3d9=n;d3dcompiler_47=n,b",
        );
    }
    let mut child = cmd.spawn().map_err(|e| {
        format!(
            "Could not start {}: {e}",
            if gamescope { "Gamescope" } else { "Wine" }
        )
    })?;

    if let Some(stdout) = child.stdout.take() {
        let tx = tx.clone();
        thread::spawn(move || read_output(stdout, tx));
    }
    if let Some(stderr) = child.stderr.take() {
        thread::spawn(move || read_output(stderr, tx));
    }

    Ok(Session {
        child,
        stopped: false,
    })
}

// Match the fast-path settings used by the original NVIDIA-RT launcher.
fn nvidia_rt_launch_args() -> impl Iterator<Item = String> {
    [
        "--res",
        "1440",
        "--dlss",
        "quality",
        "--fps",
        "1000",
        "--set",
        "VICTORY_HDR=0",
        "--set",
        "VICTORY_REFLEX=boost",
        "--set",
        "VICTORY_FULLSCREEN=1",
        "--set",
        "VICTORY_PIPELINE_SAMPLER_TABLE=1",
        "--set",
        "VICTORY_PIPELINE_WAIT_ONCE=1",
        "--set",
        "VICTORY_PIPELINE_WAIT_BACKLOG=64",
        "--set",
        "VICTORY_PIPELINE_SLOW_WORKERS=8",
        "--set",
        "VICTORY_TEXTURE_LOOK_ONCE=1",
        "--set",
        "VICTORY_TEXTURE_READ_AHEAD=1",
        "--set",
        "VICTORY_STAGING_NO_ZERO=1",
        "--set",
        "VICTORY_SEND_COPIED_FIND=1",
        "--set",
        "VICTORY_FILES_BUFFERED=1",
        "--set",
        "VICTORY_MEMORY_FRESH=1",
        "--set",
        "VICTORY_MEMORY_ARENAS_AHEAD=1",
        "--set",
        "VICTORY_MEMORY_QUIET=1",
        "--set",
        "VICTORY_MEMORY_LAND_SENT=1",
        "--set",
        "VICTORY_MEMORY_ASK_RUNS=1",
        "--set",
        "VICTORY_MEMORY_LEAN=1",
        "--set",
        "VICTORY_MEMORY_DIRTY=1",
        "--set",
        "VICTORY_MEMORY_BUFFER_FAULTS=1",
        "--set",
        "VICTORY_MEMORY_FAULT_PAGES=40000",
        "--set",
        "VICTORY_REFRESH_PROBE_US=300",
        "--set",
        "VICTORY_DECLARE_ACROSS_LISTS=1",
        "--set",
        "VICTORY_REFRESH_PROBE_TEXTURES=1",
        "--set",
        "VICTORY_REFRESH_SPLIT=1",
        "--set",
        "VICTORY_WAIT_SPLIT=1",
        "--set",
        "VICTORY_DSTORAGE_STATS=1",
        "--set",
        "VICTORY_PIPELINE_PIECES=1",
        "--set",
        "VICTORY_PIPELINE_PIECES_REFUSE_UNTRANSLATED=1",
        "--set",
        "VICTORY_PIPELINE_PIECES_ONLY=graphics",
        "--set",
        "VICTORY_REFRESH_SET=1",
        "--set",
        "VICTORY_TITLE_WRITE_RANGE=1",
        "--set",
        "VICTORY_UNDER_PARTS=1",
        "--set",
        "VICTORY_SEND_THREAD=1",
    ]
    .into_iter()
    .map(str::to_owned)
}

fn read_output(stream: impl Read, tx: SyncSender<RunnerEvent>) {
    const MAX_LINE: usize = 64 * 1024;
    let mut reader = BufReader::new(stream);
    let mut bytes = Vec::new();
    loop {
        bytes.clear();
        let Ok(count) = reader
            .by_ref()
            .take((MAX_LINE + 1) as u64)
            .read_until(b'\n', &mut bytes)
        else {
            break;
        };
        if count == 0 {
            break;
        }
        let line = if count > MAX_LINE {
            if !bytes.ends_with(b"\n") && reader.skip_until(b'\n').is_err() {
                break;
            }
            "[log line hidden: too long]".into()
        } else {
            let bytes = bytes.strip_suffix(b"\n").unwrap_or(&bytes);
            let bytes = bytes.strip_suffix(b"\r").unwrap_or(bytes);
            String::from_utf8_lossy(bytes).into_owned()
        };
        if tx.send(RunnerEvent::Line(line)).is_err() {
            break;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::mpsc;

    #[test]
    fn long_line_is_discarded_without_emitting_its_fragments() {
        let input = format!("{} path=/tmp/example\nnext line\n", "x".repeat(70 * 1024));
        let (tx, rx) = mpsc::sync_channel(2);
        read_output(input.as_bytes(), tx);
        let lines: Vec<_> = rx.into_iter().map(|RunnerEvent::Line(line)| line).collect();
        assert_eq!(lines, ["[log line hidden: too long]", "next line"]);
    }
}
