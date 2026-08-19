//! Built-in tools: bash, read, write, edit.

use crate::{Tool, new_tool};
use serde::Deserialize;

const MAX_OUTPUT: usize = 16 * 1024;

fn limit(s: &str) -> String {
    if s.len() <= MAX_OUTPUT {
        return s.to_string();
    }
    let mut start = s.len() - MAX_OUTPUT;
    while !s.is_char_boundary(start) {
        start += 1;
    }
    format!("[truncated]\n{}", &s[start..])
}

#[derive(Deserialize)]
struct BashArgs {
    command: String,
}

pub fn bash(dir: &str) -> Tool {
    let dir = dir.to_string();
    new_tool(
        "bash",
        "Run a bash command and return its combined output.",
        r#"{"type":"object","properties":{"command":{"type":"string","description":"bash command to run"}},"required":["command"]}"#,
        move |a: BashArgs| {
            let mut cmd = std::process::Command::new("bash");
            cmd.arg("-c").arg(&a.command);
            if !dir.is_empty() {
                cmd.current_dir(&dir);
            }
            match cmd.output() {
                Ok(o) => {
                    let mut s = String::from_utf8_lossy(&o.stdout).into_owned();
                    let err = String::from_utf8_lossy(&o.stderr);
                    if !err.is_empty() {
                        if !s.is_empty() && !s.ends_with('\n') {
                            s.push('\n');
                        }
                        s.push_str(&err);
                    }
                    if !o.status.success() {
                        if !s.is_empty() && !s.ends_with('\n') {
                            s.push('\n');
                        }
                        s.push_str(&format!("error: {}", status_str(o.status)));
                    }
                    limit(&s)
                }
                Err(e) => format!("error: {e}"),
            }
        },
    )
}

fn status_str(st: std::process::ExitStatus) -> String {
    match st.code() {
        Some(code) => format!("exit status {code}"),
        None => format!("signal: {st:?}"),
    }
}

#[derive(Deserialize)]
struct ReadArgs {
    path: String,
}

pub fn read() -> Tool {
    new_tool(
        "read",
        "Read a file and return its contents.",
        r#"{"type":"object","properties":{"path":{"type":"string","description":"file path"}},"required":["path"]}"#,
        |a: ReadArgs| match std::fs::read(&a.path) {
            Ok(b) => limit(&String::from_utf8_lossy(&b)),
            Err(e) => format!("error: {e}"),
        },
    )
}

#[derive(Deserialize)]
struct WriteArgs {
    path: String,
    content: String,
}

pub fn write() -> Tool {
    new_tool(
        "write",
        "Create or overwrite a file with the given content.",
        r#"{"type":"object","properties":{"path":{"type":"string","description":"file path"},"content":{"type":"string","description":"full file content"}},"required":["path","content"]}"#,
        |a: WriteArgs| match std::fs::write(&a.path, &a.content) {
            Ok(()) => format!("wrote {}", a.path),
            Err(e) => format!("error: {e}"),
        },
    )
}

#[derive(Deserialize)]
struct EditArgs {
    path: String,
    old: String,
    new: String,
}

pub fn edit() -> Tool {
    new_tool(
        "edit",
        "Replace exact text in a file. Old must match exactly once.",
        r#"{"type":"object","properties":{"path":{"type":"string","description":"file path"},"old":{"type":"string","description":"exact text to replace"},"new":{"type":"string","description":"replacement text"}},"required":["path","old","new"]}"#,
        |a: EditArgs| match std::fs::read_to_string(&a.path) {
            Err(e) => format!("error: {e}"),
            Ok(s) => {
                let n = s.matches(&a.old).count();
                if n == 0 {
                    return format!("error: old text not found in {}", a.path);
                }
                if n > 1 {
                    return format!(
                        "error: old text found {} times in {}, must be unique",
                        n, a.path
                    );
                }
                let out = s.replacen(&a.old, &a.new, 1);
                match std::fs::write(&a.path, out) {
                    Ok(()) => format!("edited {}", a.path),
                    Err(e) => format!("error: {e}"),
                }
            }
        },
    )
}
