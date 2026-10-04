# vpn

On-demand WireGuard exit node in DigitalOcean Bangalore (`blr1`), managed by
Pulumi (Go). Bring it up when needed, destroy it when done — billed hourly,
capped at ~$4/month.

> [!WARNING]
> The exit IP belongs to DigitalOcean, a datacenter range. Browsing
> India-only content usually works, but payment and checkout pages run stricter
> IP-reputation checks and may reject it or fall back to another region.
> For a reliable Indian connection, use a residential IP instead, such as a
> [Tailscale exit node](https://tailscale.com/kb/1103/exit-nodes) on a device
> in India — see
> [Unlocking geo-restricted contents with Tailscale](https://www.madhan.app/blogs/021-tailscale-geo-content/).

## What it creates

- WireGuard keypairs for the server and each client (`mac`, `phone`), held
  encrypted in Pulumi state.
- A DigitalOcean firewall (via the `wg-vpn` tag) allowing inbound UDP 51820
  only. No SSH; it exists before the droplet boots.
- A `s-1vcpu-512mb-10gb` Ubuntu 24.04 droplet whose cloud-init installs
  WireGuard and NATs tunnel traffic out of its public IP.

Outputs `mac` and `phone` are complete client configs. `pulumi destroy`
removes everything, keys included.

## Layout

| File | Purpose |
|---|---|
| `main.go` | Keys, firewall, droplet, exported client configs |
| `templates/cloud-init.yaml.tmpl` | Droplet first-boot setup |
| `templates/wg-server.conf.tmpl` | Server `wg0.conf`, one `[Peer]` per client |
| `templates/wg-client.conf.tmpl` | Client config exported as `mac` / `phone` |
| `main_test.go` | Offline test: keys, firewall rules and order, rendered configs |
| `mise.toml` | Pinned tools, local Pulumi backend, tasks |

Templates are embedded into the binary at build time.

## Prerequisites

- [mise](https://mise.jdx.dev) — installs the pinned `go` and `pulumi`
- `brew install qrencode` for the phone QR code
- WireGuard app (macOS App Store, iOS/Android)
- A DigitalOcean API token with custom scopes (see below)
- No Pulumi account: `mise.toml` points this project at local state, without
  changing your global `pulumi login`

### DigitalOcean token scopes

API → Tokens → Generate New Token → **Custom Scopes**. A payment method must
be on the account first. Scopes cannot be edited after generation.

| Resource | Scopes | Why |
|---|---|---|
| `droplet` | create, read, update, delete | The VPN server. Leave `admin` unticked: it grants console/SSH-style access |
| `firewall` | create, read, update, delete | UDP-51820-only firewall |
| `tag` | create, read, delete | `wg-vpn` tag that links firewall to droplet |
| `actions`, `regions`, `sizes`, `image` | read | Supporting lookups during droplet creation |

Everything else stays unticked. If the portal auto-selects extra
dependencies when you tick these, keep them. Every `check`/`up` first calls
the API and stops before touching anything if the token fails:

- `401 Unable to authenticate you`: token invalid, revoked or expired
- `403`: token lacks a scope; the error names it
- `droplet size … is not available`: pick another size in `main.go`

To replace the token: `pulumi config set digitalocean:token --secret`.

A 90-day expiry is fine; the token is only used during `up`/`down`.

## Usage

```sh
mise install
mise run init    # "india" stack + DO token (stored encrypted)
mise run check   # token + size check, creates nothing
mise run up      # create + write india.conf; WireGuard ready ~1 min later
mise run mac     # rewrite india.conf from the current stack
mise run qr      # scan with the phone's WireGuard app
mise run down    # destroy; billing stops
mise run test    # offline test against Pulumi mocks
```

## Connect

**Mac:** `mise run up` (or `mise run mac`) writes `india.conf` in this repo
directory (gitignored, `0600`). WireGuard app → **Import Tunnel(s) from File…** → pick `india.conf`
→ **Allow** the VPN configuration → **Activate**. Then delete `india.conf`.

**Phone:** `mise run qr` → WireGuard app → **+** → **Create from QR code**.

**Verify:** `curl ifconfig.io/country_code` should print `IN`.

Each `up` gets a new IP and new keys: re-import the Mac config and rescan
the QR code every time. Delete `india.conf` after importing; `mise run mac`
regenerates it while the stack exists.

Set `PULUMI_CONFIG_PASSPHRASE` to avoid the passphrase prompt.

Adding a device: append its name to `clients` in `main.go`, then `mise run up`
and `pulumi stack output <name> --show-secrets`.

## Secrets

Prompts for the passphrase; prints plain text.

```sh
pulumi config get digitalocean:token      # DO token
pulumi stack output --show-secrets        # ip + client configs
```

Lost passphrase = unrecoverable: delete resources in the DO dashboard, then
`pulumi stack rm india --force` and `mise run init`.

## Teardown

`mise run down` removes cloud resources and keys; keeps stack and token for
the next `up`. To remove everything:

```sh
mise run down && pulumi stack rm india && rm -f india.conf
```

Then revoke the token in DO → API → Tokens.

## Security and privacy

- **State** lives in `~/.pulumi`, not this repo. Secrets in it, and the DO token
  in `Pulumi.india.yaml` (gitignored), are encrypted with your passphrase — use
  a strong one.
- **Server private key** is in the droplet's user data, readable by anyone with
  your DO token. Keys are discarded on every `down`.
- **Client config** `india.conf` holds a private key; it is written `0600` and
  gitignored.
- **IPv6** is routed into the tunnel and dropped, so it cannot leak around the
  India exit.
- **DNS** goes to `1.1.1.1` through the tunnel; change `DNS =` in
  `templates/wg-client.conf.tmpl` for another resolver.
- **No kill switch** by default: if the tunnel drops, traffic goes out directly.
  Enable on-demand in the WireGuard app if that matters.
- DigitalOcean can see traffic metadata; the droplet is under Indian
  jurisdiction.
