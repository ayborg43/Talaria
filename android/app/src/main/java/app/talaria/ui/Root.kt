package app.talaria.ui

import app.talaria.ui.theme.Ti

import androidx.activity.compose.BackHandler
import androidx.compose.animation.togetherWith
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.fadeOut
import androidx.compose.animation.fadeIn
import androidx.compose.animation.core.tween
import androidx.compose.animation.AnimatedContent
import androidx.appcompat.app.AppCompatActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.isImeVisible
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Button
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.NavigationBarItemDefaults
import androidx.compose.material3.NavigationRail
import androidx.compose.material3.NavigationRailItem
import androidx.compose.material3.NavigationRailItemDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import app.talaria.Shared
import app.talaria.data.Conn
import app.talaria.data.Repo
import app.talaria.ui.components.Gap
import app.talaria.ui.components.TalariaMark
import app.talaria.ui.screens.ChatScreen
import app.talaria.ui.screens.ChatsSheet
import app.talaria.ui.screens.DocViewer
import app.talaria.ui.screens.FilesScreen
import app.talaria.ui.screens.MakeScreen
import app.talaria.ui.screens.ConnectScreen
import app.talaria.ui.screens.SettingsScreen
import app.talaria.ui.screens.TasksHost
import app.talaria.ui.screens.TodayScreen
import app.talaria.ui.components.VoicePanel
import app.talaria.voice.Phase
import app.talaria.voice.VoiceController

private enum class Tab(val label: String, val icon: androidx.compose.ui.graphics.vector.ImageVector) {
    Chat("Chat", Ti.Chat),
    Today("Today", Ti.Calendar),
    Make("Make", Ti.Sparkles),
    Tasks("Tasks", Ti.Agents),
    Settings("Settings", Ti.Sliders),
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun Root(
    activity: AppCompatActivity, repo: Repo, voice: VoiceController,
    locked: Boolean, onUnlocked: () -> Unit, shared: Shared?, onSharedUsed: () -> Unit,
    onNeedMic: () -> Unit, onAskNotifications: () -> Unit,
    onSaveFile: (String, String, ByteArray) -> Unit,
    quick: app.talaria.QuickAction?, onQuickHandled: () -> Unit,
    onAttach: () -> Unit, onUploadShared: (android.net.Uri) -> Unit, uploadError: String?, onUploadErrorShown: () -> Unit,
) {
    val conn by repo.conn.collectAsState()
    val needsMic by voice.needsMic.collectAsState()
    LaunchedEffect(needsMic) { if (needsMic) onNeedMic() }
    // Alerts need the notification permission on Android 13 and later: ask once, right after the first sign-in.
    LaunchedEffect(conn is Conn.Online) {
        if (conn is Conn.Online && repo.store.notifications && !repo.store.askedNotifications) { repo.store.askedNotifications = true; onAskNotifications() }
    }

    if (conn is Conn.SignedOut) { Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background)) { ConnectScreen(repo, notice = if (repo.sessionEnded.collectAsState().value) "Your session ended. Sign in again." else null) }; return }
    if (locked) { Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background)) { LockedScreen(activity, onUnlocked) }; return }

    var tab by rememberSaveable { mutableIntStateOf(0) }
    var draft by rememberSaveable { mutableStateOf("") }
    var chats by remember { mutableStateOf(false) }
    var browser by remember { mutableStateOf(false) }
    var askFocus by remember { mutableStateOf(0) }
    var approvalJump by remember { mutableStateOf(0) }
    var viewing by remember { mutableStateOf<app.talaria.data.FileMeta?>(null) }
    var allFiles by remember { mutableStateOf(false) }
    val wide = LocalConfiguration.current.screenWidthDp >= 600
    val workers by repo.workers.collectAsState()
    val busy = workers.count { it.kind != "main" }
    val messages by repo.messages.collectAsState()
    val waiting = messages.count { it.role == "approval" && it.state == "pending" }
    val ime = WindowInsets.isImeVisible

    // A request from outside the app (a shortcut, the tile, the widget, a notification) is carried out once the app is open and unlocked.
    LaunchedEffect(quick) {
        val q = quick ?: return@LaunchedEffect
        tab = 0; browser = false; viewing = null; allFiles = false; chats = false
        when (q) {
            app.talaria.QuickAction.NewChat -> runCatching { repo.newChat() }.onSuccess { askFocus++ }
            app.talaria.QuickAction.Talk -> if (voice.phase.value == app.talaria.voice.Phase.Off) voice.startConversation()
            app.talaria.QuickAction.Ask -> askFocus++
            app.talaria.QuickAction.Approvals -> approvalJump++
        }
        onQuickHandled()
    }
    LaunchedEffect(shared) {
        if (shared?.text != null) { draft = (draft + " " + shared.text).trim(); tab = 0; onSharedUsed() }
        else if (shared?.stream != null) { tab = 0; onUploadShared(shared.stream); onSharedUsed() }
    }
    BackHandler(enabled = tab != 0 && !browser) { tab = 0 }

    if (browser) {
        Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background)) { app.talaria.ui.screens.BrowserScreen(repo, onBack = { browser = false }) }
        return
    }

    val content: @Composable (Modifier) -> Unit = { mod ->
        AnimatedContent(
            tab, label = "tab",
            transitionSpec = { (fadeIn(tween(220, delayMillis = 70)) + slideInVertically(tween(260, delayMillis = 70)) { it / 40 }) togetherWith fadeOut(tween(90)) },
        ) { current ->
        when (Tab.entries[current]) {
            Tab.Chat -> Column(mod) {
                Box(Modifier.weight(1f)) {
                    ChatScreen(repo, voice, draft, { draft = it }, onOpenChats = { chats = true }, onOpenBrowser = { browser = true }, onAttach = onAttach, externalError = uploadError, onExternalErrorShown = onUploadErrorShown, focusSignal = askFocus, approvalSignal = approvalJump)
                }
            }
            Tab.Today -> TodayScreen(repo, mod, onAsk = { draft = it; tab = 0 }, onChat = { tab = 0 }, onAllFiles = { allFiles = true })
            Tab.Make -> MakeScreen(repo, mod, onStart = { draft = it; tab = 0 }, onAllFiles = { allFiles = true })
            Tab.Tasks -> TasksHost(repo, mod, onOpenBrowser = { browser = true }, onChat = { tab = 0 })
            Tab.Settings -> SettingsScreen(activity, repo, voice, mod, onAskNotifications = onAskNotifications)
        }
        }
    }

    val phase by voice.phase.collectAsState()
    val snackbar = remember { androidx.compose.material3.SnackbarHostState() }
    LaunchedEffect(Unit) { repo.toasts.collect { snackbar.showSnackbar(it) } }
    androidx.compose.runtime.CompositionLocalProvider(app.talaria.ui.LocalOpenFile provides { f -> viewing = f }) {
    Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background)) {
        if (wide) {
            Row(Modifier.fillMaxSize().statusBarsPadding()) {
                NavigationRail(containerColor = MaterialTheme.colorScheme.surfaceContainer, modifier = Modifier.navigationBarsPadding()) {
                    Gap(12.dp)
                    TalariaMark(40.dp)
                    Gap(16.dp)
                    Tab.entries.forEachIndexed { i, t -> railItem(t, i == tab, if (t == Tab.Tasks) busy else if (t == Tab.Chat && i != tab) waiting else 0) { tab = i } }
                }
                content(Modifier.weight(1f).navigationBarsPadding().imePadding())
            }
        } else {
            Scaffold(
                containerColor = MaterialTheme.colorScheme.background,
                bottomBar = {
                    if (!ime) NavigationBar(containerColor = MaterialTheme.colorScheme.surfaceContainer, tonalElevation = 0.dp) {
                        Tab.entries.forEachIndexed { i, t -> barItem(t, i == tab, if (t == Tab.Tasks) busy else if (t == Tab.Chat && i != tab) waiting else 0) { tab = i } }
                    }
                },
            ) { inner ->
                content(Modifier.padding(inner).consumeWindowInsets(inner).imePadding())
            }
        }
        if (chats) ChatsSheet(repo, onDismiss = { chats = false })
        androidx.compose.material3.SnackbarHost(snackbar, Modifier.align(Alignment.BottomCenter).padding(bottom = if (wide) 24.dp else 96.dp).padding(horizontal = 12.dp))
        androidx.compose.animation.AnimatedVisibility(allFiles, enter = androidx.compose.animation.fadeIn(), exit = androidx.compose.animation.fadeOut()) {
            Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background).statusBarsPadding().navigationBarsPadding()) { FilesScreen(repo, onBack = { allFiles = false }) }
        }
        viewing?.let { f -> Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background)) { DocViewer(repo, f, onBack = { viewing = null }, onSave = onSaveFile) } }
    }
    }
}

@Composable
private fun RowScopeBar() {}

@Composable
private fun androidx.compose.foundation.layout.RowScope.barItem(t: Tab, selected: Boolean, badge: Int, onClick: () -> Unit) {
    NavigationBarItem(
        selected = selected, onClick = onClick,
        icon = {
            BadgedBox(badge = { if (badge > 0) Badge(containerColor = MaterialTheme.colorScheme.primary, contentColor = MaterialTheme.colorScheme.onPrimary) { Text("$badge") } }) {
                Icon(t.icon, null)
            }
        },
        label = { Text(t.label, style = MaterialTheme.typography.labelMedium) },
        colors = NavigationBarItemDefaults.colors(
            selectedIconColor = MaterialTheme.colorScheme.onPrimaryContainer, selectedTextColor = MaterialTheme.colorScheme.onSurface,
            indicatorColor = MaterialTheme.colorScheme.primaryContainer, unselectedIconColor = MaterialTheme.colorScheme.onSurfaceVariant,
            unselectedTextColor = MaterialTheme.colorScheme.onSurfaceVariant,
        ),
    )
}

@Composable
private fun railItem(t: Tab, selected: Boolean, badge: Int, onClick: () -> Unit) {
    NavigationRailItem(
        selected = selected, onClick = onClick,
        icon = {
            BadgedBox(badge = { if (badge > 0) Badge(containerColor = MaterialTheme.colorScheme.primary, contentColor = MaterialTheme.colorScheme.onPrimary) { Text("$badge") } }) {
                Icon(t.icon, null)
            }
        },
        label = { Text(t.label, style = MaterialTheme.typography.labelMedium) },
        colors = NavigationRailItemDefaults.colors(
            selectedIconColor = MaterialTheme.colorScheme.onPrimaryContainer, indicatorColor = MaterialTheme.colorScheme.primaryContainer,
        ),
    )
}

@Composable
private fun LockedScreen(activity: AppCompatActivity, onUnlocked: () -> Unit) {
    var note by remember { mutableStateOf("") }
    fun ask() = Lock.prompt(activity, "Unlock Talaria", "Confirm it is you", onUnlocked) { note = it }
    LaunchedEffect(Unit) { ask() }
    Box(Modifier.fillMaxSize().safeDrawingPadding(), contentAlignment = Alignment.Center) {
        Column(Modifier.padding(32.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            TalariaMark(64.dp)
            Gap(20.dp)
            Text("Talaria is locked", style = MaterialTheme.typography.headlineSmall)
            Gap(8.dp)
            Text(if (note.isBlank()) "Confirm it is you to carry on." else note, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
            Gap(20.dp)
            Button(onClick = { ask() }, shape = MaterialTheme.shapes.large) { Text("Unlock") }
        }
    }
}
