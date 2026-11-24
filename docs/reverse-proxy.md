# Running behind a reverse proxy

MeuPreço resolves the client IP itself so that rate limiting, logging and the
login throttle work when the process is not the first hop. This page describes
the contract: how the resolver decides which headers to trust, how to configure
`KNOWN_PROXIES`, and how to verify the result.

The API is compatible with any reverse proxy that forwards the standard
`X-Forwarded-*` headers, including Cloudflare-fronted deployments: the resolver
also understands `CF-Connecting-IP` and `True-Client-IP` when the proxy passes
them through.

## The rule: trust the peer, not the header

A request carries two identities:

- `remote_addr` — the address of the peer that opened the TCP connection to the
  app. It cannot be forged, but behind a proxy it is the proxy.
- the forwarding headers — a list of addresses supplied by the client and every
  intermediate proxy. Any of them can be forged unless a trusted proxy appended
  them.

The resolver only reads forwarding headers when the **immediate peer** is listed
in `KNOWN_PROXIES`. From an untrusted peer, all forwarding headers are ignored
and `remote_addr` is used. This is what stops a client from choosing its own
rate-limit bucket with `X-Forwarded-For: 1.2.3.4`.

When the peer is trusted, headers are evaluated in this order:

1. `X-Forwarded-For`, walked right-to-left: the first valid address that is not
   itself a trusted proxy is the client. A client-injected value sits on the far
   left and is skipped.
2. RFC 7239 `Forwarded` (`for=` parameters), same right-to-left rule.
3. `CF-Connecting-IP`, `True-Client-IP`, `X-Real-IP`, in that order. These are
   single-value hints and are only used when the lists above are absent or
   unusable. A value that is itself a trusted proxy is skipped.

If nothing yields an address, the peer is used.

> [!IMPORTANT]
> List **every** proxy hop that sits between the client and the app. In a
> `proxy -> proxy -> app` chain, the intermediate proxy appears in
> `X-Forwarded-For`; if it is not trusted, the resolver stops at it and reports
> the proxy address instead of the client.

## `KNOWN_PROXIES`

Comma-separated list of trusted peers. Default: `127.0.0.1,::1` (loopback only).

Accepted entries:

| Form     | Example                  | Notes                                                |
| -------- | ------------------------ | ---------------------------------------------------- |
| IPv4     | `192.0.2.10`             | becomes `/32`                                        |
| IPv6     | `2001:db8::10`           | becomes `/128`                                       |
| CIDR     | `172.18.0.0/16`          | kept as-is                                           |
| Hostname | `proxy.internal`         | resolved at startup                                  |
| URL      | `https://proxy.internal` | scheme must be `http`/`https`; only the host is used |

Details:

- Hostnames are resolved **once at startup**, with a 5-second timeout. Every
  returned address is trusted. A hostname that fails to resolve aborts startup
  with an error naming `KNOWN_PROXIES`, so a typo cannot silently fall back to
  the untrusted path. Restart the process after a DNS change.
- IPv4-mapped IPv6 input (`::ffff:192.0.2.10`) is normalized to IPv4.
- Duplicate entries are removed.
- The list is read from the environment; the compiled binary never reads `.env`
  itself.

## Topologies

### A single proxy in front of the app

```text
client -> proxy (e.g. 172.18.0.5) -> app (172.18.0.10:8080)
```

Set `KNOWN_PROXIES` to the address the app sees as its peer. That is usually the
proxy container/host address, or its whole Docker network:

```dotenv
KNOWN_PROXIES=172.18.0.0/16
```

The proxy must forward the client address, for example in nginx syntax:

```nginx
proxy_set_header Host              $host;
proxy_set_header X-Real-IP         $remote_addr;
proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
```

Nginx Proxy Manager exposes the same options in the proxy host's _Advanced_ tab.

### Two proxies in front of the app

```text
client -> edge proxy -> local proxy -> app
```

List both hops. Trusting the whole private proxy network is the simplest form:

```dotenv
KNOWN_PROXIES=127.0.0.1,::1,172.18.0.0/16
```

If the two hops are on different networks, list both CIDRs. Only peers that
actually connect to the app need to be listed, but any proxy that appends an
address to `X-Forwarded-For` must be, or the walk stops there.

### Finding the addresses

```sh
# Container/network addresses, including the subnet to use as a CIDR
docker network inspect <network> \
  --format '{{range .Containers}}{{.Name}} {{.IPv4Address}}{{"\n"}}{{end}}'

# Subnet and gateway of a network
docker network inspect <network> \
  --format '{{range .IPAM.Config}}{{.Subnet}} {{.Gateway}}{{end}}'
```

For a proxy on the same host outside Docker, `127.0.0.1` is usually the peer
address. For a proxy on another machine, use its LAN address or the CIDR of the
LAN.

## Verifying

The access log line contains both addresses:

```text
msg=http_request method=GET path=/healthz status=200 ... client_ip=203.0.113.7 remote_addr=172.18.0.5:54321
```

- `client_ip` is the resolved client and the key used by per-IP rate limiting
  and by the request logs.
- `remote_addr` is the immediate peer. If `client_ip` equals `remote_addr` and
  you expected a public client address, either the peer is not in
  `KNOWN_PROXIES`, or the proxy is trusted but does not forward the client
  address. In the first case, and only when forwarding headers were present, the
  app logs a one-time warning:

  ```text
  proxy headers ignored from untrusted peer; add it to KNOWN_PROXIES if it is a trusted proxy
  ```

  In the second case there is no warning: the resolver simply has nothing to
  read.

Forged-header check: the header must be ignored when the request reaches the app
from a peer that is **not** in `KNOWN_PROXIES`.

```sh
# Run from a host or container whose source address is not trusted.
curl -sS -H 'X-Forwarded-For: 203.0.113.9' http://<app-host>:8080/healthz
```

Expected log line: `client_ip` equals the real source of the request, never
`203.0.113.9`. If you send the same request from loopback while the default
`KNOWN_PROXIES=127.0.0.1,::1` is active, loopback is a trusted peer and the
header **is** honored — that is the trusted path, not a bypass.

## Operational notes

- A proxy access list or authentication prompt protects the proxy, not the API.
  Application authentication and the built-in rate limits still apply.
- Keep the app port reachable only from the proxy: bind it to loopback on a
  single-host setup, or to a private Docker network. Do not publish it directly
  to the internet.
- When TLS terminates at the proxy, the app speaks plain HTTP; the
  `X-Forwarded-Proto` header is preserved for logging and any future scheme
  checks.
- `KNOWN_PROXIES` affects rate-limit keys. If it is wrong, every client can
  collapse into one bucket (when the proxy is trusted but does not forward the
  client) or every client can appear as the proxy.
