use std::fs;
use std::io::Read;
use std::path::Path;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

#[derive(Debug, Clone, Default)]
pub struct HostInfo {
    pub os_name: String,
    pub kernel: String,
    pub cpu: String,
    pub gpu: String,
    pub driver: String,
    pub vram: String,
    pub has_nvidia_smi: bool,
}

#[derive(Debug, Clone, Default)]
pub struct HostMetrics {
    pub ram_used_mib: u64,
    pub ram_total_mib: u64,
    pub gpu_util: Option<u32>,
    pub gpu_temp: Option<u32>,
    pub vram_used_mib: Option<u64>,
    pub vram_total_mib: Option<u64>,
    pub gpu_vendor: &'static str,
}

pub fn probe_host_info() -> HostInfo {
    let mut info = HostInfo {
        os_name: "Linux".into(),
        ..Default::default()
    };

    if let Ok(os_rel) = fs::read_to_string("/etc/os-release") {
        for line in os_rel.lines() {
            if let Some(val) = line.strip_prefix("PRETTY_NAME=") {
                info.os_name = val.trim_matches('"').to_string();
                break;
            }
        }
    }
    if let Ok(kern) = fs::read_to_string("/proc/sys/kernel/osrelease") {
        info.kernel = kern.trim().to_string();
    }
    if let Ok(cpuinfo) = fs::read_to_string("/proc/cpuinfo") {
        for line in cpuinfo.lines() {
            if let Some((k, v)) = line.split_once(':') {
                if k.trim() == "model name" {
                    info.cpu = v.trim().to_string();
                    break;
                }
            }
        }
    }

    if let Some(text) = nvidia_query("name,driver_version,memory.total") {
        if let Some([name, driver, memory]) = fields::<3>(&text) {
            if !name.is_empty() && !name.starts_with('[') {
                info.has_nvidia_smi = true;
                info.gpu = name.into();
                info.driver = driver.into();
                if let Ok(mb) = memory.parse::<u64>() {
                    info.vram = format!("{mb} MiB");
                }
            }
        }
    }

    if info.gpu.is_empty() {
        if let Some((gpu, vram)) = probe_amd_static() {
            info.gpu = gpu;
            info.vram = vram;
        }
    }

    info
}

fn probe_amd_static() -> Option<(String, String)> {
    let drm = Path::new("/sys/class/drm");
    let entries = fs::read_dir(drm).ok()?;
    for entry in entries.flatten() {
        let dev = entry.path().join("device");
        let Ok(vendor) = fs::read_to_string(dev.join("vendor")) else {
            continue;
        };
        if vendor.trim() == "0x1002" {
            let total = fs::read_to_string(dev.join("mem_info_vram_total"))
                .ok()
                .and_then(|s| s.trim().parse::<u64>().ok())
                .map(|b| format!("{} MiB", b / (1024 * 1024)))
                .unwrap_or_default();
            return Some(("AMD Radeon GPU".into(), total));
        }
    }
    None
}

pub fn sample_host_metrics(has_nvidia_smi: bool) -> HostMetrics {
    let mut m = HostMetrics::default();
    if let Ok(mem) = fs::read_to_string("/proc/meminfo") {
        let mut total = None;
        let mut avail = None;
        for line in mem.lines() {
            let fields: Vec<&str> = line.split_whitespace().collect();
            if fields.len() == 3 && fields[2] == "kB" {
                if fields[0] == "MemTotal:" {
                    total = fields[1].parse::<u64>().ok();
                } else if fields[0] == "MemAvailable:" {
                    avail = fields[1].parse::<u64>().ok();
                }
            }
        }
        if let (Some(t), Some(a)) = (total, avail) {
            if t >= a {
                m.ram_total_mib = t / 1024;
                m.ram_used_mib = (t - a) / 1024;
            }
        }
    }

    if has_nvidia_smi {
        if let Some(text) = nvidia_query("utilization.gpu,temperature.gpu,memory.used,memory.total")
        {
            if let Some([util, temp, used, total]) = fields::<4>(&text) {
                m.gpu_util = util.parse().ok();
                m.gpu_temp = temp.parse().ok();
                m.vram_used_mib = used.parse().ok();
                m.vram_total_mib = total.parse().ok();
                if m.gpu_util.is_some() || m.gpu_temp.is_some() || m.vram_total_mib.is_some() {
                    m.gpu_vendor = "NVIDIA";
                }
            }
        }
    }
    if m.gpu_vendor.is_empty() {
        sample_amd_metrics(&mut m);
    }
    m
}

fn fields<const N: usize>(text: &str) -> Option<[&str; N]> {
    text.lines()
        .next()?
        .split(',')
        .map(str::trim)
        .collect::<Vec<_>>()
        .try_into()
        .ok()
}

fn nvidia_query(query: &str) -> Option<String> {
    let mut child = Command::new("nvidia-smi")
        .args([
            "--id=0",
            &format!("--query-gpu={query}"),
            "--format=csv,noheader,nounits",
        ])
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .ok()?;
    let deadline = Instant::now() + Duration::from_millis(3000);
    loop {
        match child.try_wait() {
            Ok(Some(status)) if status.success() => {
                let stdout = child.stdout.take()?;
                let mut text = String::new();
                stdout.take(4096).read_to_string(&mut text).ok()?;
                return Some(text);
            }
            Ok(Some(_)) => return None,
            Ok(None) if Instant::now() < deadline => std::thread::sleep(Duration::from_millis(15)),
            _ => {
                let _ = child.kill();
                let _ = child.wait();
                return None;
            }
        }
    }
}

fn sample_amd_metrics(m: &mut HostMetrics) {
    let Ok(entries) = fs::read_dir("/sys/class/drm") else {
        return;
    };
    for entry in entries.flatten() {
        let dev = entry.path().join("device");
        let Ok(vendor) = fs::read_to_string(dev.join("vendor")) else {
            continue;
        };
        if vendor.trim() != "0x1002" {
            continue;
        }
        m.gpu_util = fs::read_to_string(dev.join("gpu_busy_percent"))
            .ok()
            .and_then(|s| s.trim().parse().ok());
        m.vram_used_mib = fs::read_to_string(dev.join("mem_info_vram_used"))
            .ok()
            .and_then(|s| s.trim().parse::<u64>().ok())
            .map(|b| b / (1024 * 1024));
        m.vram_total_mib = fs::read_to_string(dev.join("mem_info_vram_total"))
            .ok()
            .and_then(|s| s.trim().parse::<u64>().ok())
            .map(|b| b / (1024 * 1024));
        if let Ok(hwmons) = fs::read_dir(dev.join("hwmon")) {
            for hw in hwmons.flatten() {
                if let Ok(temp_mc) = fs::read_to_string(hw.path().join("temp1_input")) {
                    if let Ok(mc) = temp_mc.trim().parse::<u32>() {
                        m.gpu_temp = Some(mc / 1000);
                        break;
                    }
                }
            }
        }
        if m.gpu_util.is_some() || m.gpu_temp.is_some() || m.vram_total_mib.is_some() {
            m.gpu_vendor = "AMD";
            break;
        }
    }
}
