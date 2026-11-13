package app.talaria.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.widget.RemoteViews
import app.talaria.MainActivity
import app.talaria.QuickAction
import app.talaria.R
import app.talaria.TalariaApp
import app.talaria.data.AgentsDto
import app.talaria.data.ControlDto
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull

/** The home-screen widget: an ask box, a talk button, and a line on what is running or waiting for you. */
class TalariaWidget : AppWidgetProvider() {
    override fun onUpdate(context: Context, manager: AppWidgetManager, ids: IntArray) {
        val state = Widget.cached(context)
        ids.forEach { manager.updateAppWidget(it, Widget.views(context, state)) }
        // The system asks every half hour or so; use the turn to look at the server, if the app is not already keeping up.
        val pending = goAsync()
        CoroutineScope(Dispatchers.IO).launch { try { Widget.refresh(context) } finally { pending.finish() } }
    }
}

object Widget {
    private fun component(ctx: Context) = ComponentName(ctx, TalariaWidget::class.java)

    fun count(ctx: Context): Int = AppWidgetManager.getInstance(ctx).getAppWidgetIds(component(ctx)).size

    fun cached(ctx: Context): WidgetState {
        val store = (ctx.applicationContext as TalariaApp).repo.store
        return WidgetState(store.signedIn, store.widgetPending, store.widgetTasks)
    }

    private fun tap(ctx: Context, code: Int, a: QuickAction?): PendingIntent = PendingIntent.getActivity(
        ctx, code, a?.intent(ctx) ?: Intent(ctx, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    fun views(ctx: Context, s: WidgetState): RemoteViews {
        val w = widgetWords(s)
        val v = RemoteViews(ctx.packageName, R.layout.widget_talaria)
        v.setTextViewText(R.id.widget_status, w.chip)
        v.setTextViewText(R.id.widget_detail, w.detail)
        val (bg, fg) = when (w.look) {
            Look.Asking -> R.drawable.widget_chip_ask to R.color.widget_chip_ask_text
            Look.Working -> R.drawable.widget_chip_work to R.color.widget_chip_work_text
            Look.Quiet -> R.drawable.widget_chip_quiet to R.color.widget_chip_quiet_text
        }
        v.setInt(R.id.widget_status, "setBackgroundResource", bg)
        v.setTextColor(R.id.widget_status, ctx.getColor(fg))
        v.setOnClickPendingIntent(R.id.widget_root, tap(ctx, 10, null))
        v.setOnClickPendingIntent(R.id.widget_ask, tap(ctx, 11, QuickAction.Ask))
        v.setOnClickPendingIntent(R.id.widget_photo, tap(ctx, 14, QuickAction.Photo))
        v.setOnClickPendingIntent(R.id.widget_new, tap(ctx, 12, QuickAction.NewChat))
        v.setOnClickPendingIntent(R.id.widget_talk, tap(ctx, 13, QuickAction.Talk))
        v.setOnClickPendingIntent(R.id.widget_status, tap(ctx, 14, if (s.pending > 0) QuickAction.Approvals else null))
        return v
    }

    /** Draws the widgets (if any are on a home screen) with this state, and remembers it for the next time. */
    fun push(ctx: Context, s: WidgetState) {
        val store = (ctx.applicationContext as TalariaApp).repo.store
        store.widgetPending = s.pending; store.widgetTasks = s.tasks
        val mgr = AppWidgetManager.getInstance(ctx)
        val ids = mgr.getAppWidgetIds(component(ctx))
        if (ids.isEmpty()) return
        val v = views(ctx, s)
        ids.forEach { mgr.updateAppWidget(it, v) }
    }

    /** Asks the server for the two small numbers the widget shows. */
    suspend fun refresh(ctx: Context) {
        val repo = (ctx.applicationContext as TalariaApp).repo
        if (!repo.store.signedIn) { push(ctx, WidgetState(false)); return }
        val state = withTimeoutOrNull(15_000) {
            runCatching {
                val control = repo.api.getJson<ControlDto>("/api/control")
                val agents = repo.api.getJson<AgentsDto>("/api/agents")
                WidgetState(true, control.pending, agents.agents.count { it.kind != "main" })
            }.getOrNull()
        } ?: return // out of reach: leave what the widget already says
        push(ctx, state)
    }
}
