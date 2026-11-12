package app.talaria.ui.screens

import android.content.Intent
import android.net.Uri
import android.provider.Settings
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import app.talaria.TalariaApp
import app.talaria.data.Repo
import app.talaria.phone.LinkState
import app.talaria.phone.Op
import app.talaria.phone.PhoneOps
import app.talaria.ui.components.Gap
import app.talaria.ui.components.Panel
import app.talaria.ui.components.Pill
import app.talaria.ui.components.SectionTitle
import app.talaria.ui.components.TextButton
import app.talaria.ui.components.Tone
import app.talaria.ui.theme.Ti
import java.text.DateFormat
import java.util.Date

private class Kind(val op: String, val title: String, val body: String)

private val kinds = listOf(
    Kind(Op.STATUS, "Battery and network", "Its charge, whether it is on wifi or mobile data, free space, and do-not-disturb."),
    Kind(Op.LOCATION, "Where it is", "A rough position (about a hundred metres), with how old it is. Never tracked: it is only looked up when you ask for something that needs it."),
    Kind(Op.CONTACTS, "Contacts", "Looks up one person by name when you ask, for a number or an email. It never reads the whole list."),
    Kind(Op.CALENDAR, "Calendar on this phone", "What is coming up in the next days, from the calendars on this phone."),
    Kind(Op.NOTIFICATIONS, "Notifications", "What is showing in your notifications, when you ask. These can hold private things such as codes; only turn this on if you want that."),
)

/**
 * What the agent may read on this phone. Everything starts off. A switch is the person's yes; Android's own permission
 * is the second one, asked at the moment the switch goes on and taken back by the person at any time.
 */
@Composable
fun PhoneAccessScreen(activity: AppCompatActivity, repo: Repo, onBack: () -> Unit, modifier: Modifier = Modifier) {
    BackHandler(onBack = onBack)
    val link = (activity.application as TalariaApp).phone
    val ops by link.ops.collectAsState()
    val state by link.state.collectAsState()
    val log by link.log.collectAsState()
    var resumed by remember { mutableStateOf(0) }
    DisposableEffect(activity) {
        val o = LifecycleEventObserver { _, e -> if (e == Lifecycle.Event.ON_RESUME) { resumed++; link.refresh() } }
        activity.lifecycle.addObserver(o)
        onDispose { activity.lifecycle.removeObserver(o) }
    }
    var asking by remember { mutableStateOf<String?>(null) }
    val permission = rememberLauncherForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        val op = asking
        asking = null
        // A switch that was already on (the extra step for location) has nothing more to decide.
        if (op != null && op !in ops) {
            if (PhoneOps.permitted(activity, op)) link.setOps(ops + op) else repo.toast("Android did not allow it, so it stays off.")
        }
        link.refresh()
    }
    fun turnOn(op: String) {
        if (PhoneOps.permitted(activity, op)) { link.setOps(ops + op); return }
        if (op == Op.NOTIFICATIONS) {
            // Notification access is a page of its own in Android's settings; the switch goes on when the person comes back with it granted.
            link.setOps(ops + op)
            runCatching { activity.startActivity(Intent(Settings.ACTION_NOTIFICATION_LISTENER_SETTINGS)) }
        } else { asking = op; permission.launch(PhoneOps.permissions(op).toTypedArray()) }
    }

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("Your phone", style = MaterialTheme.typography.titleLarge) },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp)) {
            item {
                Text(
                    "Talaria can look things up on this phone when you ask it something that needs them, such as \"where am I?\" or \"what is Amina's number?\". Everything is off until you turn it on. Talaria can only read: it cannot change, send or delete anything on your phone.",
                    style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 4.dp, vertical = 8.dp),
                )
                Gap(8.dp)
                Panel(Modifier.fillMaxWidth()) {
                    Row(Modifier.fillMaxWidth().heightIn(min = 64.dp).padding(horizontal = 16.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically) {
                        Column(Modifier.weight(1f)) {
                            Text("Link to your server", style = MaterialTheme.typography.titleSmall)
                            Text(
                                if (ops.isEmpty()) "Nothing is switched on, so there is no link." else "Opens while something is on, and only while this phone is signed in. Keep Notifications on in Settings so the phone stays reachable.",
                                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                        when (state) {
                            LinkState.Linked -> Pill("Linked", Tone.Ok)
                            LinkState.Connecting -> Pill("Connecting", Tone.Neutral)
                            LinkState.Off -> Pill("Off", Tone.Neutral)
                        }
                    }
                }

                SectionTitle("What Talaria may read")
                Panel(Modifier.fillMaxWidth()) {
                    Column {
                        kinds.forEachIndexed { i, k ->
                            if (i > 0) HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                            val on = k.op in ops
                            val allowed = remember(resumed, ops) { PhoneOps.permitted(activity, k.op) }
                            ToggleRow(
                                k.title,
                                k.body + if (on && !allowed) "\nWaiting for Android's permission: switch it off and on to be asked again." else "",
                                on,
                            ) { v -> if (v) turnOn(k.op) else link.setOps(ops - k.op) }
                            if (k.op == Op.LOCATION && on && allowed && !PhoneOps.backgroundLocation(activity)) {
                                Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 8.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically) {
                                    Text("Right now it can only answer while Talaria is open.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f))
                                    TextButton(onClick = { asking = Op.LOCATION; permission.launch(arrayOf(android.Manifest.permission.ACCESS_BACKGROUND_LOCATION)) }) { Text("Allow all the time") }
                                }
                            }
                        }
                    }
                }

                SectionTitle("Recently asked")
                Panel(Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(16.dp)) {
                        if (log.isEmpty()) Text("Nothing yet. When Talaria looks something up on this phone, it is written here.", style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        log.take(10).forEach { e ->
                            val title = kinds.firstOrNull { it.op == e.op }?.title ?: e.op
                            Row(Modifier.fillMaxWidth().padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                                Column(Modifier.weight(1f)) {
                                    Text(title, style = MaterialTheme.typography.bodyMedium)
                                    Text(DateFormat.getTimeInstance(DateFormat.SHORT).format(Date(e.at)) + if (e.ok) "" else " · " + e.note, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                }
                                Pill(if (e.ok) "Shared" else "Refused", if (e.ok) Tone.Ok else Tone.Neutral)
                            }
                        }
                    }
                }
                Gap(16.dp)
                Text("To take back what Android allowed, use this phone's own settings for Talaria.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 4.dp))
                Gap(32.dp)
            }
        }
    }
}
