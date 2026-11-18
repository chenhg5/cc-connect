# Local control transport

Unix keeps `<data_dir>/run/api.sock`, its lifetime lock, and its existing
HTTP/JSON routes. Windows uses a byte-stream named pipe and discovers it from
`<data_dir>/run/api.endpoint.json`:

```json
{
  "schema_version": 1,
  "transport": "named_pipe",
  "address": "\\\\.\\pipe\\cc-connect-0123456789abcdef0123456789abcdef",
  "instance_id": "0123456789abcdef0123456789abcdef",
  "pid": 123
}
```

The descriptor is UTF-8 and at most 4096 bytes. Readers reject unsupported
versions or transports, trailing JSON, zero/out-of-range process IDs, invalid
instance IDs, and an address that is not exactly the local prefix followed by
the same 32 lowercase hexadecimal characters as `instance_id`. Unknown fields
are reserved for compatible additions. The PID is diagnostic metadata, not
authority to terminate a process.

The server creates a fresh random instance ID, binds the pipe, then atomically
publishes its descriptor under a persistent endpoint lock. Both pipe and state
files restrict access to the current Windows SID and SYSTEM. Remote pipe
clients and filesystem reparse points are rejected. Startup preserves live or
unverifiable previous endpoints; shutdown removes only its own descriptor.

Clients must connect and receive a successful response from `GET /sessions`
before reporting readiness. File existence alone does not prove the server has
started its HTTP loop. `POST /shutdown` responds with HTTP 202 and
`{"status":"accepted"}` before notifying the process owner's shutdown handler;
repeated requests notify that handler once. Other methods return 405, and an
API embedded without a shutdown handler returns 501. Shutdown uses the same
private transport authorization as the other local control routes.

Windows I/O is isolated here through `github.com/Microsoft/go-winio` v0.6.2.
This version supports explicit pipe security and rejection of remote clients
without raising the repository's Go 1.25 requirement; v0.6.3 and v0.6.4 require
Go 1.26. Native Windows tests cover pipe requests, protected file ACLs, single
ownership, stale recovery, and descriptor cleanup. Cross-compiling those tests
does not constitute a Windows runtime pass.
