package app.talaria.ui.screens

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.data.FileMeta
import app.talaria.data.FilesDto
import app.talaria.data.Repo
import app.talaria.ui.FileOpen
import app.talaria.ui.LocalOpenFile
import app.talaria.ui.MakeKind
import app.talaria.ui.ago
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Panel
import app.talaria.ui.components.SectionTitle
import app.talaria.ui.components.SkeletonCard
import app.talaria.ui.expiresIn
import app.talaria.ui.fileSize
import app.talaria.ui.kindOf
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.Ti
import kotlinx.coroutines.delay
import androidx.compose.material3.HorizontalDivider
import kotlinx.coroutines.launch

/** Every file the server is keeping: what Talaria made, and what you sent it. */
@Composable
fun FilesScreen(repo: Repo, onBack: () -> Unit, modifier: Modifier = Modifier) {
    BackHandler(onBack = onBack)
    val files = rememberPolled(20_000, "files") { repo.api.getJson<FilesDto>("/api/files") }
    val open = LocalOpenFile.current
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var refreshing by remember { mutableStateOf(false) }
    val dto = files.value
    val made = dto?.files.orEmpty().filter { it.origin == "generated" }.sortedByDescending { it.created }
    val sent = dto?.files.orEmpty().filter { it.origin != "generated" }.sortedByDescending { it.created }

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("Files", style = MaterialTheme.typography.titleLarge) },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        PullToRefreshBox(refreshing, onRefresh = { scope.launch { refreshing = true; files.refresh(); delay(500); refreshing = false } }, modifier = Modifier.fillMaxSize()) {
            LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp)) {
                files.error?.let { e -> item { ErrorNote(e) } }
                if (dto == null && files.error == null) item { SkeletonCard(lines = 3) }
                else if (dto != null && dto.files.isEmpty()) item { EmptyState("No files kept", "Things Talaria makes, and photos or PDFs you send it, are kept here for ${dto.retention_hours} hours.") }
                if (made.isNotEmpty()) {
                    item { SectionTitle("Made by Talaria") }
                    item { FileGroup(made, dto?.retention_hours ?: 12, open) { f -> scope.launch { runCatching { FileOpen.share(ctx, FileOpen.fetch(ctx, repo, f.id, f.name), f.name, f.mime) }.onFailure { repo.toast(it.message ?: "That could not be shared.") } } } }
                }
                if (sent.isNotEmpty()) {
                    item { SectionTitle("Sent by you") }
                    item { FileGroup(sent, dto?.retention_hours ?: 12, open) { f -> scope.launch { runCatching { FileOpen.share(ctx, FileOpen.fetch(ctx, repo, f.id, f.name), f.name, f.mime) }.onFailure { repo.toast(it.message ?: "That could not be shared.") } } } }
                }
                if (dto != null && dto.files.isNotEmpty()) item { Text("Files are deleted ${dto.retention_hours} hours after they were made. Open one and save it to keep it.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 4.dp, top = 12.dp, bottom = 24.dp)) }
            }
        }
    }
}

@Composable
private fun FileGroup(list: List<FileMeta>, hours: Int, open: (FileMeta) -> Unit, share: (FileMeta) -> Unit) {
    Panel(Modifier.fillMaxWidth()) {
        Column {
            list.forEachIndexed { i, f ->
                if (i > 0) HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                val k = MakeKind.of(kindOf(f))
                Row(Modifier.fillMaxWidth().clickable { open(f) }.heightIn(min = 64.dp).padding(start = 16.dp, end = 4.dp, top = 8.dp, bottom = 8.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
                    Icon(kindIcon(k ?: if (f.mime.startsWith("image/")) null else null).let { if (f.mime.startsWith("image/")) Ti.File else it }, null, Modifier.size(22.dp), tint = kindTint(k))
                    Column(Modifier.weight(1f)) {
                        Text(f.name, style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Text("${fileSize(f.size)} · ${ago(f.created)} · ${expiresIn(f.created, hours)}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    }
                    IconButton(onClick = { share(f) }) { Icon(Ti.Share, "Share ${f.name}", Modifier.size(20.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant) }
                }
            }
        }
    }
}
