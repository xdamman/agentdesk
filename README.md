# agentdesk

**Spendesk for AI agents** — issue virtual cards to your agents, set per-agent spending policies (amount, interval, allowed merchants), and approve or decline their purchase authorizations from the CLI.

Built on [Stripe Issuing](https://stripe.com/docs/issuing) with a [Bubble Tea](https://github.com/charmbracelet/bubbletea) TUI. Every command also runs non-interactively so you can script it.

---

## Requirements

- Go 1.24+
- A Stripe account with **Issuing** enabled (test mode works out of the box)
- A Stripe secret key (`sk_test_…` for test mode, `sk_live_…` for production)
- Your Stripe Issuing program must support EUR cards — the issuing currency is hardcoded to `eur`
- Default cardholder identity (first/last/DOB/address) is configured during `agentdesk setup` and reused for every new agent

## Install

**One-liner (Linux amd64/arm64):**

```bash
curl -sSL https://raw.githubusercontent.com/xdamman/agentdesk/main/install.sh | bash
```

Installs the latest release into `/usr/local/bin` (or `~/.local/bin` if that isn't writable). Re-run the same command to update. Set `AGENTDESK_VERSION=v0.1.0` to pin a specific tag, or `AGENTDESK_PREFIX=/somewhere/else/bin` to override the install dir.

**From source:**

```bash
git clone https://github.com/xdamman/agentdesk.git
cd agentdesk
go build -o agentdesk .
sudo mv agentdesk /usr/local/bin/   # optional
```

Releases are built by `.github/workflows/release.yml` on every tag matching `v*`, publishing `agentdesk-linux-amd64`, `agentdesk-linux-arm64`, and a SHA-256 `checksums.txt`.

## Storage

All state lives under `~/.agentdesk/`:

```
~/.agentdesk/
├── config.json            # Stripe API key, default cardholder, admin npub, webhook + daemon config
├── agents.json            # registry of agents → Stripe card IDs + local policy
├── rules.json             # auto-approve rules used by the daemon
├── nsec                   # daemon's Nostr secret key (hex, 0600 — never share)
└── <agent-name>/
    └── card               # card PAN, CVC, expiry, policy (0600 perms)
```

Files are created with `0600` / `0700` permissions. Delete the directory to fully reset.

---

## Commands

Every command supports an interactive (TUI) and a non-interactive mode. The non-interactive mode activates when you pass the relevant flags, or when stdout is not a terminal (e.g. piped to `jq`).

### `agentdesk setup`

Configure your Stripe API key and the **default cardholder identity + billing address** used when new agents are created. Stripe Issuing requires a real person's KYC details (first name, last name, date of birth, address) per cardholder — agentdesk reuses these defaults so that only the agent name varies per card.

**Interactive behaviour:**
- First run (no config): prompts for the Stripe key, then opens the billing form.
- Subsequent runs: prints the current settings and shows a picker — choose *Stripe API key*, *Billing & identity*, or *Admin approver (Nostr)* to edit that slice only. Existing settings are preserved if you don't touch them.
- **Alternative:** start `agentdesk daemon` and visit http://localhost:4242/setup in your browser. It serves the same form over HTTP. Saving is restricted to loopback requests, so exposing the daemon publicly (e.g. via ngrok) does *not* let strangers rewrite your Stripe key.

**Non-interactive flags** — pass any subset; unspecified flags leave existing values alone:

| Flag | Env | Description |
| --- | --- | --- |
| `--api-key <key>` | `STRIPE_API_KEY` (first-run only) | Stripe secret key |
| `--first-name <name>` | | Cardholder first name |
| `--last-name <name>` | | Cardholder last name |
| `--dob <YYYY-MM-DD>` | | Date of birth |
| `--email <email>` | | Cardholder email (optional; defaults to `<agent>@agentdesk.local`) |
| `--phone <+e164>` | | Phone number in E.164 format |
| `--address-line1 <...>` | | Street address line 1 |
| `--address-line2 <...>` | | Street address line 2 |
| `--city <...>` | | City |
| `--state <...>` | | State / region |
| `--postal-code <...>` | | Postal code |
| `--country <XX>` | | ISO 3166-1 alpha-2 country (e.g. `FR`) |
| `--admin <id>` | | Admin Nostr identity for approval prompts. Accepts `npub1…`, a 64-char hex pubkey, or a NIP-05 address (`alice@example.com`). Pass `--admin ""` to clear. |
| `--show` | | Print current settings and exit |

```bash
# fully interactive
agentdesk setup

# show current settings
agentdesk setup --show

# one-shot API key
agentdesk setup --api-key sk_test_xxx

# one-shot admin (NIP-05 or npub)
agentdesk setup --admin alice@example.com
agentdesk setup --admin npub1abc...

# one-shot billing details
agentdesk setup \
  --first-name Ada --last-name Lovelace --dob 1815-12-10 \
  --address-line1 "8 rue de Londres" --city Paris \
  --postal-code 75009 --country FR

# complete setup in one go
STRIPE_API_KEY=sk_test_xxx agentdesk setup \
  --first-name Ada --last-name Lovelace --dob 1815-12-10 \
  --address-line1 "8 rue de Londres" --city Paris \
  --postal-code 75009 --country FR
```

### `agentdesk cards`

List every Issuing card on the Stripe account, joined with the local agent registry.

| Flag | Description |
| --- | --- |
| `--json` | Emit JSON (array of card objects) instead of the TUI table. |

```bash
agentdesk cards
agentdesk cards --json | jq '.[] | {agent, last4, status}'
```

### `agentdesk agents`

List agents with their policy, amount spent this period, and remaining allowance. Spend is computed by summing approved authorizations for the card within the current daily/weekly/monthly window.

| Flag | Description |
| --- | --- |
| `--json` | Emit JSON. |

```bash
agentdesk agents
agentdesk agents --json
```

### `agentdesk agents add`

Create a new cardholder and issue a virtual card. The revealed card details are printed once and persisted to `~/.agentdesk/<agent-name>/card`.

| Flag | Required | Description |
| --- | --- | --- |
| `--name <name>` | Triggers non-interactive mode | Agent name. `[a-zA-Z0-9_-]{2,32}` |
| `--allowance <eur>` | With `--name` | EUR amount, e.g. `100` or `100.00` |
| `--interval <d\|w\|m>` | Optional (default `monthly`) | `daily` / `weekly` / `monthly` |
| `--merchants <list>` | Optional | Comma-separated merchant hints (stored in card metadata) |
| `--json` | Optional | Print the created agent + revealed card as JSON |

```bash
# interactive
agentdesk agents add

# non-interactive
agentdesk agents add \
  --name research-agent \
  --allowance 250 \
  --interval monthly \
  --merchants openai.com,anthropic.com

# scriptable
agentdesk agents add --name bot --allowance 50 --json \
  | jq -r '.number'
```

### `agentdesk agents edit [name]`

Edit an existing agent's allowance, interval, or allowed merchants. Passing any of `--allowance`, `--interval`, `--merchants` runs non-interactively and requires the name as a positional arg. Unset flags preserve the existing value.

| Flag | Description |
| --- | --- |
| `--allowance <eur>` | New allowance in EUR |
| `--interval <d\|w\|m>` | New interval |
| `--merchants <list>` | New allowed-merchants list. Pass `--merchants ""` to clear. |

```bash
# interactive (picker → form)
agentdesk agents edit

# non-interactive
agentdesk agents edit research-agent --allowance 500
agentdesk agents edit research-agent --interval weekly --merchants openai.com
agentdesk agents edit research-agent --merchants ""     # clear list
```

### `agentdesk agents rm [name]`

Cancel the agent's Stripe card and delete local state. Pass a positional name for non-interactive use; otherwise a TUI picker is shown.

```bash
agentdesk agents rm research-agent   # non-interactive
agentdesk agents rm                  # picker
```

### `agentdesk requests`

List the latest Stripe Issuing authorizations (purchase attempts from agent cards), across all agents.

| Flag | Description |
| --- | --- |
| `--json` | Emit JSON. |
| `--limit <n>` | Max number of requests (default `25`). |

```bash
agentdesk requests
agentdesk requests --limit 100 --json | jq '.[] | select(.status=="pending")'
```

### `agentdesk requests show <requestId>`

Show detailed info for a single authorization: amount, agent, vendor, category, location, timestamp.

| Flag | Description |
| --- | --- |
| `--json` | Emit JSON instead of the formatted view. |

```bash
agentdesk requests show iauth_1Abc123
agentdesk requests show iauth_1Abc123 --json
```

### `agentdesk requests approve <requestId>` / `decline <requestId>`

Approve or decline an authorization.

Stripe's `issuing_authorization.request` webhook holds an authorization in `pending` state for **2 seconds**. If you call `approve` after the window has closed, the call fails with a 400. When that happens, agentdesk automatically **saves an auto-approve rule** keyed on `card + merchant + amount + date` so that the next identical request is auto-approved by the daemon within the window.

| Flag | Description |
| --- | --- |
| `--json` | Emit the resulting authorization (or rule-save outcome) as JSON. |

```bash
agentdesk requests approve iauth_1Abc123
agentdesk requests decline iauth_1Abc123 --json
```

Example expired-approve output:

```
⚠ iauth_1Abc123 is expired — Stripe's 2s window is closed.
  Saved rule rule_17292931234: auto-approve €12.50 @ openai.com on 2026-04-18
  Run `agentdesk daemon` to catch the next matching request in time.
```

### `agentdesk rules` / `agentdesk rules rm <ruleId>`

List or remove auto-approve rules. Rules are created by the approve-on-expired flow above; the daemon consults them on each `issuing_authorization.request`.

| Flag | Description |
| --- | --- |
| `--json` | Emit rules as JSON (non-interactive). |

```bash
agentdesk rules
agentdesk rules --json | jq '.[] | {id, merchant, amount}'
agentdesk rules rm rule_17292931234
```

### `agentdesk daemon`

A long-running process that does four things in parallel:

1. **Stripe webhook listener** — receives `issuing_authorization.request` events and auto-approves the ones matching a saved rule within the 2-second Stripe window.
2. **Homepage** — serves `GET /` with the daemon's Nostr `npub` (plus a QR code), the list of agents, and recent requests. Visit `http://localhost:4242/` while the daemon runs.
3. **Nostr NIP-04 listener** — accepts DMs from agents requesting virtual cards. See [SKILL.md](./SKILL.md) for the agent-side protocol.
4. **Admin approver over Nostr** — when a request arrives that doesn't match an auto-approve rule, the daemon DMs the admin (configured via `agentdesk setup --admin`) with agent/merchant/amount. The admin replies `approve` / `yes` / `ok` / `👍` (case-insensitive) to approve, or `decline` / `no` / `👎` to decline. If multiple pending asks stack up, each reply drains the oldest; reply `approve iauth_…` to target a specific one. If the admin's reply arrives after Stripe's 2-second window, agentdesk saves an auto-approve rule (same behaviour as `agentdesk requests approve` on an expired auth) so the next identical request is handled automatically.

| Flag | Default | Description |
| --- | --- | --- |
| `--port` | config.daemon_port or `4242` | HTTP port to listen on. |
| `--secret` | `config.webhook_secret` | Webhook signing secret. Required unless `--insecure-skip-verify`. |
| `--path` | `/webhook` | HTTP path for the Stripe webhook. |
| `--insecure-skip-verify` | | Skip webhook signature verification (local dev only). |
| `--no-nostr` | | Disable the Nostr NIP-04 listener entirely. |
| `--nostr-relay <wss://…>` | | Override the default relay list. Repeatable. |

Two ways to feed events to the daemon:

**(a) Local dev — Stripe CLI listen** (no public URL required):

```bash
# in terminal A — stream live events from Stripe to your daemon
stripe listen --forward-to localhost:4242/webhook

# in terminal B — run the daemon (take the secret printed by stripe listen)
agentdesk daemon --port 4242 --secret whsec_xxxxxxx

# in terminal C — generate a test authorization
stripe trigger issuing_authorization.request
```

**(b) Production — register a real endpoint** (requires a public HTTPS URL, e.g. ngrok / cloudflared / your production host):

```bash
# register: creates /v1/webhook_endpoints on Stripe, saves the signing secret
agentdesk daemon register --url https://example.com/webhook

# run the daemon (secret is read from config.json)
agentdesk daemon
```

### `agentdesk daemon register --url <public-url>`

Call Stripe's `POST /v1/webhook_endpoints` to register your public URL for the `issuing_authorization.*` events, and save the signing secret to `~/.agentdesk/config.json`. Must be HTTPS.

| Flag | Description |
| --- | --- |
| `--url <https-url>` | Required. Publicly reachable webhook URL. |
| `--json` | Emit the endpoint + secret as JSON. |

```bash
agentdesk daemon register --url https://agentdesk.example.com/webhook
```

---

## End-to-end example

```bash
# one-time setup (API key + default cardholder identity)
agentdesk setup \
  --api-key sk_test_xxx \
  --first-name Ada --last-name Lovelace --dob 1815-12-10 \
  --address-line1 "8 rue de Londres" --city Paris \
  --postal-code 75009 --country FR

# onboard an agent with a €250/month budget and allowed merchants
agentdesk agents add \
  --name research-agent \
  --allowance 250 \
  --interval monthly \
  --merchants openai.com,anthropic.com \
  --json > agent.json

# extract card details to feed the agent
jq -r '.number' agent.json
jq -r '.cvc'    agent.json

# later: review spend
agentdesk agents --json | jq '.[] | {name, spent_cents, remaining_cents}'

# review pending authorization requests and approve one
agentdesk requests --limit 50 --json | jq '.[] | select(.status=="pending") | .id'
agentdesk requests approve iauth_1Abc123
```

---

## Security

- Card PAN/CVC are retrieved from Stripe only once at creation time (via `expand=number,cvc` on retrieve) and written locally with `0600` permissions. Treat `~/.agentdesk/` as sensitive.
- The Stripe API key is stored in plain JSON under `~/.agentdesk/config.json` (`0600`). Prefer a restricted-scope key (Issuing read/write) over your root secret key.
- Non-interactive mode is scriptable and CI-friendly; do not commit `~/.agentdesk/` to source control.

## Status

Built at a hackathon. Rough edges:

- Issuing currency is hardcoded to EUR. To support other currencies, parametrise `stripeapi.CreateVirtualCard`.
- The daemon only auto-*approves* on rule matches; it doesn't yet auto-decline. For unmatched requests it responds 200 and lets Stripe's `spending_controls` on the card decide.
- Rules match on exact `{card, merchant, amount, date}` — no fuzzy matching or TTL. Consider clearing `rules.json` periodically.
- Only a single spending-limit tier per card (amount + interval). Add more via `stripe.IssuingCardSpendingControlsParams.SpendingLimits` if you need e.g. daily + monthly caps.
- Billing details are shared across all agents. If you need per-agent KYC, extend the `add` command to accept overrides.

Contributions welcome.
