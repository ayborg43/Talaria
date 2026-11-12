package app.talaria.ui.screens

import android.annotation.SuppressLint
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Color as AColor
import android.graphics.pdf.PdfRenderer
import android.os.ParcelFileDescriptor
import android.webkit.WebView
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.unit.dp
import app.talaria.data.FileMeta
import app.talaria.data.Repo
import app.talaria.ui.FileOpen
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.RoundIconButton
import app.talaria.ui.fileSize
import app.talaria.ui.theme.Ti
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import java.io.File

/** A kept file, read inside the app: a PDF page by page, a picture, or a page the agent made with every script switched off. */
@Composable
fun DocViewer(repo: Repo, file: FileMeta, onBack: () -> Unit, onSave: (name: String, mime: String, bytes: ByteArray) -> Unit, modifier: Modifier = Modifier) {
    BackHandler(onBack = onBack)
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val loaded by produceState<Result<ByteArray>?>(null, file.id) { value = runCatching { repo.api.getBytes("/api/files/${file.id}") } }
    val mime = file.mime
    Column(modifier.fillMaxSize().background(MaterialTheme.colorScheme.background).statusBarsPadding().navigationBarsPadding()) {
        TopAppBar(
            title = { Column { Text(file.name.ifBlank { "Document" }, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis); Text(listOf(kindWord(mime), if (file.size > 0) fileSize(file.size) else "").filter { it.isNotBlank() }.joinToString(" · "), style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            actions = {
                IconButton(onClick = { loaded?.getOrNull()?.let { onSave(file.name, mime, it) } }, enabled = loaded?.isSuccess == true) { Icon(Ti.Download, "Save to this phone") }
                IconButton(
                    onClick = { scope.launch { runCatching { FileOpen.share(ctx, FileOpen.fetch(ctx, repo, file.id, file.name), file.name, mime) }.onFailure { repo.toast(it.message ?: "That could not be shared.") } } },
                    enabled = loaded?.isSuccess == true,
                ) { Icon(Ti.Share, "Share") }
            },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
            windowInsets = androidx.compose.foundation.layout.WindowInsets(0, 0, 0, 0),
        )
        Box(Modifier.weight(1f).fillMaxWidth()) {
            val r = loaded
            when {
                r == null -> CircularProgressIndicator(Modifier.align(Alignment.Center), color = MaterialTheme.colorScheme.primary)
                r.isFailure -> Box(Modifier.padding(16.dp)) { ErrorNote(r.exceptionOrNull()?.message ?: "That file could not be loaded. It may have expired.") }
                mime == "application/pdf" -> PdfView(r.getOrThrow())
                mime == "text/html" -> HtmlView(r.getOrThrow())
                mime.startsWith("image/") -> ImageView(r.getOrThrow())
                else -> EmptyState("No preview", "Talaria cannot show this kind of file. Share it to open it in another app.")
            }
        }
    }
}

private fun kindWord(mime: String) = when { mime == "application/pdf" -> "PDF"; mime == "text/html" -> "Page"; mime.startsWith("image/") -> "Image"; else -> "File" }

@Composable
private fun ZoomButtons(zoom: Float, onZoom: (Float) -> Unit, modifier: Modifier = Modifier) {
    Column(modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        RoundIconButton(Ti.Plus, "Zoom in", { onZoom((zoom * 1.4f).coerceAtMost(3f)) }, container = MaterialTheme.colorScheme.inverseSurface.copy(alpha = 0.78f), content = MaterialTheme.colorScheme.inverseOnSurface)
        RoundIconButton(Ti.Minus, "Zoom out", { onZoom((zoom / 1.4f).coerceAtLeast(1f)) }, container = MaterialTheme.colorScheme.inverseSurface.copy(alpha = 0.78f), content = MaterialTheme.colorScheme.inverseOnSurface)
    }
}

@Composable
private fun PdfView(bytes: ByteArray) {
    val ctx = LocalContext.current
    val opened = remember(bytes) {
        runCatching {
            val f = File(ctx.cacheDir, "viewing.pdf").also { it.writeBytes(bytes) }
            PdfRenderer(ParcelFileDescriptor.open(f, ParcelFileDescriptor.MODE_READ_ONLY))
        }
    }
    val renderer = opened.getOrNull()
    if (renderer == null) { Box(Modifier.padding(16.dp)) { ErrorNote("That PDF could not be opened.") }; return }
    DisposableEffect(renderer) { onDispose { runCatching { renderer.close() } } }
    val lock = remember(renderer) { Mutex() } // a PdfRenderer draws one page at a time
    var zoom by remember { mutableFloatStateOf(1f) }
    BoxWithConstraints(Modifier.fillMaxSize()) {
        val view = maxWidth
        val px = with(LocalDensity.current) { (view * zoom).roundToPx() }
        Box(Modifier.fillMaxSize().horizontalScroll(rememberScrollState())) {
            LazyColumn(Modifier.width(view * zoom).fillMaxSize(), contentPadding = PaddingValues(vertical = 8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                items((0 until renderer.pageCount).toList(), key = { it }) { i ->
                    val page by produceState<Bitmap?>(null, i, px) {
                        value = withContext(Dispatchers.IO) {
                            lock.withLock {
                                renderer.openPage(i).use { p ->
                                    val h = (px.toFloat() * p.height / p.width).toInt().coerceAtLeast(1)
                                    Bitmap.createBitmap(px, h, Bitmap.Config.ARGB_8888).also { it.eraseColor(AColor.WHITE); p.render(it, null, null, PdfRenderer.Page.RENDER_MODE_FOR_DISPLAY) }
                                }
                            }
                        }
                    }
                    val b = page
                    if (b != null) Image(b.asImageBitmap(), "Page ${i + 1}", Modifier.fillMaxWidth(), contentScale = ContentScale.FillWidth)
                    else Box(Modifier.fillMaxWidth().aspectRatio(0.77f).background(MaterialTheme.colorScheme.surfaceContainerHigh))
                }
            }
        }
        ZoomButtons(zoom, { zoom = it }, Modifier.align(Alignment.BottomEnd))
    }
}

@Composable
private fun ImageView(bytes: ByteArray) {
    val bmp = remember(bytes) {
        val o = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeByteArray(bytes, 0, bytes.size, o)
        var sample = 1
        while (o.outWidth / sample > 3000 || o.outHeight / sample > 3000) sample *= 2
        BitmapFactory.decodeByteArray(bytes, 0, bytes.size, BitmapFactory.Options().apply { inSampleSize = sample })
    }
    if (bmp == null) { Box(Modifier.padding(16.dp)) { ErrorNote("That picture could not be shown.") }; return }
    var scale by remember { mutableFloatStateOf(1f) }
    var offset by remember { mutableStateOf(Offset.Zero) }
    Box(Modifier.fillMaxSize().pointerInput(Unit) {
        detectTransformGestures { _, pan, zoom, _ ->
            scale = (scale * zoom).coerceIn(1f, 6f)
            offset = if (scale == 1f) Offset.Zero else offset + pan
        }
    }, contentAlignment = Alignment.Center) {
        Image(bmp.asImageBitmap(), "The picture", Modifier.fillMaxSize().graphicsLayer { scaleX = scale; scaleY = scale; translationX = offset.x; translationY = offset.y }, contentScale = ContentScale.Fit)
    }
}

/** A page the agent made: shown with scripts, network and file access all off, so it can read and display but do nothing. */
@SuppressLint("SetJavaScriptEnabled")
@Composable
private fun HtmlView(bytes: ByteArray) {
    val html = remember(bytes) { String(bytes, Charsets.UTF_8) }
    AndroidView(
        modifier = Modifier.fillMaxSize(),
        factory = { c ->
            WebView(c).apply {
                settings.javaScriptEnabled = false
                settings.blockNetworkLoads = true
                settings.allowFileAccess = false
                settings.allowContentAccess = false
                settings.domStorageEnabled = false
                settings.setSupportZoom(true); settings.builtInZoomControls = true; settings.displayZoomControls = false
                loadDataWithBaseURL("about:blank", html, "text/html", "utf-8", null)
            }
        },
        onRelease = { it.destroy() },
    )
}
