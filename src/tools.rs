//! Built-in tools: bash, read, write, edit.

use crate::{Tool, new_tool};
use serde::Deserialize;
use std::os::unix::process::CommandExt;

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
    #[serde(default)]
    timeout: Option<u64>,
}

static BASH_TAG: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

pub fn bash(dir: &str) -> Tool {
    let dir = dir.to_string();
    new_tool(
        "bash",
        "Execute a bash command in the current working directory. Returns stdout and stderr. Output is truncated to last 16KB. Optionally provide a timeout in seconds.",
        r#"{"type":"object","properties":{"command":{"type":"string","description":"bash command to run"},"timeout":{"type":"number","description":"Timeout in seconds (optional, no default timeout)"}},"required":["command"]}"#,
        move |a: BashArgs| {
            if a.timeout == Some(0) {
                return "error: invalid timeout: must be a positive number of seconds".to_string();
            }
            let tag = BASH_TAG.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
            let base = std::env::temp_dir().join(format!("ax-bash-{}-{tag}", std::process::id()));
            let out_path = base.with_extension("out");
            let err_path = base.with_extension("err");
            let out_file = match std::fs::File::create(&out_path) {
                Ok(f) => f,
                Err(e) => return format!("error: {e}"),
            };
            let err_file = match std::fs::File::create(&err_path) {
                Ok(f) => f,
                Err(e) => return format!("error: {e}"),
            };
            let mut cmd = std::process::Command::new("bash");
            cmd.arg("-c").arg(&a.command);
            if !dir.is_empty() {
                cmd.current_dir(&dir);
            }
            cmd.stdout(std::process::Stdio::from(out_file));
            cmd.stderr(std::process::Stdio::from(err_file));
            cmd.process_group(0);
            let mut child = match cmd.spawn() {
                Ok(c) => c,
                Err(e) => return format!("error: {e}"),
            };
            let mut exit: Option<std::process::ExitStatus> = None;
            let timed_out = if let Some(t) = a.timeout {
                let deadline = std::time::Instant::now() + std::time::Duration::from_secs(t);
                loop {
                    match child.try_wait() {
                        Ok(Some(st)) => {
                            exit = Some(st);
                            break false;
                        }
                        Ok(None) if std::time::Instant::now() >= deadline => {
                            unsafe {
                                libc::kill(-(child.id() as i32), libc::SIGKILL);
                            }
                            loop {
                                match child.try_wait() {
                                    Ok(Some(_)) => break,
                                    Ok(None) => {
                                        std::thread::sleep(std::time::Duration::from_millis(10))
                                    }
                                    Err(_) => break,
                                }
                            }
                            break true;
                        }
                        Ok(None) => std::thread::sleep(std::time::Duration::from_millis(50)),
                        Err(e) => return format!("error: {e}"),
                    }
                }
            } else {
                match child.wait() {
                    Ok(st) => {
                        exit = Some(st);
                        false
                    }
                    Err(e) => return format!("error: {e}"),
                }
            };
            let stdout = std::fs::read(&out_path)
                .map(|b| String::from_utf8_lossy(&b).into_owned())
                .unwrap_or_default();
            let stderr = std::fs::read(&err_path)
                .map(|b| String::from_utf8_lossy(&b).into_owned())
                .unwrap_or_default();
            let _ = std::fs::remove_file(&out_path);
            let _ = std::fs::remove_file(&err_path);
            let mut s = stdout;
            if !stderr.is_empty() {
                if !s.is_empty() && !s.ends_with('\n') {
                    s.push('\n');
                }
                s.push_str(&stderr);
            }
            if timed_out {
                if !s.is_empty() && !s.ends_with('\n') {
                    s.push('\n');
                }
                s.push_str(&format!(
                    "error: command timed out after {} seconds",
                    a.timeout.unwrap_or(0)
                ));
            } else if let Some(st) = exit
                && !st.success()
            {
                if !s.is_empty() && !s.ends_with('\n') {
                    s.push('\n');
                }
                s.push_str(&format!("error: {}", status_str(st)));
            }
            limit(&s)
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
    #[serde(default)]
    offset: Option<usize>,
    #[serde(default)]
    limit: Option<usize>,
}

pub fn read() -> Tool {
    new_tool(
        "read",
        "Read the contents of a file. Output is truncated to 16KB. Use offset/limit for large files. When you need the full file, continue with the suggested offset.",
        r#"{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to read (relative or absolute)"},"offset":{"type":"number","description":"Line number to start reading from (1-indexed)"},"limit":{"type":"number","description":"Maximum number of lines to read"}},"required":["path"]}"#,
        |a: ReadArgs| match std::fs::read(&a.path) {
            Err(e) => format!("error: {e}"),
            Ok(b) => {
                let text = String::from_utf8_lossy(&b);
                let lines: Vec<&str> = text.split('\n').collect();
                let total = lines.len();
                let start = a.offset.unwrap_or(1).saturating_sub(1);
                if a.offset.is_some() && start >= total {
                    return format!(
                        "error: offset {} is beyond end of file ({} lines total)",
                        a.offset.unwrap_or(1),
                        total
                    );
                }
                let end = match a.limit {
                    Some(l) => (start + l).min(total),
                    None => total,
                };
                let selected = lines[start..end].join("\n");
                let remaining = total.saturating_sub(end);
                if selected.len() <= MAX_OUTPUT {
                    let mut out = selected;
                    if remaining > 0 {
                        out.push_str(&format!(
                            "\n\n[{} more lines in file. Use offset={} to continue.]",
                            remaining,
                            end + 1
                        ));
                    }
                    return out;
                }
                let first = lines[start];
                if first.len() > MAX_OUTPUT {
                    return format!(
                        "[Line {} is {} bytes, exceeds the {} limit. Use bash: sed -n '{}p' {} | head -c {}]",
                        start + 1,
                        first.len(),
                        MAX_OUTPUT,
                        start + 1,
                        a.path,
                        MAX_OUTPUT
                    );
                }
                let mut shown = 1usize;
                let mut acc = first.to_string();
                while shown < end - start {
                    let next = lines[start + shown];
                    if acc.len() + 1 + next.len() > MAX_OUTPUT {
                        break;
                    }
                    acc.push('\n');
                    acc.push_str(next);
                    shown += 1;
                }
                let last = start + shown;
                format!(
                    "{}\n\n[Showing lines {}-{} of {} ({} limit). Use offset={} to continue.]",
                    acc,
                    start + 1,
                    last,
                    total,
                    MAX_OUTPUT,
                    last + 1
                )
            }
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
        "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories.",
        r#"{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to write (relative or absolute)"},"content":{"type":"string","description":"Content to write to the file"}},"required":["path","content"]}"#,
        |a: WriteArgs| {
            if let Some(parent) = std::path::Path::new(&a.path).parent()
                && !parent.as_os_str().is_empty()
                && let Err(e) = std::fs::create_dir_all(parent)
            {
                return format!("error: {e}");
            }
            match std::fs::write(&a.path, &a.content) {
                Ok(()) => format!("wrote {} ({} bytes)", a.path, a.content.len()),
                Err(e) => format!("error: {e}"),
            }
        },
    )
}

#[derive(Deserialize)]
struct EditArg {
    #[serde(rename = "oldText")]
    old_text: String,
    #[serde(rename = "newText")]
    new_text: String,
}

#[derive(Deserialize)]
struct EditArgs {
    path: String,
    edits: Vec<EditArg>,
}

fn normalize_lf(s: &str) -> String {
    s.replace("\r\n", "\n").replace('\r', "\n")
}

fn line_ending(s: &str) -> &'static str {
    match (s.find("\r\n"), s.find('\n')) {
        (Some(c), Some(l)) if c < l => "\r\n",
        _ => "\n",
    }
}

fn empty_old_error(path: &str, i: usize, total: usize) -> String {
    if total == 1 {
        format!("error: oldText must not be empty in {path}.")
    } else {
        format!("error: edits[{i}].oldText must not be empty in {path}.")
    }
}

fn not_found_error(path: &str, i: usize, total: usize) -> String {
    if total == 1 {
        format!(
            "error: Could not find the exact text in {path}. The old text must match exactly including all whitespace and newlines."
        )
    } else {
        format!(
            "error: Could not find edits[{i}] in {path}. The oldText must match exactly including all whitespace and newlines."
        )
    }
}

fn duplicate_error(path: &str, i: usize, total: usize, n: usize) -> String {
    if total == 1 {
        format!(
            "error: Found {n} occurrences of the text in {path}. The text must be unique. Please provide more context to make it unique."
        )
    } else {
        format!(
            "error: Found {n} occurrences of edits[{i}] in {path}. Each oldText must be unique. Please provide more context to make it unique."
        )
    }
}

fn no_change_error(path: &str, total: usize) -> String {
    if total == 1 {
        format!("error: No changes made to {path}. The replacement produced identical content.")
    } else {
        format!("error: No changes made to {path}. The replacements produced identical content.")
    }
}

fn normalize_for_fuzzy(s: &str) -> String {
    let mut out = String::new();
    for line in s.split('\n') {
        if !out.is_empty() {
            out.push('\n');
        }
        out.push_str(line.trim_end());
    }
    out.replace(['\u{2018}', '\u{2019}', '\u{201A}', '\u{201B}'], "'")
        .replace(['\u{201C}', '\u{201D}', '\u{201E}', '\u{201F}'], "\"")
        .replace(
            [
                '\u{2010}', '\u{2011}', '\u{2012}', '\u{2013}', '\u{2014}', '\u{2015}', '\u{2212}',
            ],
            "-",
        )
        .replace(
            [
                '\u{00A0}', '\u{2002}', '\u{2003}', '\u{2004}', '\u{2005}', '\u{2006}', '\u{2007}',
                '\u{2008}', '\u{2009}', '\u{200A}', '\u{202F}', '\u{205F}', '\u{3000}',
            ],
            " ",
        )
}

fn count_in(content: &str, needle: &str) -> usize {
    if needle.is_empty() {
        return 0;
    }
    content.matches(needle).count()
}

fn split_lines_with_endings(s: &str) -> Vec<&str> {
    let mut out = Vec::new();
    let mut rest = s;
    while let Some(i) = rest.find('\n') {
        out.push(&rest[..=i]);
        rest = &rest[i + 1..];
    }
    if !rest.is_empty() {
        out.push(rest);
    }
    out
}

fn line_spans(content: &str) -> Vec<(usize, usize)> {
    let mut spans = Vec::new();
    let mut offset = 0;
    for line in split_lines_with_endings(content) {
        spans.push((offset, offset + line.len()));
        offset += line.len();
    }
    spans
}

fn byte_offset_of_nth_char(s: &str, n: usize) -> usize {
    s.char_indices().nth(n).map(|(i, _)| i).unwrap_or(s.len())
}

fn fuzzy_to_original_range(
    normalized: &str,
    fuzzy: &str,
    fstart: usize,
    flen: usize,
) -> Option<(usize, usize)> {
    let fend = fstart + flen;
    let fspans = line_spans(fuzzy);
    let mut sl = None;
    for (i, (ls, le)) in fspans.iter().enumerate() {
        if fstart >= *ls && fstart < *le {
            sl = Some(i);
            break;
        }
    }
    let sl = sl?;
    let mut el = sl;
    while el < fspans.len() && fspans[el].1 < fend {
        el += 1;
    }
    if el >= fspans.len() {
        return None;
    }
    let nspans = line_spans(normalized);
    let k1 = fuzzy[fspans[sl].0..fstart].chars().count();
    let k2 = fuzzy[fspans[el].0..fend].chars().count();
    let first = &normalized[nspans[sl].0..nspans[sl].1];
    let last = &normalized[nspans[el].0..nspans[el].1];
    let start = nspans[sl].0 + byte_offset_of_nth_char(first, k1);
    let end = nspans[el].0 + byte_offset_of_nth_char(last, k2);
    Some((start, end - start))
}

fn apply_edits(path: &str, content: &str, edits: &[EditArg]) -> Result<String, String> {
    if edits.is_empty() {
        return Err("error: edits must contain at least one replacement.".to_string());
    }
    let (bom, body) = match content.strip_prefix('\u{FEFF}') {
        Some(rest) => ("\u{FEFF}", rest),
        None => ("", content),
    };
    let normalized = normalize_lf(body);
    let mut olds = Vec::with_capacity(edits.len());
    for (i, e) in edits.iter().enumerate() {
        if e.old_text.is_empty() {
            return Err(empty_old_error(path, i, edits.len()));
        }
        olds.push(normalize_lf(&e.old_text));
    }
    let mut found: Vec<(usize, usize, usize)> = Vec::new();
    let mut fuzzy_content: Option<String> = None;
    for (i, old) in olds.iter().enumerate() {
        let (start, len) = match normalized.find(old) {
            Some(idx) => {
                let n = count_in(&normalized, old);
                if n > 1 {
                    return Err(duplicate_error(path, i, edits.len(), n));
                }
                (idx, old.len())
            }
            None => {
                let fuzzy = fuzzy_content.get_or_insert_with(|| normalize_for_fuzzy(&normalized));
                let fuzzy_old = normalize_for_fuzzy(old);
                if fuzzy_old.is_empty() {
                    return Err(not_found_error(path, i, edits.len()));
                }
                let idx = match fuzzy.find(&fuzzy_old) {
                    Some(j) => j,
                    None => return Err(not_found_error(path, i, edits.len())),
                };
                let n = count_in(fuzzy, &fuzzy_old);
                if n > 1 {
                    return Err(duplicate_error(path, i, edits.len(), n));
                }
                match fuzzy_to_original_range(&normalized, fuzzy, idx, fuzzy_old.len()) {
                    Some(r) => r,
                    None => return Err(not_found_error(path, i, edits.len())),
                }
            }
        };
        found.push((i, start, len));
    }
    found.sort_by_key(|f| f.1);
    for w in found.windows(2) {
        if w[0].1 + w[0].2 > w[1].1 {
            return Err(format!(
                "error: edits[{}] and edits[{}] overlap in {path}. Merge them into one edit or target disjoint regions.",
                w[0].0, w[1].0
            ));
        }
    }
    let mut out = normalized.clone();
    for &(i, idx, len) in found.iter().rev() {
        out.replace_range(idx..idx + len, &normalize_lf(&edits[i].new_text));
    }
    if out == normalized {
        return Err(no_change_error(path, edits.len()));
    }
    let restored = if line_ending(body) == "\r\n" {
        out.replace('\n', "\r\n")
    } else {
        out
    };
    Ok(format!("{bom}{restored}"))
}

pub fn edit() -> Tool {
    new_tool(
        "edit",
        "Edit a single file using exact text replacement. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions just to connect distant changes.",
        r#"{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to edit (relative or absolute)"},"edits":{"type":"array","description":"One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.","items":{"type":"object","properties":{"oldText":{"type":"string","description":"Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."},"newText":{"type":"string","description":"Replacement text for this targeted edit."}},"required":["oldText","newText"]}}},"required":["path","edits"]}"#,
        |a: EditArgs| match std::fs::read_to_string(&a.path) {
            Err(e) => format!("error: {e}"),
            Ok(s) => match apply_edits(&a.path, &s, &a.edits) {
                Ok(out) => {
                    let n = a.edits.len();
                    match std::fs::write(&a.path, out) {
                        Ok(()) => format!("Successfully replaced {n} block(s) in {}.", a.path),
                        Err(e) => format!("error: {e}"),
                    }
                }
                Err(e) => e,
            },
        },
    )
}
