package app.talaria.ui.theme

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.addPathNodes
import androidx.compose.ui.unit.dp

/**
 * The app's icons: one family, drawn on a 24-unit grid with a 1.75 stroke and round ends, so they read as lightly as the
 * type does. The five tab icons are the web app's own. Colour comes from the Icon's tint, never from the drawing.
 */
object Ti {
    private val ink = SolidColor(Color.Black)

    private class P(val d: String, val width: Float = 1.75f, val fill: Boolean = false)

    private fun line(vararg d: String) = d.map { P(it) }

    private fun make(name: String, paths: List<P>): ImageVector = ImageVector.Builder(name, 24.dp, 24.dp, 24f, 24f).apply {
        for (p in paths) addPath(
            pathData = addPathNodes(p.d),
            fill = if (p.fill) ink else null,
            stroke = if (p.fill && p.width == 0f) null else ink,
            strokeLineWidth = p.width, strokeLineCap = StrokeCap.Round, strokeLineJoin = StrokeJoin.Round,
        )
    }.build()

    private fun dots(vararg d: String) = d.map { P(it, width = 2.6f) }

    val Chat by lazy { make("chat", line("M20 12a8 8 0 0 1-11.6 7.1L4 20l1-4.2A8 8 0 1 1 20 12z")) }
    val Calendar by lazy { make("calendar", line("M7 4.5h10A3.5 3.5 0 0 1 20.5 8v9a3.5 3.5 0 0 1-3.5 3.5H7A3.5 3.5 0 0 1 3.5 17V8A3.5 3.5 0 0 1 7 4.5z", "M3.5 10h17", "M8.5 3v3", "M15.5 3v3")) }
    val Agents by lazy { make("agents", line("M8.5 8h7a3.5 3.5 0 0 1 3.5 3.5v4a3.5 3.5 0 0 1-3.5 3.5h-7A3.5 3.5 0 0 1 5 15.5v-4A3.5 3.5 0 0 1 8.5 8z", "M12 8V5", "M9 4.5h6", "M3 12.5v2", "M21 12.5v2") + dots("M9.5 13.5h.01", "M14.5 13.5h.01")) }
    val Sliders by lazy { make("sliders", line("M4 7h10", "M18 7h2", "M4 17h2", "M10 17h10", "M16 4.8a2.2 2.2 0 1 0 0 4.4a2.2 2.2 0 1 0 0-4.4z", "M8 14.8a2.2 2.2 0 1 0 0 4.4a2.2 2.2 0 1 0 0-4.4z")) }
    val History by lazy { make("history", line("M3.5 12a8.5 8.5 0 1 0 2.6-6.1L3.5 8.5", "M3.5 3.5v5h5", "M12 7.5V12l3 2")) }
    val Plus by lazy { make("plus", line("M12 5v14", "M5 12h14")) }
    val Minus by lazy { make("minus", line("M5 12h14")) }
    val Compose by lazy { make("compose", line("M12 4.5H7A3 3 0 0 0 4 7.5v9.5a3 3 0 0 0 3 3h9.5a3 3 0 0 0 3-3V12", "M17.4 3.6a1.9 1.9 0 0 1 2.7 2.7L11 15.4l-3.6.8.8-3.6z")) }
    val Mic by lazy { make("mic", line("M12 3a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V6a3 3 0 0 0-3-3z", "M5.5 11a6.5 6.5 0 0 0 13 0", "M12 17.5V21")) }
    val Wave by lazy { make("wave", line("M4.5 10v4", "M8.5 7v10", "M12.5 4v16", "M16.5 8v8", "M20 10.5v3")) }
    val Send by lazy { make("send", line("M12 19.5v-15", "M5.5 11 12 4.5l6.5 6.5")) }
    val Close by lazy { make("close", line("M6 6l12 12", "M18 6 6 18")) }
    val Check by lazy { make("check", line("M5 12.5l4.5 4.5L19 7.5")) }
    val ChevronRight by lazy { make("chevronRight", line("M9.5 6l6 6-6 6")) }
    val ChevronUp by lazy { make("chevronUp", line("M6 14.5l6-6 6 6")) }
    val ChevronDown by lazy { make("chevronDown", line("M6 9.5l6 6 6-6")) }
    val Back by lazy { make("back", line("M19 12H5", "M11.5 5.5 5 12l6.5 6.5")) }
    val Globe by lazy { make("globe", line("M12 3a9 9 0 1 0 0 18a9 9 0 0 0 0-18z", "M3 12h18", "M12 3c2.4 2.5 3.6 5.5 3.6 9s-1.2 6.5-3.6 9c-2.4-2.5-3.6-5.5-3.6-9S9.6 5.5 12 3z")) }
    val More by lazy { make("more", dots("M12 5.5h.01", "M12 12h.01", "M12 18.5h.01")) }
    val Pause by lazy { make("pause", line("M8.5 5.5v13", "M15.5 5.5v13")) }
    val Play by lazy { make("play", listOf(P("M8 5.2v13.6a.8.8 0 0 0 1.2.7l10.8-6.8a.8.8 0 0 0 0-1.4L9.2 4.5A.8.8 0 0 0 8 5.2z"))) }
    val Stop by lazy { make("stop", listOf(P("M8 6.5h8A1.5 1.5 0 0 1 17.5 8v8a1.5 1.5 0 0 1-1.5 1.5H8A1.5 1.5 0 0 1 6.5 16V8A1.5 1.5 0 0 1 8 6.5z", fill = true))) }
    val Record by lazy { make("record", listOf(P("M12 6.5a5.5 5.5 0 1 0 0 11a5.5 5.5 0 0 0 0-11z", fill = true))) }
    val Lock by lazy { make("lock", line("M7 10.5h10A1.5 1.5 0 0 1 18.5 12v6.5A1.5 1.5 0 0 1 17 20H7a1.5 1.5 0 0 1-1.5-1.5V12A1.5 1.5 0 0 1 7 10.5z", "M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5")) }
    val Keyboard by lazy { make("keyboard", line("M4 6.5h16A1.5 1.5 0 0 1 21.5 8v8a1.5 1.5 0 0 1-1.5 1.5H4A1.5 1.5 0 0 1 2.5 16V8A1.5 1.5 0 0 1 4 6.5z", "M7 15h10") + dots("M6.5 10.5h.01", "M10.2 10.5h.01", "M13.8 10.5h.01", "M17.5 10.5h.01")) }
    val Refresh by lazy { make("refresh", line("M19.5 11a7.5 7.5 0 0 0-13.4-3.6L4.5 9", "M4.5 4.5V9H9", "M4.5 13a7.5 7.5 0 0 0 13.4 3.6l1.6-1.6", "M19.5 19.5V15H15")) }
    val Backspace by lazy { make("backspace", line("M20 5.5H9.2a1.5 1.5 0 0 0-1.1.5L3 12l5.1 6a1.5 1.5 0 0 0 1.1.5H20a1.5 1.5 0 0 0 1.5-1.5V7A1.5 1.5 0 0 0 20 5.5z", "M16.5 9.5l-5 5", "M11.5 9.5l5 5")) }
    val Return by lazy { make("return", line("M19.5 6v5.5a2.5 2.5 0 0 1-2.5 2.5H5", "M9 10l-4 4 4 4")) }
    val Trash by lazy { make("trash", line("M4 7h16", "M10 11v5.5", "M14 11v5.5", "M6 7l.9 11.2A2 2 0 0 0 8.9 20h6.2a2 2 0 0 0 2-1.8L18 7", "M9 7V4.5h6V7")) }
    val Edit by lazy { make("edit", line("M4.5 19.5l.8-3.6L16.5 4.7a2 2 0 0 1 2.8 0l0 0a2 2 0 0 1 0 2.8L8.1 18.7z", "M14.5 6.7l2.8 2.8")) }
    val Archive by lazy { make("archive", line("M4.5 4.5h15A1 1 0 0 1 20.5 5.5v2.5a1 1 0 0 1-1 1h-15a1 1 0 0 1-1-1V5.5a1 1 0 0 1 1-1z", "M5.5 9v9a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2V9", "M10 13h4")) }
    val Eye by lazy { make("eye", line("M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z", "M12 9.2a2.8 2.8 0 1 0 0 5.6a2.8 2.8 0 0 0 0-5.6z")) }
    val EyeOff by lazy { make("eyeOff", line("M9.9 5.8A9.6 9.6 0 0 1 12 5.5c6 0 9.5 6.5 9.5 6.5a16 16 0 0 1-2.4 3.2", "M6.4 7.4A15.6 15.6 0 0 0 2.5 12S6 18.5 12 18.5c1.5 0 2.8-.4 4-1", "M3.5 3.5l17 17", "M10.2 10.2a2.8 2.8 0 0 0 3.6 3.6")) }
    val File by lazy { make("file", line("M13.5 3.5H7.5A2 2 0 0 0 5.5 5.5v13a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2V8.5z", "M13.5 3.5v5h5", "M9 13h6", "M9 16.5h6")) }
    val Sparkles by lazy { make("sparkles", line("M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z", "M18.5 15.5l.8 2.2 2.2.8-2.2.8-.8 2.2-.8-2.2-2.2-.8 2.2-.8z")) }
    val WifiOff by lazy { make("wifiOff", line("M3.5 3.5l17 17", "M8.6 8.2A13.3 13.3 0 0 0 2.5 11.5", "M12 6.7a13.3 13.3 0 0 1 9.5 4.1", "M6.2 14.7a8.3 8.3 0 0 1 4-2.2", "M13.8 12.7a8.3 8.3 0 0 1 3.8 2", "M9.4 18a4 4 0 0 1 5.2 0") + dots("M12 21.2h.01")) }
    val Alert by lazy { make("alert", line("M12 3.5a8.5 8.5 0 1 0 0 17a8.5 8.5 0 0 0 0-17z", "M12 8v4.8") + dots("M12 16.3h.01")) }
    val External by lazy { make("external", line("M14 4.5h5.5V10", "M19.5 4.5l-8 8", "M17.5 14v3.5a2 2 0 0 1-2 2h-9a2 2 0 0 1-2-2v-9a2 2 0 0 1 2-2H10")) }
    val ThumbUp by lazy { make("thumbUp", line("M7.5 11.5V20H4.5a1 1 0 0 1-1-1v-6.5a1 1 0 0 1 1-1h3z", "M7.5 11.5l3-6.4a1.8 1.8 0 0 1 3.3 1.3L13.2 10h5.1a1.7 1.7 0 0 1 1.7 2l-1.2 6a2 2 0 0 1-2 1.6H7.5")) }
    val ThumbDown by lazy { make("thumbDown", line("M7.5 12.5V4H4.5a1 1 0 0 0-1 1v6.5a1 1 0 0 0 1 1h3z", "M7.5 12.5l3 6.4a1.8 1.8 0 0 0 3.3-1.3L13.2 14h5.1a1.7 1.7 0 0 0 1.7-2l-1.2-6a2 2 0 0 0-2-1.6H7.5")) }
    val Copy by lazy { make("copy", line("M10 8.5h8.5a1 1 0 0 1 1 1V19a1 1 0 0 1-1 1H10a1 1 0 0 1-1-1V9.5a1 1 0 0 1 1-1z", "M5.5 15V5a1 1 0 0 1 1-1H16")) }
    val Shield by lazy { make("shield", line("M12 3.5l7 2.8v5.4c0 4.4-2.9 7.5-7 9-4.1-1.5-7-4.6-7-9V6.3z", "M9 12l2.2 2.2L15.2 10")) }
    val Hand by lazy { make("hand", line("M9 11V5.5a1.5 1.5 0 0 1 3 0V10", "M12 10V4.5a1.5 1.5 0 0 1 3 0V11", "M15 11V6.5a1.5 1.5 0 0 1 3 0V14a7 7 0 0 1-7 7h-.5A6 6 0 0 1 5.8 17.6L4 14.5a1.5 1.5 0 0 1 2.5-1.6L9 16V11")) }
    val Slides by lazy { make("slides", line("M5.5 4.5h13A1.5 1.5 0 0 1 20 6v9a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 15V6a1.5 1.5 0 0 1 1.5-1.5z", "M12 16.5v3.5", "M8.5 20h7", "M8 9h5", "M8 12.5h8")) }
    val Chart by lazy { make("chart", line("M6 4.5h12A1.5 1.5 0 0 1 19.5 6v12a1.5 1.5 0 0 1-1.5 1.5H6A1.5 1.5 0 0 1 4.5 18V6A1.5 1.5 0 0 1 6 4.5z", "M8.5 16v-3", "M12 16V8.5", "M15.5 16v-5")) }
    val Share by lazy { make("share", line("M12 15.5V4", "M7.8 8.2 12 4l4.2 4.2", "M5 12.5v5.5a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-5.5")) }
    val Download by lazy { make("download", line("M12 4v11.5", "M7.8 11.3l4.2 4.2 4.2-4.2", "M5 19.5h14")) }
    val Search by lazy { make("search", line("M11 4.5a6.5 6.5 0 1 0 0 13a6.5 6.5 0 0 0 0-13z", "M20 20l-4-4")) }
    val Pin by lazy { make("pin", line("M9 4.5h6l-.8 5.5 2.8 2.8v1H7v-1L9.8 10z", "M12 14v6")) }
    val User by lazy { make("user", line("M12 11.5a3.8 3.8 0 1 0 0-7.6a3.8 3.8 0 0 0 0 7.6z", "M4.5 20a7.5 7.5 0 0 1 15 0")) }
    val Sun by lazy { make("sun", line("M12 8a4 4 0 1 0 0 8a4 4 0 0 0 0-8z", "M12 2.5v2", "M12 19.5v2", "M5.3 5.3l1.4 1.4", "M17.3 17.3l1.4 1.4", "M2.5 12h2", "M19.5 12h2", "M5.3 18.7l1.4-1.4", "M17.3 6.7l1.4-1.4")) }
    val Brain by lazy { make("brain", line("M9.5 4.5A3 3 0 0 0 6.6 8 3.2 3.2 0 0 0 5 11a3 3 0 0 0 1.4 2.6A3.2 3.2 0 0 0 9 18.5a2.5 2.5 0 0 0 3-1.6V6.5a2.5 2.5 0 0 0-2.5-2z", "M14.5 4.5A3 3 0 0 1 17.4 8 3.2 3.2 0 0 1 19 11a3 3 0 0 1-1.4 2.6A3.2 3.2 0 0 1 15 18.5a2.5 2.5 0 0 1-3-1.6")) }
    val Mail by lazy { make("mail", line("M5 5.5h14A1.5 1.5 0 0 1 20.5 7v10a1.5 1.5 0 0 1-1.5 1.5H5A1.5 1.5 0 0 1 3.5 17V7A1.5 1.5 0 0 1 5 5.5z", "M4 7.5l8 6 8-6")) }
    val Target by lazy { make("target", line("M12 3.5a8.5 8.5 0 1 0 0 17a8.5 8.5 0 0 0 0-17z", "M12 8a4 4 0 1 0 0 8a4 4 0 0 0 0-8z", "M12 11.5h.01")) }
}
