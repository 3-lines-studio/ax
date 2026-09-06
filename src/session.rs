//! Explicit session file store. A session is one JSONL file: a message entry
//! per line, plus a usage entry recording the last request's context size.
//!
//! Lines are append-only. Older files with bare messages (no `type` tag) still
//! load.

use crate::Message;
use serde::{Deserialize, Serialize};
use std::path::Path;

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum Entry {
    Message {
        message: Message,
    },
    Usage {
        input: usize,
        output: usize,
        cached_input: usize,
        context_input: usize,
        #[serde(default)]
        context_output: usize,
        #[serde(default)]
        window: Option<usize>,
        #[serde(default)]
        model: String,
    },
}

/// Project entries into the LLM context. Usage entries carry no conversation
/// content and are skipped.
pub fn context_messages(entries: &[Entry]) -> Vec<Message> {
    entries
        .iter()
        .filter_map(|entry| match entry {
            Entry::Message { message } => Some(message.clone()),
            Entry::Usage { .. } => None,
        })
        .collect()
}

fn parse_entry_line(line: &str) -> Option<Entry> {
    if let Ok(e) = serde_json::from_str::<Entry>(line) {
        return Some(e);
    }
    serde_json::from_str::<Message>(line)
        .ok()
        .map(|message| Entry::Message { message })
}

pub fn load_path(path: &Path) -> Result<Vec<Entry>, String> {
    let data = match std::fs::read_to_string(path) {
        Ok(data) => data,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(format!("read session {}: {e}", path.display())),
    };
    data.lines()
        .filter(|line| !line.trim().is_empty())
        .map(|line| {
            parse_entry_line(line).ok_or_else(|| format!("invalid session {}", path.display()))
        })
        .collect()
}

pub fn append_usage(
    path: &Path,
    input: usize,
    output: usize,
    cached_input: usize,
    window: Option<usize>,
    model: &str,
) -> Result<(), String> {
    use std::io::Write;
    let entry = Entry::Usage {
        input,
        output,
        cached_input,
        context_input: input,
        context_output: output,
        window,
        model: model.to_string(),
    };
    let line = serde_json::to_string(&entry)
        .map_err(|e| format!("encode session {}: {e}", path.display()))?;
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)
            .map_err(|e| format!("create session dir {}: {e}", path.display()))?;
    }
    let mut file = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
        .map_err(|e| format!("open session {}: {e}", path.display()))?;
    file.write_all(format!("{line}\n").as_bytes())
        .map_err(|e| format!("write session {}: {e}", path.display()))
}

pub fn append_messages(path: &Path, messages: &[Message]) -> Result<(), String> {
    let mut entries = load_path(path)?;
    entries.extend(
        messages
            .iter()
            .cloned()
            .map(|message| Entry::Message { message }),
    );
    let mut out = String::new();
    for entry in entries {
        let line = serde_json::to_string(&entry)
            .map_err(|e| format!("encode session {}: {e}", path.display()))?;
        out.push_str(&line);
        out.push('\n');
    }
    crate::atomic_write(path, out.as_bytes())
        .map_err(|e| format!("write session {}: {e}", path.display()))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn message(role: &str, content: &str) -> Message {
        Message {
            role: role.into(),
            content: content.into(),
            tool_calls: Vec::new(),
            tool_call_id: String::new(),
        }
    }

    fn usage(context_input: usize, context_output: usize) -> Entry {
        Entry::Usage {
            input: context_input,
            output: context_output,
            cached_input: 0,
            context_input,
            context_output,
            window: None,
            model: String::new(),
        }
    }

    #[test]
    fn context_skips_usage_and_keeps_messages() {
        let entries = vec![
            Entry::Message {
                message: message("user", "hello"),
            },
            usage(100, 5),
            Entry::Message {
                message: message("assistant", "hi"),
            },
        ];
        let msgs = context_messages(&entries);
        assert_eq!(msgs.len(), 2);
        assert_eq!(msgs[0].content, "hello");
        assert_eq!(msgs[1].content, "hi");
    }

    #[test]
    fn append_roundtrip() {
        let dir = std::env::temp_dir().join(format!("ax-session-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("session.jsonl");
        append_messages(&path, &[message("user", "go")]).unwrap();
        append_messages(
            &path,
            &[message("assistant", "ok"), message("user", "more")],
        )
        .unwrap();
        append_usage(&path, 10, 5, 0, None, "m").unwrap();
        let entries = load_path(&path).unwrap();
        let msgs = context_messages(&entries);
        assert_eq!(msgs.len(), 3);
        assert_eq!(msgs[0].content, "go");
        assert_eq!(msgs[2].content, "more");
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn old_bare_message_lines_load() {
        let dir = std::env::temp_dir().join(format!("ax-oldmsg-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("session.jsonl");
        std::fs::write(&path, "{\"Role\":\"user\",\"Content\":\"hi\"}\n").unwrap();
        let entries = load_path(&path).unwrap();
        assert_eq!(entries.len(), 1);
        assert_eq!(context_messages(&entries)[0].content, "hi");
        std::fs::remove_dir_all(&dir).ok();
    }
}
