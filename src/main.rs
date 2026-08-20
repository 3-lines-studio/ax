//! ax CLI: full-screen transcript TUI (fx-style) or one-shot prompt.

#![forbid(unsafe_code)]

use ax::{Agent, Error, Event, Message, OpenAI, ToolCall};
use std::cell::RefCell;
use std::io::{IsTerminal, Read};
use std::rc::Rc;
use std::time::Instant;

struct Config {
    base: String,
    model: String,
    system: String,
    dir: String,
    resume: Option<String>,
}

struct FileConfig {
    api_key: String,
    model: String,
    base: String,
}

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let fc = load_config();
    let (cfg, prompt) = match parse_args(&args, &fc) {
        Ok(x) => x,
        Err(e) => {
            eprintln!("error: {e}");
            usage();
            std::process::exit(2);
        }
    };
    let mut prompt = prompt;
    if prompt.is_empty() && std::io::stdin().is_terminal() {
        let tui_cfg = ax::tui::TuiConfig {
            base: cfg.base.clone(),
            model: cfg.model.clone(),
            system: resolve_system(&cfg),
            dir: cfg.dir.clone(),
            ax_root: ax_root(),
            skills_root: skills_root(),
            api_key: api_key(&fc),
            resume: cfg.resume.clone(),
        };
        if let Err(e) = ax::tui::run(tui_cfg) {
            eprintln!("error: {e}");
            std::process::exit(1);
        }
        return;
    }
    if prompt.is_empty() {
        let mut b = String::new();
        if std::io::stdin().read_to_string(&mut b).is_err() {
            eprintln!("error: read stdin");
            std::process::exit(1);
        }
        prompt = vec![b];
    }
    one_shot(&cfg, &fc, &prompt);
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
        resume: None,
    };
    let mut rest = Vec::new();
    let mut i = 0;
    while i < args.len() {
        let a = &args[i];
        if a == "-h" || a == "--help" {
            usage();
            std::process::exit(0);
        }
        if a == "-r" {
            cfg.resume = Some(String::new());
            i += 1;
            continue;
        }
        if a == "--resume" || a == "resume" {
            // Bare resume opens the picker; a following non-flag names the target.
            if let Some(next) = args.get(i + 1)
                && !next.starts_with('-')
            {
                cfg.resume = Some(next.clone());
                i += 2;
                continue;
            }
            cfg.resume = Some(String::new());
            i += 1;
            continue;
        }
        if let Some(v) = a.strip_prefix("--resume=") {
            cfg.resume = Some(v.to_string());
            i += 1;
            continue;
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
            "base" | "model" | "system" | "C" => {
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
         \x20 -C DIR       working directory for tools\n\
         \x20 -r, --resume  open the session picker\n\
         \x20 --resume last  resume the most recent session\n\
         \x20 --resume ID   resume a saved session by id\n\
         \n\
         With no prompt and a TTY, starts the interactive transcript TUI\n\
         (fresh session; \"/resume\" reopens saved ones).\n\
         With no prompt and no TTY, reads the prompt from stdin.
         A prompt of the form /name [args] expands a user command from
         ~/.config/ax/commands/<name>.md (same expansion as the TUI)."
    );
}

fn one_shot(cfg: &Config, fc: &FileConfig, prompt: &[String]) {
    let stats = Rc::new(RefCell::new((0usize, 0usize)));
    let s2 = stats.clone();
    let on = move |e: Event| {
        let mut st = s2.borrow_mut();
        st.0 += e.usage.input;
        st.1 += e.usage.output;
        drop(st);
        let m = &e.message;
        if m.role == "assistant" {
            for c in &m.tool_calls {
                eprintln!("[{}] {} {}", e.turn, c.name, render_args(c));
            }
        } else if m.role == "tool" {
            eprintln!("[{}] -> {}", e.turn, render_result(&m.content, false));
        }
    };
    let start = Instant::now();
    let mut agent = build_agent(cfg, fc, on);
    let user = Message {
        role: "user".into(),
        content: expand_user_command(&prompt.join(" "), &ax_root()),
        tool_calls: Vec::new(),
        tool_call_id: String::new(),
    };
    let msgs = match agent.run(&[user]) {
        Ok(msgs) => msgs,
        Err(Error::MaxTurns(h)) => {
            eprintln!("stopped: ax: max turns reached");
            h
        }
        Err(e) => {
            eprintln!("error: {e}");
            std::process::exit(1);
        }
    };
    let (i, o) = *stats.borrow();
    if i + o > 0 {
        eprintln!(
            "tokens: {} in / {} out · {}",
            tok(i),
            tok(o),
            fmt_dur(start.elapsed())
        );
    }
    let pretty = std::io::stdout().is_terminal();
    for m in &msgs {
        if m.role == "assistant" && !m.content.is_empty() && m.tool_calls.is_empty() {
            if pretty {
                let rendered = ax::markdown::Markdown::render(&m.content);
                print!("{rendered}");
            } else {
                println!("{}", m.content);
            }
        }
    }
}

fn expand_user_command(prompt: &str, ax_root: &str) -> String {
    let Some(cmd) = prompt.strip_prefix('/') else {
        return prompt.to_string();
    };
    let (name, rest) = match cmd.split_once(' ') {
        Some((n, r)) => (n, r.trim()),
        None => (cmd, ""),
    };
    let Some(uc) = ax::tui::load_user_commands(ax_root)
        .into_iter()
        .find(|c| c.name == name)
    else {
        return prompt.to_string();
    };
    ax::tui::expand_user_command(&uc, rest)
}

fn build_agent(cfg: &Config, fc: &FileConfig, on: impl FnMut(Event) + 'static) -> Agent<OpenAI> {
    let system = resolve_system(cfg);
    let mut tools = vec![
        ax::tools::read(),
        ax::tools::write(),
        ax::tools::edit(),
        ax::tools::bash(&cfg.dir),
    ];
    tools.extend(ax::skills::skill_tools(&skills_root()));
    Agent::new(OpenAI::new(cfg.base.clone(), api_key(fc)))
        .model(cfg.model.clone())
        .system(system)
        .tools(tools)
        .on(on)
}

fn skills_root() -> String {
    ax::skills::skills_root().unwrap_or_default()
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

fn resolve_system(cfg: &Config) -> String {
    if cfg.system.is_empty() {
        system_prompt(&work_dir(cfg))
    } else {
        cfg.system.clone()
    }
}

fn system_prompt(dir: &str) -> String {
    let mut out = format!(
        "You are a coding agent with read, write, edit and bash tools. \
         Working directory: {dir}."
    );
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
            _ => {}
        }
    }
    c
}

fn render_args(call: &ToolCall) -> String {
    #[derive(serde::Deserialize)]
    struct A {
        path: Option<String>,
        content: Option<String>,
        command: Option<String>,
    }
    if let Ok(a) = serde_json::from_str::<A>(&call.arguments) {
        match call.name.as_str() {
            "bash" => {
                if let Some(c) = a.command
                    && !c.is_empty()
                {
                    return c;
                }
            }
            "read" | "edit" => {
                if let Some(p) = a.path
                    && !p.is_empty()
                {
                    return p;
                }
            }
            "write" => {
                if let Some(p) = a.path
                    && !p.is_empty()
                {
                    return format!(
                        "{} ({} bytes)",
                        p,
                        a.content.as_deref().map(|c| c.len()).unwrap_or(0)
                    );
                }
            }
            _ => {}
        }
    }
    call.arguments.trim().to_string()
}

fn render_result(s: &str, verbose: bool) -> String {
    if verbose {
        return s.to_string();
    }
    if s.contains("error:") {
        s.lines()
            .rev()
            .find(|l| !l.trim().is_empty())
            .map(str::to_string)
            .unwrap_or_default()
    } else {
        first_line(s)
    }
}

fn first_line(s: &str) -> String {
    s.lines().next().unwrap_or("").to_string()
}

fn tok(n: usize) -> String {
    if n < 1000 {
        return n.to_string();
    }
    format!("{:.1}k", n as f64 / 1000.0)
}

fn fmt_dur(d: std::time::Duration) -> String {
    let secs = d.as_secs_f64();
    if secs < 60.0 {
        return format!("{secs:.1}s");
    }
    let total = secs.round() as u64;
    format!("{}m{}s", total / 60, total % 60)
}

#[cfg(test)]
mod tests {
    use super::*;

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

    #[test]
    fn render_args_cases() {
        let cases: Vec<(&str, &str, &str)> = vec![
            ("bash", r#"{"command":"go test ./..."}"#, "go test ./..."),
            ("read", r#"{"path":"main.go"}"#, "main.go"),
            ("edit", r#"{"path":"a.go","old":"x","new":"y"}"#, "a.go"),
            (
                "write",
                r#"{"path":"b.txt","content":"hello"}"#,
                "b.txt (5 bytes)",
            ),
            ("custom", r#"{"q":1}"#, r#"{"q":1}"#),
            ("bash", "not json", "not json"),
        ];
        for (name, args, want) in cases {
            let call = ToolCall {
                id: String::new(),
                name: name.into(),
                arguments: args.into(),
            };
            assert_eq!(render_args(&call), want, "case {name}");
        }
    }

    #[test]
    fn tok_cases() {
        assert_eq!(tok(999), "999");
        assert_eq!(tok(1234), "1.2k");
    }

    #[test]
    fn render_result_cases() {
        let fail = "# pkg/a\n./a.go:12:3: undefined: Foo\nerror: exit status 1\n";
        assert_eq!(render_result(fail, false), "error: exit status 1");
        assert_eq!(render_result("ok ax 0.003s\nmore", false), "ok ax 0.003s");
        assert_eq!(render_result(fail, true), fail);
        assert_eq!(render_result("build error: bad\nnext", false), "next");
    }

    #[test]
    fn fmt_dur_cases() {
        assert_eq!(fmt_dur(std::time::Duration::from_millis(1500)), "1.5s");
        assert_eq!(fmt_dur(std::time::Duration::from_secs(65)), "1m5s");
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
    fn parse_args_fuzz() {
        let fc = FileConfig {
            api_key: String::new(),
            model: String::new(),
            base: String::new(),
        };
        let tokens = [
            "-base",
            "-model",
            "-system",
            "-C",
            "-r",
            "--resume",
            "--resume=",
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
    }
}
