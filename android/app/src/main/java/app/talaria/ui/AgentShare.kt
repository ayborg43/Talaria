package app.talaria.ui

import android.content.ClipData
import android.content.Context
import android.content.Intent
import androidx.core.content.FileProvider
import app.talaria.data.Repo
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File

/**
 * Sends one of your agents to someone as a file: downloads it from the server and opens Android's share sheet. The file
 * holds only the agent's name, icon, whether it looks only, and its instructions: no model, no keys.
 */
suspend fun shareAgentFile(ctx: Context, repo: Repo, id: String, name: String) {
    val bytes = repo.api.getBytes("/api/agent-file?id=" + java.net.URLEncoder.encode(id, "UTF-8"))
    val file = withContext(Dispatchers.IO) {
        // Cache folder the FileProvider already serves; only the file being shared is kept.
        val dir = File(ctx.cacheDir, "files").apply { mkdirs() }
        dir.listFiles { f -> f.name.endsWith(".agent.md") }?.forEach { runCatching { it.delete() } }
        File(dir, AgentFile.fileName(name)).also { it.writeBytes(bytes) }
    }
    val uri = FileProvider.getUriForFile(ctx, "${ctx.packageName}.files", file)
    val send = Intent(Intent.ACTION_SEND).setType("text/markdown").putExtra(Intent.EXTRA_STREAM, uri)
        .putExtra(Intent.EXTRA_SUBJECT, "$name (a Talaria agent)")
        .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
    send.clipData = ClipData.newRawUri(file.name, uri)
    val chooser = Intent.createChooser(send, "Share $name").addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    ctx.startActivity(chooser)
}
