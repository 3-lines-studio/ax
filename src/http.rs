//! Minimal HTTP/1.1 response parsing. Requests are performed with libcurl
//! directly (src/openai.rs) so streaming and cancellation stay on one thread.

#![forbid(unsafe_code)]

pub struct Response {
    pub status: u16,
    pub body: Vec<u8>,
}
