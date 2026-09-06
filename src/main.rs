//! ax CLI: full-screen transcript TUI (fx-style) or one-shot prompt.

#![forbid(unsafe_code)]

use ax::run::{self, Outcome, RunOptions, Sink};
use ax::{Message, OpenAI, Tool, ToolCall};
use std::io::{IsTerminal, Read};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

struct Config {
    base: String,
    model: String,
    system: String,
    dir: String,
    session: Option<String>,
}

struct FileConfig {
    api_key: String,
    model: String,
    base: String,
    tools: String,
}

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let fc = load_config();
    let (mut cfg, prompt) = match parse_args(&args, &fc) {
        Ok(x) => x,
        Err(e) => {
            eprintln!("error: {e}");
            usage();
            std::process::exit(2);
        }
    };
    if !cfg.dir.is_empty() {
        let directory = std::fs::canonicalize(&cfg.dir).unwrap_or_else(|error| {
            eprintln!("error: cannot use {}: {error}", cfg.dir);
            std::process::exit(1);
        });
        std::env::set_current_dir(&directory).unwrap_or_else(|error| {
            eprintln!("error: cannot use {}: {error}", directory.display());
            std::process::exit(1);
        });
        cfg.dir = directory.display().to_string();
    }
    let mut prompt = prompt;
    if prompt.is_empty() {
        let mut b = String::new();
        if std::io::stdin().read_to_string(&mut b).is_err() {
            eprintln!("error: read stdin");
            std::process::exit(1);
        }
        prompt = vec![b];
    }
    one_shot_events(&cfg, &fc, &prompt);
}

fn parse_args(args: &[String], fc: &FileConfig) -> Result<(Config, Vec<String>), String> {
    let mut cfg = Config {
        base: if fc.base.is_empty() {
            "https://api.openai.com/v1".into()
        } else {
            fc.base.clone()
        },
        model: if fc.model.is_empty() {
            "gpt-4.1-mini".into()
        } else {
            fc.model.clone()
        },
        system: String::new(),
        dir: String::new(),
        session: None,
    };
    let mut rest = Vec::new();
    let mut i = 0;
    while i < args.len() {
        let a = &args[i];
        if a == "-h" || a == "--help" {
            usage();
            std::process::exit(0);
        }
        if a == "--events" {
            i += 1;
            continue;
        }
        if a == "--" {
            rest = args[i + 1..].to_vec();
            break;
        }
        let Some(stripped) = a.strip_prefix('-') else {
            rest = args[i..].to_vec();
            break;
        };
        let stripped = stripped.strip_prefix('-').unwrap_or(stripped);
        let (name, inline) = match stripped.split_once('=') {
            Some((n, v)) => (n.to_string(), Some(v.to_string())),
            None => (stripped.to_string(), None),
        };
        match name.as_str() {
            "base" | "model" | "system" | "C" | "session" => {
                let v = match inline {
                    Some(v) => v,
                    None => {
                        i += 1;
                        args.get(i)
                            .cloned()
                            .ok_or_else(|| format!("flag needs an argument: {a}"))?
                    }
                };
                match name.as_str() {
                    "base" => cfg.base = v,
                    "model" => cfg.model = v,
                    "system" => cfg.system = v,
                    "C" => cfg.dir = v,
                    "session" => cfg.session = Some(v),
                    _ => unreachable!(),
                }
            }
            _ => return Err(format!("flag provided but not defined: {a}")),
        }
        i += 1;
    }
    Ok((cfg, rest))
}

fn usage() {
    eprintln!(
        "Usage: ax [flags] [prompt]\n\
         \n\
         Flags:\n\
         \x20 -base URL    OpenAI-compatible API base URL (default \"https://api.openai.com/v1\")\n\
         \x20 -model NAME  model name (default \"gpt-4.1-mini\")\n\
         \x20 -system TEXT  system prompt (default: built-in)\n\
         \x20 -C DIR       working directory context\n\
         \x20 --session FILE  use an explicit session file\n\
         \x20 --events      emit JSONL events on stdout\n\
         \n\
         With no prompt, reads the prompt from stdin.
         A prompt of the form /name [args] expands a user command from
         ~/.config/ax/commands/<name>.md."
    );
}

struct EventSink {
    input: std::sync::mpsc::Receiver<String>,
    session: Option<std::path::PathBuf>,
    seen: Vec<Message>,
    flushed: usize,
    pending_calls: Option<usize>,
    results: usize,
    last_usage: Option<ax::Usage>,
    model: String,
    host_tools: std::collections::HashSet<String>,
}

impl EventSink {
    fn emit(&self, value: serde_json::Value) {
        use std::io::Write;
        println!("{value}");
        let _ = std::io::stdout().flush();
    }

    fn record(&mut self, message: Message) {
        let holding = self.pending_calls.is_some();
        self.seen.push(message);
        let last = self.seen.last().unwrap();
        if last.role == "assistant" && !last.tool_calls.is_empty() {
            self.pending_calls = Some(last.tool_calls.len());
            self.results = 0;
            return;
        }
        if holding {
            if last.role == "tool" {
                self.results += 1;
                if self.results >= self.pending_calls.unwrap_or(0) {
                    self.pending_calls = None;
                    self.flush();
                }
            }
            return;
        }
        self.flush();
    }

    fn flush(&mut self) {
        let Some(path) = self.session.clone() else {
            return;
        };
        if self.flushed >= self.seen.len() {
            return;
        }
        if let Err(e) = ax::session::append_messages(&path, &self.seen[self.flushed..]) {
            event_failure(&e);
        }
        self.flushed = self.seen.len();
    }

    fn finalize(&mut self, messages: &[Message], base: usize) {
        let Some(path) = self.session.clone() else {
            return;
        };
        let held = &self.seen[self.flushed..];
        let rest = &messages[base + self.seen.len()..];
        if held.is_empty() && rest.is_empty() {
            return;
        }
        let mut tail = held.to_vec();
        tail.extend_from_slice(rest);
        if let Err(e) = ax::session::append_messages(&path, &tail) {
            event_failure(&e);
        }
        self.flushed = self.seen.len();
    }
}

impl Sink for EventSink {
    fn assistant_delta(&mut self, text: &str) {
        self.emit(serde_json::json!({"type": "assistant_delta", "text": text}));
    }

    fn assistant_done(&mut self) {
        self.emit(serde_json::json!({"type": "assistant_done"}));
    }

    fn assistant(&mut self, _turn: usize, message: &Message, _usage: ax::Usage) {
        self.emit(serde_json::json!({"type": "message", "message": message}));
        self.record(message.clone());
    }

    fn tool_start(&mut self, call: &ToolCall) {
        self.emit(serde_json::json!({
            "type": "tool_start",
            "id": call.id,
            "name": call.name,
            "arguments": call.arguments
        }));
        if self.host_tools.contains(&call.name) {
            self.emit(serde_json::json!({
                "type": "host_call",
                "id": call.id,
                "name": call.name,
                "arguments": call.arguments
            }));
        }
    }

    fn tool_delta(&mut self, call: &ToolCall, text: &str) {
        self.emit(serde_json::json!({"type": "tool_delta", "id": call.id, "text": text}));
    }

    fn tool_result(&mut self, call: &ToolCall) {
        self.emit(serde_json::json!({"type": "tool_done", "id": call.id}));
    }

    fn tokens(&mut self, input: usize, output: usize, cached_input: usize) {
        self.last_usage = Some(ax::Usage {
            input,
            output,
            cached_input,
        });
        self.emit(serde_json::json!({
            "type": "usage",
            "input": input,
            "output": output,
            "cached_input": cached_input,
            "model": self.model
        }));
    }

    fn tool(&mut self, _turn: usize, message: &Message) {
        self.emit(serde_json::json!({
            "type": "tool_result",
            "id": message.tool_call_id,
            "output": message.content
        }));
        self.emit(serde_json::json!({"type": "message", "message": message}));
        self.record(message.clone());
    }

    fn pending_user_input(&mut self) -> Option<String> {
        let text = self.input.try_recv().ok()?;
        self.record(Message {
            role: "user".into(),
            content: text.clone(),
            tool_calls: Vec::new(),
            tool_call_id: String::new(),
        });
        Some(text)
    }
}

fn one_shot_events(cfg: &Config, fc: &FileConfig, prompt: &[String]) {
    println!("{}", serde_json::json!({"type": "protocol", "version": 1}));
    let mut history = match cfg.session.as_deref() {
        Some(path) => match ax::session::load_path(std::path::Path::new(path)) {
            Ok(entries) => ax::session::context_messages(&entries),
            Err(e) => event_failure(&e),
        },
        None => Vec::new(),
    };
    history.push(Message {
        role: "user".into(),
        content: expand_user_command(&prompt.join(" "), &ax_root()),
        tool_calls: Vec::new(),
        tool_call_id: String::new(),
    });
    let old_len = history.len().saturating_sub(1);
    let cancel = Arc::new(AtomicBool::new(false));
    let (tx, rx) = std::sync::mpsc::channel();
    if !std::io::stdin().is_terminal() {
        let cancel = cancel.clone();
        std::thread::spawn(move || {
            use std::io::BufRead;
            for line in std::io::stdin().lock().lines().map_while(Result::ok) {
                let Ok(value) = serde_json::from_str::<serde_json::Value>(&line) else {
                    continue;
                };
                match value.get("type").and_then(|value| value.as_str()) {
                    Some("cancel") => cancel.store(true, Ordering::Relaxed),
                    Some("steer") => {
                        if let Some(text) = value.get("text").and_then(|value| value.as_str()) {
                            let _ = tx.send(text.to_string());
                        }
                    }
                    _ => {}
                }
            }
        });
    }
    let tools = tools_or_exit(fc);
    let system = resolve_system(cfg, &tools);
    let provider = OpenAI::new(cfg.base.clone(), api_key(fc));
    let mut sink = EventSink {
        input: rx,
        session: cfg.session.as_deref().map(std::path::PathBuf::from),
        seen: Vec::new(),
        flushed: 0,
        pending_calls: None,
        results: 0,
        last_usage: None,
        model: cfg.model.clone(),
        host_tools: host_tool_names(),
    };
    sink.record(history[old_len].clone());
    let end = run::run_stream(
        &provider,
        &RunOptions {
            model: &cfg.model,
            system: &system,
            tools: &tools,
            max_turns: usize::MAX,
        },
        &history,
        &cancel,
        &mut sink,
    );
    sink.finalize(&end.messages, old_len);
    let final_usage = sink.last_usage.unwrap_or(end.usage);
    if let Some(path) = cfg.session.as_deref().map(std::path::Path::new)
        && let Err(e) = ax::session::append_usage(
            path,
            final_usage.input,
            final_usage.output,
            final_usage.cached_input,
            None,
            &sink.model,
        )
    {
        event_failure(&e);
    }
    println!(
        "{}",
        serde_json::json!({"type": "result", "messages": end.messages, "usage": {
            "input": end.usage.input,
            "output": end.usage.output,
            "cached_input": end.usage.cached_input
        }})
    );
    let (outcome, failed) = match end.outcome {
        Outcome::Done => ("done", false),
        Outcome::Cancelled => ("cancelled", false),
        Outcome::Compact => ("compact", false),
        Outcome::MaxTurns => ("max_turns", true),
        Outcome::Failed(message) => {
            println!(
                "{}",
                serde_json::json!({"type": "error", "message": message})
            );
            ("failed", true)
        }
    };
    println!(
        "{}",
        serde_json::json!({"type": "done", "outcome": outcome})
    );
    if failed {
        std::process::exit(1);
    }
}

fn event_failure(message: &str) -> ! {
    println!(
        "{}",
        serde_json::json!({"type": "error", "message": message})
    );
    println!(
        "{}",
        serde_json::json!({"type": "done", "outcome": "failed"})
    );
    std::process::exit(1);
}

fn expand_user_command(prompt: &str, ax_root: &str) -> String {
    let Some(cmd) = prompt.strip_prefix('/') else {
        return prompt.to_string();
    };
    let (name, rest) = match cmd.split_once(' ') {
        Some((n, r)) => (n, r.trim()),
        None => (cmd, ""),
    };
    let Some(uc) = ax::commands::load_user_commands(ax_root)
        .into_iter()
        .find(|c| c.name == name)
    else {
        return prompt.to_string();
    };
    ax::commands::expand_user_command(&uc, rest)
}

fn tools_or_exit(config: &FileConfig) -> Vec<Tool> {
    let commands = std::env::var("AX_TOOLS").unwrap_or_else(|_| config.tools.clone());
    let mut tools = match ax::tools::try_external_tools(&commands) {
        Ok(tools) => tools,
        Err(error) => {
            eprintln!("error: {error}");
            std::process::exit(1);
        }
    };
    tools.extend(host_tools_or_exit());
    tools
}

fn host_tools_or_exit() -> Vec<Tool> {
    #[derive(serde::Deserialize)]
    struct HostTool {
        name: String,
        description: String,
        parameters: serde_json::Value,
    }
    let value = match std::env::var("AX_HOST_TOOLS") {
        Ok(value) if !value.trim().is_empty() => value,
        _ => return Vec::new(),
    };
    let definitions: Vec<HostTool> = serde_json::from_str(&value).unwrap_or_else(|error| {
        eprintln!("error: invalid AX_HOST_TOOLS: {error}");
        std::process::exit(1);
    });
    definitions
        .into_iter()
        .map(|definition| {
            let name = Box::leak(definition.name.into_boxed_str());
            let description = Box::leak(definition.description.into_boxed_str());
            Tool {
                name,
                description,
                parameters: definition.parameters,
                snippet: "",
                sequential: true,
                run: Box::new(|_, _| "Host accepted the operation.".into()),
            }
        })
        .collect()
}

fn host_tool_names() -> std::collections::HashSet<String> {
    std::env::var("AX_HOST_TOOLS")
        .ok()
        .and_then(|value| serde_json::from_str::<Vec<serde_json::Value>>(&value).ok())
        .unwrap_or_default()
        .into_iter()
        .filter_map(|tool| {
            tool.get("name")
                .and_then(|name| name.as_str())
                .map(str::to_string)
        })
        .collect()
}

fn api_key(fc: &FileConfig) -> String {
    if let Ok(k) = std::env::var("OPENAI_API_KEY")
        && !k.is_empty()
    {
        return k;
    }
    fc.api_key.clone()
}

fn work_dir(cfg: &Config) -> String {
    if !cfg.dir.is_empty() {
        return cfg.dir.clone();
    }
    std::env::current_dir()
        .map(|p| p.display().to_string())
        .unwrap_or_default()
}

fn resolve_system(cfg: &Config, tools: &[Tool]) -> String {
    if cfg.system.is_empty() {
        system_prompt(tools, &work_dir(cfg))
    } else {
        cfg.system.clone()
    }
}

fn system_prompt(tools: &[Tool], dir: &str) -> String {
    let mut out =
        String::from("You are an expert assistant operating inside ax.\n\nAvailable tools:\n");
    for tool in tools {
        if !tool.snippet.is_empty() {
            out.push_str(&format!("- {}: {}\n", tool.name, tool.snippet));
        }
    }
    if tools.is_empty() {
        out.push_str("- none\n");
    }
    out.push_str(
        "\nGuidelines:\n\
         - Be concise in your responses\n\
         - Tool errors return to you as text; fix them and re-issue\n",
    );
    if tools.iter().any(|tool| tool.name == "edit") {
        out.push_str(
            "- Show file paths clearly when working with files\n\
             - Use edit for precise changes; edits[].oldText must match exactly\n\
             - When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls\n\
             - Keep edits[].oldText small while still unique; do not pad with unchanged regions\n",
        );
    }
    out.push_str(&format!("\nCurrent working directory: {dir}"));
    if let Some(user) = user_system_prompt() {
        out.push_str("\n\n");
        out.push_str(&user);
    }
    out
}

fn user_system_prompt() -> Option<String> {
    let path = config_dir()?.join("ax").join("SYSTEM.md");
    let text = std::fs::read_to_string(path).ok()?;
    let text = text.trim();
    if text.is_empty() {
        None
    } else {
        Some(text.to_string())
    }
}

fn config_dir() -> Option<std::path::PathBuf> {
    if let Ok(x) = std::env::var("XDG_CONFIG_HOME")
        && !x.is_empty()
    {
        return Some(std::path::PathBuf::from(x));
    }
    std::env::var("HOME")
        .ok()
        .map(|h| std::path::PathBuf::from(h).join(".config"))
}

fn ax_root() -> String {
    match config_dir() {
        Some(d) => d.join("ax").display().to_string(),
        None => work_dir_abs(),
    }
}

fn work_dir_abs() -> String {
    std::env::current_dir()
        .map(|p| p.join(".ax").display().to_string())
        .unwrap_or_else(|_| ".ax".to_string())
}

fn load_config() -> FileConfig {
    let mut c = FileConfig {
        api_key: String::new(),
        model: String::new(),
        base: String::new(),
        tools: String::new(),
    };
    let Some(dir) = config_dir() else {
        return c;
    };
    let Ok(text) = std::fs::read_to_string(dir.join("ax").join("config")) else {
        return c;
    };
    for line in text.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let Some((k, v)) = line.split_once('=') else {
            continue;
        };
        let mut val = v.trim().to_string();
        if val.len() >= 2 && val.starts_with('"') && val.ends_with('"') {
            val = val[1..val.len() - 1].to_string();
        }
        match k.trim() {
            "api_key" => c.api_key = val,
            "model" => c.model = val,
            "base" => c.base = val,
            "tools" => c.tools = val,
            _ => {}
        }
    }
    c
}

#[cfg(test)]
mod tests {
    use super::*;

    struct Recorder {
        sink: EventSink,
        tx: std::sync::mpsc::Sender<String>,
        path: std::path::PathBuf,
    }

    impl Drop for Recorder {
        fn drop(&mut self) {
            std::fs::remove_file(&self.path).ok();
        }
    }

    fn recorder() -> Recorder {
        let (tx, rx) = std::sync::mpsc::channel();
        let path = std::env::temp_dir().join(format!(
            "ax-flush-{}-{p}",
            std::process::id(),
            p = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .subsec_nanos()
        ));
        std::fs::remove_file(&path).ok();
        Recorder {
            sink: EventSink {
                input: rx,
                session: Some(path.clone()),
                seen: Vec::new(),
                flushed: 0,
                pending_calls: None,
                results: 0,
                last_usage: None,
                model: String::new(),
                host_tools: std::collections::HashSet::new(),
            },
            path,
            tx,
        }
    }

    fn stored(path: &std::path::Path) -> Vec<Message> {
        ax::session::context_messages(&ax::session::load_path(path).unwrap())
    }

    fn message(role: &str, content: &str) -> Message {
        Message {
            role: role.into(),
            content: content.into(),
            tool_calls: Vec::new(),
            tool_call_id: String::new(),
        }
    }

    #[test]
    fn event_sink_flushes_each_turn() {
        let mut r = recorder();
        r.sink.record(message("user", "go"));
        assert_eq!(stored(&r.path).len(), 1);
        r.sink.record(message("assistant", "hello"));
        assert_eq!(stored(&r.path).len(), 2);
    }

    #[test]
    fn event_sink_holds_tool_exchange() {
        let mut r = recorder();
        r.sink.record(message("user", "go"));
        let mut call = message("assistant", "");
        call.tool_calls.push(ToolCall {
            id: "c1".into(),
            name: "read".into(),
            arguments: "{}".into(),
        });
        r.sink.record(call);
        assert_eq!(
            stored(&r.path).len(),
            1,
            "assistant with calls must not land alone"
        );
        let mut result = message("tool", "data");
        result.tool_call_id = "c1".into();
        r.sink.record(result);
        assert_eq!(stored(&r.path).len(), 3);
    }

    #[test]
    fn event_sink_cancel_mid_batch_persists_completed_work() {
        let mut r = recorder();
        r.sink.record(message("user", "go"));
        let mut call = message("assistant", "");
        call.tool_calls.push(ToolCall {
            id: "c1".into(),
            name: "read".into(),
            arguments: "{}".into(),
        });
        call.tool_calls.push(ToolCall {
            id: "c2".into(),
            name: "read".into(),
            arguments: "{}".into(),
        });
        r.sink.record(call);
        let mut first = message("tool", "first result");
        first.tool_call_id = "c1".into();
        r.sink.record(first);
        assert_eq!(
            stored(&r.path).len(),
            1,
            "incomplete exchange stays in memory"
        );
        let mut second = message(
            "tool",
            "error: tool call not executed: the run was interrupted.",
        );
        second.tool_call_id = "c2".into();
        r.sink.record(second);
        assert_eq!(stored(&r.path).len(), 4);
    }

    #[test]
    fn event_sink_finalize_appends_unflushed() {
        let mut r = recorder();
        r.sink.record(message("user", "go"));
        let mut call = message("assistant", "");
        call.tool_calls.push(ToolCall {
            id: "c1".into(),
            name: "read".into(),
            arguments: "{}".into(),
        });
        r.sink.record(call.clone());
        let final_messages = vec![
            message("user", "earlier"),
            message("user", "go"),
            call,
            message("assistant", "done"),
        ];
        r.sink.finalize(&final_messages, 1);
        assert_eq!(stored(&r.path), final_messages[1..]);
    }

    #[test]
    fn event_sink_records_steer_messages() {
        let mut r = recorder();
        let _ = r.tx.send("focus".into());
        assert_eq!(r.sink.pending_user_input().unwrap(), "focus");
        assert_eq!(stored(&r.path).len(), 1);
    }

    #[test]
    fn expand_user_command_cases() {
        let dir = std::env::temp_dir().join(format!("ax-uc-{}", std::process::id()));
        let cmds = dir.join("commands");
        std::fs::create_dir_all(&cmds).unwrap();
        std::fs::write(
            cmds.join("commit.md"),
            "---\ndescription: stage and commit\n---\n\nstage everything now",
        )
        .unwrap();
        std::fs::write(cmds.join("args.md"), "say: $ARGUMENTS").unwrap();
        let root = dir.to_str().unwrap();

        assert_eq!(expand_user_command("/commit", root), "stage everything now");
        assert_eq!(
            expand_user_command("/commit blablabla", root),
            "stage everything now\n\nblablabla"
        );
        assert_eq!(expand_user_command("/args hi there", root), "say: hi there");
        assert_eq!(expand_user_command("/missing x", root), "/missing x");
        assert_eq!(expand_user_command("not a command", root), "not a command");

        std::fs::remove_dir_all(&dir).ok();
    }

    struct Rng(u64);

    impl Rng {
        fn next(&mut self) -> u64 {
            let mut x = self.0;
            x ^= x >> 12;
            x ^= x << 25;
            x ^= x >> 27;
            self.0 = x;
            x.wrapping_mul(0x2545F4914F6CDD1D)
        }

        fn below(&mut self, n: usize) -> usize {
            (self.next() % n.max(1) as u64) as usize
        }
    }

    #[test]
    fn double_dash_ends_options() {
        let fc = FileConfig {
            api_key: String::new(),
            model: String::new(),
            base: String::new(),
            tools: String::new(),
        };
        let (_, prompt) = parse_args(&["--".into(), "-prompt".into()], &fc).unwrap();
        assert_eq!(prompt, ["-prompt"]);
    }

    #[test]
    fn parse_args_fuzz() {
        let fc = FileConfig {
            api_key: String::new(),
            model: String::new(),
            base: String::new(),
            tools: String::new(),
        };
        let tokens = [
            "-base",
            "-model",
            "-system",
            "-C",
            "--session",
            "--events",
            "-",
            "--",
            "foo",
            "",
            "=x",
            "-x",
            "--x",
            "-base=",
            "-model=x",
            "prompt",
            "with space",
        ];
        for si in 0..64u64 {
            let mut rng = Rng(si ^ 0xABCDEF1234567890);
            for _ in 0..200 {
                let n = rng.below(6);
                let mut args = Vec::new();
                for _ in 0..n {
                    args.push(tokens[rng.below(tokens.len())].to_string());
                }
                if args.iter().any(|a| a == "-h" || a == "--help") {
                    continue;
                }
                let _ = parse_args(&args, &fc);
            }
        }
    }

    #[test]
    fn load_config_real_file_roundtrip() {
        let Some(home) = std::env::var_os("HOME") else {
            return;
        };
        let path = std::path::Path::new(&home)
            .join(".config")
            .join("ax")
            .join("config");
        if !path.exists() {
            return;
        }
        let c = load_config();
        let text = std::fs::read_to_string(&path).unwrap();
        let expect = |k: &str| -> String {
            text.lines()
                .find_map(|l| {
                    let (kk, v) = l.split_once('=')?;
                    (kk.trim() == k).then(|| v.trim().trim_matches('"').to_string())
                })
                .unwrap_or_default()
        };
        assert_eq!(c.base, expect("base"), "base round-trip");
        assert_eq!(c.model, expect("model"), "model round-trip");
        assert_eq!(c.api_key, expect("api_key"), "api_key round-trip");
        assert_eq!(c.tools, expect("tools"), "tools round-trip");
    }
}
