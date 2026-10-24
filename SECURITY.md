# Security

Talaria is an agent that can run commands, edit files, drive a browser and act on your accounts, and it
reads text written by other people. This file says what it defends against, what it does not, and how
far it has been checked.

## What it assumes

- **You, on your own machine, are trusted.** Talaria is one person's agent, run by that person. The people in
  `allowed_users` are trusted with everything the agent can do and hold (see "Only one person" in the README).
- **Everything the model reads is not.** Web pages, mail, a connected service's replies, files from elsewhere
  and what a subagent brings back may contain instructions meant for the model, and the model may follow them.
  The model's own output is not trusted either: what stops harm is what the program will do whatever the model
  says (approval, rules, the guard on outside content, the file fence), not the model behaving.
- **Anyone on the internet can message the bot.** Only allowlisted user IDs are served; everyone else gets
  nothing (at most one reply a few minutes apart).
- **A connected service or an authorization server can be hostile**, within limits (see the README).

## What it does not defend against

- Someone who already has your user account on the machine, or your unlocked vault and its key.
- A shell command you approve: it runs as you.
- **A deletion or a post that is not recognised.** By default (`policy.permissions: "deletes_and_posts"`) Talaria asks only
  before it deletes something or posts on social media, so what counts is decided by the tool (a tool declares that it deletes
  or posts), by what a shell command says (a list of deleting commands and patterns, best effort: a script that removes a file
  from inside, or a command spelled in an unusual way, is not seen), and by the site a browser is on (a list of social sites).
  In that mode the guard that makes anything able to send something out ask once outside content was read is off, so text on
  a web page can talk the agent into commands that are not deletes or posts. `"strict"` restores asking for everything risky
  and that guard. A connected service's send, pay or publish tools are asked about only if their name or a rule says so.
- **Questions that wait.** A question is saved in `~/.talaria/held_*.json` (mode 0600) with the exact call, and carried out when
  answered, so a question left for days can be answered long after the page that showed it has changed; it names what will
  run and lapses after a week. Only a signed-in user (or an allowlisted chat) can answer it, once, by its unguessable token.
- A false statement the model is talked into making to you. Nothing here checks the model's claims.
- Prompt injection that needs no tool at all.
- A compromised model provider, Telegram or Discord, or Docker. Messages and model requests pass through them.
- Several mutually distrusting users. There is no isolation between people on the allowlist apart from their chat history.

## Reviews so far

Three reviews of the code were made by separate AI reviewers, each working from the source alone with no knowledge of
what the author thought was sound, covering (1) sign-in, vault and connected services, (2) permissions, the agent loop, tools,
browser and unattended runs, and (3) the chat gateways, history, skills and model clients. Their findings were checked against
the code, and each one that held was fixed with a test that fails without the fix. The ones not fixed are listed below. The
tests also include deliberate breakages of the new code to confirm they are caught.

**This is not an independent human audit, and it has no cryptographer's or penetration tester's sign-off.** Before
trusting Talaria with something valuable, have a person who does this for a living look at:

- `internal/vault` (key derivation, file format, locking), `internal/oauth` (discovery, login, refresh, revocation),
- `internal/guard`, `internal/agent` (the gate and the guard on outside content), `internal/execenv/fence.go`,
- `internal/browser` (address checks, filling saved logins), `internal/gateway/*` (who may talk to the agent, approvals).

## Calendars

A calendar is read from the secret iCal address the calendar service gives out. That address is a bearer secret: it is kept in the
environment (named in the config, never written in it), removed from the environment of the agent's shell commands, scrubbed
from output and history, fetched over https only and never from a private address, and never put in an error message. What is on a
calendar is written by others and is treated as outside content. The tool cannot change a calendar.

## Email

A mailbox is read over IMAP with an app password, which is kept in the environment, removed from the environment of the agent's
shell commands, scrubbed from output and history, and sent only over a verified TLS connection. The client can send seven commands
and none can change a mailbox (folders are opened with EXAMINE, messages fetched with BODY.PEEK); anything that is not on that list is
refused in code, and a test checks what the server is sent. Text from the model goes into a search only after control characters are
refused and quotes escaped, or as a length-prefixed literal, so it cannot end a command and start another. What the server sends back
is treated as hostile: lines, blocks of data and whole answers have size limits, and nesting has a depth limit. Mail is written by
others and is treated as outside content: invisible characters are removed, the real sender address is always shown, and the agent has
no tool that sends mail. Attachments are named, never opened.

## Web app

The web app is the one part of Talaria that listens for the internet, so it is held to more than the rest. It signs in one person with a
password (at least 12 characters, refused at start if shorter), compared in constant time. Wrong guesses are limited per address and overall,
and each costs time. A session is a random value whose hash alone is kept; the cookie is `HttpOnly`, `SameSite=Strict` and, over https,
`Secure` with the `__Host-` prefix. Every request that changes anything must carry a header only the app adds, and an `Origin` that is the
app's own, so another website cannot make your browser act: this does not rest on the cookie alone. Every response carries a strict content
policy (no inline script or style, nothing loaded from elsewhere, no framing), and the page puts anything that came from outside (replies,
mail, calendar titles) on screen as text, never as markup: a test fails if the script ever builds markup from a string. It stores nothing
on the phone but the app itself, and its service worker never keeps an API answer. Push messages are encrypted for the phone (RFC 8291,
checked against the RFC's own example) and are only ever sent to a push service's own addresses, so a stolen session cannot point the
server at the network behind it. It reaches the same chat engine as Telegram, so the same approvals, limits and outside-content rules
apply; an approval is answered by a token that exists only while it is waiting. What it does not have: more than one user, passkeys or a
second factor, and protection from someone who already holds your password.

## Known gaps

- **Discovery of a sign-in server is checked by name, not by where the address leads.** A public name that resolves to a
  private address, or changes its answer between the check and the request (DNS rebinding), gets one blind GET of a
  `.well-known` path before the issuer check rejects it.
- **The browser's address check is advisory.** It is made before a page is opened; a page's own requests, and a redirect to a
  private address, are not filtered request by request. Keep `browser.allow_private` off and run the browser in the
  workspace container, with the container's network limited, if the pages are not yours.
- **`permissions` on a connected service trusts its tool names.** A server that calls a sending tool `get_report` is
  believed. Use `permissions` only with servers you trust, and prefer `ask` for the rest.
- **Tool lists and schemas from a server are not pinned.** A server that changes its tools after you connected is not
  noticed. Descriptions are cut at 2,000 characters and a schema is not otherwise inspected.
- **The activity log is plain text on disk.** The agent's file tools cannot write it (the file fence), but an
  approved shell command runs as you and can append, forge or truncate it — approving one voids the log's integrity —
  as can any other person or program with your account. Rotation keeps one old file.
- **Jobs run with `allow_risky` record their calls as asked-and-approved**, the same as a person's yes.
- **No TLS on the monitoring endpoint.** Keep it on this machine or behind something that terminates TLS.
- **The model's reasoning (DeepSeek) is sent back to DeepSeek** with each request, as its API requires. It is not
  stored in history or shown.

## Reporting a problem

Open an issue on the repository for anything that is not a vulnerability. For one, do not post details in a public issue:
send them privately to the repository owner through GitHub's "Report a vulnerability" (Security tab) if it is enabled, or
by e-mail to the address on the owner's profile. Say what you did, what happened, and what you expected.
