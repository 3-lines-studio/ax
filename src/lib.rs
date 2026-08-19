//! Minimal LLM coding agent harness.
//!
//! Port of ax-go. The loop is the only logic: messages -> LLM -> tool calls ->
//! results -> repeat. No memory, sessions, retries, parallel tool execution,
//! or streaming. `run` never mutates its input.
//!
//! Message/ToolCall serialize with ax-go's field names so `.ax/session.jsonl`
//! files are interchangeable with the Go version.

use serde::{de::DeserializeOwned, Deserialize, Serialize};
use serde_json::Value;

pub mod markdown;
pub mod term;
pub mod tui;
mod http;
pub mod openai;
pub mod tools;

pub use openai::OpenAI;

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct ToolCall {
    #[serde(rename = "ID")]
    pub id: String,
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "Arguments")]
    pub arguments: String,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Message {
    #[serde(rename = "Role")]
    pub role: String,
    #[serde(rename = "Content")]
    pub content: String,
    #[serde(rename = "ToolCalls", default, skip_serializing_if = "Vec::is_empty")]
    pub tool_calls: Vec<ToolCall>,
    #[serde(rename = "ToolCallID", default, skip_serializing_if = "String::is_empty")]
    pub tool_call_id: String,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Usage {
    pub input: usize,
    pub output: usize,
}

#[derive(Clone, Debug)]
pub struct Event {
    pub turn: usize,
    pub message: Message,
    pub usage: Usage,
}

#[derive(Debug)]
pub enum Error {
    MaxTurns(Vec<Message>),
    Provider(String),
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Error::MaxTurns(_) => write!(f, "ax: max turns reached"),
            Error::Provider(s) => write!(f, "{s}"),
        }
    }
}

impl std::error::Error for Error {}

pub struct Tool {
    pub name: &'static str,
    pub description: &'static str,
    pub parameters: Value,
    pub run: Box<dyn Fn(&str) -> String + Send>,
}

pub fn new_tool<T>(
    name: &'static str,
    description: &'static str,
    schema: &'static str,
    run: impl Fn(T) -> String + Send + 'static,
) -> Tool
where
    T: DeserializeOwned,
{
    Tool {
        name,
        description,
        parameters: serde_json::from_str(schema).unwrap_or(Value::Null),
        run: Box::new(move |raw| match serde_json::from_str::<T>(raw) {
            Ok(args) => run(args),
            Err(e) => format!("error: invalid arguments: {e}"),
        }),
    }
}

pub trait Provider {
    fn complete(&self, req: &Request) -> Result<Response, Error>;
}

pub struct Request<'a> {
    pub model: &'a str,
    pub system: &'a str,
    pub messages: &'a [Message],
    pub tools: &'a [Tool],
}

#[derive(Debug)]
pub struct Response {
    pub message: Message,
    pub usage: Usage,
}

pub struct Agent<P: Provider> {
    provider: P,
    model: String,
    system: String,
    tools: Vec<Tool>,
    max_turns: usize,
    on: Option<Box<dyn FnMut(Event)>>,
}

impl<P: Provider> Agent<P> {
    pub fn new(provider: P) -> Self {
        Agent {
            provider,
            model: String::new(),
            system: String::new(),
            tools: Vec::new(),
            max_turns: 20,
            on: None,
        }
    }

    pub fn model(mut self, m: impl Into<String>) -> Self {
        self.model = m.into();
        self
    }

    pub fn system(mut self, s: impl Into<String>) -> Self {
        self.system = s.into();
        self
    }

    pub fn tools(mut self, ts: Vec<Tool>) -> Self {
        self.tools = ts;
        self
    }

    pub fn max_turns(mut self, n: usize) -> Self {
        self.max_turns = n;
        self
    }

    pub fn on(mut self, f: impl FnMut(Event) + 'static) -> Self {
        self.on = Some(Box::new(f));
        self
    }

    pub fn provider(&self) -> &P {
        &self.provider
    }

    pub fn run(&mut self, msgs: &[Message]) -> Result<Vec<Message>, Error> {
        let mut h = msgs.to_vec();
        for turn in 0..self.max_turns {
            let resp = self.provider.complete(&Request {
                model: &self.model,
                system: &self.system,
                messages: &h,
                tools: &self.tools,
            })?;
            h.push(resp.message.clone());
            self.emit(turn, &resp.message, resp.usage);
            if resp.message.tool_calls.is_empty() {
                return Ok(h);
            }
            let calls = resp.message.tool_calls.clone();
            for call in calls {
                let m = Message {
                    role: "tool".into(),
                    content: self.exec(&call),
                    tool_calls: Vec::new(),
                    tool_call_id: call.id,
                };
                h.push(m.clone());
                self.emit(turn, &m, Usage::default());
            }
        }
        Err(Error::MaxTurns(h))
    }

    fn exec(&self, call: &ToolCall) -> String {
        for t in &self.tools {
            if t.name == call.name {
                return (t.run)(&call.arguments);
            }
        }
        format!("error: unknown tool: {}", call.name)
    }

    fn emit(&mut self, turn: usize, m: &Message, u: Usage) {
        if let Some(f) = &mut self.on {
            f(Event {
                turn,
                message: m.clone(),
                usage: u,
            });
        }
    }
}
