# Testing the Android app

## Automated (no phone needed)

```bash
cd android && export JAVA_HOME=/path/to/jdk17
./gradlew :app:testDebugUnitTest     # 44 tests: voice detection, server-address rules, the API client against a mock server, Make, pins, search and quick-action helpers
./gradlew :app:lintDebug             # no errors expected
```

## On an emulator or phone

Run a gateway you can sign in to (a throwaway password is fine), install `app-debug.apk`, and use `10.0.2.2:<port>` as the server address from the Android emulator.
Checked this way: sign-in, chat and Markdown, approvals (in the app and from a notification), uploads from "+" (photos, refused types), share-in,
session expiry, recipes, routine recording and running, conversation rename / archive / restore / delete, the live browser, voice (through the debug hook),
app lock, dark mode, large text, tablet layout, Make (every kind opens: PDF, page, slides, dashboard, document), saving a file to Downloads, Files screen, profile and notes (add, edit, forget), morning brief on / time / off, conversation search, pinning, plan card, the home-screen widget (placed on the launcher; quiet and "1 waiting" states; Ask opens the keyboard, New chat, Talk and the status chip open the right place; the status refreshed by itself after the question was cleared), launcher shortcuts and the quick-settings Talk tile (both open the app on the right action).

## Real-phone checklist (cannot be done on an emulator)

1. **Microphone**: start a voice conversation in a quiet room, then with a fan or street noise. It should not start by itself, and should send after about a second of silence. Speak over a reply: it should stop talking.
2. **Alerts with the app closed**: turn on Notifications, swipe the app away, send yourself an approval. Repeat after the phone has been idle with the screen off for 30 minutes, and with battery saver on.
3. **Fingerprint / face**: turn on "Lock the app", leave for over a minute, come back.
4. **Real network**: sign in over https from outside your home network; try Wi-Fi off and on (the banner should say it cannot reach the server, then clear by itself).
5. **Takeover over mobile data**: open the browser view, scroll, type, pinch to zoom.
6. **Upload**: send a photo from the gallery and a PDF from Files; share a photo from another app.
7. **Quick access**: long-press the app icon (Talk, New chat, Approvals should be listed); add the widget and leave it for 30 minutes with the phone idle, then check it is still right; add the Talk tile and tap it from the lock screen (it should ask to unlock first); sign in with your password manager's fill, and see that it offers to save.
