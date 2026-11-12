package app.talaria

import android.app.Application
import android.app.Activity
import android.os.Bundle
import app.talaria.data.Repo
import kotlinx.coroutines.flow.debounce
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.launch
import app.talaria.service.Notifier

class TalariaApp : Application() {
    lateinit var repo: Repo
        private set
    lateinit var notifier: Notifier
        private set
    val voice: app.talaria.voice.VoiceController by lazy { app.talaria.voice.VoiceController(this, repo) }

    override fun onCreate() {
        super.onCreate()
        repo = Repo(this)
        notifier = Notifier(this, repo)
        notifier.start()
        repo.start()
        keepWidgetCurrent()
        // Whether a screen is showing decides if a reply is worth a notification.
        var started = 0
        registerActivityLifecycleCallbacks(object : ActivityLifecycleCallbacks {
            override fun onActivityStarted(activity: Activity) { started++; repo.foreground = true; notifier.cancelAll() }
            override fun onActivityStopped(activity: Activity) { started--; if (started <= 0) repo.foreground = false }
            override fun onActivityCreated(a: Activity, b: Bundle?) {}
            override fun onActivityResumed(a: Activity) {}
            override fun onActivityPaused(a: Activity) {}
            override fun onActivitySaveInstanceState(a: Activity, b: Bundle) {}
            override fun onActivityDestroyed(a: Activity) {}
        })
    }
}

@OptIn(kotlinx.coroutines.FlowPreview::class)
private fun TalariaApp.keepWidgetCurrent() {
    // While the app is alive it tells the widget what is going on; when it is not, the widget asks the server itself now and then.
    kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.SupervisorJob() + kotlinx.coroutines.Dispatchers.Default).launch {
        kotlinx.coroutines.flow.combine(repo.conn, repo.workers, repo.messages) { conn, workers, messages ->
            // Until the first look at the server arrives (or while it is out of reach) the widget keeps what it last said.
            if (conn is app.talaria.data.Conn.Connecting || conn is app.talaria.data.Conn.Offline) null
            else app.talaria.widget.WidgetState(
                signedIn = conn !is app.talaria.data.Conn.SignedOut,
                pending = messages.count { it.role == "approval" && it.state == "pending" },
                tasks = workers.count { it.kind != "main" },
            )
        }.filterNotNull().distinctUntilChanged().debounce(1_500).collect { app.talaria.widget.Widget.push(this@keepWidgetCurrent, it) }
    }
}

val Activity.repo: Repo get() = (application as TalariaApp).repo