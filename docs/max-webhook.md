# MAX bot deployment guide

The MAX platform adapter (`platform/max`) supports two delivery modes:

- **Long-poll** (default) — bot pulls updates from `platform-api.max.ru/updates`. Works behind NAT, no public URL needed. From 2026-05-11 MAX throttles long-poll to 2 RPS, so this is best for personal/low-traffic bots.
- **Webhook** — MAX pushes each update to your HTTPS endpoint. Recommended for production; required if you need >2 RPS sustained.

This guide covers the three real-world topologies and a copy-paste config for each.

## Topology A — VPS with public IP and reverse proxy (recommended)

The bot runs on a server that has a public domain and TLS-terminating reverse proxy (nginx, Caddy, Traefik) in front.

```
                                          ┌─────────── VPS (one host) ────────────┐
   user → MAX cloud ─── HTTPS POST ───▶  │  nginx :443 (TLS)                     │
                       https://your.tld   │     └ proxy_pass → 127.0.0.1:8090    │
                       /webhook           │                                       │
                                          │  cc-connect (HTTP :8090, localhost)   │
                                          └───────────────────────────────────────┘
```

### Bot config

```toml
[[projects.platforms]]
type = "max"

[projects.platforms.options]
token          = "your-max-bot-token"
allow_from     = "12345678"
webhook_url    = "https://bot.example.com/webhook"
webhook_listen = "127.0.0.1:8090"   # bind to loopback only — nginx is the public face
webhook_secret = "long-random-string-here"   # optional; recommended
```

### nginx site (`/etc/nginx/sites-available/bot.example.com`)

```nginx
server {
    listen 443 ssl;
    server_name bot.example.com;

    ssl_certificate     /etc/letsencrypt/live/bot.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/bot.example.com/privkey.pem;
    include             /etc/letsencrypt/options-ssl-nginx.conf;

    location /webhook {
        proxy_pass         http://127.0.0.1:8090;
        proxy_http_version 1.1;
        proxy_set_header   Host              $host;
        proxy_set_header   X-Real-IP         $remote_addr;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;
        proxy_read_timeout 30s;
        proxy_connect_timeout 5s;
        client_max_body_size 50M;
    }

    location / {
        default_type text/plain;
        return 200 "ok\n";
    }
}

server {
    listen 80;
    server_name bot.example.com;
    return 301 https://$host$request_uri;
}
```

Get the cert with `certbot --nginx -d bot.example.com`, then `nginx -t && systemctl reload nginx`.

### Caddy alternative (single file, auto-TLS)

```caddy
bot.example.com {
    handle /webhook {
        reverse_proxy 127.0.0.1:8090
    }
    respond / "ok" 200
}
```

That's the entire `Caddyfile`. Caddy obtains and renews the certificate automatically.

## Topology B — Home server + cheap VPS as proxy (current author's setup)

The bot runs at home (no public IP) and a small VPS forwards traffic to it via SSH reverse-tunnel.

```
                                          ┌─── VPS ───┐         ┌──── Home ────┐
   user → MAX cloud ─── HTTPS ─────────▶ │  nginx    │ ──SSH──▶│  cc-connect  │
                       /webhook           │ :443→:8090│  -R     │   :8090      │
                                          └───────────┘ tunnel  └──────────────┘
```

### Bot config (on the home machine)

Same as Topology A — bind to `:8090` (or `127.0.0.1:8090`), set `webhook_url` to the public URL on the VPS:

```toml
webhook_url    = "https://bot.example.com/webhook"
webhook_listen = "127.0.0.1:8090"
webhook_secret = "long-random-string-here"
```

### SSH reverse tunnel (from home to VPS)

Add a systemd-user unit, e.g. `~/.config/systemd/user/max-tunnel.service`:

```ini
[Unit]
Description=SSH reverse tunnel for MAX webhook
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/bin/ssh -N \
    -R 127.0.0.1:8090:127.0.0.1:8090 \
    -p 22 -i %h/.ssh/tunnel_key \
    -o ServerAliveInterval=30 \
    -o ServerAliveCountMax=3 \
    -o ExitOnForwardFailure=yes \
    -o StrictHostKeyChecking=accept-new \
    tunnel@vps.example.com
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
```

Enable: `systemctl --user enable --now max-tunnel`.

The tunnel binds `127.0.0.1:8090` on the VPS to the home machine's `:8090`. nginx (Topology A config) then proxies to that loopback address.

### Why a tunnel and not just opening the home firewall

- No need for a static IP at home.
- No port-forwarding on the home router.
- Works the same way from any home network (laptop, mobile hotspot).
- TLS still terminates on the VPS — your home machine never speaks TLS to the internet.

## Topology C — Long-poll (no public URL at all)

Simplest deployment: the bot polls MAX. No reverse proxy, no tunnel, no domain.

```toml
[[projects.platforms]]
type = "max"

[projects.platforms.options]
token      = "your-max-bot-token"
allow_from = "12345678"
# webhook_* fields omitted → long-poll mode
```

Use this for personal bots, development, or behind restrictive corporate networks. Not recommended once MAX's 2 RPS long-poll throttle takes effect for higher-traffic bots.

## Configuration reference

| Field | Required | Default | Purpose |
|---|---|---|---|
| `token` | yes | — | Bot token from MAX bot creator |
| `allow_from` | no | `*` (all) | Comma-separated user IDs allowed to message the bot. `*` or empty = no restriction. **Always set this in production** |
| `api_base` | no | `https://platform-api.max.ru` | Override for MAX API base URL (rarely needed) |
| `webhook_url` | no | (empty → long-poll) | Public HTTPS URL MAX will POST updates to. Setting this enables webhook mode |
| `webhook_listen` | no | `:8080` | TCP address the bot binds for incoming webhooks. Use `127.0.0.1:PORT` to restrict to loopback (recommended when behind a reverse proxy) |
| `webhook_path` | no | `/webhook` | Path component the bot serves. Must match the path in `webhook_url`. Lets you host multiple bots on one domain (e.g. `/bot1`, `/bot2`) |
| `webhook_secret` | no | (empty → no check) | Shared secret. If set, requests must include it as `X-Webhook-Secret` header **or** `?s=` query parameter. Mismatch returns 401 |

## Securing the webhook

The MAX public bot API does not currently sign webhook deliveries. Anyone who learns your `webhook_url` can POST garbage to it. Layered defenses:

1. **`webhook_secret`** — set a long random value and embed it in `webhook_url` itself, e.g. `https://bot.example.com/webhook?s=<secret>`. The bot verifies it on every request and rejects mismatches. Keep the secret out of the public URL when possible (use a header instead — see below).
2. **`allow_from`** — restricts which MAX user IDs the bot will respond to. Even if a stranger reaches the webhook, they can't make the bot do anything.
3. **Reverse proxy** — terminate TLS, rate-limit, log. Keep the bot bound to `127.0.0.1` so the only way in is through the proxy.

### Passing the secret as a header instead of a query parameter

If you control the proxy in front of the bot, you can keep the secret out of URLs and access logs:

```nginx
location /webhook {
    proxy_pass http://127.0.0.1:8090;
    proxy_set_header X-Webhook-Secret "long-random-string-here";
    # ...
}
```

Then in the bot's config set `webhook_url = "https://bot.example.com/webhook"` (no query string) and `webhook_secret = "long-random-string-here"`. MAX → nginx adds the header → bot verifies. The secret never appears in URLs MAX or upstream logs see.

## Switching between modes

The bot decides which mode to use purely from config — no rebuild.

### Long-poll → webhook

1. Set `webhook_url`, `webhook_listen` (and optional `webhook_path`, `webhook_secret`) in `config.toml`.
2. Make sure the public URL is reachable and TLS works.
3. `systemctl restart cc-connect` (or however you run it).

On startup the bot calls `POST /subscriptions` against MAX with the new URL. MAX immediately stops delivering long-poll updates and starts pushing.

### Webhook → long-poll

1. Comment out / remove `webhook_url` (and the other `webhook_*` fields) in `config.toml`.
2. Restart the bot.

When the bot stops, it makes a best-effort `DELETE /subscriptions?url=...` to remove the registration. If that call fails (network down, etc.), MAX may keep delivering to the old URL. To force-clear:

```bash
curl -X DELETE \
  "https://platform-api.max.ru/subscriptions?url=$(printf %s "$URL" | jq -sRr @uri)&access_token=$TOKEN"
```

After that, restart the bot in long-poll mode.

## Troubleshooting

### `502 Bad Gateway` from nginx when MAX hits the webhook

The bot is not listening on `webhook_listen`. Check, in order:
1. `systemctl --user status cc-connect` — is it running?
2. `ss -tlnp | grep 8090` (or your port) — is something bound?
3. Bot logs — look for `max: webhook listening addr=...` and `max: webhook subscribed url=...`. If you see `connected` but neither of those, you have a startup hang.

### Bot logs `max: connected` but nothing after

Stuck during `Start()`. Common causes:
- `subscribe` HTTP call is timing out — check `platform-api.max.ru` reachability and TLS.
- A mutex deadlock — file a bug.

### Webhook returns 401

Either the secret is wrong, or the request isn't bringing it. Check:
- Header `X-Webhook-Secret` matches `webhook_secret` exactly, OR
- Query param `?s=...` matches.
- If you went via nginx's `proxy_set_header`, verify nginx is actually adding the header (`curl -v` from another box).

### MAX still hits the old webhook after you removed it from config

`Stop()` does best-effort unsubscribe but does not retry on failure. Manually delete the subscription with the `curl -X DELETE` command above, or call `GET /subscriptions?access_token=...` to see what's currently registered.

### How to verify what MAX has registered

```bash
curl "https://platform-api.max.ru/subscriptions?access_token=$TOKEN" | jq
```

Returns the active webhook URL(s) for the bot. Should be at most one.
