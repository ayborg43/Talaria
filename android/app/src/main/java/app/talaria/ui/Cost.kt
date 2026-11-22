package app.talaria.ui

import app.talaria.data.UsageDay
import app.talaria.data.UsageLine
import java.util.Locale

/** The words and numbers on the cost screen, kept free of Android so they can be tested. They match what the server prints. */
object Cost {
    /** The three periods the screen offers, as the server's `days` value. */
    enum class Period(val days: Int, val label: String) { Today(1, "Today"), Week(7, "Week"), Month(30, "Month") }

    /** "$0", "under $0.01", "$1.25", "$42": the server's own way of writing dollars. */
    fun money(f: Double): String = when {
        f <= 0.0 -> "$0"
        f < 0.01 -> "under $0.01"
        f < 10 -> String.format(Locale.US, "$%.2f", f)
        else -> String.format(Locale.US, "$%.0f", f)
    }

    /** 1,200,000 is "1.2M", 12,000 is "12k", 1,500 is "1.5k". */
    fun tokens(n: Long): String = when {
        n >= 1_000_000 -> String.format(Locale.US, "%.1fM", n / 1e6)
        n >= 10_000 -> String.format(Locale.US, "%.0fk", n / 1e3)
        n >= 1000 -> String.format(Locale.US, "%.1fk", n / 1e3)
        else -> n.toString()
    }

    fun requests(n: Long): String = if (n == 1L) "1 request" else "$n requests"

    /** "3 requests · 1.2k in · 340 out". */
    fun usage(l: UsageLine): String = "${requests(l.totals.calls)} · ${tokens(l.totals.tokensIn)} in · ${tokens(l.totals.tokensOut)} out"

    /** What it cost, or "no price" when a model in it has none set (the server leaves those out of the dollars). */
    fun price(l: UsageLine): String = if (l.priced) money(l.cost) else if (l.cost > 0) "${money(l.cost)}+ (some with no price)" else "no price"

    /**
     * How tall each bar of the daily chart is, from 0 to 1 against the busiest day. A day with anything in it is never
     * thinner than [floor], so a quiet day is still seen; an empty day is 0.
     */
    fun barHeights(days: List<UsageDay>, floor: Float = 0.04f): List<Float> {
        val top = days.maxOfOrNull { it.tokens } ?: 0L
        if (top <= 0L) return days.map { 0f }
        return days.map { if (it.tokens <= 0L) 0f else (it.tokens.toFloat() / top).coerceIn(floor, 1f) }
    }

    /** "12 Nov" from "2026-11-12", for the ends of the chart; the text unchanged if it is not a date. */
    fun shortDate(iso: String): String {
        val p = iso.split('-')
        val months = listOf("Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec")
        val m = p.getOrNull(1)?.toIntOrNull(); val d = p.getOrNull(2)?.toIntOrNull()
        return if (p.size == 3 && m != null && d != null && m in 1..12) "$d ${months[m - 1]}" else iso
    }

    /** A line for screen readers: "3 Nov, 1.2k tokens". */
    fun dayWords(d: UsageDay): String = "${shortDate(d.date)}, ${tokens(d.tokens)} tokens"

    // ---- setting a price ----

    sealed interface PriceEdit {
        /** Both boxes empty: the price is forgotten. */
        data object Clear : PriceEdit
        data class Set(val priceIn: Double, val priceOut: Double) : PriceEdit
        data class Bad(val why: String) : PriceEdit
    }

    const val MAX_PRICE = 100_000.0

    private fun one(text: String): Result<Double?> {
        val t = text.trim().replace(',', '.').removePrefix("$").trim()
        if (t.isEmpty()) return Result.success(null)
        val v = t.toDoubleOrNull() ?: return Result.failure(IllegalArgumentException("Prices are numbers, such as 2.5."))
        if (v.isNaN() || v.isInfinite() || v < 0) return Result.failure(IllegalArgumentException("A price cannot be negative."))
        if (v > MAX_PRICE) return Result.failure(IllegalArgumentException("A price is dollars for a million tokens, up to 100000."))
        return Result.success(v)
    }

    /** What the two boxes mean: both empty clears, both filled sets, one filled is refused rather than guessed at. */
    fun parsePrice(inText: String, outText: String): PriceEdit {
        val a = one(inText).getOrElse { return PriceEdit.Bad(it.message.orEmpty()) }
        val b = one(outText).getOrElse { return PriceEdit.Bad(it.message.orEmpty()) }
        return when {
            a == null && b == null -> PriceEdit.Clear
            a == null || b == null -> PriceEdit.Bad("Fill in both prices, or empty both to remove the price.")
            else -> PriceEdit.Set(a, b)
        }
    }

    /** A price as the box shows it: "2.5", not "2.50000". */
    fun priceText(v: Double?): String = when {
        v == null -> ""
        v == Math.floor(v) -> v.toLong().toString()
        else -> String.format(Locale.US, "%.4f", v).trimEnd('0').trimEnd('.')
    }
}
