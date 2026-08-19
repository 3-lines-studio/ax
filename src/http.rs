//! Minimal HTTP performed with libcurl directly (src/openai.rs) so streaming
//! and cancellation stay on one thread.

#![forbid(unsafe_code)]

pub struct Response {
    pub status: u16,
    pub body: Vec<u8>,
}

pub fn get(url: &str, headers: &[(String, String)]) -> Result<Response, String> {
    let mut easy = curl::easy::Easy::new();
    easy.url(url).map_err(|e| e.to_string())?;
    easy.get(true).map_err(|e| e.to_string())?;
    let mut list = curl::easy::List::new();
    for (k, v) in headers {
        list.append(&format!("{k}: {v}")).map_err(|e| e.to_string())?;
    }
    easy.http_headers(list).map_err(|e| e.to_string())?;
    let mut sink: Vec<u8> = Vec::new();
    {
        let mut transfer = easy.transfer();
        transfer
            .write_function(|data| {
                sink.extend_from_slice(data);
                Ok(data.len())
            })
            .map_err(|e| e.to_string())?;
        transfer.perform().map_err(|e| e.to_string())?;
    }
    let status = easy.response_code().map_err(|e| e.to_string())? as u16;
    Ok(Response { status, body: sink })
}
