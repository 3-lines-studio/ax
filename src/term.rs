//! Raw terminal layer: termios, key parsing, window size, bracketed paste.
//! No dependencies beyond libc.

#![allow(unsafe_code)]

use std::io::{Read, Write};

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
        let mut stdin = std::io::stdin();
        let mut buf = [0u8; 1];
        loop {
            let n = stdin.read(&mut buf).map_err(|e| format!("read: {e}"))?;
            if n == 0 {
                return Ok(Key::Eof);
            }
            let b = buf[0];
            match b {
                0x03 => return Ok(Key::CtrlC),
                0x0d | 0x0a => return Ok(Key::Enter),
                0x09 => return Ok(Key::Tab),
                0x7f | 0x08 => return Ok(Key::Backspace),
                0x1b => {
                    // Possible escape sequence. Peek without blocking long.
                    let mut next = [0u8; 1];
                    let n = stdin.read(&mut next).map_err(|e| format!("read: {e}"))?;
                    if n == 0 {
                        return Ok(Key::Esc);
                    }
                    match next[0] {
                        b'[' => return self.read_csi(),
                        b'O' => {
                            let mut c = [0u8; 1];
                            let _ = stdin.read(&mut c);
                            return Ok(match c[0] {
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
                        let mut c = [0u8; 1];
                        let n = stdin.read(&mut c).map_err(|e| format!("read: {e}"))?;
                        if n == 0 {
                            break;
                        }
                        bytes.push(c[0]);
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
        let mut stdin = std::io::stdin();
        let mut bytes = Vec::new();
        let mut b = [0u8; 1];
        // Collect up to 8 parameter/final bytes (arrows are short).
        for _ in 0..8 {
            let n = stdin.read(&mut b).map_err(|e| format!("read: {e}"))?;
            if n == 0 {
                break;
            }
            bytes.push(b[0]);
            if (0x40..=0x7e).contains(&b[0]) {
                break;
            }
        }
        if bytes.is_empty() {
            return Ok(Key::Esc);
        }
        let final_byte = bytes[bytes.len() - 1];
        let params: Vec<u8> = bytes
            .iter()
            .take(bytes.len() - 1)
            .copied()
            .filter(|b| b.is_ascii_digit())
            .collect();
        match final_byte {
            b'A' => Ok(Key::Up),
            b'B' => Ok(Key::Down),
            b'C' => Ok(Key::Right),
            b'D' => Ok(Key::Left),
            b'H' => Ok(Key::Home),
            b'F' => Ok(Key::End),
            b'~' => match params.first() {
                Some(1) | Some(7) => Ok(Key::Home),
                Some(3) => Ok(Key::Delete),
                Some(4) | Some(8) => Ok(Key::End),
                Some(5) => Ok(Key::PageUp),
                Some(6) => Ok(Key::PageDown),
                _ => Ok(Key::Esc),
            },
            b'M' | b'm' => Ok(Key::Paste(bytes.clone())),
            _ => Ok(Key::Esc),
        }
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
    Tab,
    Backspace,
    Delete,
    Up,
    Down,
    Left,
    Right,
    Home,
    End,
    PageUp,
    PageDown,
    Esc,
    CtrlC,
    Eof,
    Paste(Vec<u8>),
}

/// ANSI helpers for composing frames.
pub fn move_to(row: u16, col: u16) -> String {
    format!("\x1b[{row};{col}H")
}

pub fn clear_eol() -> &'static str {
    "\x1b[K"
}

pub fn cursor_visible() -> &'static str {
    "\x1b[?25h"
}

pub fn cursor_hidden() -> &'static str {
    "\x1b[?25l"
}
