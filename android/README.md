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
| AI models | Settings -> AI models: add a key for any service from a preset (OpenAI, Anthropic, DeepSeek, Meta Muse, Itera Compute, NVIDIA, OpenRouter, Ollama and more), see which models work and which are resting and why, test one, list what a key can use, write what each model is for (the agent reads it), and set the order of what to try when a model stops working. The server does the switching; the phone only edits the settings. |
| Phone tools | Optional, off by default: the agent can look things up on this phone when you ask it something that needs them (battery and network, a rough location, one contact by name, the phone's calendar, what is in your notifications). One switch each in Settings -> Your phone, Android's own permission asked when it goes on, a list of what was asked and when, and nothing that can change anything on the phone. Needs the app connected to the server; location needs "all the time" to answer with the app closed. |
| Voice | Hands-free conversation. By default Android's own speech recognizer hears you (on the phone, nothing leaves it, no server setup needed), shows words as you speak, and sends when you stop; then it reads the reply aloud with the phone's voice and listens again. It does not listen while a reply is being read, so tap to stop one. Settings -> Voice can switch to the speech service on your server instead (which does let you talk over a reply), and "Keep speech on this phone" (on by default) refuses Android's online recognizer. If the phone's offline language pack is missing, Android offers to download it. |
| Tasks | What is working now, run activity (steps, files, result), steering and stopping a running task, following up a finished one, jobs (pause, resume, remove), suggestions, recipes, routines, named agents (create, edit, chat). |
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
| Quick access | Home-screen widget (4×2, resizable: shows what is running or waiting, an Ask box, New chat and Talk buttons; tapping the status goes straight to the question), a Talk tile for quick settings, long-press shortcuts on the app icon (Talk, New chat, Approvals), and the phone's password manager can fill and save the sign-in. The widget refreshes when the app sees a change and about every 30 minutes otherwise. |

## Build

Needs the Android SDK (platform 36), JDK 17 and network access for the first Gradle run.

```bash
cd android
export JAVA_HOME=/path/to/jdk17
./gradlew :app:assembleDebug        # app/build/outputs/apk/debug/app-debug.apk
./gradlew :app:assembleRelease      # minified, about 2.6 MB; signed with the debug key until you add your own
./gradlew :app:testDebugUnitTest    # 51 tests: voice detection, server-address rules, the API client, Make, search, quick-action and alert helpers
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
- Phone tools are read-only by design: nothing that sends a message, places a call or changes data on the phone is built (SMS and call logs are not offered at all).
- Tool results that carry a typed record (mail lists, plans) are shown as text, not as the web app's cards.
