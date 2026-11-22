# Talaria for Android

A native Android client for a Talaria server (the Go gateway in this repository). The agent, its memory, its browser and
its schedules all stay on your server; the phone is the screen, the microphone, and the place alerts arrive.

It talks to the same HTTP API as the web app (`internal/web`): a session cookie, plus the `X-Talaria` header and an
`Origin` equal to the server's address on anything that changes something. No server changes are needed.

## What it does

| Area | What works |
|---|---|
| Sign in | Server address (https required unless it is on your own network or VPN), password, session kept sealed with an Android Keystore key. |
| Chat | Live conversation over the event stream, Markdown (headings, lists, tables, code, links), tool cards, approval cards (Allow / Always / Deny), several conversations (open, rename, archive, delete), suggestions, share-to-Talaria from any app. |
| Alerts | No Google services and no extra servers. The app keeps one live connection to your server (a small foreground service whose notice tells you if the link is up), reconnects the moment the network returns, and starts again after a restart. If Android stops it, a quarter-hourly check starts it again and looks for anything waiting for you. Approvals arrive with Allow and Deny buttons (not offered while the app lock is on), several fold into one stack, replies can be answered from the notification, replies can be silent during quiet hours, and a notice is removed as soon as you answer anywhere. Settings shows the link, and asks Android to let it run in the background. |
| Servers | Sign in to more than one Talaria and switch between them. The one in use sends alerts live; the others are checked every quarter hour for questions waiting for you (tapping that notice switches to that server). |
| AI models | Settings -> AI models: add a key for any service from a preset (OpenAI, Anthropic, DeepSeek, Meta Muse, Itera Compute, NVIDIA, OpenRouter, Ollama and more), see which models work and which are resting and why, test one, list what a key can use, write what each model is for (the agent reads it), and set the order of what to try when a model stops working. The server does the switching; the phone only edits the settings. Under each reply the app names the model that wrote it and why, with thumbs and "Try a stronger model"; the same screen turns smart routing and learning on and off and shows what has been learned. |
| Phone tools | Optional, off by default: the agent can look things up on this phone when you ask it something that needs them (battery and network, a rough location, one contact by name, the phone's calendar, what is in your notifications). One switch each in Settings -> Your phone, Android's own permission asked when it goes on, a list of what was asked and when, and nothing that can change anything on the phone. Needs the app connected to the server; location needs "all the time" to answer with the app closed. |
| Phone actions | Optional, off by default, one switch each in Settings -> Your phone: the agent can set an alarm, start a timer, prepare a calendar reminder, open the maps app at a place, or open the messaging app with a text already written. Each time, the server shows you exactly what it will do and waits for your yes (no "always allow"). Nothing is ever sent, called or deleted: a text is only written out and you press send; a reminder is only filled in and you press Save. The app has no SMS, phone or calendar-write permission. Android 10+ stops an app opening another one from the background, so while Talaria is open the screen opens at once (alarms and timers are set quietly by the clock app), and otherwise a notification is posted and one tap on it does it. |
| Quick capture | **Take a photo** from the + menu, the widget's camera button, or the "Ask about a photo" shortcut: it goes to the agent as an attachment (not kept in your gallery) and the message box asks "What is in this photo?". A screenshot is shared from Android's own screenshot tool through the share sheet. |
| Offline | A message written while the server cannot be reached is kept on the phone, shown as waiting, and sent when the connection is back, once (each carries an id the server checks). Photos and files need a connection. |
| Events | Optional: when the battery drops to a level, the charger is plugged in or out, the phone joins a Wi-Fi network or connects to a Bluetooth device, Talaria can do something you asked (it asks you first). Switch on and manage in Settings -> Your phone. |
| Your files | Settings -> Search my files and notes: what Talaria keeps and notes are searchable by the agent; add folders on your server. |
| Test questions | Settings -> AI models -> Your test questions: write questions and what a good answer contains, run them on any model, see the score. |
| Voice | Hands-free conversation. By default Android's own speech recognizer hears you (on the phone, nothing leaves it, no server setup needed), shows words as you speak, and sends when you stop; then it reads the reply aloud with the phone's voice and listens again. It does not listen while a reply is being read, so tap to stop one. Settings -> Voice can switch to the speech service on your server instead (which does let you talk over a reply), and "Keep speech on this phone" (on by default) refuses Android's online recognizer. If the phone's offline language pack is missing, Android offers to download it. |
| Tasks | What is working now, run activity (steps, files, result), steering and stopping a running task, following up a finished one, the board (cards in Backlog, Ready, Running, Review and Done; a Ready card with an agent starts as a task, with Stop and Watch while it runs), jobs (pause, resume, remove), suggestions, recipes, routines, named agents (create, edit, chat). |
| Agents as contacts | The conversation list starts with your named agents as a contacts list (icon, name, what it is for from its instructions, and a small dot while a card given to it is running on the board); tapping one starts a conversation with it, as "Chat" does. In the message box, typing `@` offers agent names (up to eight, matched from the start of the name); choosing one writes `@name `. The message is sent as typed. |
| Today | Calendar, mail that needs you, files the agent made (opened with the phone's own apps). |
| Browser | The agent's browser, live: tap, scroll, pinch-zoom, type, adaptive sign-in boxes, record a routine. |
| Settings | Pause everything, what it asks first, speech service, notifications, app lock (fingerprint, face or PIN), light / dark / match phone. |
| Layout | Bottom navigation on phones, navigation rail on wide screens; light and dark; follows the system font size. |
| Look | One family of thin line icons (the web app's own for the tabs), borderless tonal surfaces, a floating composer, spring press feedback, haptics on send and approve, loading skeletons, a system splash screen, a glowing voice orb. |
| Files | Attach a photo or PDF from the composer, or share one to Talaria from another app. A Files screen lists everything the server keeps; PDFs, pictures and pages open inside the app (pages with scripts, network and file access off), and can be saved to the phone or shared. |
| Make | What Talaria made (documents, pages, slides, dashboards, PDFs) with filters and expiry times, and quick starters that fill the message box. |
| You | Profile (name, tone, icon) and the notes it keeps about you and for itself, editable; the morning brief on or off, with its time. |
| Finding things | Search inside a conversation, search the conversation list, pin conversations (pins are kept on this phone). |
| Result cards | Mail, mail triage, plans, spending summaries, pages and documents drawn as what they are, with buttons that send the follow-up for you. |
| Cost and digest | Settings -> "What the models cost": Today / Week / Month, the server's own summary sentence, tokens per day as small bars, rows by model, kind of work, agent and who asked (requests, tokens in and out, dollars where a price is set, else "no price"), and a box pair per model to set your own price (dollars per million tokens; both empty removes it). Settings -> "While you were away" (also a card on Today): the server's plain digest for the last 24 hours or 7 days, and the schedule (on or off, time, every day or every Monday). The scheduled one arrives as an ordinary reply, with the usual reply notification. |
| Old conversations | Settings -> "Old conversations": choose how long a conversation can go untouched before it is archived (Forever by default, 30 days, 90 days, 6 months, a year; other values set elsewhere show as "N days"), and where the server allows it, how old the searchable history may get before it is forgotten (with a warning). Shows what would happen if it ran now, what the last tidy did, and a "Tidy now" button behind a confirmation. Nothing is removed unless a time is chosen; the main conversation, the open one, channels, conversations with a named agent and anything you archived yourself are never removed. |
| Agent files | Tasks -> Up next -> "Import an agent": choose a file or paste, read the instructions in full, "Looks only" switch on by default, Add (a name that already exists is refused). Sharing an `*.agent.md` file (or text that starts with the agent header) to Talaria opens the same screen; any other shared text, picture or PDF still goes to the chat as before. Each agent has a Share button that sends its file through Android's share sheet. |
| Task progress | While a background task runs and the app is not on screen, one quiet notice per task (the newest three) says what it is doing now, with a Stop button; it changes at most every 5 seconds and is gone when the task ends. It rides on the link the app already keeps (no new service), is not shown in quiet hours or without notification permission, and has no Stop button while the app lock is on. |
| Assistant | Settings -> "Ask from anywhere": choose Talaria as the phone's Default digital assistant app and the power-button long-press or assist gesture opens the message box (or, with the switch on, the hands-free voice conversation). With "Use screenshot" turned on in that Android setting, the screen is attached like a photo and the box says "What is on my screen?" for you to edit and send; nothing is sent for you. The assistant services do nothing until Android binds them. |
| Clipboard | A Quick Settings tile ("Ask clipboard") and a long-press shortcut open a message "About this text: ..." (first 4,000 characters, text only, nothing Android marks as sensitive) for you to edit and send. The clipboard is read once, only after the tap. |
| Quick access | Home-screen widget (4×2, resizable: shows what is running or waiting, an Ask box, New chat and Talk buttons; tapping the status goes straight to the question), a second 3×1 widget with the board's Ready, Running and Review counts (a tap opens Tasks on the Board tab), a Talk tile for quick settings, long-press shortcuts on the app icon (Talk, New chat, Approvals), and the phone's password manager can fill and save the sign-in. Both widgets refresh when the app sees a change and about every 30 minutes otherwise (nothing runs in between); with the server out of reach they keep what they last showed, and without a board they say so. |

## Build

Needs the Android SDK (platform 36), JDK 17 and network access for the first Gradle run.

```bash
cd android
export JAVA_HOME=/path/to/jdk17
./gradlew :app:assembleDebug        # app/build/outputs/apk/debug/app-debug.apk
./gradlew :app:assembleRelease      # minified, about 2.6 MB; signed with the debug key until you add your own
./gradlew :app:testDebugUnitTest    # 134 tests: voice detection, server-address rules, the API client, Make, search, quick-action and alert helpers, phone actions, @-mentions, the roster, the board widget, cost and digest wording, price parsing, bar scaling, agent-file share detection, clipboard rules, task-notice rules
```

To publish, make your own key and describe it in `android/keystore.properties` (never commit either):

```bash
keytool -genkeypair -v -keystore talaria.jks -alias talaria -keyalg RSA -keysize 2048 -validity 10000
# keystore.properties
storeFile=talaria.jks
storePassword=...
keyAlias=talaria
keyPassword=...
```

See `TESTING.md` for what has been checked and a checklist for a real phone.

A debug build also contains a test hook (`src/debug`) that feeds the voice pipeline a recording; release builds do not.

## Layout

```
app/src/main/java/app/talaria/
  data/      Api (HTTP, SSE, WebSocket), Repo (live state), SessionStore (Keystore), BrowserSession, Servers, Models
  service/   Notifier, NotificationActions, ConnectionService (foreground service that keeps alerts coming)
  voice/     VoiceController (conversation), Vad (when someone starts and stops talking), Wav
  ui/        theme (the web app's palette and fonts, Icons.kt), components, screens
```

## Known limits

- Alerts while the app is closed rely on a foreground service holding the event stream open (there is no Firebase
  project). Android may stop it under strict battery saving; the notice it shows can be turned off in Settings.
- The takeover shows pictures of the agent's browser, as the web app does: no file upload or download, and no copy out
  of the page.
- Phone reads are read-only. Phone actions only set an alarm or timer, or open another app with something filled in: nothing that sends a message, places a call or deletes anything on the phone is built (SMS and call logs are not offered at all). The clock app does not report back, so an alarm or timer is "asked for", not confirmed. Android does not let an app open another from the background, so with Talaria closed a notification must be tapped first.
- Tool results that carry a typed record (mail lists, plans) are shown as text, not as the web app's cards.
