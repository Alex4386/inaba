# Configuration

Each setting is read from, lowest first:

1. built-in defaults,
2. every settings file found, merged in this order:
   1. `/etc/inaba/<name>` (not on Windows),
   2. `$CONFIGURATION_DIRECTORY/<name>` (systemd's `ConfigurationDirectory=`),
   3. your user config folder: `${XDG_CONFIG_HOME:-~/.config}/inaba/<name>` on Linux,
      `~/Library/Application Support/inaba/<name>` on macOS, `%AppData%\inaba\<name>` on Windows,
   4. the file named by `--config` or `INABA_CONFIG`, which must exist;
3. for the server, the systemd credential `oidc_client_secret` (`LoadCredential=`),
4. `INABA_` environment variables,
5. command-line flags you actually pass.

`<name>` is `server.json`, `server.yaml` or `server.yml` for the server, and `config.json`,
`config.yaml` or `config.yml` for the host; the extension picks the format, and two formats
of one name in one folder is an error. The working directory is never searched, so running
inaba inside a repository cannot let that repository's file change which commands it runs;
use `--config` to name a file on purpose. The server and host log which files they read when
they start.

Environment names are the setting's key in capitals; `__` goes one level down, so
`oidc.client_secret` is `INABA_OIDC__CLIENT_SECRET`. The `oidc` settings also take a single
underscore, `INABA_OIDC_CLIENT_SECRET`; when both forms are set, the `__` one wins. Lists
take commas: `INABA_ICE_URLS=stun:a.example.com:3478,stun:b.example.com:3478`. Maps take one
variable per entry, with the entry's name lowercased: `INABA_OIDC__EXTRA_PARAMS__HD=example.com`.
Once read, variables whose names hold `SECRET` or `TOKEN` are removed from the process, so
agents and terminals the host starts never see them.

| Server setting | Flag | Environment |
| --- | --- | --- |
| `listen` (`:8080`) | `--listen` | `INABA_LISTEN` |
| `public_url` | `--public-url` | `INABA_PUBLIC_URL` |
| `data` | `--data` | `INABA_DATA` |
| `socket` | `--socket` | `INABA_SOCKET` |
| `ice_urls` (only the built-in STUN server if `stun_enable` is on, which needs `public_url`, else `stun:stun.cloudflare.com:3478`) | `--ice-url` | `INABA_ICE_URLS` |
| `stun_enable` (`false`) runs the built-in STUN server; its UDP port must be reachable from outside | `--stun-enable` | `INABA_STUN_ENABLE` |
| `stun_listen` (`:3478`) | `--stun-listen` | `INABA_STUN_LISTEN` |
| `oidc.issuer` | `--oidc-issuer` | `INABA_OIDC__ISSUER` |
| `oidc.client_id` | `--oidc-client-id` | `INABA_OIDC__CLIENT_ID` |
| `oidc.client_secret` | `--oidc-client-secret` | `INABA_OIDC__CLIENT_SECRET` |
| `oidc.name` (`SSO`) | `--oidc-name` | `INABA_OIDC__NAME` |
| `oidc.scopes` (`profile`, `email`) | `--oidc-scope` | `INABA_OIDC__SCOPES` |
| `oidc.extra_params` | `--oidc-extra-param KEY=VALUE` | `INABA_OIDC__EXTRA_PARAMS__<KEY>` |
| `oidc.allowed_domains` | `--oidc-allowed-domain` | `INABA_OIDC__ALLOWED_DOMAINS` |
| `oidc.allowed_users` | `--oidc-allowed-user` | `INABA_OIDC__ALLOWED_USERS` |
| `oidc.allowed_groups` | `--oidc-allowed-group` | `INABA_OIDC__ALLOWED_GROUPS` |
| `oidc.groups_claim` (`groups`) | `--oidc-groups-claim` | `INABA_OIDC__GROUPS_CLAIM` |
| `oidc.email_verified_required` (`true`) | `--oidc-email-verified-required` | `INABA_OIDC__EMAIL_VERIFIED_REQUIRED` |
| `oidc.admin_groups` | `--oidc-admin-group` | `INABA_OIDC__ADMIN_GROUPS` |
| `trusted_proxies` | `--trusted-proxy` | `INABA_TRUSTED_PROXIES` |
| `frame_ancestors` (`'self'`) | `--frame-ancestor` | `INABA_FRAME_ANCESTORS` |
| `notify_private` (`false`) lets notification channels (webhooks, ntfy) post to loopback, private, link-local and CGNAT addresses, such as a self-hosted ntfy on the same network | `--notify-private` | `INABA_NOTIFY_PRIVATE` |

List flags repeat (`--oidc-allowed-domain a.example --oidc-allowed-domain b.example`) or take
commas. Every `oidc` row also works as `INABA_OIDC_<NAME>`, such as `INABA_OIDC_ISSUER`.

## OIDC

Set `oidc.issuer` and `oidc.client_id` to turn on OIDC sign-in. `oidc.client_secret` is
optional: without it inaba is a public client and relies on PKCE alone, which suits a
provider app registered as public or "native". With no access rule, **anyone your identity
provider accepts can sign in and gets an account**, and the server logs a warning at start.
Shared hosts are open to every account unless you make them members-only.

- **Access rules.** `allowed_domains` (the part after `@`), `allowed_users` (whole email
  addresses) and `allowed_groups` each limit who may sign in. Every rule you set must pass,
  on every sign-in, for new and existing accounts alike; a refused sign-in gets a plain 403
  and the reason is logged on the server. Email rules need the provider to mark the address
  verified unless `email_verified_required` is `false`. Domain and email matches ignore
  case; group names match exactly.
- **Groups.** `groups_claim` names the ID token claim that lists the user's groups, a string
  or an array (default `groups`). Many providers send it only when asked: add their groups
  scope to `scopes`.
- **Scopes.** `openid` is always asked for; `scopes` replaces the default `profile email`.
- **Admin groups.** With `admin_groups` set, the provider decides who is an administrator:
  members are made administrators at sign-in, and an OIDC account that left the groups loses
  it at its next sign-in, unless it is the last administrator. A session already open keeps
  its rights until then. Left empty, inaba never changes administrator rights itself.
- **Extra params** are added to the provider's sign-in URL, such as `prompt=login`,
  `login_hint` or Microsoft's `domain_hint`. Parameters the flow sets itself (`state`,
  `nonce`, `scope`, `client_id`, ...) are refused at start. Google's `hd=example.com` only
  hints which account to pick; anyone can change it in the URL. `allowed_domains` is what
  enforces the domain, and also checks Google's `hd` claim.

```yaml
oidc:
  issuer: https://accounts.google.com
  client_id: 1234.apps.googleusercontent.com
  name: Google
  extra_params: { hd: example.com }
  allowed_domains: [example.com]
```

## Behind a reverse proxy

Set `trusted_proxies` to the proxy's addresses (CIDRs or single IPs, such as
`127.0.0.1,10.0.0.0/8`). Only requests from those peers have their `X-Forwarded-For` or
`X-Real-IP` read for the client's address. **Without it, behind a proxy every request looks
like it comes from the proxy**, so one client's failed sign-ins slow down everyone's. Never
list addresses that untrusted clients can connect from, or they can claim any address.
`X-Forwarded-For` is read first, so the proxy must set or append it (nginx:
`proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`); a proxy that only sets
`X-Real-IP` and passes a client's own `X-Forwarded-For` through lets that client pick its
address.

`frame_ancestors` lists the pages that may show the web UI in a frame, as CSP
`frame-ancestors` sources: `'self'` (the default), `'none'`, or origins and host patterns
such as `https://*.example.com`. `*` sends no frame header at all, letting any page frame it.

| Host setting | Flag | Environment |
| --- | --- | --- |
| `server` | `--server` | `INABA_SERVER` |
| `token` | `--token` | `INABA_TOKEN` |
| `data` | `--data` | `INABA_DATA` |
| `socket` | `--socket` | `INABA_SOCKET` |
| `listen` | `--listen` | `INABA_LISTEN` |

`listen` serves the host's API with every permission and no sign-in, for local
development; it accepts only a loopback address such as `127.0.0.1:8081`, answers only
requests addressed to `localhost`, `127.0.0.1` or `[::1]`, and refuses cross-origin browser
requests.

The host's files also hold the agents it offers under `"providers"`; a provider in a later
file replaces one with the same id. The host settings page writes providers only to your
user config file (created `0600` if missing), and only the entries you edit there, leaving
its other keys alone; it never rewrites `/etc/inaba` or `--config`, so providers defined
there keep following those files. Deleting a provider that a lower file defines writes
`removed: true` for that id to your user file, which hides it; adding it again replaces
that entry. A settings file holding a
secret (`oidc.client_secret`, `token`) must be readable by its owner only (`chmod 600`), or
inaba refuses to start; on Windows this is not checked. Prefer the environment or a systemd credential for secrets: flags
show up in `ps`.

**Where data goes** when no setting says otherwise. `data` is the SQLite file; its folder
also holds the host's worktrees and sign-in. The control socket is `DATA.sock` beside it
(moved to a short name in the temp folder if the path is too long for the OS), so every
command that finds the database also finds a running server or host.

| | Database |
| --- | --- |
| Linux | `${XDG_DATA_HOME:-~/.local/share}/inaba/` |
| macOS | `~/Library/Application Support/inaba/` |
| Windows | `%LocalAppData%\inaba\` |
| Server as root | `/var/lib/inaba/` |

The server's database is `inaba-server.db`, the host's `inaba.db`.

On Windows, whoever may enter the folder the control socket is in may administer inaba
through it. The default folder is yours alone; keep `data` in one that is, too.

Example `server.yaml`:

```yaml
listen: ":8080"
public_url: https://inaba.example.com
ice_urls: ["stun:inaba.example.com:3478"]
oidc:
  issuer: https://sso.example.com
  client_id: inaba
  name: Example SSO
```
