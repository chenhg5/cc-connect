# Optional service transport authentication

Webhook and Management API clients must send credentials in an Authorization
Bearer header. Existing X-Webhook-Token and X-Bridge-Token clients remain
supported. URLs containing a `token` query parameter are rejected, including
requests that also carry a valid header. Remove the parameter from saved URLs,
proxy configuration and integrations.

Browser WebSocket clients cannot set an Authorization header. They can offer
`cc-connect` and `cc-connect-token.<base64url-encoded UTF-8 token>` subprotocols.
The server selects only `cc-connect`; it does not echo the credential. Preserve
the WebSocket protocol headers through a private reverse proxy and redact
credential-bearing headers in its logs. The bundled dashboard uses this method.

Bridge connections are bounded before registration: 16 sockets, 16 MiB frames,
64 MiB / 600 messages per minute per direction, 90-second read-idle and 30-second
write deadlines. Closing the server also closes clients awaiting registration.
Insecure development mode accepts loopback peers only, even if another handler
proxies bridge traffic. Execution and management endpoints should remain on
loopback or a controlled private network.

`cc-connect web` opens a clean login URL and displays its token separately.
Paste it into the login form; saved credential-bearing query URLs no longer
authenticate automatically and are removed from the displayed browser address.
