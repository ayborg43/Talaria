package app.talaria.ui

import android.content.Context
import android.content.Intent
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.core.content.FileProvider
import app.talaria.data.FileMeta
import app.talaria.data.Repo
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File

/** Opens a kept file in Talaria's own viewer: set by the app's root, used by any screen that lists files. */
val LocalOpenFile = staticCompositionLocalOf<(FileMeta) -> Unit> { {} }

/** Fetches a file the agent made, and hands it to whichever app opens or takes that kind of file. */
object FileOpen {
    suspend fun fetch(ctx: Context, repo: Repo, id: String, name: String): File {
        val bytes = repo.api.getBytes("/api/files/$id")
        return withContext(Dispatchers.IO) {
            val dir = File(ctx.cacheDir, "files").apply { mkdirs() }
            File(dir, id.filter { it.isLetterOrDigit() || it == '-' }.take(40) + "-" + name.replace(Regex("[^A-Za-z0-9._-]"), "_").take(60)).also { it.writeBytes(bytes) }
        }
    }

    suspend fun open(ctx: Context, repo: Repo, id: String, name: String, mime: String) = view(ctx, fetch(ctx, repo, id, name), name, mime)

    fun view(ctx: Context, file: File, name: String, mime: String) {
        val uri = FileProvider.getUriForFile(ctx, "${ctx.packageName}.files", file)
        val type = mime.ifBlank { "*/*" }
        val view = Intent(Intent.ACTION_VIEW).setDataAndType(uri, type).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        try { ctx.startActivity(view) } catch (_: Exception) { share(ctx, file, name, mime) }
    }

    fun share(ctx: Context, file: File, name: String, mime: String) {
        val uri = FileProvider.getUriForFile(ctx, "${ctx.packageName}.files", file)
        val type = mime.ifBlank { "*/*" }
        ctx.startActivity(Intent.createChooser(Intent(Intent.ACTION_SEND).setType(type).putExtra(Intent.EXTRA_STREAM, uri).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION), name).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }
}
