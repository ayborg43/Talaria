package app.talaria.ui.screens

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.data.FileMeta
import app.talaria.ui.Artifact
import app.talaria.ui.LocalOpenFile
import app.talaria.ui.components.Button
import app.talaria.ui.components.Gap
import app.talaria.ui.components.Panel
import app.talaria.ui.components.Pill
import app.talaria.ui.components.SecondaryButton
import app.talaria.ui.components.TextButton
import app.talaria.ui.components.Tone
import app.talaria.ui.senderName
import app.talaria.ui.theme.Mono
import app.talaria.ui.theme.Ti
import app.talaria.ui.theme.extra
import java.util.Locale

/**
 * What a tool's result is, drawn as that: a mail, a plan, a document, a page. [send] puts a message in the chat as if you had
 * typed it (the buttons on a mail say what to do with it), and [openBrowser] goes to the agent's browser.
 */
@Composable
fun ArtifactCard(a: Artifact, send: (String) -> Unit, openBrowser: () -> Unit, modifier: Modifier = Modifier) {
    when (a.kind) {
        "mail_triage" -> TriageCard(a, send, modifier)
        "mail_list" -> MailListCard(a, send, modifier)
        "mail" -> MailCard(a, send, modifier)
        "page" -> PageCard(a, openBrowser, modifier)
        "plan" -> PlanCard(a, modifier)
        "spending" -> SpendingCard(a, send, modifier)
        "document" -> DocumentCard(a, modifier)
    }
}

@Composable
private fun Avatar(name: String, index: Int) {
    val x = MaterialTheme.extra
    val tints = listOf(x.infoTint to x.infoInk, x.okTint to x.ok, x.brassTint to x.brassText, x.badTint to x.bad)
    val (bg, fg) = tints[index % tints.size]
    Box(Modifier.size(36.dp).clip(CircleShape).background(bg), contentAlignment = Alignment.Center) { Text((name.trim().firstOrNull() ?: '?').uppercase(), style = MaterialTheme.typography.labelLarge, color = fg) }
}

@Composable
private fun MailRow(r: Artifact, i: Int, onClick: () -> Unit, actions: @Composable (() -> Unit)? = null) {
    val name = senderName(r.str("from"))
    Column(Modifier.fillMaxWidth().clickable(onClick = onClick).padding(horizontal = 14.dp, vertical = 10.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Avatar(name, i)
            Column(Modifier.weight(1f)) {
                Text("$name · ${r.str("subject").ifBlank { "(no subject)" }}", style = MaterialTheme.typography.titleSmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
                val sub = r.str("why").ifBlank { r.str("when") }
                if (sub.isNotBlank()) Text(sub, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
            if (r.bool("unread")) Pill("New", Tone.Info)
        }
        actions?.invoke()
    }
}

@Composable
private fun TriageCard(a: Artifact, send: (String) -> Unit, modifier: Modifier) {
    val needs = a.list("needs"); val wait = a.list("wait")
    if (needs.isEmpty() && wait.isEmpty()) return
    var showWait by remember { mutableStateOf(false) }
    Panel(modifier.fillMaxWidth().animateContentSize()) {
        Column {
            Row(Modifier.padding(start = 14.dp, end = 14.dp, top = 14.dp, bottom = 4.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Icon(Ti.Mail, null, Modifier.size(18.dp), tint = MaterialTheme.extra.brassText)
                Text("Needs you", style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                Pill(needs.size.toString(), if (needs.isEmpty()) Tone.Ok else Tone.Brass)
            }
            if (needs.isEmpty()) Text("Nothing needs you.", Modifier.padding(14.dp), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            needs.take(10).forEachIndexed { i, r ->
                MailRow(r, i, onClick = { send("Read message ${r.str("id")} and tell me what it says.") }) {
                    Row(Modifier.padding(start = 48.dp)) {
                        TextButton(onClick = { send("Handle message ${r.str("id")}: read it, work out what it needs from me, and do what you can (draft a reply, add to my calendar). Ask me before anything is sent.") }) { Text("Start") }
                        TextButton(onClick = { send("Draft a reply to message ${r.str("id")} and save it in my Drafts. Do not send it.") }) { Text("Reply") }
                    }
                }
            }
            if (needs.size > 10) Text("+${needs.size - 10} more.", Modifier.padding(start = 14.dp, bottom = 8.dp), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            if (wait.isNotEmpty()) {
                TextButton(onClick = { showWait = !showWait }, Modifier.padding(horizontal = 6.dp)) { Text(if (showWait) "Hide what can wait" else "Can wait (${wait.size})") }
                if (showWait) wait.take(10).forEachIndexed { i, r -> MailRow(r, i + 2, onClick = { send("Read message ${r.str("id")} and tell me what it says.") }) }
            }
            Gap(6.dp)
        }
    }
}

@Composable
private fun MailListCard(a: Artifact, send: (String) -> Unit, modifier: Modifier) {
    val items = a.list("items"); if (items.isEmpty()) return
    Panel(modifier.fillMaxWidth()) {
        Column(Modifier.padding(vertical = 4.dp)) {
            items.take(8).forEachIndexed { i, r -> MailRow(r, i, onClick = { send("Read message ${r.str("id")} and tell me what it says.") }) }
            if (items.size > 8) Text("+${items.size - 8} more in the full result.", Modifier.padding(start = 14.dp, bottom = 8.dp, top = 2.dp), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

@Composable
private fun MailCard(a: Artifact, send: (String) -> Unit, modifier: Modifier) {
    if (a.str("from").isBlank() && a.str("subject").isBlank()) return
    Panel(modifier.fillMaxWidth()) {
        Column(Modifier.padding(14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                Avatar(senderName(a.str("from")), 0)
                Column {
                    Text(a.str("subject").ifBlank { "(no subject)" }, style = MaterialTheme.typography.titleSmall)
                    Text(a.str("from"), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            if (a.str("excerpt").isNotBlank()) { Gap(10.dp); Text(a.str("excerpt"), style = MaterialTheme.typography.bodyMedium) }
            val att = a.strings("attachments")
            if (att.isNotEmpty()) { Gap(8.dp); Text("Attached: ${att.joinToString(", ")}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
            if (a.str("id").isNotBlank()) Row(Modifier.padding(top = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                SecondaryButton(onClick = { send("Draft a reply to message ${a.str("id")} and show it to me before anything is sent.") }) { Text("Draft a reply") }
                TextButton(onClick = { send("Show me the whole conversation of message ${a.str("id")}.") }) { Text("Whole thread") }
            }
        }
    }
}

@Composable
private fun PageCard(a: Artifact, openBrowser: () -> Unit, modifier: Modifier) {
    val url = a.str("url"); if (url.isBlank()) return
    val host = runCatching { java.net.URI(url).host }.getOrNull() ?: url
    Panel(modifier.fillMaxWidth()) {
        Row(Modifier.padding(14.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Icon(Ti.Globe, null, Modifier.size(22.dp), tint = MaterialTheme.extra.info)
            Column(Modifier.weight(1f)) {
                Text(a.str("title").ifBlank { host }, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(url, style = MaterialTheme.typography.bodySmall.copy(fontFamily = Mono), color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            SecondaryButton(onClick = openBrowser) { Text("Take control") }
        }
    }
}

@Composable
private fun PlanCard(a: Artifact, modifier: Modifier) {
    val title = a.str("title"); if (title.isBlank()) return
    val steps = a.list("steps"); val done = steps.count { it.bool("done") }; val status = a.str("status").ifBlank { "open" }
    Panel(modifier.fillMaxWidth()) {
        Column(Modifier.padding(14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                Icon(Ti.Target, null, Modifier.size(20.dp), tint = MaterialTheme.extra.brassText)
                Column(Modifier.weight(1f)) {
                    Text(title, style = MaterialTheme.typography.titleSmall)
                    Text(if (steps.isNotEmpty()) "$done of ${steps.size} steps done" else status, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                Pill(status.replaceFirstChar { it.uppercase() }, if (status == "open") Tone.Ok else Tone.Brass)
            }
            if (steps.isNotEmpty()) {
                Gap(10.dp)
                Box(Modifier.fillMaxWidth().height(6.dp).clip(CircleShape).background(MaterialTheme.colorScheme.surfaceContainerHigh)) {
                    Box(Modifier.fillMaxHeight().fillMaxWidth(done.toFloat() / steps.size).clip(CircleShape).background(MaterialTheme.colorScheme.primary))
                }
                Gap(10.dp)
                steps.forEach { s ->
                    Row(Modifier.padding(vertical = 3.dp), verticalAlignment = Alignment.Top, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                        Box(Modifier.padding(top = 2.dp).size(18.dp).clip(CircleShape).background(if (s.bool("done")) MaterialTheme.extra.ok else MaterialTheme.colorScheme.surfaceContainerHigh), contentAlignment = Alignment.Center) {
                            if (s.bool("done")) Icon(Ti.Check, null, Modifier.size(12.dp), tint = Color.White)
                        }
                        Text(s.str("text"), style = MaterialTheme.typography.bodyMedium, color = if (s.bool("done")) MaterialTheme.colorScheme.onSurfaceVariant else MaterialTheme.colorScheme.onSurface)
                    }
                }
            }
            if (a.str("next").isNotBlank()) { Gap(8.dp); Text("Next: ${a.str("next")}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
        }
    }
}

private fun money(n: Double) = String.format(Locale.getDefault(), "%,.2f", n)

@Composable
private fun SpendingCard(a: Artifact, send: (String) -> Unit, modifier: Modifier) {
    if (a.str("spending").isBlank()) return
    val cats = a.list("categories"); val top = cats.maxOfOrNull { it.num("amount") }?.coerceAtLeast(1.0) ?: 1.0
    val x = MaterialTheme.extra
    Panel(modifier.fillMaxWidth()) {
        Column(Modifier.padding(14.dp)) {
            Text("Spending summary", style = MaterialTheme.typography.titleSmall)
            Text("${a.str("count")} transactions · ${a.str("from")} to ${a.str("to")}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Gap(12.dp)
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                listOf("Spent" to a.num("spending") to MaterialTheme.colorScheme.onSurface, "Income" to a.num("income") to MaterialTheme.colorScheme.onSurface, "Saved" to a.num("saved") to (if (a.num("saved") >= 0) x.ok else x.bad)).forEach { (pair, color) ->
                    Column(Modifier.weight(1f).clip(MaterialTheme.shapes.medium).background(MaterialTheme.colorScheme.surfaceContainerHigh).padding(10.dp)) {
                        Text(pair.first, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        Text(money(pair.second), style = MaterialTheme.typography.titleSmall, color = color, maxLines = 1)
                    }
                }
            }
            Gap(12.dp)
            cats.take(8).forEach { c ->
                Row(Modifier.padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    Text(c.str("name"), Modifier.weight(0.9f), style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Box(Modifier.weight(1.1f).height(6.dp).clip(CircleShape).background(MaterialTheme.colorScheme.surfaceContainerHigh)) {
                        Box(Modifier.fillMaxHeight().fillMaxWidth((c.num("amount") / top).toFloat().coerceIn(0.02f, 1f)).clip(CircleShape).background(MaterialTheme.colorScheme.primary))
                    }
                    Text(money(c.num("amount")), style = MaterialTheme.typography.labelSmall.copy(fontFamily = Mono), maxLines = 1)
                }
            }
            Gap(8.dp)
            SecondaryButton(onClick = { send("Set up a goal to save a sensible amount a month, using the spending summary above: say which categories to cut and by how much, and check in on it.") }) { Text("Set a savings goal") }
        }
    }
}

@Composable
private fun DocumentCard(a: Artifact, modifier: Modifier) {
    val id = a.str("id"); if (id.isBlank()) return
    val open = LocalOpenFile.current
    val mime = a.str("mime")
    val meta = FileMeta(id = id, name = a.str("name").ifBlank { "Document" }, mime = mime.ifBlank { "application/pdf" }, size = a.num("size").toLong())
    Panel(modifier.fillMaxWidth().clip(MaterialTheme.shapes.large).clickable { open(meta) }) {
        Row(Modifier.padding(14.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Icon(Ti.File, null, Modifier.size(22.dp), tint = MaterialTheme.extra.brassText)
            Column(Modifier.weight(1f)) {
                Text(meta.name, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(if (meta.size > 0) app.talaria.ui.fileSize(meta.size) else "Kept on your server", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            Button(onClick = { open(meta) }, contentPadding = androidx.compose.foundation.layout.PaddingValues(horizontal = 18.dp, vertical = 8.dp)) { Text("View") }
        }
    }
}
