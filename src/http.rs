//! Minimal HTTP helper for the OpenAI provider (src/openai.rs).
//! libcurl is dlopen'd lazily; see src/curlffi.rs.

pub struct Response {
    pub status: u16,
    pub body: Vec<u8>,
}
