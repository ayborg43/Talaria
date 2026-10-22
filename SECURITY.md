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
- **The activity log is plain text on disk.** The agent cannot write it (the file fence), but a person or program
  with your account can, and rotation keeps one old file.
- **Jobs run with `allow_risky` record their calls as asked-and-approved**, the same as a person's yes.
- **No TLS on the monitoring endpoint.** Keep it on this machine or behind something that terminates TLS.
- **The model's reasoning (DeepSeek) is sent back to DeepSeek** with each request, as its API requires. It is not
  stored in history or shown.

## Reporting a problem

Open an issue on the repository for anything that is not a vulnerability. For one, do not post details in a public issue:
send them privately to the repository owner through GitHub's "Report a vulnerability" (Security tab) if it is enabled, or
by e-mail to the address on the owner's profile. Say what you did, what happened, and what you expected.
