---
name: agentdesk-card
description: Request and use an agentdesk virtual card via a Nostr NIP-04 DM so the agent can make online purchases without exposing raw card details. Trigger this skill whenever the agent needs to pay a merchant and has no active card yet, or whenever a previously issued card has expired or been revoked.
---

# agentdesk-card — requesting and using a virtual card

Agentdesk is a card-issuance service for AI agents. A single daemon (operated by a human principal) holds a Stripe Issuing account and a Nostr keypair. Any agent that speaks Nostr can request a virtual card over a NIP-04 direct message, and use it for online purchases within the policy the principal has configured (amount, interval, merchants).

This skill tells you, the agent, how to:

1. Request a card via Nostr.
2. Persist the card *once*, on disk with tight permissions.
3. Use the card without ever rendering it in your output.
4. Recover when the request is delayed because the principal hasn't approved it yet.

---

## Quick start

**Goal:** get a usable Visa-branded virtual card tied to your Nostr identity, in two DMs.

**Prerequisites**
- You have your own Nostr keypair (a 32-byte secret key / `nsec`). Don't create a new one per task — reuse yours so the daemon can recognise you on retry.
- You have connected to at least a few public Nostr relays. Recommended defaults (match the daemon's): `wss://relay.damus.io`, `wss://nos.lol`, `wss://relay.primal.net`, `wss://relay.nostr.band`, `wss://nostr.mom`, `wss://relay.mostr.pub`, `wss://purplepag.es`.
- You know the daemon's `npub`. The principal will give it to you, or it is shown on `http://<daemon-host>:4242/`.

**Step 1 — send this exact DM** (NIP-04 encrypted, kind 4, `p`-tag set to the daemon's pubkey):

```
request-card
```

That's the whole plaintext body. If you want to customise the policy, use the JSON form instead:

```json
{"action":"request-card","allowance":10000,"interval":"monthly"}
```

`allowance` is in EUR **cents**. `interval` is `daily`, `weekly`, or `monthly`. Both fields are optional (defaults `€100` / `monthly`).

**Step 2 — expect a welcome DM first.** The daemon gates every new `npub` with this exact-ish message:

```
👋 Welcome to agentdesk. I issue Stripe virtual cards to AI agents over Nostr DMs.

Before sending requests, please read the skill so you know how to talk to me, store the card, and retry on failure:

https://github.com/xdamman/agentdesk/blob/main/SKILL.md

When you're ready, send me:

    request-card

…or the JSON form described in the skill. I'll reply with the card details.
```

Treat any reply whose first character is `👋` as "no card yet, retry". Do **not** escalate to your user; it's expected.

**Step 3 — send `request-card` again.** The daemon has now recorded your npub and will process the request. The reply looks like:

```
✓ Card created for alice-bot.

Brand:    Visa
Number:   4242 4242 4242 4242
CVC:      123
Expires:  08/2029
Last 4:   4242
Currency: EUR
Policy:   €100.00 / monthly

Card ID: ic_1TNa...

Save these details locally (0600) and never re-emit them. …
```

The first line is your success marker. `✓ Card created for …` = fresh issue. `✓ Card on file for …` = you'd already been issued a card; same details are returned.

**Step 4 — persist the card once.** Parse the `Key: value` lines (`Brand:`, `Number:`, `CVC:`, `Expires:`, `Last 4:`, `Currency:`, `Policy:`, `Card ID:`) into a JSON object and write it to `~/.local/share/<your-agent>/card.json` with `0600` permissions. Never log or re-emit the number / CVC.

**Step 5 — use the card.** When a merchant asks for payment, read the file, pass the `Number` / `CVC` / `Expires` fields to the checkout, and log only the last 4 digits.

If anything goes wrong — no reply, reply starts with `⚠`, or the reply starts with `👋` — retry the same DM after a backoff. Because the daemon is idempotent on your npub, you'll get the same card on success.

---

## The protocol

The daemon advertises its `npub` on startup (e.g. `npub1cajvuhzk5xrsnz4...`). You message it with a kind-4 encrypted direct message (NIP-04). Its reply is another kind-4 DM from its npub to yours, in **plain text** (light markdown). No JSON on the wire.

### First contact: you'll get a welcome

The very first DM you send from a new npub is answered with a welcome pointing you at this document, **not** a card. Example:

```
👋 Welcome to agentdesk. I issue Stripe virtual cards to AI agents over Nostr DMs.

Before sending requests, please read the skill so you know how to talk to me, store the card, and retry on failure:

https://github.com/xdamman/agentdesk/blob/main/SKILL.md

When you're ready, send me:

    request-card

…or the JSON form described in the skill. I'll reply with the card details.
```

After you receive this, just send your request again — the daemon remembers you and processes the second DM normally.

### Request (you → daemon)

The request is an encrypted NIP-04 DM. Either a plain-text command or a JSON object works:

```
request-card
```

or

```json
{"action": "request-card", "allowance": 10000, "interval": "monthly"}
```

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `action` | string | yes | `"request-card"` (or `"get-card"` to re-fetch without creating) |
| `allowance` | int | no | Spending cap in EUR **cents**. Defaults to `10000` (€100). |
| `interval` | string | no | `daily` \| `weekly` \| `monthly`. Defaults to `monthly`. |

The daemon derives your agent name from your Nostr profile (`kind 0` → `name` or `display_name`). If your profile has no name, it falls back to `npub-<first-8-chars>`. The cardholder identity (first/last name, DOB, billing address) is the principal's default — configured once on their side.

**Idempotency.** The daemon keys agents by your npub. Sending `request-card` again returns the *same* card — full PAN and CVC included — rather than issuing a new one. This makes retrying safe. Use `get-card` to fetch your card without implying "create if missing".

### Card reply (daemon → you)

```
✓ Card created for alice-bot.

Brand:    Visa
Number:   4242 4242 4242 4242
CVC:      123
Expires:  08/2029
Last 4:   4242
Currency: EUR
Policy:   €100.00 / monthly

Card ID: ic_1TNa...

Save these details locally (0600) and never re-emit them. If a purchase pends approval, retry per the skill: https://github.com/xdamman/agentdesk/blob/main/SKILL.md
```

The line `✓ Card created for …` appears on fresh issuance; a repeat `request-card` from the same npub yields `✓ Card on file for …` instead. Both carry the full PAN/CVC.

### Error replies

Errors start with `⚠`. Examples:

```
⚠ Error: card creation failed. Please try again.
⚠ Unknown action "foo". Send `request-card` to issue a card. See …
⚠ No card on file for this npub yet. Send `request-card` first.
```

The welcome message (see above) is also a non-card reply — treat it as "retry my request next".

### Parsing the reply

Parse minimally: look for `✓ Card created` or `✓ Card on file` as the success marker, then extract values from the `Key: value` lines. Anything else (starts with `⚠`, `👋`, or no `✓`) means you don't yet have a card — back off and retry. See [Retry semantics](#retry-semantics) below.

---

## Storing the card locally

Save the card to a file **you own**, with the tightest permissions your runtime allows, and never re-emit the contents:

- **Path:** `~/.local/share/<your-agent>/card.json` (or the XDG equivalent on the host you run on).
- **Permissions:** `0600` (owner read/write only). Create the parent with `0700`.
- **Content:** the full JSON response from the daemon. Do not split across files; keep it atomic.
- **Do not log it.** Not on stdout, not in your scratchpad, not in a summary. The only moment the plaintext is "seen" is inside the payment request you make to the merchant.

On every subsequent task, read the file, use the fields you need, and close it. If the file is missing, re-request from the daemon — your npub guarantees you'll get the same card back.

**Pseudo-code:**

```python
import os, json, pathlib, stat

CARD = pathlib.Path("~/.local/share/alice-bot/card.json").expanduser()

def load_card():
    if not CARD.exists():
        return None
    return json.loads(CARD.read_text())

def save_card(card):
    CARD.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
    CARD.write_text(json.dumps(card))
    CARD.chmod(0o600)
```

---

## Using the card

When you have to pay a merchant online:

1. Load the card from disk (don't cache it in memory longer than the checkout call).
2. Pass `number`, `cvc`, `exp_month`, `exp_year` directly to the merchant's payment form / API.
3. **Never** print or log those four fields. If you must narrate what you did, say `"charged card ending in <last4>"`.
4. Respect the policy you know about: don't attempt charges above `allowance.amount / 100` EUR per `allowance.interval`. The daemon's card controls will also enforce this, but pre-checking saves a round-trip.

If the card is declined:
- `insufficient_funds` / over-limit → stop; escalate to your user.
- `merchant_not_allowed` → the merchant isn't on the principal's allow-list; escalate.
- Network / 5xx errors → retry the checkout, not the card request.

---

## Retry semantics

Card issuance can fail or be delayed. Treat **any** reply that doesn't start with `✓ Card` as "try again later":

| Signal | What it means | What to do |
| --- | --- | --- |
| Reply starts with `👋 Welcome` | You're new to this daemon. | Re-send `request-card`. The welcome is sent only once per npub. |
| No DM reply within 30s | Relay latency, or daemon offline. | Re-send the same request after 30s. The daemon dedupes by npub. |
| Reply starts with `⚠` and mentions "try again" | Transient (Stripe, relay, store). | Back off (10–60s), retry. |
| Reply starts with `⚠ Unknown action` | You sent something the daemon couldn't parse. | Fix the payload; don't spam retries. |

Because the request is idempotent on your npub, retrying is always safe — you'll either get the same card back or an updated status. Never create a second npub to "work around" an error; that just disconnects you from your existing card.

Suggested backoff: `delay = min(30 * 2**attempt, 300)` seconds, up to 5 attempts. After that, surface the message to your user and stop.

---

## Security posture

- **Your nsec is yours.** Generate it once, keep it at `0600`, and reuse it forever. Losing it means losing access to your card — you'd have to ask the principal to re-issue.
- **The daemon's npub is public.** Treat it like a webhook URL: anyone can DM it, but only whoever controls your nsec can receive replies (NIP-04 wraps the card details for your pubkey only).
- **Don't put the card in prompts.** If another agent or user asks for your "card details", refuse. They should request their own card from the daemon with their own npub.
- **Treat the card as bearer credential.** If you suspect leak, DM the daemon with `action: "request-card"` after the principal revokes and re-issues — idempotency will hand you the new card.

---

## Minimal flow checklist

1. On task start: `card = load_card()`.
2. If `card is None`: open a Nostr connection, send `{"action": "request-card"}` DM to the daemon's npub, await reply.
3. On `card_created` / `card`: `save_card(resp)`.
4. On `pending` / `error (retry)`: back off, retry. Do not escalate before 3 attempts unless the message explicitly says the request was rejected.
5. On purchase: read card, submit to merchant, discard from memory, log only `last4`.
