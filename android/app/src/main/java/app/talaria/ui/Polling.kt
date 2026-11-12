package app.talaria.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay

/** Something fetched from the server, kept fresh while the screen is showing. */
@Stable
class Polled<T> {
    var value by mutableStateOf<T?>(null)
        internal set
    var error by mutableStateOf<String?>(null)
        internal set
    var loading by mutableStateOf(true)
        internal set
    internal var tick by mutableIntStateOf(0)
    fun refresh() { tick++ }
}

/** Fetches now, then every [intervalMs] (0 means once) for as long as the screen is on view. */
@Composable
fun <T> rememberPolled(intervalMs: Long = 0, key: Any? = null, fetch: suspend () -> T): Polled<T> {
    val p = remember(key) { Polled<T>() }
    val owner = LocalLifecycleOwner.current
    LaunchedEffect(p, p.tick) {
        owner.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            while (true) {
                try { p.value = fetch(); p.error = null }
                catch (e: CancellationException) { throw e }
                catch (e: Exception) { p.error = e.message ?: "That could not be loaded." }
                p.loading = false
                if (intervalMs <= 0) break
                delay(intervalMs)
            }
        }
    }
    return p
}
