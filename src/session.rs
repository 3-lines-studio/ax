//! Per-user session store: live session file plus an archive catalog,
//! rooted at `~/.config/ax` (or `$XDG_CONFIG_HOME/ax`).
//! Live transcript: `{root}/session.jsonl`.
//! Archived sessions live in `{root}/sessions/<unix_ms>.jsonl`.
//!
//! Lines are append-only entries: a message, or a compaction summary with the
//! recent messages it retains. Older files with bare messages still load.

use crate::{Message, Provider, Request};
use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum Entry {
    Message {
        message: Message,
    },
    Compaction {
        summary: String,
        tokens_before: usize,
        timestamp: i64,
        retained: Vec<Message>,
    },
}

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

pub fn now_ms() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

pub const COMPACTION_PREFIX: &str = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n";
pub const COMPACTION_SUFFIX: &str = "\n</summary>";

/// Project entries into the LLM context: a compaction entry becomes a summary
/// message followed by its retained recent messages.
pub fn context_messages(entries: &[Entry]) -> Vec<Message> {
    let mut out = Vec::new();
    for e in entries {
        match e {
            Entry::Message { message } => out.push(message.clone()),
            Entry::Compaction {
                summary, retained, ..
            } => {
                out.push(Message {
                    role: "user".into(),
                    content: format!("{COMPACTION_PREFIX}{summary}{COMPACTION_SUFFIX}"),
                    tool_calls: Vec::new(),
                    tool_call_id: String::new(),
                });
                out.extend(retained.iter().cloned());
            }
        }
    }
    out
}

fn parse_entry_line(line: &str) -> Option<Entry> {
    if let Ok(e) = serde_json::from_str::<Entry>(line) {
        return Some(e);
    }
    serde_json::from_str::<Message>(line)
        .ok()
        .map(|message| Entry::Message { message })
}

fn read_entries(path: &Path) -> Vec<Entry> {
    let Ok(data) = std::fs::read_to_string(path) else {
        return Vec::new();
    };
    data.lines()
        .filter(|l| !l.trim().is_empty())
        .filter_map(parse_entry_line)
        .collect()
}

pub fn save_live(dir: &str, entries: &[Entry]) {
    if entries.is_empty() {
        return;
    }
    let path = live_path(dir);
    let _ = std::fs::create_dir_all(path.parent().unwrap());
    let mut out = String::new();
    for e in entries {
        if let Ok(line) = serde_json::to_string(e) {
            out.push_str(&line);
            out.push('\n');
        }
    }
    let _ = std::fs::write(path, out);
}

pub fn load_live(dir: &str) -> Vec<Entry> {
    read_entries(&live_path(dir))
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
        let entries = read_entries(&path);
        let title = std::fs::read_to_string(title_path(dir, &id))
            .ok()
            .filter(|t| !t.trim().is_empty())
            .unwrap_or_else(|| title_from_entries(&entries));
        let meta = std::fs::metadata(&path).ok();
        let updated = meta
            .and_then(|m| m.modified().ok())
            .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
            .map(|d| d.as_millis() as i64)
            .unwrap_or(0);
        let turns = context_messages(&entries)
            .iter()
            .filter(|m| m.role == "user")
            .count();
        out.push(SessionMeta {
            id,
            title,
            updated,
            turns,
            path,
        });
    }
    out.sort_by_key(|m| std::cmp::Reverse(m.updated));
    out
}

fn title_from_entries(entries: &[Entry]) -> String {
    for m in context_messages(entries) {
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

pub fn load_session(path: &Path) -> Vec<Entry> {
    read_entries(path)
}

pub fn archive_live(dir: &str) -> Option<String> {
    if read_entries(&live_path(dir)).is_empty() {
        return None;
    }
    let store = store_dir(dir);
    let _ = std::fs::create_dir_all(&store);
    let base = format!("{}", now_ms());
    let mut id = base.clone();
    let mut dest = store.join(format!("{id}.jsonl"));
    let mut n = 1;
    while dest.exists() {
        id = format!("{base}-{n}");
        dest = store.join(format!("{id}.jsonl"));
        n += 1;
    }
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

pub fn load_by_id(dir: &str, id: &str) -> Option<Vec<Entry>> {
    if id.is_empty() || id == "." || id == ".." || id.contains('/') || id.contains('\\') {
        return None;
    }
    let path = store_dir(dir).join(format!("{id}.jsonl"));
    if !path.exists() {
        return None;
    }
    Some(read_entries(&path))
}

fn title_path(dir: &str, id: &str) -> PathBuf {
    store_dir(dir).join(format!("{id}.title"))
}

pub fn set_live_title(dir: &str, title: &str) {
    let path = Path::new(dir).join("session.title");
    let _ = std::fs::create_dir_all(path.parent().unwrap());
    let _ = std::fs::write(path, title);
}

/// Rough token estimate for context budgeting: chars/4.
pub fn estimate_tokens(msgs: &[Message]) -> usize {
    let mut chars = 0usize;
    for m in msgs {
        chars += m.content.len();
        for c in &m.tool_calls {
            chars += c.name.len() + c.arguments.len();
        }
    }
    chars / 4
}

/// Drop trailing tool-result messages. A request whose last message is a
/// tool result (no following assistant response) is rejected by providers;
/// this happens after a run failed between turns or a session was resumed
/// mid-batch.
pub fn trim_trailing_tool_messages(msgs: &mut Vec<Message>) {
    while msgs.last().map(|m| m.role == "tool").unwrap_or(false) {
        msgs.pop();
    }
}

/// Recent messages to keep verbatim after compaction (approximate 20k tokens).
const RETAIN_TOKENS: usize = 20_000;

fn split_retained(msgs: &[Message]) -> (Vec<Message>, Vec<Message>) {
    let mut kept = Vec::new();
    let mut tokens = 0usize;
    for m in msgs.iter().rev() {
        let t = estimate_tokens(std::slice::from_ref(m));
        if tokens + t > RETAIN_TOKENS && !kept.is_empty() {
            break;
        }
        kept.push(m.clone());
        tokens += t;
    }
    kept.reverse();
    let summarize_len = msgs.len() - kept.len();
    (kept, msgs[..summarize_len].to_vec())
}

fn serialize_conversation(msgs: &[Message]) -> String {
    let mut parts = Vec::new();
    for m in msgs {
        match m.role.as_str() {
            "user" => parts.push(format!("[User]: {}", m.content)),
            "assistant" => {
                if !m.content.is_empty() {
                    parts.push(format!("[Assistant]: {}", m.content));
                }
                for c in &m.tool_calls {
                    parts.push(format!("[Tool call]: {}({})", c.name, c.arguments));
                }
            }
            "tool" => {
                let content = if m.content.len() > 2000 {
                    format!("{}… [truncated]", &m.content[..2000])
                } else {
                    m.content.clone()
                };
                parts.push(format!("[Tool result for {}]: {}", m.tool_call_id, content));
            }
            _ => {}
        }
    }
    parts.join("\n\n")
}

const SUMMARY_SYSTEM: &str = "You are a context summarization assistant. Read the conversation and produce a structured summary so another LLM can continue the work. Do NOT continue the conversation. Do NOT respond to questions in it. ONLY output the summary.";

const SUMMARY_PROMPT: &str = "Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or \"(none)\" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or \"(none)\" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.";

/// Summarize the conversation in `entries`, returning the summary text, the
/// estimated tokens before compaction, and the retained recent messages.
pub fn compact(
    provider: &impl Provider,
    model: &str,
    entries: &[Entry],
) -> Result<(String, usize, Vec<Message>), String> {
    let msgs = context_messages(entries);
    let tokens_before = estimate_tokens(&msgs);
    let (retained, to_summarize) = split_retained(&msgs);
    if to_summarize.is_empty() {
        return Err("nothing to summarize".into());
    }
    let conversation = serialize_conversation(&to_summarize);
    let prompt = format!("<conversation>\n{conversation}\n</conversation>\n\n{SUMMARY_PROMPT}");
    let req = Request {
        model,
        system: SUMMARY_SYSTEM,
        messages: &[Message {
            role: "user".into(),
            content: prompt,
            tool_calls: Vec::new(),
            tool_call_id: String::new(),
        }],
        tools: &[],
    };
    let resp = provider.complete(&req).map_err(|e| e.to_string())?;
    Ok((resp.message.content, tokens_before, retained))
}

/// Provider error messages that indicate the context window was exceeded.
const OVERFLOW_PATTERNS: [&str; 7] = [
    "prompt is too long",
    "exceeds the context window",
    "maximum context length",
    "input token count",
    "context_length_exceeded",
    "prompt too long",
    "exceeds the model's maximum",
];

/// Errors that look like overflow but are throttling or server failures.
const NON_OVERFLOW_PATTERNS: [&str; 3] = ["throttling", "rate limit", "service unavailable"];

pub fn is_overflow_error(err: &str) -> bool {
    let e = err.to_lowercase();
    if NON_OVERFLOW_PATTERNS.iter().any(|p| e.contains(p)) {
        return false;
    }
    OVERFLOW_PATTERNS.iter().any(|p| e.contains(p))
}

#[derive(Debug)]
pub struct SearchHit {
    pub id: String,
    pub title: String,
    pub updated: i64,
    pub text: String,
}

fn clip(s: &str, n: usize) -> String {
    let count = s.chars().count();
    let mut out: String = s.chars().take(n).collect();
    if count > n {
        out.push('…');
    }
    out
}

/// Case-insensitive substring search over the live session and archived
/// sessions. JSONL is one entry per line, so line matches map to entries.
pub fn search(dir: &str, text: &str) -> Vec<SearchHit> {
    let needle = text.to_lowercase();
    let mut out = Vec::new();
    let mut scan = |path: &Path, id: &str| {
        let Ok(data) = std::fs::read_to_string(path) else {
            return;
        };
        let entries = read_entries(path);
        let title = std::fs::read_to_string(title_path(dir, id))
            .ok()
            .filter(|t| !t.trim().is_empty())
            .unwrap_or_else(|| title_from_entries(&entries));
        let updated = std::fs::metadata(path)
            .ok()
            .and_then(|m| m.modified().ok())
            .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
            .map(|d| d.as_millis() as i64)
            .unwrap_or(0);
        for line in data.lines() {
            if !line.to_lowercase().contains(&needle) {
                continue;
            }
            let text = parse_entry_line(line)
                .map(|e| match e {
                    Entry::Message { message } => message.content,
                    Entry::Compaction { summary, .. } => summary,
                })
                .unwrap_or_else(|| line.to_string());
            out.push(SearchHit {
                id: id.into(),
                title: title.clone(),
                updated,
                text: clip(&text, 200),
            });
            if out.len() >= 50 {
                return;
            }
        }
    };
    let live = live_path(dir);
    if live.exists() {
        scan(&live, "live");
    }
    if let Ok(entries) = std::fs::read_dir(store_dir(dir)) {
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
            scan(&path, &id);
        }
    }
    out.sort_by_key(|h| std::cmp::Reverse(h.updated));
    out
}
