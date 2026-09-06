//! Markdown frontmatter helpers shared with user commands.

pub fn parse_frontmatter(text: &str) -> (String, String) {
    if let Some(rest) = text.strip_prefix("---")
        && let Some(end) = rest.find("\n---")
    {
        let fm = &rest[..end];
        let body = rest[end + 4..].trim_start_matches('\n').trim_start();
        let mut description = String::new();
        for line in fm.lines() {
            if let Some((k, v)) = line.split_once(':')
                && k.trim() == "description"
            {
                description = v.trim().to_string();
            }
        }
        if description.is_empty() {
            description = first_line(body);
        }
        return (description, body.to_string());
    }
    (first_line(text), text.to_string())
}

fn first_line(s: &str) -> String {
    s.lines()
        .map(|l| l.trim())
        .find(|l| !l.is_empty())
        .unwrap_or("")
        .to_string()
}
