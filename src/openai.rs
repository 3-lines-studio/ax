//! OpenAI-compatible chat completions provider.

use crate::{Error, Message, Provider, Request, Response, ToolCall, Usage};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::cell::RefCell;
use std::ffi::{c_char, c_int, c_void};
use std::rc::Rc;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::Sender;

pub struct OpenAI {
    base_url: String,
    api_key: String,
}

type BuiltRequest = (String, Vec<(String, String)>, Vec<u8>);

pub enum StreamEvent {
    Content(String),
    ToolCall(ToolCall),
    Tokens { input: usize, output: usize },
    Done,
}

impl OpenAI {
    pub fn new(base_url: impl Into<String>, api_key: impl Into<String>) -> Self {
        OpenAI {
            base_url: base_url.into(),
            api_key: api_key.into(),
        }
    }

    pub fn list_models(&self) -> Result<Vec<String>, Error> {
        let url = format!("{}/models", self.base_url);
        let headers = vec![(
            "Authorization".to_string(),
            format!("Bearer {}", self.api_key),
        )];
        let resp = crate::http::get(&url, &headers).map_err(Error::Provider)?;
        if resp.status != 200 {
            return Err(Error::Provider(format!(
                "openai: models: unexpected status {}",
                resp.status
            )));
        }
        let v: Value = serde_json::from_slice(&resp.body).map_err(err)?;
        let mut out = Vec::new();
        if let Some(data) = v.get("data").and_then(|d| d.as_array()) {
            for item in data {
                if let Some(id) = item.get("id").and_then(|i| i.as_str()) {
                    out.push(id.to_string());
                }
            }
        }
        out.sort();
        Ok(out)
    }

    pub fn complete_stream(
        &self,
        req: &Request,
        cancel: &std::sync::Arc<std::sync::atomic::AtomicBool>,
        tx: std::sync::mpsc::Sender<StreamEvent>,
    ) -> std::thread::JoinHandle<Result<Response, Error>> {
        match self.build_request(req, true) {
            Ok((url, headers, body)) => {
                let c2 = cancel.clone();
                let tx2 = tx.clone();
                std::thread::spawn(move || run_request(&url, &headers, &body, true, &c2, &tx2))
            }
            Err(e) => std::thread::spawn(move || Err(e)),
        }
    }

    fn build_request(&self, req: &Request, stream: bool) -> Result<BuiltRequest, Error> {
        let mut msgs = Vec::with_capacity(req.messages.len() + 1);
        if !req.system.is_empty() {
            msgs.push(OaMessage {
                role: "system".into(),
                content: Some(req.system.to_string()),
                tool_calls: None,
                tool_call_id: None,
            });
        }
        for m in req.messages {
            let tool_calls = if m.tool_calls.is_empty() {
                None
            } else {
                Some(
                    m.tool_calls
                        .iter()
                        .map(|c| OaToolCall {
                            id: c.id.clone(),
                            r#type: "function".into(),
                            function: OaFunction {
                                name: c.name.clone(),
                                arguments: c.arguments.clone(),
                            },
                        })
                        .collect(),
                )
            };
            msgs.push(OaMessage {
                role: m.role.clone(),
                content: if m.content.is_empty() {
                    None
                } else {
                    Some(m.content.clone())
                },
                tool_calls,
                tool_call_id: if m.tool_call_id.is_empty() {
                    None
                } else {
                    Some(m.tool_call_id.clone())
                },
            });
        }

        let mut tools = Vec::new();
        for t in req.tools {
            tools.push(OaTool {
                r#type: "function".into(),
                function: OaToolFunction {
                    name: t.name.to_string(),
                    description: t.description.to_string(),
                    parameters: t.parameters.clone(),
                },
            });
        }

        let body = serde_json::to_vec(&OaRequest {
            model: req.model,
            messages: msgs,
            tools,
            stream,
        })
        .map_err(err)?;
        let url = format!("{}/chat/completions", self.base_url);
        let mut headers = Vec::new();
        headers.push(("Content-Type".to_string(), "application/json".to_string()));
        headers.push((
            "Authorization".to_string(),
            format!("Bearer {}", self.api_key),
        ));
        Ok((url, headers, body))
    }
}

fn run_request(
    url: &str,
    headers: &[(String, String)],
    body: &[u8],
    stream: bool,
    cancel: &Arc<AtomicBool>,
    tx: &Sender<StreamEvent>,
) -> Result<Response, Error> {
    let mut easy = crate::curlffi::Easy::new().map_err(err)?;
    easy.url(url).map_err(err)?;
    easy.post().map_err(err)?;
    easy.post_fields(body).map_err(err)?;
    easy.fail_on_error(false).map_err(err)?;
    easy.headers(headers).map_err(err)?;

    let acc = Rc::new(RefCell::new(StreamAcc::default()));
    let raw = Rc::new(RefCell::new(Vec::<u8>::new()));
    let sink = if stream {
        Sink::Stream(acc.clone(), tx.clone())
    } else {
        Sink::Raw(raw.clone())
    };
    let status = {
        let mut state = ReqState {
            sink,
            cancel: cancel.clone(),
        };
        let mut transfer = easy.transfer();
        transfer.write_function(write_cb, &mut state as *mut ReqState as *mut c_void);
        if stream {
            transfer.progress_function(progress_cb, &mut state as *mut ReqState as *mut c_void);
        }
        transfer.perform().map_err(|e| {
            if cancel.load(Ordering::Relaxed) {
                Error::Provider("interrupted".into())
            } else {
                err(e)
            }
        })?;
        easy.response_code().map_err(err)? as u16
    };

    let mut acc = Rc::try_unwrap(acc).ok().unwrap().into_inner();
    let resp = if stream {
        let body = if status == 200 {
            let body = acc.response()?.body;
            acc.finish(tx);
            body
        } else {
            std::mem::take(&mut acc.buf)
        };
        crate::http::Response { status, body }
    } else {
        let raw = Rc::try_unwrap(raw).ok().unwrap().into_inner();
        crate::http::Response { status, body: raw }
    };

    if resp.status != 200 {
        let mut msg = String::new();
        if let Ok(e) = serde_json::from_slice::<OaError>(&resp.body) {
            msg = e.error.message;
        }
        if !msg.is_empty() {
            return Err(Error::Provider(format!("openai: {}: {}", resp.status, msg)));
        }
        return Err(Error::Provider(format!(
            "openai: unexpected status {}",
            resp.status
        )));
    }

    let parsed: OaResponse = serde_json::from_slice(&resp.body).map_err(err)?;
    if parsed.choices.is_empty() {
        return Err(Error::Provider("openai: no choices in response".into()));
    }
    let om = &parsed.choices[0].message;
    let mut calls = Vec::new();
    if let Some(cs) = &om.tool_calls {
        for c in cs {
            calls.push(ToolCall {
                id: c.id.clone(),
                name: c.function.name.clone(),
                arguments: c.function.arguments.clone(),
            });
        }
    }
    Ok(Response {
        message: Message {
            role: "assistant".into(),
            content: om.content.clone().unwrap_or_default(),
            tool_calls: calls,
            tool_call_id: String::new(),
        },
        usage: Usage {
            input: parsed.usage.prompt_tokens,
            output: parsed.usage.completion_tokens,
        },
    })
}
impl Provider for OpenAI {
    fn complete(&self, req: &Request) -> Result<Response, Error> {
        let (url, headers, body) = self.build_request(req, false)?;
        run_request(
            &url,
            &headers,
            &body,
            false,
            &std::sync::Arc::new(std::sync::atomic::AtomicBool::new(false)),
            &std::sync::mpsc::channel().0,
        )
    }
}

#[derive(Default)]
struct StreamAcc {
    buf: Vec<u8>,
    content: String,
    calls: Vec<OaToolCallDelta>,
    usage: OaUsage,
    out_tokens: usize,
}

#[derive(Default, Deserialize)]
struct OaToolCallDelta {
    #[serde(default)]
    index: usize,
    #[serde(default)]
    id: Option<String>,
    #[serde(default)]
    function: Option<OaFunctionDelta>,
}

#[derive(Default, Deserialize)]
struct OaFunctionDelta {
    #[serde(default)]
    name: Option<String>,
    #[serde(default)]
    arguments: Option<String>,
}

#[derive(Default, Deserialize)]
struct OaDeltaMessage {
    #[serde(default)]
    content: Option<String>,
    #[serde(default)]
    tool_calls: Option<Vec<OaToolCallDelta>>,
}

#[derive(Default, Deserialize)]
struct OaStreamChunk {
    #[serde(default)]
    choices: Vec<OaStreamChoice>,
    #[serde(default)]
    usage: OaUsage,
}

#[derive(Default, Deserialize)]
struct OaStreamChoice {
    #[serde(default)]
    delta: OaDeltaMessage,
}

impl StreamAcc {
    fn feed(&mut self, data: &[u8], tx: &std::sync::mpsc::Sender<StreamEvent>) {
        self.buf.extend_from_slice(data);
        loop {
            let sep = find_bytes(&self.buf, b"\n\n");
            let Some(sep) = sep else { break };
            let event = self.buf.drain(..sep + 2).collect::<Vec<u8>>();
            self.handle_event(&event, tx);
        }
    }

    fn handle_event(&mut self, event: &[u8], tx: &std::sync::mpsc::Sender<StreamEvent>) {
        let mut payload = String::new();
        for line in event.split(|&b| b == b'\n') {
            let line = std::str::from_utf8(line).unwrap_or("");
            if let Some(data) = line.strip_prefix("data:") {
                payload.push_str(data.trim());
            }
        }
        if payload == "[DONE]" {
            return;
        }
        let Ok(chunk) = serde_json::from_str::<OaStreamChunk>(&payload) else {
            return;
        };
        if chunk.usage.prompt_tokens != 0 || chunk.usage.completion_tokens != 0 {
            self.usage = chunk.usage;
            let _ = tx.send(StreamEvent::Tokens {
                input: self.usage.prompt_tokens,
                output: self.usage.completion_tokens,
            });
        }
        let Some(choice) = chunk.choices.into_iter().next() else {
            return;
        };
        if let Some(content) = choice.delta.content
            && !content.is_empty()
        {
            self.content.push_str(&content);
            self.out_tokens += 1;
            let _ = tx.send(StreamEvent::Content(content));
            let _ = tx.send(StreamEvent::Tokens {
                input: self.usage.prompt_tokens,
                output: self.out_tokens,
            });
        }
        if let Some(calls) = choice.delta.tool_calls {
            for call in calls {
                let index = call.index;
                while self.calls.len() <= index {
                    self.calls.push(OaToolCallDelta::default());
                }
                let entry = &mut self.calls[index];
                if let Some(id) = call.id {
                    entry.id = Some(id);
                }
                if let Some(f) = call.function {
                    if let Some(name) = f.name {
                        let func = entry.function.get_or_insert_with(Default::default);
                        func.name = Some(name);
                    }
                    if let Some(args) = f.arguments {
                        let func = entry.function.get_or_insert_with(Default::default);
                        func.arguments = Some(func.arguments.take().unwrap_or_default() + &args);
                    }
                }
            }
        }
    }

    fn finish(&mut self, tx: &std::sync::mpsc::Sender<StreamEvent>) {
        for call in std::mem::take(&mut self.calls) {
            let _ = tx.send(StreamEvent::ToolCall(ToolCall {
                id: call.id.unwrap_or_default(),
                name: call
                    .function
                    .as_ref()
                    .and_then(|f| f.name.clone())
                    .unwrap_or_default(),
                arguments: call
                    .function
                    .as_ref()
                    .and_then(|f| f.arguments.clone())
                    .unwrap_or_default(),
            }));
        }
        let _ = tx.send(StreamEvent::Done);
    }

    fn response(&self) -> Result<crate::http::Response, Error> {
        let mut calls = Vec::new();
        for call in &self.calls {
            calls.push(ToolCall {
                id: call.id.clone().unwrap_or_default(),
                name: call
                    .function
                    .as_ref()
                    .and_then(|f| f.name.clone())
                    .unwrap_or_default(),
                arguments: call
                    .function
                    .as_ref()
                    .and_then(|f| f.arguments.clone())
                    .unwrap_or_default(),
            });
        }
        let body = serde_json::to_vec(&OaResponse {
            choices: vec![OaChoice {
                message: OaMessage {
                    role: "assistant".into(),
                    content: if self.content.is_empty() {
                        None
                    } else {
                        Some(self.content.clone())
                    },
                    tool_calls: if calls.is_empty() {
                        None
                    } else {
                        Some(
                            calls
                                .iter()
                                .map(|c| OaToolCall {
                                    id: c.id.clone(),
                                    r#type: "function".into(),
                                    function: OaFunction {
                                        name: c.name.clone(),
                                        arguments: c.arguments.clone(),
                                    },
                                })
                                .collect(),
                        )
                    },
                    tool_call_id: None,
                },
            }],
            usage: OaUsage {
                prompt_tokens: self.usage.prompt_tokens,
                completion_tokens: self.usage.completion_tokens,
            },
        })
        .map_err(err)?;
        Ok(crate::http::Response { status: 200, body })
    }
}

fn find_bytes(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack.windows(needle.len()).position(|w| w == needle)
}

enum Sink {
    Stream(Rc<RefCell<StreamAcc>>, Sender<StreamEvent>),
    Raw(Rc<RefCell<Vec<u8>>>),
}

struct ReqState {
    sink: Sink,
    cancel: Arc<AtomicBool>,
}

unsafe extern "C" fn write_cb(
    ptr: *mut c_char,
    size: usize,
    nmemb: usize,
    userdata: *mut c_void,
) -> usize {
    let st = unsafe { &mut *(userdata as *mut ReqState) };
    let data = unsafe { std::slice::from_raw_parts(ptr as *const u8, size * nmemb) };
    match &st.sink {
        Sink::Stream(acc, tx) => acc.borrow_mut().feed(data, tx),
        Sink::Raw(buf) => buf.borrow_mut().extend_from_slice(data),
    }
    size * nmemb
}

unsafe extern "C" fn progress_cb(
    userdata: *mut c_void,
    _dltotal: f64,
    _dlnow: f64,
    _ultotal: f64,
    _ulnow: f64,
) -> c_int {
    let st = unsafe { &mut *(userdata as *mut ReqState) };
    if st.cancel.load(Ordering::Relaxed) {
        1
    } else {
        0
    }
}

fn err(e: impl std::fmt::Display) -> Error {
    Error::Provider(e.to_string())
}

#[derive(Serialize)]
struct OaRequest<'a> {
    model: &'a str,
    messages: Vec<OaMessage>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    tools: Vec<OaTool>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    stream: bool,
}

#[derive(Serialize, Deserialize)]
struct OaMessage {
    role: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    content: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    tool_calls: Option<Vec<OaToolCall>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    tool_call_id: Option<String>,
}

#[derive(Serialize, Deserialize)]
struct OaToolCall {
    id: String,
    #[serde(rename = "type")]
    r#type: String,
    function: OaFunction,
}

#[derive(Serialize, Deserialize)]
struct OaFunction {
    name: String,
    arguments: String,
}

#[derive(Serialize)]
struct OaTool {
    #[serde(rename = "type")]
    r#type: String,
    function: OaToolFunction,
}

#[derive(Serialize)]
struct OaToolFunction {
    name: String,
    description: String,
    parameters: Value,
}

#[derive(Serialize, Deserialize)]
struct OaResponse {
    #[serde(default)]
    choices: Vec<OaChoice>,
    #[serde(default)]
    usage: OaUsage,
}

#[derive(Serialize, Deserialize)]
struct OaChoice {
    message: OaMessage,
}

#[derive(Deserialize)]
struct OaErrorPayload {
    message: String,
}

#[derive(Serialize, Deserialize, Default)]
struct OaUsage {
    #[serde(default, rename = "prompt_tokens")]
    prompt_tokens: usize,
    #[serde(default, rename = "completion_tokens")]
    completion_tokens: usize,
}

#[derive(Deserialize)]
struct OaError {
    error: OaErrorPayload,
}
