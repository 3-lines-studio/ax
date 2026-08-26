use crate::Tool;
use serde::Deserialize;
use serde_json::Value;
use std::io::Read;
use std::os::unix::process::CommandExt;
use std::sync::atomic::{AtomicI32, Ordering};

const MAX_OUTPUT: usize = 16 * 1024;
const MAX_DESCRIBE_OUTPUT: usize = 64 * 1024;
const MAX_CHILDREN: usize = 256;

static CHILD_PGIDS: [AtomicI32; MAX_CHILDREN] = [const { AtomicI32::new(0) }; MAX_CHILDREN];

pub fn defaults() -> Vec<Tool> {
    try_defaults().unwrap_or_else(|error| {
        eprintln!("ax: {error}");
        Vec::new()
    })
}

pub fn try_defaults() -> Result<Vec<Tool>, String> {
    match std::env::var("AX_TOOLS") {
        Ok(commands) => try_external_tools(&commands),
        Err(_) => Ok(Vec::new()),
    }
}

fn sanitize(value: &str) -> String {
    value
        .chars()
        .filter(|&character| {
            let code = character as u32;
            code == 0x09
                || code == 0x0a
                || code == 0x0d
                || !(code <= 0x1f || (0xfff9..=0xfffb).contains(&code))
        })
        .collect()
}

fn tail(value: &str) -> &str {
    if value.len() <= MAX_OUTPUT {
        return value;
    }
    let mut start = value.len() - MAX_OUTPUT;
    while !value.is_char_boundary(start) {
        start += 1;
    }
    match value[start..].find('\n') {
        Some(index) => &value[start + index + 1..],
        None => &value[start..],
    }
}

unsafe extern "C" fn signal_reap_children(signal: libc::c_int) {
    for group in &CHILD_PGIDS {
        let group = group.load(Ordering::Relaxed);
        if group != 0 {
            unsafe { libc::kill(-group, libc::SIGKILL) };
        }
    }
    unsafe { libc::_exit(128 + signal) }
}

fn ensure_signal_handler() {
    static INSTALLED: std::sync::Once = std::sync::Once::new();
    INSTALLED.call_once(|| unsafe {
        libc::signal(
            libc::SIGINT,
            signal_reap_children as *const () as libc::sighandler_t,
        );
        libc::signal(
            libc::SIGTERM,
            signal_reap_children as *const () as libc::sighandler_t,
        );
    });
}

struct PgidGuard(usize);

impl PgidGuard {
    fn register(group: i32) -> Result<Self, String> {
        for (index, slot) in CHILD_PGIDS.iter().enumerate() {
            if slot
                .compare_exchange(0, group, Ordering::Relaxed, Ordering::Relaxed)
                .is_ok()
            {
                return Ok(Self(index));
            }
        }
        Err("too many running tool providers".to_string())
    }
}

impl Drop for PgidGuard {
    fn drop(&mut self) {
        CHILD_PGIDS[self.0].store(0, Ordering::Relaxed);
    }
}

struct BoundedOutput {
    status: std::process::ExitStatus,
    stdout: Vec<u8>,
    stderr: Vec<u8>,
}

fn read_bounded(
    mut reader: impl Read,
    limit: usize,
    exceeded: std::sync::mpsc::Sender<()>,
) -> Result<Vec<u8>, String> {
    let mut output = Vec::with_capacity(limit.min(4096));
    let mut buffer = [0_u8; 4096];
    loop {
        let count = match reader.read(&mut buffer) {
            Ok(count) => count,
            Err(error) => {
                let _ = exceeded.send(());
                return Err(error.to_string());
            }
        };
        if count == 0 {
            return Ok(output);
        }
        if output.len() + count > limit {
            let _ = exceeded.send(());
            return Err(format!("output exceeds {limit} bytes"));
        }
        output.extend_from_slice(&buffer[..count]);
    }
}

fn bounded_output(
    command: &mut std::process::Command,
    limit: usize,
) -> Result<BoundedOutput, String> {
    use std::process::Stdio;

    ensure_signal_handler();
    let mut child = command
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .process_group(0)
        .spawn()
        .map_err(|error| error.to_string())?;
    let group = child.id() as i32;
    let guard = match PgidGuard::register(group) {
        Ok(guard) => guard,
        Err(error) => {
            unsafe { libc::kill(-group, libc::SIGKILL) };
            let _ = child.wait();
            return Err(error);
        }
    };
    let (tx, rx) = std::sync::mpsc::channel();
    let stdout = child.stdout.take().ok_or("cannot read provider stdout")?;
    let stderr = child.stderr.take().ok_or("cannot read provider stderr")?;
    let stdout_thread = std::thread::spawn({
        let tx = tx.clone();
        move || read_bounded(stdout, limit, tx)
    });
    let stderr_thread = std::thread::spawn(move || read_bounded(stderr, limit, tx));
    let status = loop {
        if rx.try_recv().is_ok() {
            unsafe { libc::kill(-group, libc::SIGKILL) };
            break child.wait().map_err(|error| error.to_string())?;
        }
        if let Some(status) = child.try_wait().map_err(|error| error.to_string())? {
            break status;
        }
        std::thread::sleep(std::time::Duration::from_millis(1));
    };
    drop(guard);
    let stdout = stdout_thread
        .join()
        .map_err(|_| "provider stdout reader panicked".to_string())??;
    let stderr = stderr_thread
        .join()
        .map_err(|_| "provider stderr reader panicked".to_string())??;
    Ok(BoundedOutput {
        status,
        stdout,
        stderr,
    })
}

#[derive(Deserialize)]
struct ExternalToolSpec {
    name: String,
    description: String,
    parameters: Value,
    #[serde(default)]
    snippet: String,
}

pub fn external_tools(commands: &str) -> Vec<Tool> {
    try_external_tools(commands).unwrap_or_else(|error| {
        eprintln!("ax: {error}");
        Vec::new()
    })
}

pub fn try_external_tools(commands: &str) -> Result<Vec<Tool>, String> {
    let mut tools = Vec::new();
    for command in commands.split_whitespace() {
        let output = bounded_output(
            std::process::Command::new(command).arg("describe"),
            MAX_DESCRIBE_OUTPUT,
        )
        .map_err(|error| format!("discover tools from {command}: {error}"))?;
        if !output.status.success() {
            let error = sanitize(&String::from_utf8_lossy(&output.stderr));
            let error = tail(&error).trim();
            if error.is_empty() {
                return Err(format!(
                    "discover tools from {command}: exited with {}",
                    status_str(output.status)
                ));
            }
            return Err(format!("discover tools from {command}: {error}"));
        }
        for line in String::from_utf8_lossy(&output.stdout)
            .lines()
            .filter(|line| !line.trim().is_empty())
        {
            let spec = serde_json::from_str::<ExternalToolSpec>(line)
                .map_err(|error| format!("invalid tool from {command}: {error}"))?;
            if !valid_tool_name(&spec.name) {
                return Err(format!("invalid tool name from {command}: {}", spec.name));
            }
            if spec.description.trim().is_empty() {
                return Err(format!(
                    "empty tool description from {command}: {}",
                    spec.name
                ));
            }
            if !spec.parameters.is_object() {
                return Err(format!(
                    "invalid tool parameters from {command}: {}",
                    spec.name
                ));
            }
            if tools.iter().any(|tool: &Tool| tool.name == spec.name) {
                return Err(format!("duplicate tool name: {}", spec.name));
            }
            let name: &'static str = Box::leak(spec.name.into_boxed_str());
            let description: &'static str = Box::leak(spec.description.into_boxed_str());
            let snippet: &'static str = Box::leak(spec.snippet.into_boxed_str());
            let executable = command.to_string();
            let mut tool = Tool {
                name,
                description,
                parameters: spec.parameters,
                snippet,
                sequential: false,
                run: Box::new(move |arguments, _progress| {
                    run_external_tool(&executable, name, arguments)
                }),
            };
            if tool.snippet.is_empty() {
                tool.snippet = description;
            }
            tools.push(tool);
        }
    }
    Ok(tools)
}

fn valid_tool_name(name: &str) -> bool {
    !name.is_empty()
        && name.len() <= 64
        && name
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || byte == b'_' || byte == b'-')
}

fn run_external_tool(command: &str, name: &str, arguments: &str) -> String {
    use std::io::Write;
    use std::process::Stdio;

    ensure_signal_handler();
    let mut child = match std::process::Command::new(command)
        .args(["run", name])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .process_group(0)
        .spawn()
    {
        Ok(child) => child,
        Err(error) => return format!("error: {error}"),
    };
    let group = child.id() as i32;
    let _guard = match PgidGuard::register(group) {
        Ok(guard) => guard,
        Err(error) => {
            unsafe { libc::kill(-group, libc::SIGKILL) };
            let _ = child.wait();
            return format!("error: {error}");
        }
    };
    if let Some(mut stdin) = child.stdin.take()
        && let Err(error) = stdin.write_all(arguments.as_bytes())
    {
        return format!("error: {error}");
    }
    let output = match child.wait_with_output() {
        Ok(output) => output,
        Err(error) => return format!("error: {error}"),
    };
    if !output.status.success() {
        let error = sanitize(&String::from_utf8_lossy(&output.stderr));
        if error.trim().is_empty() {
            return format!("error: {command} exited with {}", status_str(output.status));
        }
        return tail(&error).trim().to_string();
    }
    let result = sanitize(&String::from_utf8_lossy(&output.stdout));
    if result.trim().is_empty() {
        return format!("error: {command} returned no output");
    }
    tail(&result).to_string()
}

fn status_str(status: std::process::ExitStatus) -> String {
    use std::os::unix::process::ExitStatusExt;
    match status.code() {
        Some(code) => format!("exit status: {code}"),
        None => match status.signal() {
            Some(signal) => format!("signal: {signal}"),
            None => "unknown status".to_string(),
        },
    }
}

#[cfg(test)]
mod tests {
    use super::{PgidGuard, ensure_signal_handler, sanitize, tail, try_external_tools};
    use std::os::unix::fs::PermissionsExt;
    use std::os::unix::process::CommandExt;

    struct TestProvider {
        directory: std::path::PathBuf,
        command: std::path::PathBuf,
    }

    impl Drop for TestProvider {
        fn drop(&mut self) {
            std::fs::remove_dir_all(&self.directory).ok();
        }
    }

    fn provider(body: &str) -> TestProvider {
        static ID: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        let directory = std::env::temp_dir().join(format!(
            "ax-provider-{}-{}",
            std::process::id(),
            ID.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        ));
        std::fs::create_dir(&directory).unwrap();
        let command = directory.join("provider");
        std::fs::write(&command, body).unwrap();
        std::fs::set_permissions(&command, std::fs::Permissions::from_mode(0o755)).unwrap();
        TestProvider { directory, command }
    }

    #[test]
    fn sanitizes_and_truncates_output() {
        assert_eq!(sanitize("a\x00b\x1bc"), "abc");
        let value = "x".repeat(20_000);
        assert!(tail(&value).len() <= 16 * 1024);
    }

    #[test]
    fn accepts_provider_without_tools() {
        let provider = provider("#!/bin/sh\nexit 0\n");
        assert!(
            try_external_tools(provider.command.to_str().unwrap())
                .unwrap()
                .is_empty()
        );
    }

    #[test]
    fn runs_external_tool() {
        let provider = provider(
            "#!/bin/sh\nif [ \"$1\" = describe ]; then\n  echo '{\"name\":\"echo\",\"description\":\"Echo input\",\"parameters\":{\"type\":\"object\"}}'\nelse\n  cat\nfi\n",
        );
        let tools = try_external_tools(provider.command.to_str().unwrap()).unwrap();
        assert_eq!(tools.len(), 1);
        assert_eq!(
            (tools[0].run)("{\"value\":1}", &mut |_| {}),
            "{\"value\":1}"
        );
    }

    #[test]
    fn rejects_invalid_provider() {
        let provider = provider(
            "#!/bin/sh\necho '{\"name\":\"bad name\",\"description\":\"Bad\",\"parameters\":{}}'\n",
        );
        assert!(try_external_tools(provider.command.to_str().unwrap()).is_err());
    }

    #[test]
    fn rejects_large_description_output() {
        let provider = provider("#!/bin/sh\nyes x | head -c 70000\n");
        let error = match try_external_tools(provider.command.to_str().unwrap()) {
            Ok(_) => panic!("large output accepted"),
            Err(error) => error,
        };
        assert!(error.contains("output exceeds 65536 bytes"));
    }

    #[test]
    fn signal_helper() {
        let Ok(directory) = std::env::var("AX_SIGNAL_TEST") else {
            return;
        };
        let pid_path = std::path::Path::new(&directory).join("pid");
        let mut child = std::process::Command::new("sh")
            .args([
                "-c",
                &format!("sleep 30 & echo $! > '{}'; wait", pid_path.display()),
            ])
            .process_group(0)
            .spawn()
            .unwrap();
        let _guard = PgidGuard::register(child.id() as i32).unwrap();
        ensure_signal_handler();
        std::fs::write(std::path::Path::new(&directory).join("ready"), "").unwrap();
        let _ = child.wait();
    }

    #[test]
    fn signal_kills_provider_group() {
        let directory = std::env::temp_dir().join(format!("ax-signal-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&directory);
        std::fs::create_dir(&directory).unwrap();
        let mut helper = std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "tools::tests::signal_helper"])
            .env("AX_SIGNAL_TEST", &directory)
            .spawn()
            .unwrap();
        let ready = directory.join("ready");
        let pid_path = directory.join("pid");
        for _ in 0..5000 {
            if ready.exists() && pid_path.exists() {
                break;
            }
            std::thread::sleep(std::time::Duration::from_millis(1));
        }
        assert!(ready.exists());
        let provider_pid = std::fs::read_to_string(pid_path)
            .unwrap()
            .trim()
            .parse::<i32>()
            .unwrap();
        unsafe { libc::kill(helper.id() as i32, libc::SIGTERM) };
        let status = helper.wait().unwrap();
        assert_eq!(status.code(), Some(143));
        for _ in 0..5000 {
            if unsafe { libc::kill(provider_pid, 0) } == -1 {
                break;
            }
            std::thread::sleep(std::time::Duration::from_millis(1));
        }
        assert_eq!(unsafe { libc::kill(provider_pid, 0) }, -1);
        std::fs::remove_dir_all(directory).unwrap();
    }
}
