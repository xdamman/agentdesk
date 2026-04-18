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

## The protocol

The daemon advertises its `npub` on startup (e.g. `npub1cajvuhzk5xrsnz4...`). You message it with a kind-4 encrypted direct message (NIP-04). Its reply is another kind-4 DM from its npub to yours.

### Request payload (you → daemon)

JSON, encrypted with NIP-04:

```json
{
  "action": "request-card",
  "allowance": 10000,
  "interval": "monthly"
}
```

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `action` | string | yes | `"request-card"` (or `"get-card"` to re-fetch without creating) |
| `allowance` | int | no | Spending cap in EUR **cents**. Defaults to `10000` (€100). |
| `interval` | string | no | `daily` \| `weekly` \| `monthly`. Defaults to `monthly`. |

The daemon derives your agent name from your Nostr profile (`kind 0` → `name` or `display_name`). If your profile has no name, it falls back to `npub-<first-8-chars>`. The cardholder identity (first/last name, DOB, billing address) is the principal's default — configured once on their side.

**Idempotency.** The daemon keys agents by your npub. Sending `request-card` again returns the *same* card — full PAN and CVC included — rather than issuing a new one. This makes retrying safe. Use `action: "get-card"` to fetch your card without implying "create if missing".

### Successful response (daemon → you)

```json
{
  "type": "card_created",   // or "card" when it already existed
  "agent": "alice-bot",
  "card_id": "ic_1TNa...",
  "number": "4242424242424242",
  "cvc": "123",
  "exp_month": 8,
  "exp_year": 2029,
  "brand": "Visa",
  "last4": "4242",
  "currency": "EUR",
  "allowance": { "amount": 10000, "interval": "monthly" }
}
```

### Error / pending response

```json
{ "type": "error",   "message": "card creation failed — retry" }
{ "type": "pending", "message": "awaiting principal approval", "retry_in": 30 }
```

Anything other than `type: "card_created"` or `type: "card"` means you do **not** yet have a usable card — treat it as a transient failure. See [Retry semantics](#retry-semantics) below.

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

Card issuance can fail or be delayed. Treat **any** response other than `card_created` or `card` as "try again later":

| Signal | What it means | What to do |
| --- | --- | --- |
| No DM reply within 30s | Relay latency, or daemon offline. | Re-send the same request after 30s. The daemon dedupes by npub. |
| `type: "pending"` | Principal configured manual approval and hasn't approved yet. | Wait `retry_in` seconds (default 30 if absent), then re-send `action: "get-card"`. |
| `type: "error"` with `"retry"` in message | Transient (Stripe, relay, store). | Re-send after 10-30 seconds with exponential backoff (cap ~5 min). |
| `type: "error"` with no `"retry"` hint | Permanent (bad action, unknown npub gating). | Do **not** retry; surface the `message` to your user. |

Because the request is idempotent on your npub, retrying is always safe — you'll either get the same card back or an updated status. Never create a second npub to "work around" an error; that just disconnects you from your existing card.

Suggested backoff: `delay = min(30 * 2**attempt, 300)` seconds, up to 5 attempts. After that, surface the last `message` and stop.

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
