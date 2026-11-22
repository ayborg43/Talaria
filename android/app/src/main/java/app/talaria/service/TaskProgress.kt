package app.talaria.service

import app.talaria.data.RunSummary
import app.talaria.ui.humanTool

/** One running task as its notification shows it. */
data class ProgressItem(val runId: String, val title: String, val text: String)

/**
 * The rules behind the "what is it doing now" notices for running tasks, free of Android so they can be tested: which
 * runs get one (the newest few), what they say, and how seldom they change.
 */
object TaskProgress {
    /** At most this many tasks have a notice at once. */
    const val MAX = 3
    /** A notice is looked at and changed at most this often. */
    const val MIN_GAP_MS = 5_000L

    /** The newest [max] runs that are still going, newest first. */
    fun items(runs: List<RunSummary>, max: Int = MAX): List<ProgressItem> =
        runs.filter { it.status == "running" && it.id.isNotBlank() }
            .sortedByDescending { it.started }
            .take(max)
            .map { ProgressItem(it.id, it.name.ifBlank { "Task" }.take(60), words(it.doing)) }

    /** The latest step as a line, such as "web fetch https://example.com", or "Working…" before there is one. */
    fun words(doing: String): String {
        val t = humanTool(doing).replace(Regex("\\s+"), " ").trim()
        return if (t.isEmpty()) "Working…" else if (t.length > 110) t.take(109).trimEnd() + "…" else t
    }

    /** Whether enough time has passed since [lastAt] to look again. */
    fun due(lastAt: Long, now: Long, gap: Long = MIN_GAP_MS) = lastAt == 0L || now - lastAt >= gap

    /** A notification id for a run, in a range of its own (3000 to 3999). */
    fun id(runId: String): Int = 3000 + (runId.hashCode() and 0x3FF) % 1000

    /** The ids that were shown and are not wanted now: tasks that ended, or fell out of the newest few. */
    fun gone(shown: Set<Int>, items: List<ProgressItem>): Set<Int> = shown - items.map { id(it.runId) }.toSet()

    /** The items whose notice is new or says something different from what is on screen ([shown]: id to the text shown). */
    fun changed(shown: Map<Int, String>, items: List<ProgressItem>): List<ProgressItem> = items.filter { shown[id(it.runId)] != it.text }
}
