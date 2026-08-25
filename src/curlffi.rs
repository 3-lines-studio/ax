//! Minimal libcurl FFI, dlopen'd lazily on first use.
//!
//! The binary has no DT_NEEDED on libcurl: exec only loads libc, and
//! libcurl's dependency tree (openssl, krb5, zstd, brotli, nghttp2/3, ssh2,
//! ...) is mapped only when the first HTTP request starts. Cold start is
//! ~1.5 ms faster; the first request pays the dlopen once.
//!
//! The FFI is deliberately tiny: Easy + slist + a write/progress callback
//! pair for streaming, plus the getinfo/strerror plumbing. All CURLOPT and
//! CURLINFO values are stable public ABI from curl.h.

use std::ffi::{CStr, CString, c_char, c_int, c_long, c_void};
use std::sync::OnceLock;

const CURL_GLOBAL_DEFAULT: c_long = 3;
const CURLOPT_WRITEDATA: c_int = 10001;
const CURLOPT_URL: c_int = 10002;
const CURLOPT_POSTFIELDS: c_int = 10015;
const CURLOPT_HTTPHEADER: c_int = 10023;
const CURLOPT_NOPROGRESS: c_int = 43;
const CURLOPT_FAILONERROR: c_int = 45;
const CURLOPT_POST: c_int = 47;
const CURLOPT_POSTFIELDSIZE: c_int = 60;
const CURLOPT_CONNECTTIMEOUT: c_int = 78;
const CURLOPT_HTTPGET: c_int = 80;
const CURLOPT_TIMEOUT: c_int = 13;
const CURLOPT_LOW_SPEED_LIMIT: c_int = 19;
const CURLOPT_LOW_SPEED_TIME: c_int = 20;
const CURLOPT_NOSIGNAL: c_int = 99;
const CURLOPT_PROGRESSDATA: c_int = 10057;
const CURLOPT_WRITEFUNCTION: c_int = 20011;
const CURLOPT_PROGRESSFUNCTION: c_int = 20056;
const CURLINFO_RESPONSE_CODE: c_int = 0x200002;

pub type WriteCb = unsafe extern "C" fn(*mut c_char, usize, usize, *mut c_void) -> usize;
pub type ProgressCb = unsafe extern "C" fn(*mut c_void, f64, f64, f64, f64) -> c_int;

// `curl_easy_setopt` is variadic in C (`CURLcode curl_easy_setopt(CURL *,
// CURLoption, ...)`). It must stay typed as variadic here too: transmuting
// the dlsym'd pointer to several fixed-arity `fn(..., T) -> c_int` types and
// calling those instead is undefined behavior, and not just in theory. On
// Apple's arm64 ABI, variadic arguments are passed on the stack while a
// fixed-arity call of the same shape passes them in a register, so libcurl
// reads stack garbage for the option argument and segfaults inside
// `Curl_setstropt`'s `strlen`. A single variadic fn pointer type, called
// with the real argument type at each call site, gets the correct
// per-platform variadic calling convention from the compiler.
type SetoptFn = unsafe extern "C" fn(*mut c_void, c_int, ...) -> c_int;
// `curl_easy_getinfo` is variadic too (`CURLcode curl_easy_getinfo(CURL *,
// CURLINFO, ...)`); same reasoning as `SetoptFn` above applies.
type GetinfoFn = unsafe extern "C" fn(*mut c_void, c_int, ...) -> c_int;

struct Curl {
    global_init: unsafe extern "C" fn(c_long) -> c_int,
    easy_init: unsafe extern "C" fn() -> *mut c_void,
    setopt: SetoptFn,
    perform: unsafe extern "C" fn(*mut c_void) -> c_int,
    getinfo: GetinfoFn,
    cleanup: unsafe extern "C" fn(*mut c_void),
    strerror: unsafe extern "C" fn(c_int) -> *const c_char,
    slist_append: unsafe extern "C" fn(*mut c_void, *const c_char) -> *mut c_void,
    slist_free_all: unsafe extern "C" fn(*mut c_void),
}

fn curl() -> Result<&'static Curl, String> {
    static C: OnceLock<Result<Curl, String>> = OnceLock::new();
    C.get_or_init(|| unsafe { load() })
        .as_ref()
        .map_err(|e| e.clone())
}

unsafe fn load() -> Result<Curl, String> {
    #[cfg(target_os = "macos")]
    const LIBS: &[&[u8]] = &[b"libcurl.4.dylib\0", b"libcurl.dylib\0"];
    #[cfg(not(target_os = "macos"))]
    const LIBS: &[&[u8]] = &[b"libcurl.so.4\0", b"libcurl.so\0", b"libcurl.so.3\0"];
    let mut last_err = String::new();
    for lib in LIBS {
        let handle = unsafe {
            libc::dlopen(
                lib.as_ptr() as *const libc::c_char,
                libc::RTLD_NOW | libc::RTLD_LOCAL,
            )
        };
        if !handle.is_null() {
            return unsafe { load_syms(handle) };
        }
        last_err = dlerror_str().unwrap_or_else(|| "dlopen failed".into());
    }
    Err(last_err)
}

unsafe fn load_syms(handle: *mut libc::c_void) -> Result<Curl, String> {
    let sym = |name: &[u8]| -> Result<*mut c_void, String> {
        let s = CString::new(name).unwrap();
        let p = unsafe { libc::dlsym(handle, s.as_ptr()) };
        if p.is_null() {
            return Err(dlerror_str().unwrap_or_else(|| format!("dlsym {name:?} failed")));
        }
        Ok(p)
    };
    let setopt = sym(b"curl_easy_setopt")?;
    let c = unsafe {
        Curl {
            global_init: std::mem::transmute::<*mut c_void, unsafe extern "C" fn(c_long) -> c_int>(
                sym(b"curl_global_init")?,
            ),
            easy_init: std::mem::transmute::<*mut c_void, unsafe extern "C" fn() -> *mut c_void>(
                sym(b"curl_easy_init")?,
            ),
            setopt: std::mem::transmute::<*mut c_void, SetoptFn>(setopt),
            perform: std::mem::transmute::<*mut c_void, unsafe extern "C" fn(*mut c_void) -> c_int>(
                sym(b"curl_easy_perform")?,
            ),
            getinfo: std::mem::transmute::<*mut c_void, GetinfoFn>(sym(b"curl_easy_getinfo")?),
            cleanup: std::mem::transmute::<*mut c_void, unsafe extern "C" fn(*mut c_void)>(sym(
                b"curl_easy_cleanup",
            )?),
            strerror: std::mem::transmute::<
                *mut c_void,
                unsafe extern "C" fn(c_int) -> *const c_char,
            >(sym(b"curl_easy_strerror")?),
            slist_append: std::mem::transmute::<
                *mut c_void,
                unsafe extern "C" fn(*mut c_void, *const c_char) -> *mut c_void,
            >(sym(b"curl_slist_append")?),
            slist_free_all: std::mem::transmute::<*mut c_void, unsafe extern "C" fn(*mut c_void)>(
                sym(b"curl_slist_free_all")?,
            ),
        }
    };
    if unsafe { (c.global_init)(CURL_GLOBAL_DEFAULT) } != 0 {
        return Err("curl_global_init failed".into());
    }
    Ok(c)
}

fn dlerror_str() -> Option<String> {
    let p = unsafe { libc::dlerror() };
    if p.is_null() {
        return None;
    }
    Some(unsafe { CStr::from_ptr(p) }.to_string_lossy().into_owned())
}

pub struct Easy {
    handle: *mut c_void,
    headers: *mut c_void,
}

impl Easy {
    pub fn new() -> Result<Easy, String> {
        let c = curl()?;
        let handle = unsafe { (c.easy_init)() };
        if handle.is_null() {
            return Err("curl_easy_init failed".into());
        }
        // Required for multi-threaded use: without it the sync DNS resolver
        // can raise SIGALRM into arbitrary threads.
        setopt_long_raw(handle, CURLOPT_NOSIGNAL, 1)?;
        Ok(Easy {
            handle,
            headers: std::ptr::null_mut(),
        })
    }

    pub fn url(&mut self, url: &str) -> Result<(), String> {
        let s = CString::new(url).map_err(|_| "url contains NUL".to_string())?;
        setopt(self, CURLOPT_URL, s.as_ptr())
    }

    pub fn post(&mut self) -> Result<(), String> {
        setopt_long(self, CURLOPT_POST, 1)
    }

    pub fn http_get(&mut self) -> Result<(), String> {
        setopt_long(self, CURLOPT_HTTPGET, 1)
    }

    pub fn post_fields(&mut self, body: &[u8]) -> Result<(), String> {
        setopt(self, CURLOPT_POSTFIELDS, body.as_ptr() as *const c_char)?;
        setopt_long(self, CURLOPT_POSTFIELDSIZE, body.len() as c_long)
    }

    pub fn fail_on_error(&mut self, v: bool) -> Result<(), String> {
        setopt_long(self, CURLOPT_FAILONERROR, v as c_long)
    }

    /// Seconds to wait for the connection to be established. Without this a
    /// server that accepts but never responds blocks the run forever.
    pub fn connect_timeout(&mut self, secs: c_long) -> Result<(), String> {
        setopt_long(self, CURLOPT_CONNECTTIMEOUT, secs)
    }

    /// Hard cap on the whole transfer. Only for non-streaming requests.
    pub fn timeout(&mut self, secs: c_long) -> Result<(), String> {
        setopt_long(self, CURLOPT_TIMEOUT, secs)
    }

    /// Abort when slower than `limit` bytes/s for `time` seconds: kills
    /// stalled streams without capping long healthy ones.
    pub fn low_speed(&mut self, limit: c_long, time: c_long) -> Result<(), String> {
        setopt_long(self, CURLOPT_LOW_SPEED_LIMIT, limit)?;
        setopt_long(self, CURLOPT_LOW_SPEED_TIME, time)
    }

    pub fn headers(&mut self, headers: &[(String, String)]) -> Result<(), String> {
        let c = curl()?;
        for (k, v) in headers {
            let line =
                CString::new(format!("{k}: {v}")).map_err(|_| "header contains NUL".to_string())?;
            let p = unsafe { (c.slist_append)(self.headers, line.as_ptr()) };
            if p.is_null() {
                return Err("curl_slist_append failed".into());
            }
            self.headers = p;
        }
        setopt_ptr(self, CURLOPT_HTTPHEADER, self.headers)
    }

    pub fn transfer(&mut self) -> Transfer<'_> {
        Transfer {
            easy: self,
            write_fn: None,
            write_data: std::ptr::null_mut(),
            progress_fn: None,
            progress_data: std::ptr::null_mut(),
        }
    }

    pub fn response_code(&self) -> Result<u32, String> {
        let c = curl()?;
        let mut code: c_long = 0;
        let r = unsafe { (c.getinfo)(self.handle, CURLINFO_RESPONSE_CODE, &mut code) };
        if r != 0 {
            return Err(curl_err(c, r));
        }
        Ok(code as u32)
    }
}

impl Drop for Easy {
    fn drop(&mut self) {
        let Ok(c) = curl() else { return };
        unsafe { (c.slist_free_all)(self.headers) };
        unsafe { (c.cleanup)(self.handle) };
    }
}

fn setopt(e: &Easy, opt: c_int, arg: *const c_char) -> Result<(), String> {
    let c = curl()?;
    let r = unsafe { (c.setopt)(e.handle, opt, arg) };
    if r != 0 {
        return Err(curl_err(c, r));
    }
    Ok(())
}

fn setopt_long(e: &Easy, opt: c_int, v: c_long) -> Result<(), String> {
    setopt_long_raw(e.handle, opt, v)
}

fn setopt_long_raw(handle: *mut c_void, opt: c_int, v: c_long) -> Result<(), String> {
    let c = curl()?;
    let r = unsafe { (c.setopt)(handle, opt, v) };
    if r != 0 {
        return Err(curl_err(c, r));
    }
    Ok(())
}

fn setopt_ptr(e: &Easy, opt: c_int, p: *mut c_void) -> Result<(), String> {
    let c = curl()?;
    let r = unsafe { (c.setopt)(e.handle, opt, p) };
    if r != 0 {
        return Err(curl_err(c, r));
    }
    Ok(())
}

fn setopt_write_cb(e: &Easy, opt: c_int, f: WriteCb) -> Result<(), String> {
    let c = curl()?;
    let r = unsafe { (c.setopt)(e.handle, opt, f) };
    if r != 0 {
        return Err(curl_err(c, r));
    }
    Ok(())
}

fn setopt_progress_cb(e: &Easy, opt: c_int, f: ProgressCb) -> Result<(), String> {
    let c = curl()?;
    let r = unsafe { (c.setopt)(e.handle, opt, f) };
    if r != 0 {
        return Err(curl_err(c, r));
    }
    Ok(())
}

fn curl_err(c: &Curl, code: c_int) -> String {
    let p = unsafe { (c.strerror)(code) };
    if p.is_null() {
        return format!("curl error {code}");
    }
    unsafe { CStr::from_ptr(p) }.to_string_lossy().into_owned()
}

pub struct Transfer<'a> {
    easy: &'a mut Easy,
    write_fn: Option<WriteCb>,
    write_data: *mut c_void,
    progress_fn: Option<ProgressCb>,
    progress_data: *mut c_void,
}

impl<'a> Transfer<'a> {
    pub fn write_function(&mut self, f: WriteCb, data: *mut c_void) {
        self.write_fn = Some(f);
        self.write_data = data;
    }

    pub fn progress_function(&mut self, f: ProgressCb, data: *mut c_void) {
        self.progress_fn = Some(f);
        self.progress_data = data;
    }

    pub fn perform(&mut self) -> Result<(), String> {
        let e = &mut *self.easy;
        if let Some(f) = self.write_fn {
            setopt_write_cb(e, CURLOPT_WRITEFUNCTION, f)?;
            setopt_ptr(e, CURLOPT_WRITEDATA, self.write_data)?;
        }
        if let Some(f) = self.progress_fn {
            setopt_progress_cb(e, CURLOPT_PROGRESSFUNCTION, f)?;
            setopt_ptr(e, CURLOPT_PROGRESSDATA, self.progress_data)?;
            setopt_long(e, CURLOPT_NOPROGRESS, 0)?;
        }
        let c = curl()?;
        let r = unsafe { (c.perform)(e.handle) };
        setopt_long(e, CURLOPT_NOPROGRESS, 1)?;
        if r != 0 {
            return Err(curl_err(c, r));
        }
        Ok(())
    }
}

pub fn perform_with_sink(easy: &mut Easy, sink: &mut Vec<u8>) -> Result<(), String> {
    unsafe extern "C" fn write_vec(
        ptr: *mut c_char,
        size: usize,
        nmemb: usize,
        userdata: *mut c_void,
    ) -> usize {
        let sink = unsafe { &mut *(userdata as *mut Vec<u8>) };
        let data = unsafe { std::slice::from_raw_parts(ptr as *const u8, size * nmemb) };
        sink.extend_from_slice(data);
        size * nmemb
    }
    let mut t = easy.transfer();
    t.write_function(write_vec, sink as *mut Vec<u8> as *mut c_void);
    t.perform()
}
