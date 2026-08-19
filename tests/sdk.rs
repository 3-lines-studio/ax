//! Agent loop against a fake provider.

use ax::{new_tool, Agent, Error, Message, Provider, Request, Response, ToolCall, Usage};
use serde::Deserialize;
use std::cell::RefCell;
use std::collections::VecDeque;
use std::rc::Rc;

#[derive(Default)]
struct Fake {
    responses: RefCell<VecDeque<Response>>,
    requests: RefCell<Vec<Vec<Message>>>,
}

impl Provider for Fake {
    fn complete(&self, req: &Request) -> Result<Response, Error> {
        self.requests.borrow_mut().push(req.messages.to_vec());
        Ok(self.responses.borrow_mut().pop_front().expect("no fake response"))
    }
}

fn call_tool(id: &str, name: &str, args: &str) -> Message {
    Message {
        role: "assistant".into(),
        content: String::new(),
        tool_calls: vec![ToolCall {
            id: id.into(),
            name: name.into(),
            arguments: args.into(),
        }],
        tool_call_id: String::new(),
    }
}

fn user(content: &str) -> Message {
    Message {
        role: "user".into(),
        content: content.into(),
        tool_calls: Vec::new(),
        tool_call_id: String::new(),
    }
}

fn assistant(content: &str) -> Message {
    Message {
        role: "assistant".into(),
        content: content.into(),
        tool_calls: Vec::new(),
        tool_call_id: String::new(),
    }
}

#[derive(Deserialize)]
struct Upper {
    s: String,
}

#[test]
fn run_executes_tools_and_returns_transcript() {
    let events = Rc::new(RefCell::new(Vec::new()));
    let ev = events.clone();
    let upper = new_tool("upper", "uppercase", "{}", |a: Upper| a.s.to_uppercase());
    let p = Fake {
        responses: RefCell::new(VecDeque::from([
            Response {
                message: call_tool("c1", "upper", r#"{"s":"hi"}"#),
                usage: Usage { input: 10, output: 5 },
            },
            Response {
                message: assistant("done"),
                usage: Usage { input: 20, output: 2 },
            },
        ])),
        ..Default::default()
    };
    let mut a = Agent::new(p)
        .tools(vec![upper])
        .max_turns(5)
        .on(move |e| ev.borrow_mut().push(e));
    let in_msgs = vec![user("go")];
    let out = a.run(&in_msgs).expect("run");

    assert_eq!(in_msgs.len(), 1);
    assert_eq!(in_msgs[0].content, "go");
    assert_eq!(out.len(), 4);
    assert_eq!(out[2].role, "tool");
    assert_eq!(out[2].tool_call_id, "c1");
    assert_eq!(out[2].content, "HI");
    assert_eq!(out[3].content, "done");

    let events = events.borrow();
    assert_eq!(events.len(), 3);
    assert_eq!(events[0].usage.input, 10);
    assert_eq!(events[0].usage.output, 5);
    assert_eq!(events[1].usage, Usage::default());
    assert_eq!(events[2].usage.input, 20);

    let reqs = a.provider().requests.borrow();
    let last = reqs.last().unwrap();
    assert_eq!(last.len(), 3);
    assert_eq!(last[2].content, "HI");
}

#[derive(Deserialize)]
struct Empty {}

#[test]
fn run_continues_on_tool_error() {
    let boom = new_tool("boom", "always fails", "{}", |_: Empty| {
        "error: boom".to_string()
    });
    let p = Fake {
        responses: RefCell::new(VecDeque::from([
            Response {
                message: call_tool("c1", "boom", "{}"),
                usage: Usage::default(),
            },
            Response {
                message: assistant("recovered"),
                usage: Usage::default(),
            },
        ])),
        ..Default::default()
    };
    let mut a = Agent::new(p).tools(vec![boom]);
    let out = a.run(&[user("go")]).expect("run");
    assert_eq!(out[2].content, "error: boom");
    assert_eq!(out[3].content, "recovered");
}

#[test]
fn run_unknown_tool() {
    let p = Fake {
        responses: RefCell::new(VecDeque::from([
            Response {
                message: call_tool("c1", "nope", "{}"),
                usage: Usage::default(),
            },
            Response {
                message: assistant("ok"),
                usage: Usage::default(),
            },
        ])),
        ..Default::default()
    };
    let mut a = Agent::new(p);
    let out = a.run(&[user("go")]).expect("run");
    assert!(out[2].content.contains("unknown tool"));
}

#[test]
fn run_max_turns() {
    let responses: VecDeque<Response> = (0..10)
        .map(|_| Response {
            message: call_tool("c", "x", "{}"),
            usage: Usage::default(),
        })
        .collect();
    let p = Fake {
        responses: RefCell::new(responses),
        ..Default::default()
    };
    let mut a = Agent::new(p).max_turns(3);
    let err = a.run(&[user("go")]).unwrap_err();
    match err {
        Error::MaxTurns(h) => assert_eq!(h.len(), 1 + 3 * 2),
        e => panic!("want MaxTurns, got {e:?}"),
    }
}

#[derive(Deserialize)]
struct NeedsInt {
    n: i64,
}

#[test]
fn new_tool_bad_arguments() {
    let bad = new_tool("bad", "needs int", "{}", |_: NeedsInt| "unreachable".to_string());
    let got = (bad.run)(r#"{"n":"x"}"#);
    assert!(got.contains("invalid arguments"), "got: {got}");
}
