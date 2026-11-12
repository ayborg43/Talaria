package app.talaria.ui.screens

import app.talaria.ui.theme.Ti

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import app.talaria.ui.components.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import app.talaria.ui.components.TextButton
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.data.ChatMeta
import app.talaria.data.Repo
import app.talaria.ui.ago
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.Pill
import app.talaria.ui.components.Tone
import app.talaria.ui.theme.extra
import kotlinx.coroutines.launch

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ChatsSheet(repo: Repo, onDismiss: () -> Unit) {
    val chats by repo.chats.collectAsState()
    val archived by repo.archived.collectAsState()
    var showArchived by remember { mutableStateOf(false) }
    val pinned by repo.pinned.collectAsState()
    var query by remember { mutableStateOf("") }
    val listed = remember(chats, pinned, query) { app.talaria.ui.arrangeChats(chats, pinned, query) }
    val scope = rememberCoroutineScope()
    val sheet = rememberModalBottomSheetState(skipPartiallyExpanded = true)
    var renaming by remember { mutableStateOf<ChatMeta?>(null) }
    var deleting by remember { mutableStateOf<ChatMeta?>(null) }
    var error by remember { mutableStateOf<String?>(null) }

    fun act(block: suspend () -> Unit, close: Boolean = false) {
        scope.launch {
            try { block(); if (close) { sheet.hide(); onDismiss() } } catch (e: Exception) { error = e.message }
        }
    }

    ModalBottomSheet(onDismissRequest = onDismiss, sheetState = sheet, containerColor = MaterialTheme.colorScheme.surfaceContainer) {
        Column(Modifier.navigationBarsPadding()) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 20.dp), verticalAlignment = Alignment.CenterVertically) {
                Text("Conversations", style = MaterialTheme.typography.titleLarge, modifier = Modifier.weight(1f))
                TextButton(onClick = { act({ repo.newChat() }, close = true) }) {
                    Icon(Ti.Plus, null, Modifier.size(18.dp)); Text("New chat", Modifier.padding(start = 6.dp))
                }
            }
            if (chats.size > 4) app.talaria.ui.components.OutlinedTextField(
                query, { query = it }, placeholder = { Text("Search conversations") }, singleLine = true,
                leadingIcon = { Icon(Ti.Search, null, Modifier.size(18.dp)) }, modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 4.dp), shape = MaterialTheme.shapes.extraLarge,
            )
            error?.let { Text(it, Modifier.padding(horizontal = 20.dp, vertical = 4.dp), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.extra.bad) }
            if (chats.isEmpty()) EmptyState("Nothing kept yet", "Your conversations are listed here once you have had one.")
            if (archived.isNotEmpty()) TextButton(onClick = { showArchived = !showArchived }, Modifier.padding(horizontal = 12.dp)) { Text(if (showArchived) "Hide archived" else "Archived (${archived.size})") }
            if (showArchived) archived.forEach { c ->
                Row(Modifier.fillMaxWidth().padding(horizontal = 20.dp).heightIn(min = 52.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(c.title.ifBlank { "New chat" }, Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    TextButton(onClick = { act({ repo.chatAction(mapOf("unarchive" to c.id)) }) }) { Text("Restore") }
                    IconButton(onClick = { deleting = c }) { Icon(Ti.Trash, "Delete ${c.title}", Modifier.size(20.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant) }
                }
            }
            val listState = androidx.compose.foundation.lazy.rememberLazyListState()
            LaunchedEffect(pinned) { listState.animateScrollToItem(0) }
            LazyColumn(Modifier.heightIn(max = 560.dp), state = listState, contentPadding = androidx.compose.foundation.layout.PaddingValues(horizontal = 12.dp, vertical = 8.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                items(listed, key = { it.id }) { c ->
                    var menu by remember { mutableStateOf(false) }
                    Surface(
                        shape = MaterialTheme.shapes.large,
                        color = if (c.active) MaterialTheme.extra.brassSoft else MaterialTheme.colorScheme.surfaceContainerLow,
                        border = BorderStroke(1.dp, if (c.active) MaterialTheme.extra.brassLine else MaterialTheme.colorScheme.outlineVariant),
                        modifier = Modifier.fillMaxWidth().clickable(enabled = !c.active) { act({ repo.openChat(c.id) }, close = true) },
                    ) {
                        Row(Modifier.heightIn(min = 64.dp).padding(start = 16.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f).padding(vertical = 10.dp)) {
                                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                                    if (c.id in pinned) Icon(Ti.Pin, "Pinned", Modifier.size(14.dp), tint = MaterialTheme.extra.brassText)
                                    Text(app.talaria.ui.chatTitle(c.title), style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                }
                                Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                                    Text("${c.messages} messages · ${ago(c.updated_at)}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                    if (c.agent.isNotBlank()) Pill(c.agent, Tone.Info)
                                }
                            }
                            if (c.active) Pill("Open", Tone.Brass)
                            Box {
                                IconButton(onClick = { menu = true }) { Icon(Ti.More, "More for ${c.title}") }
                                DropdownMenu(menu, onDismissRequest = { menu = false }) {
                                    DropdownMenuItem(text = { Text(if (c.id in pinned) "Unpin" else "Pin to top") }, leadingIcon = { Icon(Ti.Pin, null) }, onClick = { menu = false; repo.togglePin(c.id) })
                                    DropdownMenuItem(text = { Text("Rename") }, leadingIcon = { Icon(Ti.Edit, null) }, onClick = { menu = false; renaming = c })
                                    if (!c.main) {
                                        DropdownMenuItem(text = { Text("Archive") }, leadingIcon = { Icon(Ti.Archive, null) }, onClick = { menu = false; act({ repo.chatAction(mapOf("archive" to c.id)) }) })
                                        DropdownMenuItem(text = { Text("Delete") }, leadingIcon = { Icon(Ti.Trash, null) }, onClick = { menu = false; deleting = c })
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    }

    renaming?.let { c ->
        var title by remember(c.id) { mutableStateOf(c.title) }
        AlertDialog(
            onDismissRequest = { renaming = null }, title = { Text("Rename conversation") },
            text = { OutlinedTextField(title, { title = it }, singleLine = true, shape = MaterialTheme.shapes.medium) },
            confirmButton = { TextButton(onClick = { renaming = null; act({ repo.chatAction(mapOf("rename" to c.id, "title" to title.trim())) }) }, enabled = title.isNotBlank()) { Text("Save") } },
            dismissButton = { TextButton(onClick = { renaming = null }) { Text("Cancel") } },
        )
    }
    deleting?.let { c ->
        AlertDialog(
            onDismissRequest = { deleting = null }, title = { Text("Delete this conversation?") },
            text = { Text("“${c.title.ifBlank { "New chat" }}” and everything in it will be removed from your server. This cannot be undone.") },
            confirmButton = { TextButton(onClick = { deleting = null; act({ repo.chatAction(mapOf("delete" to c.id)) }) }) { Text("Delete", color = MaterialTheme.extra.bad) } },
            dismissButton = { TextButton(onClick = { deleting = null }) { Text("Keep it") } },
        )
    }
}
