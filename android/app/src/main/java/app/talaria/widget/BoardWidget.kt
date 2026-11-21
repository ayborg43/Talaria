package app.talaria.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.view.View
import android.widget.RemoteViews
import app.talaria.MainActivity
import app.talaria.QuickAction
import app.talaria.R
import app.talaria.TalariaApp
import app.talaria.data.BoardDto
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull

/**
 * The board widget: how many cards are Ready, Running and in Review. A tap opens Tasks, on the Board tab. It is kept up
 * to date the way the other widget is: the app draws it when it sees something change, and otherwise the system asks
 * about every half hour, which is when it looks at the server itself. Nothing runs in between.
 */
class TalariaBoardWidget : AppWidgetProvider() {
    override fun onUpdate(context: Context, manager: AppWidgetManager, ids: IntArray) {
        val state = BoardWidget.cached(context)
        ids.forEach { manager.updateAppWidget(it, BoardWidget.views(context, state)) }
        val pending = goAsync()
        CoroutineScope(Dispatchers.IO).launch { try { BoardWidget.refresh(context) } finally { pending.finish() } }
    }
}

object BoardWidget {
    private fun component(ctx: Context) = ComponentName(ctx, TalariaBoardWidget::class.java)

    private fun ids(ctx: Context): IntArray = AppWidgetManager.getInstance(ctx).getAppWidgetIds(component(ctx))

    fun cached(ctx: Context): BoardWidgetState {
        val store = (ctx.applicationContext as TalariaApp).repo.store
        return BoardWidgetState.decode(store.signedIn, store.widgetBoard)
    }

    private fun open(ctx: Context): PendingIntent =
        PendingIntent.getActivity(ctx, 20, QuickAction.Board.intent(ctx), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)

    fun views(ctx: Context, s: BoardWidgetState): RemoteViews {
        val v = RemoteViews(ctx.packageName, R.layout.widget_board)
        val note = boardWidgetNote(s)
        v.setViewVisibility(R.id.widget_board_counts, if (note == null) View.VISIBLE else View.GONE)
        v.setViewVisibility(R.id.widget_board_note, if (note == null) View.GONE else View.VISIBLE)
        if (note != null) v.setTextViewText(R.id.widget_board_note, note)
        else {
            fun count(id: Int, n: Int, lit: Int) { v.setTextViewText(id, n.toString()); v.setTextColor(id, ctx.getColor(if (n > 0) lit else R.color.widget_muted)) }
            count(R.id.widget_board_ready, s.ready, R.color.widget_ink)
            count(R.id.widget_board_running, s.running, R.color.widget_chip_work_text)
            count(R.id.widget_board_review, s.review, R.color.widget_chip_ask_text) // a card in Review is waiting for you
        }
        v.setOnClickPendingIntent(R.id.widget_board_root, open(ctx))
        return v
    }

    /** Draws the widget (if one is on a home screen) with this state, and remembers it for the next time. */
    fun push(ctx: Context, s: BoardWidgetState) {
        val store = (ctx.applicationContext as TalariaApp).repo.store
        store.widgetBoard = s.encode()
        val mgr = AppWidgetManager.getInstance(ctx)
        val ids = ids(ctx)
        if (ids.isEmpty()) return
        val v = views(ctx, s)
        ids.forEach { mgr.updateAppWidget(it, v) }
    }

    /** The Board tab already holds the cards: it hands the widget the counts, with no request of its own. */
    fun pushBoard(ctx: Context, b: BoardDto) = push(ctx, if (b.available) BoardWidgetState.of(b.cards) else BoardWidgetState(true, known = true, available = false))

    /** Looks at the server's board, if the widget is on a home screen. Unreachable or failing: it keeps what it already says. */
    suspend fun refresh(ctx: Context) {
        if (ids(ctx).isEmpty()) return
        val repo = (ctx.applicationContext as TalariaApp).repo
        if (!repo.store.signedIn) { push(ctx, BoardWidgetState()); return }
        val board = withTimeoutOrNull(15_000) { runCatching { repo.api.getJson<BoardDto>("/api/board") }.getOrNull() } ?: return
        pushBoard(ctx, board)
    }
}
