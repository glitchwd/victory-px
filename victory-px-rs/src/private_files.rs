use std::fs::{self, DirBuilder, File, OpenOptions, Permissions};
use std::io::{self, Read, Write};
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};

static SEQUENCE: AtomicU64 = AtomicU64::new(0);

pub fn directory(path: &Path) -> io::Result<()> {
    DirBuilder::new().recursive(true).mode(0o700).create(path)?;
    let dir = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW)
        .open(path)?;
    dir.set_permissions(Permissions::from_mode(0o700))
}

pub fn unique_file(dir: &Path, prefix: &str, ext: &str) -> io::Result<(PathBuf, File)> {
    loop {
        let id = SEQUENCE.fetch_add(1, Ordering::Relaxed);
        let path = dir.join(format!("{prefix}-{}-{id}.{ext}", std::process::id()));
        match OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&path)
        {
            Ok(file) => return Ok((path, file)),
            Err(err) if err.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(err) => return Err(err),
        }
    }
}

pub fn write_config(path: &Path, bytes: &[u8]) -> io::Result<()> {
    let dir = path.parent().expect("config path has a parent");
    let (tmp, mut file) = unique_file(dir, ".config", "tmp")?;
    let result = file
        .write_all(bytes)
        .and_then(|()| file.sync_all())
        .and_then(|()| fs::rename(&tmp, path));
    if result.is_err() {
        let _ = fs::remove_file(tmp);
    }
    result
}

pub fn read_config(path: &Path) -> io::Result<String> {
    let mut file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)?;
    if !file.metadata()?.is_file() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "expected a config file",
        ));
    }
    file.set_permissions(Permissions::from_mode(0o600))?;
    let mut data = String::new();
    file.read_to_string(&mut data)?;
    Ok(data)
}
