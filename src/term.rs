//! Raw terminal layer: termios, key parsing (CSI modifiers, SGR mouse),
//! window size, bracketed paste, scroll regions, alternate screen.

#![allow(unsafe_code)]

use std::io::Write;

pub struct Terminal {
    original: libc::termios,
    out: std::io::Stdout,
}

impl Terminal {
    pub fn new() -> Result<Terminal, String> {
        let fd = std::io::stdout().as_raw_fd();
        let mut original: libc::termios = unsafe { std::mem::zeroed() };
        if unsafe { libc::tcgetattr(fd, &mut original) } != 0 {
            return Err("tcgetattr failed".into());
        }
        let mut raw = original;
        raw.c_lflag &= !(libc::ECHO | libc::ICANON | libc::ISIG | libc::IEXTEN);
        raw.c_iflag &= !(libc::IXON | libc::ICRNL | libc::INLCR | libc::IGNCR | libc::BRKINT | libc::PARMRK);
        raw.c_oflag &= !libc::OPOST;
        raw.c_cflag |= libc::CS8;
        raw.c_cc[libc::VMIN] = 1;
        raw.c_cc[libc::VTIME] = 0;
        if unsafe { libc::tcsetattr(fd, libc::TCSANOW, &raw) } != 0 {
            return Err("tcsetattr failed".into());
        }
        let mut out = std::io::stdout();
        let _ = out.write_all(b"\x1b[?2004h"); // bracketed paste on
        let _ = out.write_all(b"\x1b[?25l"); // hide cursor (we draw it)
        let _ = out.flush();
        Ok(Terminal { original, out })
    }

    pub fn restore(&mut self) {
        let _ = self.out.write_all(b"\x1b[?2004l");
        let _ = self.out.write_all(b"\x1b[?25h");
        let _ = self.out.write_all(b"\x1b[0m\x1b[?1000l\x1b[?1002l\x1b[?1006l\x1b[?1049l\x1b[?25h");
        let _ = self.out.flush();
        let fd = std::io::stdout().as_raw_fd();
        unsafe {
            libc::tcsetattr(fd, libc::TCSANOW, &self.original);
        }
    }

    pub fn size(&self) -> (u16, u16) {
        let mut ws = libc::winsize {
            ws_row: 0,
            ws_col: 0,
            ws_xpixel: 0,
            ws_ypixel: 0,
        };
        let fd = std::io::stdout().as_raw_fd();
        if unsafe { libc::ioctl(fd, libc::TIOCGWINSZ, &mut ws) } != 0 {
            return (24, 80);
        }
        if ws.ws_row == 0 || ws.ws_col == 0 {
            return (24, 80);
        }
        (ws.ws_row, ws.ws_col)
    }

    pub fn out(&mut self) -> &mut std::io::Stdout {
        &mut self.out
    }

    pub fn read_key(&mut self) -> Result<Key, String> {
        let fd = libc::STDIN_FILENO;
        loop {
            let b = read_byte(fd)?;
            match b {
                0x03 => return Ok(Key::CtrlC),
                0x0d | 0x0a => return Ok(Key::Enter),
                0x09 => return Ok(Key::Tab),
                0x7f | 0x08 => return Ok(Key::Backspace),
                0x1b => {
                    // Possible escape sequence. Peek with a short timeout so a
                    // lone Esc does not block waiting for a next byte. Reads
                    // go through libc so the kernel buffer is never pre-consumed
                    // by stdio read-ahead.
                    let mut pfd = [libc::pollfd {
                        fd,
                        events: libc::POLLIN,
                        revents: 0,
                    }];
                    unsafe {
                        libc::poll(pfd.as_mut_ptr(), 1, 30);
                    }
                    if pfd[0].revents & libc::POLLIN == 0 {
                        return Ok(Key::Esc);
                    }
                    let next = read_byte(fd)?;
                    match next {
                        b'[' => return self.read_csi(),
                        b'O' => {
                            let c = read_byte(fd)?;
                            return Ok(match c {
                                b'H' => Key::Home,
                                b'F' => Key::End,
                                b'A' => Key::Up,
                                b'B' => Key::Down,
                                b'C' => Key::Right,
                                b'D' => Key::Left,
                                _ => Key::Esc,
                            });
                        }
                        c if c < 0x80 => {
                            return Ok(Key::Alt(c as char));
                        }
                        _ => return Ok(Key::Esc),
                    }
                }
                b if b < 0x20 => return Ok(Key::Ctrl(b as char)),
                0x20..=0x7e => return Ok(Key::Char(b as char)),
                _ => {
                    // UTF-8 lead byte: read continuation bytes.
                    let len = utf8_len(b);
                    let mut bytes = vec![b];
                    for _ in 1..len {
                        match read_byte(fd) {
                            Ok(c) => bytes.push(c),
                            Err(_) => break,
                        }
                    }
                    let s = String::from_utf8_lossy(&bytes).into_owned();
                    if let Some(ch) = s.chars().next() {
                        return Ok(Key::Char(ch));
                    }
                }
            }
        }
    }

    fn read_csi(&mut self) -> Result<Key, String> {
        let fd = libc::STDIN_FILENO;
        let mut bytes = Vec::new();
        loop {
            let b = read_byte(fd)?;
            bytes.push(b);
            if (0x40..=0x7e).contains(&b) {
                break;
            }
            if bytes.len() > 32 {
                break;
            }
        }
        // Bracketed paste: ESC [ 200 ~ ... ESC [ 201 ~
        if bytes == b"200~" {
            let mut content = Vec::new();
            loop {
                let b = read_byte(fd)?;
                if b == 0x1b {
                    // Expect ESC [ 201 ~
                    let mut tail = vec![b];
                    loop {
                        let c = read_byte(fd)?;
                        tail.push(c);
                        if tail == b"\x1b[201~" {
                            break;
                        }
                        if tail.len() > 8 {
                            break;
                        }
                    }
                    if tail == b"\x1b[201~" {
                        return Ok(Key::Paste(content));
                    }
                    content.extend_from_slice(&tail);
                    continue;
                }
                content.push(b);
            }
        }
        if bytes == b"201~" {
            return Ok(Key::PasteEnd);
        }
        // SGR mouse: ESC [ < b ; x ; y M/m
        if bytes[0] == b'<' {
            return Ok(self.decode_mouse(&bytes));
        }
        let final_byte = bytes[bytes.len() - 1];
        let params: Vec<&[u8]> = bytes[..bytes.len() - 1].split(|b| *b == b';').collect();
        let num = |i: usize| -> Option<u32> {
            params
                .get(i)
                .and_then(|p| std::str::from_utf8(p).ok())
                .and_then(|s| s.parse::<u32>().ok())
        };
        let mods = num(1).unwrap_or(0) as u8;
        let ctrl = mods & 4 != 0;
        let alt = mods & 2 != 0;
        let shift = mods & 1 != 0;
        let arrow = |up: bool| -> Key {
            if ctrl {
                if up {
                    Key::CtrlUp
                } else {
                    Key::CtrlDown
                }
            } else if alt {
                if up {
                    Key::AltUp
                } else {
                    Key::AltDown
                }
            } else if up {
                Key::Up
            } else {
                Key::Down
            }
        };
        match final_byte {
            b'A' => return Ok(arrow(true)),
            b'B' => return Ok(arrow(false)),
            b'C' => {
                return Ok(match (ctrl, alt, shift) {
                    (true, _, _) => Key::CtrlRight,
                    (_, true, _) => Key::AltRight,
                    (false, false, true) => Key::Right,
                    _ => Key::Right,
                })
            }
            b'D' => {
                return Ok(match (ctrl, alt, shift) {
                    (true, _, _) => Key::CtrlLeft,
                    (_, true, _) => Key::AltLeft,
                    (false, false, true) => Key::Left,
                    _ => Key::Left,
                })
            }
            b'H' => {
                if ctrl {
                    return Ok(Key::CtrlHome);
                }
                return Ok(Key::Home);
            }
            b'F' => {
                if ctrl {
                    return Ok(Key::CtrlEnd);
                }
                return Ok(Key::End);
            }
            b'Z' => return Ok(Key::ShiftTab),
            b'u' => {
                // Kitty CSI-u: ESC [ <key> ; <mod> u
                return Ok(match (num(0), mods) {
                    (Some(13), 2) => Key::ShiftEnter,
                    (Some(13), _) => Key::Enter,
                    (Some(127), 2) => Key::Backspace,
                    (Some(127), _) => Key::Backspace,
                    (Some(9), 2) => Key::ShiftTab,
                    (Some(9), _) => Key::Tab,
                    _ => Key::Esc,
                });
            }
            b'~' => {
                return Ok(match num(0) {
                    Some(1) | Some(7) => Key::Home,
                    Some(3) => Key::Delete,
                    Some(4) | Some(8) => Key::End,
                    Some(5) => Key::PageUp,
                    Some(6) => Key::PageDown,
                    Some(15) => Key::Ctrl(char::from(15)), // F5 -> toggle (Ctrl+O)
                    _ => Key::Esc,
                })
            }
            b'M' | b'm' => Ok(Key::Paste(bytes.clone())),
            _ => Ok(Key::Esc),
        }
    }

    fn decode_mouse(&self, bytes: &[u8]) -> Key {
        // ESC [ < cb ; x ; y M|m
        let body = &bytes[1..bytes.len() - 1];
        let mut parts = body.split(|b| *b == b';');
        let cb = parts
            .next()
            .and_then(|p| std::str::from_utf8(p).ok())
            .and_then(|s| s.parse::<u32>().ok())
            .unwrap_or(0);
        let x = parts
            .next()
            .and_then(|p| std::str::from_utf8(p).ok())
            .and_then(|s| s.parse::<u16>().ok())
            .unwrap_or(0);
        let y = parts
            .next()
            .and_then(|p| std::str::from_utf8(p).ok())
            .and_then(|s| s.parse::<u16>().ok())
            .unwrap_or(0);
        let release = bytes.last() == Some(&b'm');
        match cb {
            64 => Key::WheelUp,
            65 => Key::WheelDown,
            66 => Key::WheelLeft,
            67 => Key::WheelRight,
            0 | 32 | 1 | 33 | 2 | 34 | 3 | 35 => {
                if release {
                    Key::MouseRelease
                } else {
                    Key::MousePress(x, y)
                }
            }
            _ => Key::MouseOther,
        }
    }
}

fn read_byte(fd: i32) -> Result<u8, String> {
    let mut b = [0u8; 1];
    let n = unsafe { libc::read(fd, b.as_mut_ptr() as *mut libc::c_void, 1) };
    if n < 0 {
        return Err(format!("read: {}", std::io::Error::last_os_error()));
    }
    if n == 0 {
        return Err("eof".into());
    }
    Ok(b[0])
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

pub trait RawFd {
    fn as_raw_fd(&self) -> i32;
}

impl RawFd for std::io::Stdout {
    fn as_raw_fd(&self) -> i32 {
        libc::STDOUT_FILENO
    }
}

#[derive(Clone, Debug, PartialEq)]
pub enum Key {
    Char(char),
    Ctrl(char),
    Alt(char),
    Enter,
    ShiftEnter,
    Tab,
    ShiftTab,
    Backspace,
    Delete,
    Up,
    Down,
    Left,
    Right,
    Home,
    End,
    CtrlHome,
    CtrlEnd,
    PageUp,
    PageDown,
    CtrlUp,
    CtrlDown,
    CtrlLeft,
    CtrlRight,
    AltUp,
    AltDown,
    AltLeft,
    AltRight,
    WheelUp,
    WheelDown,
    WheelLeft,
    WheelRight,
    MousePress(u16, u16),
    MouseRelease,
    MouseOther,
    Esc,
    CtrlC,
    Eof,
    Paste(Vec<u8>),
    PasteStart,
    PasteEnd,
}

/// ANSI helpers for composing frames.
pub fn move_to(row: u16, col: u16) -> String {
    format!("\x1b[{row};{col}H")
}

pub fn clear_eol() -> &'static str {
    "\x1b[K"
}

pub fn clear_display() -> &'static str {
    "\x1b[2J"
}

pub fn cursor_visible() -> &'static str {
    "\x1b[?25h"
}

pub fn cursor_hidden() -> &'static str {
    "\x1b[?25l"
}

pub fn scroll_region(top: u16, bottom: u16) -> String {
    format!("\x1b[{top};{bottom}r")
}

pub fn reset_scroll_region() -> &'static str {
    "\x1b[r"
}

pub fn enter_alt() -> &'static str {
    "\x1b[?1049h"
}

pub fn leave_alt() -> &'static str {
    "\x1b[?1049l"
}

pub fn mouse_on() -> &'static str {
    "\x1b[?1000h\x1b[?1002h\x1b[?1006h"
}

pub fn mouse_off() -> &'static str {
    "\x1b[?1000l\x1b[?1002l\x1b[?1006l"
}
