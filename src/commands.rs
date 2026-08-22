#[derive(Clone)]
pub struct UserCommand {
    pub name: String,
    pub description: String,
    pub content: String,
}

pub fn load_user_commands(ax_root: &str) -> Vec<UserCommand> {
    let dir = std::path::Path::new(ax_root).join("commands");
    let Ok(entries) = std::fs::read_dir(&dir) else {
        return Vec::new();
    };
    let mut out = Vec::new();
    for e in entries.flatten() {
        let path = e.path();
        if path.extension().and_then(|x| x.to_str()) != Some("md") {
            continue;
        }
        let Some(name) = path.file_stem().and_then(|s| s.to_str()) else {
            continue;
        };
        let Ok(content) = std::fs::read_to_string(&path) else {
            continue;
        };
        let (description, content) = crate::skills::parse_frontmatter(&content);
        out.push(UserCommand {
            name: name.to_string(),
            description,
            content,
        });
    }
    out.sort_by(|a, b| a.name.cmp(&b.name));
    out
}

pub fn expand_user_command(uc: &UserCommand, rest: &str) -> String {
    let content = uc.content.clone();
    if rest.is_empty() {
        return content;
    }
    let args = parse_command_args(rest);
    let substituted = substitute_args(&content, &args);
    if substituted == content && !content.contains("$ARGUMENTS") {
        return format!("{content}\n\n{rest}");
    }
    substituted
}

/// Split command arguments respecting quoted strings (bash-style).
pub fn parse_command_args(s: &str) -> Vec<String> {
    let mut args = Vec::new();
    let mut current = String::new();
    let mut in_quote: Option<char> = None;
    for c in s.chars() {
        match in_quote {
            Some(q) => {
                if c == q {
                    in_quote = None;
                } else {
                    current.push(c);
                }
            }
            None => {
                if c == '"' || c == '\'' {
                    in_quote = Some(c);
                } else if c.is_whitespace() {
                    if !current.is_empty() {
                        args.push(std::mem::take(&mut current));
                    }
                } else {
                    current.push(c);
                }
            }
        }
    }
    if !current.is_empty() {
        args.push(current);
    }
    args
}

/// Substitute argument placeholders in a prompt template:
/// `$1`..`$9`, `$@`/`$ARGUMENTS` for all args, `${2:-default}`, `${@:N}` and
/// `${@:N:L}` slices.
pub fn substitute_args(content: &str, args: &[String]) -> String {
    let all = args.join(" ");
    let mut out = String::with_capacity(content.len());
    let mut rest = content;
    while let Some(start) = rest.find('$') {
        out.push_str(&rest[..start]);
        let tail = &rest[start..];
        let (token, len) = parse_placeholder(tail, args, &all);
        out.push_str(&token);
        rest = &tail[len..];
    }
    out.push_str(rest);
    out
}

fn parse_placeholder(tail: &str, args: &[String], all: &str) -> (String, usize) {
    let chars: Vec<char> = tail.chars().collect();
    if chars.first() != Some(&'$') {
        return (String::new(), 0);
    }
    if chars.get(1) == Some(&'{') {
        // ${...}
        let mut depth = 0usize;
        let mut end = 0usize;
        for (i, c) in chars.iter().enumerate() {
            if *c == '{' {
                depth += 1;
            } else if *c == '}' {
                depth -= 1;
                if depth == 0 {
                    end = i;
                    break;
                }
            }
        }
        if end == 0 {
            return (String::new(), 1);
        }
        let inner = &chars[2..end].iter().collect::<String>();
        let (replacement, _) = expand_braced(inner, args, all);
        return (replacement, end + 1);
    }
    if chars.get(1) == Some(&'@') {
        return (all.to_string(), 2);
    }
    if let Some(c) = chars.get(1)
        && c.is_ascii_digit()
    {
        let idx = c.to_digit(10).unwrap() as usize - 1;
        let value = args.get(idx).cloned().unwrap_or_default();
        return (value, 2);
    }
    let mut id_len = 0usize;
    while id_len + 1 < chars.len()
        && (chars[id_len + 1].is_ascii_alphanumeric() || chars[id_len + 1] == '_')
    {
        id_len += 1;
    }
    if id_len > 0 {
        let ident: String = chars[1..=id_len].iter().collect();
        if ident == "ARGUMENTS" {
            return (all.to_string(), id_len + 1);
        }
    }
    (String::new(), 1)
}

fn expand_braced(inner: &str, args: &[String], all: &str) -> (String, usize) {
    // ${N:-default}, ${@:-default}, ${ARGUMENTS:-default}
    if let Some((target, default)) = inner.split_once(":-") {
        let value = if target == "@" || target == "ARGUMENTS" {
            all
        } else {
            target
                .parse::<usize>()
                .ok()
                .and_then(|n| args.get(n - 1))
                .map(String::as_str)
                .unwrap_or("")
        };
        return if value.is_empty() {
            (default.to_string(), inner.len())
        } else {
            (value.to_string(), inner.len())
        };
    }
    // ${@:N} and ${@:N:L}
    if let Some(slice) = inner.strip_prefix("@:") {
        let parts: Vec<&str> = slice.split(':').collect();
        if let Ok(n) = parts[0].parse::<usize>() {
            let start = n.saturating_sub(1);
            let sliced: Vec<&str> = args[start..].iter().map(String::as_str).collect();
            let chosen: Vec<&str> = if parts.len() > 1 {
                if let Ok(len) = parts[1].parse::<usize>() {
                    sliced.iter().take(len).copied().collect()
                } else {
                    sliced
                }
            } else {
                sliced
            };
            return (chosen.join(" "), inner.len());
        }
    }
    (String::new(), inner.len())
}
