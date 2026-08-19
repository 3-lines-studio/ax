//! Full-screen transcript TUI, replicating the vercel-labs/fx terminal UX:
//! user cards on a ┃ rail, streamed markdown, tool status lines, command
//! output rails, a bottom input bar with ❯ prompt, and a status hint row.

use crate::markdown::{self, Block, Markdown};
use crate::openai::{OpenAI, StreamEvent};
use crate::term::{Key, Terminal};
use crate::{Message, Tool, ToolCall, Usage};
use std::io::Write;
use std::cell::RefCell;
use std::rc::Rc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender};
use std::sync::Arc;
use std::time::Instant;

pub const VERSION: &str = env!("CARGO_PKG_VERSION");

const DIM: &str = "\x1b[38;5;245m";
const STATUSLINE: &str = "\x1b[38;5;245m";
const DIVIDER: &str = "\x1b[38;5;240m";
const PERMISSION_AUTO: &str = "\x1b[38;5;252m";
const SYSTEM_NOTICE_TEXT: &str = "\x1b[38;5;250m";
const SYSTEM_NOTICE_LABEL: &str = "\x1b[1;38;5;252m";
const RESET: &str = "\x1b[0m";
const USER_RAIL: &str = "\x1b[38;5;255m";

pub struct TuiConfig {
    pub base: String,
    pub model: String,
    pub system: String,
    pub dir: String,
    pub api_key: String,
}

enum Entry {
    Welcome,
    User(String),
    Text(String),
    Code(String),
    Table(String),
    Rule,
    Tool { active: bool, label: String },
    Output { stderr: bool, text: String },
    Notice(String),
}

pub fn run(cfg: TuiConfig) -> Result<(), String> {
    let mut term = Terminal::new()?;
    let (rows, cols) = term.size();
    let model_display = compact_model_label(&cfg.model);
    let mut tui = Tui {
        cfg,
        entries: Vec::new(),
        running: false,
        cancel: Arc::new(AtomicBool::new(false)),
        tx: None,
        rx: None,
        cur_text: None,
        md: None,
        md_pending: None,
        msgs: Vec::new(),
        activity: Activity::Idle,
        turn_start: Instant::now(),
        scroll: 0,
        input: Input::default(),
        model_display,
        sess_in: 0,
        sess_out: 0,
        want_quit: false,
        rows,
        cols,
        last_frame: Vec::new(),
    };
    tui.entries.push(Entry::Welcome);
    tui.msgs = load_session(&tui.cfg.dir, &mut tui.entries);
    let result = tui.loop_forever(&mut term);
    term.restore();
    if result.is_ok() {
        tui.dump_transcript();
    }
    result
}

struct Tui {
    cfg: TuiConfig,
    entries: Vec<Entry>,
    running: bool,
    cancel: Arc<AtomicBool>,
    tx: Option<Sender<TurnEvent>>,
    rx: Option<Receiver<TurnEvent>>,
    cur_text: Option<usize>,
    md: Option<Markdown>,
    md_pending: Option<Rc<RefCell<Vec<(String, Block)>>>>,
    msgs: Vec<Message>,
    activity: Activity,
    turn_start: Instant,
    scroll: usize,
    input: Input,
    model_display: String,
    sess_in: usize,
    sess_out: usize,
    want_quit: bool,
    rows: u16,
    cols: u16,
    last_frame: Vec<String>,
}

#[derive(PartialEq, Clone)]
enum Activity {
    Idle,
    Thinking,
    Tool(String),
    Streaming,
}

pub enum TurnEvent {
    AssistantDelta(String),
    AssistantDone,
    ToolStart(String),
    ToolResult {
        label: String,
        output: String,
        cancelled: bool,
    },
    Notice(String),
    End {
        messages: Vec<Message>,
        usage: Usage,
        err: Option<String>,
        cancelled: bool,
    },
}

impl Tui {
    fn loop_forever(&mut self, term: &mut Terminal) -> Result<(), String> {
        let stdin_fd = libc::STDIN_FILENO;
        loop {
            self.paint(term);
            let mut fds = [libc::pollfd {
                fd: stdin_fd,
                events: libc::POLLIN,
                revents: 0,
            }];
            unsafe {
                libc::poll(fds.as_mut_ptr(), 1, 100);
            }
            if fds[0].revents & libc::POLLIN != 0 {
                if !self.handle_key(term.read_key()?)? {
                    return Ok(());
                }
            }
            self.drain_events();
        }
    }

    fn handle_key(&mut self, key: Key) -> Result<bool, String> {
        match key {
            Key::CtrlC => {
                if self.running {
                    self.cancel.store(true, Ordering::Relaxed);
                    return Ok(true);
                }
                return Ok(false);
            }
            Key::Ctrl(c) => {
                if !self.handle_ctrl(c) {
                    return Ok(false);
                }
            }
            Key::Char(c) => self.input.insert(c),
            Key::Enter => {
                if !self.running {
                    self.submit();
                    if self.want_quit {
                        return Ok(false);
                    }
                }
            }
            Key::Tab => self.tab(),
            Key::Backspace => self.input.backspace(),
            Key::Delete => self.input.delete(),
            Key::Left => self.input.move_left(),
            Key::Right => self.input.move_right(),
            Key::Home => self.input.home(),
            Key::End => self.input.end(),
            Key::Up => self.input.history_prev(),
            Key::Down => self.input.history_next(),
            Key::PageUp => self.scroll_up(),
            Key::PageDown => self.scroll_down(),
            Key::Alt(_) | Key::Esc => self.input.esc(),
            Key::Paste(bytes) => self.input.paste(&bytes),
            Key::Eof => return Ok(false),
        }
        Ok(true)
    }

    fn submit(&mut self) {
        let v = self.input.take();
        if v.is_empty() {
            return;
        }
        if let Some(rest) = v.strip_prefix('/') {
            self.slash(rest);
            return;
        }
        self.scroll = 0;
        self.entries.push(Entry::User(v.clone()));
        self.input.history.push(v.clone());
        self.msgs.push(Message {
            role: "user".into(),
            content: v,
            tool_calls: Vec::new(),
            tool_call_id: String::new(),
        });
        self.start_turn();
    }

    fn start_turn(&mut self) {
        let msgs = self.msgs.clone();
        let (tx, rx) = std::sync::mpsc::channel();
        self.tx = Some(tx.clone());
        self.rx = Some(rx);
        self.running = true;
        self.cancel = Arc::new(AtomicBool::new(false));
        self.turn_start = Instant::now();
        self.activity = Activity::Thinking;
        self.cur_text = None;
        let cancel = self.cancel.clone();
        let provider = OpenAI::new(self.cfg.base.clone(), self.cfg.api_key.clone());
        let model = self.cfg.model.clone();
        let system = self.cfg.system.clone();
        let dir = self.cfg.dir.clone();
        let tools = build_tools(&dir);
        std::thread::spawn(move || {
            run_turn(provider, model, system, tools, msgs, cancel, tx);
        });
    }

    fn drain_events(&mut self) -> bool {
        let mut any = false;
        let rx = match self.rx.take() {
            Some(rx) => rx,
            None => return false,
        };
        let mut cur = self.cur_text;
        while let Ok(ev) = rx.try_recv() {
            any = true;
            match ev {
                TurnEvent::AssistantDelta(delta) => {
                    if self.md.is_none() {
                        let pending = Rc::new(RefCell::new(Vec::new()));
                        let p2 = pending.clone();
                        let mut md = Markdown::new();
                        md.set_on_block(move |b, out| {
                            p2.borrow_mut().push((std::mem::take(out), b));
                        });
                        self.md = Some(md);
                        self.md_pending = Some(pending);
                    }
                    let pending = self.md_pending.as_ref().unwrap().clone();
                    self.md.as_mut().unwrap().push(&delta);
                    for (drained, block) in pending.borrow_mut().drain(..) {
                        if let Some(i) = cur.take() {
                            self.entries[i] = Entry::Text(drained);
                        } else if !drained.is_empty() {
                            self.entries.push(Entry::Text(drained));
                        }
                        self.push_block(block);
                        cur = None;
                    }
                    let text = self.md.as_ref().unwrap().current_text().to_string();
                    if let Some(i) = cur {
                        if !text.is_empty() {
                            self.entries[i] = Entry::Text(text);
                        }
                    } else if !text.is_empty() {
                        self.entries.push(Entry::Text(text));
                        cur = Some(self.entries.len() - 1);
                    }
                    self.activity = Activity::Streaming;
                }
                TurnEvent::AssistantDone => {
                    if let Some(mut md) = self.md.take() {
                        let pending = self.md_pending.take();
                        let tail = md.finish();
                        if let Some(pending) = pending {
                            for (drained, block) in pending.borrow_mut().drain(..) {
                                if !drained.is_empty() {
                                    self.entries.push(Entry::Text(drained));
                                }
                                self.push_block(block);
                            }
                        }
                        if !tail.is_empty() {
                            if let Some(i) = cur.take() {
                                self.entries[i] = Entry::Text(tail);
                            } else {
                                self.entries.push(Entry::Text(tail));
                            }
                        }
                    }
                    cur = None;
                    self.activity = Activity::Thinking;
                }
                TurnEvent::ToolStart(label) => {
                    self.entries.push(Entry::Tool {
                        active: true,
                        label: label.clone(),
                    });
                    self.activity = Activity::Tool(label);
                }
                TurnEvent::ToolResult {
                    label,
                    output,
                    cancelled,
                } => {
                    for e in self.entries.iter_mut().rev() {
                        if let Entry::Tool { active, .. } = e {
                            if *active {
                                *e = Entry::Tool {
                                    active: false,
                                    label: label.clone(),
                                };
                                break;
                            }
                        }
                    }
                    if !cancelled {
                        self.entries.push(Entry::Output {
                            stderr: false,
                            text: output,
                        });
                    }
                    self.activity = Activity::Thinking;
                }
                TurnEvent::Notice(text) => {
                    self.entries.push(Entry::Notice(text));
                }
                TurnEvent::End {
                    messages,
                    usage,
                    err,
                    cancelled,
                } => {
                    self.running = false;
                    self.sess_in += usage.input;
                    self.sess_out += usage.output;
                    if let Some(mut md) = self.md.take() {
                        let pending = self.md_pending.take();
                        let tail = md.finish();
                        if let Some(pending) = pending {
                            for (drained, block) in pending.borrow_mut().drain(..) {
                                if !drained.is_empty() {
                                    self.entries.push(Entry::Text(drained));
                                }
                                self.push_block(block);
                            }
                        }
                        if !tail.is_empty() {
                            self.entries.push(Entry::Text(tail));
                        }
                    }
                    self.cur_text = None;
                    self.msgs = messages.clone();
                    if let Some(err) = err {
                        self.entries.push(Entry::Notice(format!("error: {err}")));
                    }
                    if cancelled {
                        self.entries.push(Entry::Notice("interrupted".into()));
                    }
                    save_session(&self.cfg.dir, &messages);
                    self.activity = Activity::Idle;
                }
            }
        }
        self.cur_text = cur;
        self.rx = Some(rx);
        any
    }

    fn push_block(&mut self, block: Block) {
        match block {
            Block::Code { language, code } => {
                self.entries.push(Entry::Code(markdown::render_code_block(
                    language.as_bytes(),
                    code.as_bytes(),
                )));
            }
            Block::Table(t) => self.entries.push(Entry::Table(t)),
            Block::Rule => self.entries.push(Entry::Rule),
        }
    }

    fn slash(&mut self, cmd: &str) {
        let (name, rest) = match cmd.split_once(' ') {
            Some((n, r)) => (n, r.trim()),
            None => (cmd, ""),
        };
        match name {
            "help" => {
                let help = "/help /clear /new /reset /resume /model /system /status /stats /version /quit";
                self.entries.push(Entry::Notice(format!("{SYSTEM_NOTICE_LABEL}help{RESET} {DIM}{help}{RESET}")));
            }
            "clear" => {
                self.entries.clear();
                self.entries.push(Entry::Welcome);
            }
            "new" | "reset" => {
                self.entries.clear();
                self.entries.push(Entry::Welcome);
                self.msgs.clear();
                self.sess_in = 0;
                self.sess_out = 0;
                save_session(&self.cfg.dir, &[]);
            }
            "resume" => {
                let before = self.entries.len();
                self.msgs = load_session(&self.cfg.dir, &mut self.entries);
                let n = self.entries.len() - before;
                self.entries.push(Entry::Notice(format!(
                    "{DIM}resumed {n} messages{RESET}"
                )));
            }
            "model" => {
                if !rest.is_empty() {
                    self.cfg.model = rest.to_string();
                    self.model_display = compact_model_label(rest);
                }
                self.entries.push(Entry::Notice(format!(
                    "{DIM}model: {}{RESET}",
                    self.cfg.model
                )));
            }
            "system" => {
                if !rest.is_empty() {
                    self.cfg.system = rest.to_string();
                }
                let s = if self.cfg.system.is_empty() {
                    "(default)".to_string()
                } else {
                    clip(&self.cfg.system, 120)
                };
                self.entries.push(Entry::Notice(format!(
                    "{DIM}system: {s}{RESET}"
                )));
            }
            "status" => {
                let key = if self.cfg.api_key.is_empty() {
                    "missing".to_string()
                } else {
                    "set".to_string()
                };
                let msg = format!(
                    "{DIM}base: {} · model: {} · dir: {} · key: {}{RESET}",
                    self.cfg.base, self.cfg.model, self.cfg.dir, key
                );
                self.entries.push(Entry::Notice(msg));
            }
            "stats" => {
                let msg = format!(
                    "{DIM}session · {} in / {} out{RESET}",
                    tok(self.sess_in),
                    tok(self.sess_out)
                );
                self.entries.push(Entry::Notice(msg));
            }
            "version" => {
                self.entries
                    .push(Entry::Notice(format!("{DIM}v{VERSION}{RESET}")));
            }
            "quit" | "exit" => {
                self.want_quit = true;
            }
            _ => {
                self.entries
                    .push(Entry::Notice(format!("{DIM}unknown command: /{name}{RESET}")));
            }
        }
        if name == "quit" || name == "exit" {
            return;
        }
        self.scroll = 0;
    }

    fn scroll_up(&mut self) {
        self.scroll = self.scroll.saturating_add(self.content_height() - 2);
    }

    fn scroll_down(&mut self) {
        self.scroll = self.scroll.saturating_sub(self.content_height() - 2);
    }

    fn content_height(&self) -> usize {
        (self.rows as usize).saturating_sub(if self.picker_active() { 4 } else { 2 })
    }

    fn picker_active(&self) -> bool {
        self.input.buf().starts_with('/') && slash_matches(&self.input.buf()).len() > 1
    }

    fn paint(&mut self, term: &mut Terminal) {
        let (rows, cols) = term.size();
        if rows != self.rows || cols != self.cols {
            self.rows = rows;
            self.cols = cols;
            self.last_frame.clear();
            let _ = term.out().write_all(crate::term::clear_screen().as_bytes());
        }
        let frame = self.compose();
        self.emit(term, &frame);
    }

    fn emit(&mut self, term: &mut Terminal, frame: &Frame) {
        let out = term.out();
        for (i, row) in frame.rows.iter().enumerate() {
            let prev = self.last_frame.get(i).map(|s| s.as_str()).unwrap_or("");
            if prev != row {
                let _ = out.write_all(
                    format!("\x1b[{};1H{row}{}", i + 1, crate::term::clear_eol()).as_bytes(),
                );
            }
        }
        if self.last_frame.len() > frame.rows.len() {
            for i in frame.rows.len()..self.last_frame.len() {
                let _ = out.write_all(
                    format!("\x1b[{};1H{}", i + 1, crate::term::clear_eol()).as_bytes(),
                );
            }
        }
        self.last_frame = frame.rows.clone();
        let _ = out.write_all(
            format!(
                "\x1b[{};{}H{}",
                frame.input_row, frame.input_col, crate::term::cursor_visible()
            )
            .as_bytes(),
        );
        let _ = out.flush();
    }

    fn compose(&self) -> Frame {
        let width = self.cols as usize;
        let mut rows: Vec<String> = Vec::new();
        for entry in &self.entries {
            match entry {
                Entry::Welcome => rows.push(format!(
                    "{SYSTEM_NOTICE_LABEL}𝒂x{RESET}{DIM} v{VERSION} · Run /help for commands{RESET}"
                )),
                Entry::User(text) => {
                    for line in wrap_text(text, width.saturating_sub(2)) {
                        rows.push(format!("{USER_RAIL}┃{RESET} {BOLD}{line}{RESET}"));
                    }
                }
                Entry::Text(t) => rows.extend(wrap_ansi(t, width)),
                Entry::Code(c) => rows.extend(wrap_ansi(c, width)),
                Entry::Table(t) => rows.extend(wrap_ansi(t, width)),
                Entry::Rule => rows.push(format!(
                    "{DIM}{}{RESET}",
                    "\u{2500}".repeat(markdown::ansi::HORIZONTAL_RULE_WIDTH)
                )),
                Entry::Tool { active, label } => {
                    if *active {
                        rows.push(format!("● {label}"));
                    } else {
                        rows.push(format!("{SYSTEM_NOTICE_TEXT}●{RESET} {label}"));
                    }
                }
                Entry::Output { stderr, text } => {
                    for line in text.lines() {
                        let style = if *stderr { SYSTEM_NOTICE_TEXT } else { DIM };
                        rows.push(format!("{style}│ {line}{RESET}"));
                    }
                }
                Entry::Notice(text) => rows.push(text.clone()),
            }
        }
        // Activity line while running.
        match &self.activity {
            Activity::Thinking => {
                let secs = self.turn_start.elapsed().as_secs();
                rows.push(format!("{PERMISSION_AUTO}• Thinking ({secs}s){RESET}"));
            }
            Activity::Tool(label) => {
                rows.push(format!("{PERMISSION_AUTO}●{RESET} {label}"));
            }
            _ => {}
        }
        let content_height = self.content_height().max(1);
        let total = rows.len();
        let start = total.saturating_sub(content_height + self.scroll.min(total));
        let mut visible = rows[start..].to_vec();
        if visible.len() < content_height {
            visible.resize(content_height, String::new());
        }

        let mut frame = Frame {
            rows: Vec::new(),
            input_row: 0,
            input_col: 0,
        };
        let picker = self.picker_active();
        frame.rows.extend(visible);
        // input line
        let (input_line, cursor_col) = self.input.render(width);
        frame.input_row = content_height as u16 + 1;
        frame.input_col = (cursor_col + 1).min(width) as u16;
        frame.rows.push(input_line);
        if picker {
            frame.rows.push(format!(
                "{DIVIDER}{}{RESET}",
                "\u{2500}".repeat(width)
            ));
            frame.rows.push(slash_completion_line(&self.input.buf(), width));
        }
        // hint line
        frame.rows.push(format!(
            "{PERMISSION_AUTO}auto{RESET}{STATUSLINE} · {}{RESET}",
            self.model_display
        ));
        frame
    }

    fn dump_transcript(&self) {
        let width = self.cols as usize;
        let mut out = String::new();
        let mut last_nl = true;
        for entry in &self.entries {
            let mut chunk = String::new();
            match entry {
                Entry::Welcome => continue,
                Entry::User(text) => {
                    for line in wrap_text(text, width.saturating_sub(2)) {
                        chunk.push_str(&format!("{USER_RAIL}┃{RESET} {BOLD}{line}{RESET}\n"));
                    }
                }
                Entry::Text(t) => chunk.push_str(t),
                Entry::Code(c) => chunk.push_str(c),
                Entry::Table(t) => chunk.push_str(t),
                Entry::Rule => continue,
                Entry::Tool { label, .. } => {
                    chunk.push_str(&format!("● {label}\n"));
                }
                Entry::Output { stderr, text } => {
                    for line in text.lines() {
                        let style = if *stderr { SYSTEM_NOTICE_TEXT } else { DIM };
                        chunk.push_str(&format!("{style}│ {line}{RESET}\n"));
                    }
                }
                Entry::Notice(text) => chunk.push_str(&format!("{text}\n")),
            }
            if chunk.is_empty() {
                continue;
            }
            if !last_nl {
                out.push('\n');
            }
            out.push_str(&chunk);
            last_nl = chunk.ends_with('\n');
        }
        print!("{out}");
    }

    fn tab(&mut self) {
        self.input.tab();
    }

    fn handle_ctrl(&mut self, c: char) -> bool {
        match c {
            'a' => self.input.home(),
            'e' => self.input.end(),
            'w' => self.input.delete_word_left(),
            'u' => {
                let chars: Vec<char> = self.input.buf.chars().collect();
                self.input.buf = chars[self.input.cursor..].iter().collect();
                self.input.cursor = 0;
            }
            'k' => {
                let chars: Vec<char> = self.input.buf.chars().collect();
                self.input.buf = chars[..self.input.cursor].iter().collect();
            }
            'd' => {
                if !self.running && self.input.buf.is_empty() {
                    return false;
                }
            }
            'l' => {
                self.entries.clear();
                self.entries.push(Entry::Welcome);
            }
            _ => {}
        }
        true
    }
}

const BOLD: &str = "\x1b[1m";

struct Frame {
    rows: Vec<String>,
    input_row: u16,
    input_col: u16,
}

#[derive(Default)]
struct Input {
    buf: String,
    cursor: usize,
    history: Vec<String>,
    hist_idx: Option<usize>,
    scroll: usize,
}
impl Input {
    fn buf(&self) -> &str {
        &self.buf
    }

    fn take(&mut self) -> String {
        let v = std::mem::take(&mut self.buf);
        self.cursor = 0;
        self.scroll = 0;
        self.hist_idx = None;
        v
    }

    fn insert(&mut self, c: char) {
        self.buf.insert(self.cursor, c);
        self.cursor += 1;
    }

    fn backspace(&mut self) {
        if self.cursor > 0 {
            self.cursor -= 1;
            self.buf.remove(self.cursor);
        }
    }

    fn delete(&mut self) {
        if self.cursor < self.buf.chars().count() {
            self.buf.remove(self.cursor);
        }
    }

    fn move_left(&mut self) {
        self.cursor = self.cursor.saturating_sub(1);
    }

    fn move_right(&mut self) {
        if self.cursor < self.buf.chars().count() {
            self.cursor += 1;
        }
    }

    fn home(&mut self) {
        self.cursor = 0;
    }

    fn end(&mut self) {
        self.cursor = self.buf.chars().count();
    }

    fn history_prev(&mut self) {
        if self.history.is_empty() {
            return;
        }
        let idx = match self.hist_idx {
            Some(i) if i > 0 => i - 1,
            Some(_) => return,
            None => self.history.len() - 1,
        };
        self.hist_idx = Some(idx);
        self.buf = self.history[idx].clone();
        self.cursor = self.buf.chars().count();
    }

    fn history_next(&mut self) {
        match self.hist_idx {
            Some(i) if i + 1 < self.history.len() => {
                self.hist_idx = Some(i + 1);
                self.buf = self.history[i + 1].clone();
                self.cursor = self.buf.chars().count();
            }
            Some(_) => {
                self.hist_idx = None;
                self.buf.clear();
                self.cursor = 0;
            }
            None => {}
        }
    }

    fn esc(&mut self) {
        self.hist_idx = None;
    }

    fn paste(&mut self, bytes: &[u8]) {
        let s = String::from_utf8_lossy(bytes);
        for c in s.chars() {
            if c == '\n' || c == '\r' {
                continue;
            }
            self.insert(c);
        }
    }

    fn tab(&mut self) {
        if !self.buf.starts_with('/') {
            return;
        }
        let matches = slash_matches(&self.buf);
        if matches.is_empty() {
            return;
        }
        if matches.len() == 1 {
            self.buf = matches[0].to_string() + " ";
            self.cursor = self.buf.chars().count();
        } else {
            let current = self.buf.trim();
            let pos = matches
                .iter()
                .position(|m| m == current)
                .unwrap_or(usize::MAX);
            let next = matches[(pos + 1) % matches.len()].clone();
            self.buf = next.to_string();
            self.cursor = self.buf.chars().count();
        }
    }

    fn delete_word_left(&mut self) {
        let chars: Vec<char> = self.buf.chars().collect();
        let mut i = self.cursor;
        while i > 0 && chars[i - 1] == ' ' {
            i -= 1;
        }
        while i > 0 && chars[i - 1] != ' ' {
            i -= 1;
        }
        self.buf = chars[..i].iter().chain(chars[self.cursor..].iter()).collect();
        self.cursor = i;
    }

    fn render(&self, width: usize) -> (String, usize) {
        let chars: Vec<char> = self.buf.chars().collect();
        let mut scroll = self.scroll;
        if self.cursor < scroll {
            scroll = self.cursor;
        }
        let visible: String = chars
            .iter()
            .skip(scroll)
            .take(width.saturating_sub(2))
            .collect();
        let cursor_col = 2 + self.cursor.saturating_sub(scroll);
        (format!("❯ {visible}"), cursor_col)
    }
}

fn slash_matches(input: &str) -> Vec<String> {
    const COMMANDS: &[&str] = &[
        "/help",
        "/clear",
        "/new",
        "/reset",
        "/resume",
        "/model",
        "/system",
        "/status",
        "/stats",
        "/version",
        "/quit",
    ];
    let token = input.trim();
    COMMANDS
        .iter()
        .filter(|c| c.starts_with(token) || (token.is_empty() && input.starts_with('/')))
        .map(|s| s.to_string())
        .collect()
}

fn slash_completion_line(input: &str, width: usize) -> String {
    let matches = slash_matches(input);
    let line = matches.join("  ");
    format!("{DIM}{}{RESET}", clip(&line, width))
}

fn wrap_text(text: &str, width: usize) -> Vec<String> {
    if width == 0 {
        return vec![String::new()];
    }
    let mut rows = Vec::new();
    for line in text.split('\n') {
        let mut cur = String::new();
        let mut w = 0usize;
        for c in line.chars() {
            let cw = char_width(c);
            if w + cw > width && !cur.is_empty() {
                rows.push(std::mem::take(&mut cur));
                w = 0;
            }
            cur.push(c);
            w += cw;
        }
        rows.push(cur);
    }
    rows
}

fn char_width(c: char) -> usize {
    if c.is_ascii() {
        1
    } else {
        let cp = c as u32;
        if (0x1100..=0x115f).contains(&cp)
            || (0x2e80..=0xa4cf).contains(&cp)
            || (0xac00..=0xd7a3).contains(&cp)
            || (0xf900..=0xfaff).contains(&cp)
            || (0xfe30..=0xfe4f).contains(&cp)
            || (0xff00..=0xff60).contains(&cp)
            || (0x1f300..=0x1f64f).contains(&cp)
            || (0x1f900..=0x1f9ff).contains(&cp)
            || (0x20000..=0x2fffd).contains(&cp)
        {
            2
        } else {
            1
        }
    }
}

fn ansi_seq_end(bytes: &[u8], start: usize) -> usize {
    if start >= bytes.len() || bytes[start] != 0x1b {
        return start + 1;
    }
    let mut i = start + 1;
    if i < bytes.len() && bytes[i] == b'[' {
        i += 1;
        while i < bytes.len() && !(0x40..=0x7e).contains(&bytes[i]) {
            i += 1;
        }
        return (i + 1).min(bytes.len());
    }
    if i < bytes.len() && bytes[i] == b']' {
        i += 1;
        while i < bytes.len() {
            if bytes[i] == 0x07 {
                return i + 1;
            }
            if bytes[i] == 0x1b && i + 1 < bytes.len() && bytes[i + 1] == b'\\' {
                return i + 2;
            }
            i += 1;
        }
        return bytes.len();
    }
    start + 1
}

fn sgr_kind(seq: &str) -> Option<&'static str> {
    if !seq.starts_with("\x1b[") || !seq.ends_with('m') {
        return None;
    }
    let body = &seq[2..seq.len() - 1];
    Some(match body {
        "0" => "reset",
        "1" => "bold",
        "2" => "dim",
        "3" => "italic",
        "4" => "underline",
        "9" => "strike",
        "22" => "bold-off",
        "23" => "italic-off",
        "24" => "underline-off",
        "29" => "strike-off",
        "39" => "fg-off",
        _ if body.starts_with("38;") => "fg",
        _ => "other",
    })
}

pub fn wrap_ansi(text: &str, width: usize) -> Vec<String> {
    if width == 0 {
        return vec![String::new()];
    }
    let mut rows: Vec<String> = Vec::new();
    // Split into logical lines first: in raw mode an embedded \n moves down
    // at the current column, so each line must be emitted as its own row.
    // A trailing newline ends the last line, not an extra blank row.
    let text = text.strip_suffix('\n').unwrap_or(text);
    for part in text.split('\n') {
        let part = part.strip_suffix('\r').unwrap_or(part);
        if part.is_empty() {
            rows.push(String::new());
            continue;
        }
        wrap_ansi_line(&mut rows, part.to_string(), width);
    }
    rows
}

fn wrap_ansi_line(rows: &mut Vec<String>, text: String, width: usize) {
    let mut cur = String::new();
    let mut cur_width = 0usize;
    let mut active: Vec<&'static str> = Vec::new();
    let bytes = text.as_bytes();
    let mut i = 0usize;
    while i < bytes.len() {
        if bytes[i] == 0x1b {
            let end = ansi_seq_end(bytes, i);
            let seq = &text[i..end];
            if let Some(kind) = sgr_kind(seq) {
                match kind {
                    "reset" => active.clear(),
                    "bold" | "dim" | "italic" | "underline" | "strike" | "fg" => {
                        if !active.contains(&kind) {
                            active.push(kind);
                        }
                    }
                    "bold-off" => active.retain(|k| *k != "bold"),
                    "italic-off" => active.retain(|k| *k != "italic"),
                    "underline-off" => active.retain(|k| *k != "underline"),
                    "strike-off" => active.retain(|k| *k != "strike"),
                    "fg-off" => active.retain(|k| *k != "fg"),
                    _ => {}
                }
            }
            cur.push_str(seq);
            i = end;
            continue;
        }
        let len = utf8_len(bytes[i]);
        let ch = &text[i..i + len];
        let w = char_width(ch.chars().next().unwrap_or(' '));
        if cur_width + w > width && cur_width > 0 {
            rows.push(std::mem::take(&mut cur));
            for kind in &active {
                cur.push_str(sgr_for(kind));
            }
            cur_width = 0;
        }
        cur.push_str(ch);
        cur_width += w;
        i += len;
    }
    rows.push(cur);
}

fn sgr_for(kind: &str) -> &'static str {
    match kind {
        "bold" => "\x1b[1m",
        "dim" => "\x1b[2m",
        "italic" => "\x1b[3m",
        "underline" => "\x1b[4m",
        "strike" => "\x1b[9m",
        "fg" => "\x1b[38;5;250m",
        _ => "",
    }
}

fn utf8_len(first: u8) -> usize {
    if first < 0x80 {
        1
    } else if first >> 5 == 0b110 {
        2
    } else if first >> 4 == 0b1110 {
        3
    } else if first >> 3 == 0b11110 {
        4
    } else {
        1
    }
}

fn compact_model_label(model: &str) -> String {
    let bare = model.rsplit('/').next().unwrap_or(model);
    if let Some(rest) = bare.strip_prefix("claude-") {
        for (prefix, label) in [
            ("opus-", "opus "),
            ("sonnet-", "sonnet "),
            ("haiku-", "haiku "),
        ] {
            if let Some(tail) = rest.strip_prefix(prefix) {
                return format!("{label}{tail}");
            }
        }
        return rest.to_string();
    }
    bare.to_string()
}

fn clip(s: &str, n: usize) -> String {
    let chars: Vec<char> = s.chars().collect();
    if chars.len() <= n {
        return s.to_string();
    }
    let head: String = chars.into_iter().take(n).collect();
    format!("{head}…")
}

fn tok(n: usize) -> String {
    if n < 1000 {
        return n.to_string();
    }
    format!("{:.1}k", n as f64 / 1000.0)
}

fn build_tools(dir: &str) -> Vec<Tool> {
    vec![
        crate::tools::read(),
        crate::tools::write(),
        crate::tools::edit(),
        crate::tools::bash(dir),
    ]
}

fn tool_label(call: &ToolCall, running: bool) -> String {
    #[derive(serde::Deserialize)]
    struct Args {
        path: Option<String>,
        command: Option<String>,
    }
    let args: Option<Args> = serde_json::from_str(&call.arguments).ok();
    let path = args.as_ref().and_then(|a| a.path.clone()).unwrap_or_default();
    let command = args.as_ref().and_then(|a| a.command.clone()).unwrap_or_default();
    match call.name.as_str() {
        "bash" => {
            let cmd = command.split_whitespace().collect::<Vec<_>>().join(" ");
            let cmd = clip(&cmd, 120);
            if running {
                format!("Running {cmd}")
            } else {
                format!("Ran {cmd}")
            }
        }
        "read" => format!("{} {path}", if running { "Reading" } else { "Read" }),
        "write" => format!("{} {path}", if running { "Writing" } else { "Wrote" }),
        "edit" => format!("{} {path}", if running { "Editing" } else { "Edited" }),
        _ => format!("Working: {}", call.name),
    }
}

fn run_turn(
    provider: OpenAI,
    model: String,
    system: String,
    tools: Vec<Tool>,
    msgs: Vec<Message>,
    cancel: Arc<AtomicBool>,
    tx: Sender<TurnEvent>,
) {
    let mut h = msgs;
    let mut usage = Usage::default();
    let mut err: Option<String> = None;
    let mut cancelled = false;
    for _turn in 0..20 {
        if cancel.load(Ordering::Relaxed) {
            cancelled = true;
            break;
        }
        let (resp, calls) = match stream_request(&provider, &model, &system, &h, &tools, &cancel, &tx) {
            Ok(x) => x,
            Err(e) => {
                if cancel.load(Ordering::Relaxed) {
                    cancelled = true;
                } else {
                    err = Some(e);
                }
                break;
            }
        };
        usage = Usage {
            input: usage.input + resp.usage.input,
            output: usage.output + resp.usage.output,
        };
        h.push(resp.message);
        let _ = tx.send(TurnEvent::AssistantDone);
        if calls.is_empty() {
            break;
        }
        for call in calls {
            if cancel.load(Ordering::Relaxed) {
                cancelled = true;
                break;
            }
            let label = tool_label(&call, true);
            let _ = tx.send(TurnEvent::ToolStart(label.clone()));
            let output = exec_tool(&tools, &call);
            let _ = tx.send(TurnEvent::ToolResult {
                label: tool_label(&call, false),
                output: output.clone(),
                cancelled: false,
            });
            h.push(Message {
                role: "tool".into(),
                content: output,
                tool_calls: Vec::new(),
                tool_call_id: call.id,
            });
        }
        if cancelled {
            break;
        }
    }
    if !cancelled && err.is_none() && h.len() > 0 && h.last().map(|m| !m.tool_calls.is_empty()).unwrap_or(false) {
        // max turns with dangling tool calls
        err = Some("stopped: max turns reached".into());
    }
    let _ = tx.send(TurnEvent::End {
        messages: h,
        usage,
        err,
        cancelled,
    });
}

fn stream_request(
    provider: &OpenAI,
    model: &str,
    system: &str,
    h: &[Message],
    tools: &[Tool],
    cancel: &Arc<AtomicBool>,
    tx: &Sender<TurnEvent>,
) -> Result<(crate::Response, Vec<ToolCall>), String> {
    let (etx, erx) = std::sync::mpsc::channel();
    let req = crate::Request {
        model,
        system,
        messages: h,
        tools,
    };
    let handle = provider.complete_stream(&req, cancel, etx);
    let mut calls = Vec::new();
    while let Ok(ev) = erx.recv() {
        match ev {
            StreamEvent::Content(d) => {
                let _ = tx.send(TurnEvent::AssistantDelta(d));
            }
            StreamEvent::ToolCall(c) => calls.push(c),
            StreamEvent::Done => break,
        }
    }
    let resp = handle
        .join()
        .map_err(|_| "request thread panicked".to_string())?
        .map_err(|e| e.to_string())?;
    Ok((resp, calls))
}

fn exec_tool(tools: &[Tool], call: &ToolCall) -> String {
    for t in tools {
        if t.name == call.name {
            return (t.run)(&call.arguments);
        }
    }
    format!("error: unknown tool: {}", call.name)
}

fn session_path(dir: &str) -> std::path::PathBuf {
    std::path::PathBuf::from(dir).join(".ax").join("session.jsonl")
}

fn save_session(dir: &str, msgs: &[Message]) {
    let p = session_path(dir);
    if let Some(parent) = p.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    let mut b = Vec::new();
    for m in msgs {
        if let Ok(line) = serde_json::to_string(m) {
            b.extend_from_slice(line.as_bytes());
            b.push(b'\n');
        }
    }
    let _ = std::fs::write(p, b);
}

fn load_session(dir: &str, entries: &mut Vec<Entry>) -> Vec<Message> {
    let mut msgs = Vec::new();
    let b = match std::fs::read(session_path(dir)) {
        Ok(b) => b,
        Err(_) => return msgs,
    };
    for line in String::from_utf8_lossy(&b).lines() {
        if line.is_empty() {
            continue;
        }
        let Ok(m) = serde_json::from_str::<Message>(line) else {
            continue;
        };
        msgs.push(m.clone());
        match m.role.as_str() {
            "user" => entries.push(Entry::User(m.content)),
            "assistant" => {
                if !m.content.is_empty() {
                    entries.push(Entry::Text(markdown::Markdown::render(&m.content)));
                }
                for c in &m.tool_calls {
                    entries.push(Entry::Tool {
                        active: false,
                        label: tool_label(c, false),
                    });
                }
            }
            "tool" => {
                if !m.content.is_empty() {
                    entries.push(Entry::Output {
                        stderr: false,
                        text: m.content,
                    });
                }
            }
            _ => {}
        }
    }
    msgs
}
