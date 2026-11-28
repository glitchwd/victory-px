use crate::private_files;
use serde::{Deserialize, Serialize};
use std::env;
use std::fs;
use std::path::{Path, PathBuf};

pub const DIAG_BINARY: &str = "target/x86_64-pc-windows-msvc/diag/victory.exe";
pub const NVIDIA_FORK: &str = "nvidia";
pub const NVIDIA_RT_FORK: &str = "nvidia-rt";
pub const VILOCITY_FORK: &str = "vilocity";

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ForkBuild {
    #[serde(skip)]
    pub name: String,
    pub directory: String,
    pub binary: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ForkTier {
    Covert,
    Classified,
    Restricted,
    MilSpec,
}

impl ForkBuild {
    pub fn label(&self) -> &str {
        match self.name.as_str() {
            "" => "Command line",
            NVIDIA_FORK => "VIctory NVIDIA",
            NVIDIA_RT_FORK => "VIctory NVIDIA-RT",
            VILOCITY_FORK => "Vilocity",
            other => other,
        }
    }

    pub fn tier(&self) -> ForkTier {
        match self.name.as_str() {
            NVIDIA_RT_FORK => ForkTier::Covert,
            NVIDIA_FORK => ForkTier::Classified,
            VILOCITY_FORK => ForkTier::Restricted,
            _ => ForkTier::MilSpec,
        }
    }

    pub fn glyph(&self) -> &'static str {
        match self.name.as_str() {
            NVIDIA_RT_FORK => "╾━╤デ╦︻",
            NVIDIA_FORK => "╾━╤デ═",
            VILOCITY_FORK => "═╦══━",
            _ => "╠══━",
        }
    }
}

pub fn config_root() -> Option<PathBuf> {
    if let Ok(xdg) = env::var("XDG_CONFIG_HOME") {
        if Path::new(&xdg).is_absolute() {
            return Some(PathBuf::from(xdg).join("victory-px"));
        }
    }
    env::var("HOME")
        .ok()
        .map(|h| PathBuf::from(h).join(".config").join("victory-px"))
}

pub fn state_root() -> Option<PathBuf> {
    if let Ok(xdg) = env::var("XDG_STATE_HOME") {
        if Path::new(&xdg).is_absolute() {
            return Some(PathBuf::from(xdg).join("victory-px"));
        }
    }
    env::var("HOME").ok().map(|h| {
        PathBuf::from(h)
            .join(".local")
            .join("state")
            .join("victory-px")
    })
}

pub fn load_fork_builds() -> Vec<ForkBuild> {
    let Some(dir) = config_root().map(|r| r.join("forks")) else {
        return Vec::new();
    };
    let Ok(entries) = fs::read_dir(dir) else {
        return Vec::new();
    };
    let mut builds = Vec::new();
    for entry in entries.flatten() {
        let path = entry.path();
        if path.extension().and_then(|s| s.to_str()) != Some("json") {
            continue;
        }
        let Some(stem) = path.file_stem().and_then(|s| s.to_str()) else {
            continue;
        };
        if !valid_name(stem) {
            continue;
        }
        if let Ok(raw) = private_files::read_config(&path) {
            if let Ok(mut b) = serde_json::from_str::<ForkBuild>(&raw) {
                b.name = stem.to_string();
                builds.push(b);
            }
        }
    }
    builds.sort_by(|a, b| a.name.cmp(&b.name));
    builds
}

pub fn valid_name(name: &str) -> bool {
    (1..=40).contains(&name.len())
        && name.as_bytes()[0].is_ascii_alphanumeric()
        && name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'_' | b'-'))
}

pub fn save_fork_build(name: &str, dir: &str, bin: Option<&str>) -> Result<ForkBuild, String> {
    if !valid_name(name) {
        return Err("Use 1–40 letters, digits, dots, underscores or hyphens, starting with a letter or digit".into());
    }
    let abs_dir = fs::canonicalize(dir).map_err(|e| format!("--victory-dir: {e}"))?;
    if !abs_dir.is_dir() {
        return Err("--victory-dir must be a directory".into());
    }
    let bin_path = match bin {
        Some(b) if !b.is_empty() => PathBuf::from(b),
        _ => abs_dir.join(DIAG_BINARY),
    };
    let abs_bin = fs::canonicalize(&bin_path)
        .map_err(|e| format!("VIctory binary: {e}; build VIctory or set --victory-bin"))?;
    if !abs_bin.is_file() {
        return Err("VIctory binary must be a file".into());
    }
    let build = ForkBuild {
        name: name.to_string(),
        directory: abs_dir.to_string_lossy().into_owned(),
        binary: abs_bin.to_string_lossy().into_owned(),
    };
    let forks_dir = config_root()
        .ok_or_else(|| "cannot resolve config directory".to_string())?
        .join("forks");
    private_files::directory(forks_dir.parent().unwrap()).map_err(|e| e.to_string())?;
    private_files::directory(&forks_dir).map_err(|e| e.to_string())?;
    let json = serde_json::to_string_pretty(&build).map_err(|e| e.to_string())?;
    private_files::write_config(&forks_dir.join(format!("{name}.json")), json.as_bytes())
        .map_err(|e| e.to_string())?;
    Ok(build)
}

pub fn delete_fork_build(name: &str) -> Result<(), String> {
    if !valid_name(name) {
        return Err("invalid fork name".into());
    }
    let path = config_root()
        .ok_or_else(|| "cannot resolve config directory".to_string())?
        .join("forks")
        .join(format!("{name}.json"));
    if path.exists() {
        fs::remove_file(path).map_err(|e| e.to_string())?;
    }
    Ok(())
}

pub fn load_game_path() -> Option<String> {
    let path = config_root()?.join("game.json");
    let raw = private_files::read_config(&path).ok()?;
    serde_json::from_str::<String>(&raw).ok()
}

pub fn save_game_path(game: &str) -> Result<(), String> {
    let root = config_root().ok_or("cannot resolve config directory")?;
    private_files::directory(&root).map_err(|e| e.to_string())?;
    let json = serde_json::to_string(game).map_err(|e| e.to_string())?;
    private_files::write_config(&root.join("game.json"), json.as_bytes()).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fork_names_are_single_portable_components() {
        for name in ["nvidia-rt", "Build_1.2", "1"] {
            assert!(valid_name(name));
        }
        for name in ["", ".", "..", "folder/profile", "two words"] {
            assert!(!valid_name(name));
        }
        assert!(!valid_name(&"a".repeat(41)));
    }
}
