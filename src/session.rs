//! Per-user session store: live session file plus an archive catalog,
//! rooted at `~/.config/ax` (or `$XDG_CONFIG_HOME/ax`).
//! Live file stays byte-compatible with ax-go: `{root}/session.jsonl`.
//! Archived sessions live in `{root}/sessions/<unix_ms>.jsonl`.

use crate::Message;
use std::path::{Path, PathBuf};

pub struct SessionMeta {
    pub id: String,
    pub title: String,
    pub updated: i64,
    pub turns: usize,
    pub path: PathBuf,
}

fn store_dir(dir: &str) -> PathBuf {
    Path::new(dir).join("sessions")
}

pub fn live_path(dir: &str) -> PathBuf {
    Path::new(dir).join("session.jsonl")
}

fn now_ms() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

fn title_from_messages(msgs: &[Message]) -> String {
    for m in msgs {
        if m.role == "user" && !m.content.is_empty() {
            return first_words(&m.content, 8);
        }
    }
    "Untitled session".into()
}

fn first_words(s: &str, n: usize) -> String {
    let words: Vec<&str> = s.split_whitespace().take(n).collect();
    if words.is_empty() {
        return "Untitled session".into();
    }
    words.join(" ")
}

fn read_messages(path: &Path) -> Vec<Message> {
    let Ok(data) = std::fs::read_to_string(path) else {
        return Vec::new();
    };
    data.lines()
        .filter(|l| !l.trim().is_empty())
        .filter_map(|l| serde_json::from_str::<Message>(l).ok())
        .collect()
}

pub fn save_live(dir: &str, msgs: &[Message]) {
    if msgs.is_empty() {
        return;
    }
    let path = live_path(dir);
    let _ = std::fs::create_dir_all(path.parent().unwrap());
    let mut out = String::new();
    for m in msgs {
        if let Ok(line) = serde_json::to_string(m) {
            out.push_str(&line);
            out.push('\n');
        }
    }
    let _ = std::fs::write(path, out);
}

pub fn load_live(dir: &str) -> Vec<Message> {
    read_messages(&live_path(dir))
}

pub fn list_sessions(dir: &str) -> Vec<SessionMeta> {
    let store = store_dir(dir);
    let Ok(entries) = std::fs::read_dir(&store) else {
        return Vec::new();
    };
    let mut out = Vec::new();
    for e in entries.flatten() {
        let path = e.path();
        if path.extension().and_then(|x| x.to_str()) != Some("jsonl") {
            continue;
        }
        let id = path
            .file_stem()
            .and_then(|s| s.to_str())
            .unwrap_or("")
            .to_string();
        let msgs = read_messages(&path);
        let title = std::fs::read_to_string(title_path(dir, &id))
            .ok()
            .filter(|t| !t.trim().is_empty())
            .unwrap_or_else(|| title_from_messages(&msgs));
        let meta = std::fs::metadata(&path).ok();
        let updated = meta
            .and_then(|m| m.modified().ok())
            .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
            .map(|d| d.as_millis() as i64)
            .unwrap_or(0);
        let turns = msgs.iter().filter(|m| m.role == "user").count();
        out.push(SessionMeta {
            id,
            title,
            updated,
            turns,
            path,
        });
    }
    out.sort_by(|a, b| b.updated.cmp(&a.updated));
    out
}

pub fn load_session(path: &Path) -> Vec<Message> {
    read_messages(path)
}

pub fn archive_live(dir: &str) -> Option<String> {
    let msgs = load_live(dir);
    if msgs.is_empty() {
        return None;
    }
    let id = format!("{}", now_ms());
    let store = store_dir(dir);
    let _ = std::fs::create_dir_all(&store);
    let dest = store.join(format!("{id}.jsonl"));
    if std::fs::copy(live_path(dir), &dest).is_err() {
        return None;
    }
    let live_title_path = Path::new(dir).join("session.title");
    if let Ok(t) = std::fs::read_to_string(&live_title_path) {
        if !t.trim().is_empty() {
            let _ = std::fs::write(title_path(dir, &id), t.trim());
        }
        let _ = std::fs::remove_file(live_title_path);
    }
    let _ = std::fs::remove_file(live_path(dir));
    Some(id)
}

pub fn load_by_id(dir: &str, id: &str) -> Option<Vec<Message>> {
    let path = store_dir(dir).join(format!("{id}.jsonl"));
    if !path.exists() {
        return None;
    }
    let msgs = read_messages(&path);
    Some(msgs)
}

fn title_path(dir: &str, id: &str) -> PathBuf {
    store_dir(dir).join(format!("{id}.title"))
}

pub fn set_live_title(dir: &str, title: &str) {
    let path = Path::new(dir).join("session.title");
    let _ = std::fs::create_dir_all(path.parent().unwrap());
    let _ = std::fs::write(path, title);
}
