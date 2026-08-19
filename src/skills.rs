//! Skills: markdown files in `~/.agents/skills/<name>/SKILL.md`.
//! Frontmatter may carry a `description:`; the body is the skill content.

use crate::Tool;

pub struct Skill {
    pub name: String,
    pub description: String,
    pub content: String,
}

pub fn skills_root() -> Option<String> {
    std::env::var("HOME")
        .ok()
        .map(|h| format!("{h}/.agents/skills"))
}

pub fn list_skills(root: &str) -> Vec<Skill> {
    let Ok(entries) = std::fs::read_dir(root) else {
        return Vec::new();
    };
    let mut out = Vec::new();
    for e in entries.flatten() {
        let dir = e.path();
        if !dir.is_dir() {
            continue;
        }
        let name = match dir.file_name().and_then(|s| s.to_str()) {
            Some(n) => n.to_string(),
            None => continue,
        };
        let skill_file = dir.join("SKILL.md");
        let Ok(text) = std::fs::read_to_string(&skill_file) else {
            continue;
        };
        let (description, content) = parse_frontmatter(&text);
        out.push(Skill {
            name,
            description,
            content,
        });
    }
    out.sort_by(|a, b| a.name.cmp(&b.name));
    out
}

fn parse_frontmatter(text: &str) -> (String, String) {
    if let Some(rest) = text.strip_prefix("---") {
        if let Some(end) = rest.find("\n---") {
            let fm = &rest[..end];
            let body = rest[end + 4..].trim_start_matches('\n').trim_start();
            let mut description = String::new();
            for line in fm.lines() {
                if let Some((k, v)) = line.split_once(':') {
                    if k.trim() == "description" {
                        description = v.trim().to_string();
                    }
                }
            }
            if description.is_empty() {
                description = first_line(body);
            }
            return (description, body.to_string());
        }
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

pub fn skill_tools(root: &str) -> Vec<Tool> {
    let root_list = root.to_string();
    let list = crate::new_tool(
        "skills",
        "List available skills with their names and descriptions.",
        "{}",
        move |_args: ()| {
            let skills = list_skills(&root_list);
            if skills.is_empty() {
                return "No skills installed (see ~/.agents/skills).".to_string();
            }
            let mut out = String::new();
            for s in skills {
                out.push_str(&format!("- {}: {}\n", s.name, s.description));
            }
            out
        },
    );
    #[derive(serde::Deserialize)]
    struct SkillArgs {
        name: String,
    }
    let root_read = root.to_string();
    let read = crate::new_tool(
        "skill",
        "Read one skill's content by name. Use the skills tool to list names first.",
        "{\"type\":\"object\",\"properties\":{\"name\":{\"type\":\"string\",\"description\":\"skill name\"}},\"required\":[\"name\"]}",
        move |args: SkillArgs| {
            let skills = list_skills(&root_read);
            match skills.into_iter().find(|s| s.name == args.name) {
                Some(s) => s.content,
                None => format!("skill not found: {}", args.name),
            }
        },
    );
    vec![list, read]
}
