package app.talaria

import app.talaria.assist.AssistRules
import app.talaria.data.AgentPreviewDto
import app.talaria.data.AppJson
import app.talaria.data.DigestDto
import app.talaria.data.DigestSchedule
import app.talaria.data.RunSummary
import app.talaria.data.UsageDay
import app.talaria.data.UsageDto
import app.talaria.data.UsageLine
import app.talaria.data.UsageTotals
import app.talaria.service.ProgressItem
import app.talaria.service.TaskProgress
import app.talaria.ui.AgentFile
import app.talaria.ui.Cost
import app.talaria.ui.Digest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class CostTest {
    private val serverJson = """{"available":true,"summary":"This week: 3 requests, 400 tokens in and 40 out, about ${'$'}0.01.",
        "report":{"since":"2026-11-15","days":7,"total":{"name":"total","totals":{"calls":3,"in":400,"out":40},"cost":0.0123,"priced":true},
        "by_model":[{"name":"fast (m1)","totals":{"calls":2,"in":300,"out":30,"estimated":1},"cost":0.01,"priced":true},{"name":"big","totals":{"calls":1,"in":100,"out":10},"cost":0,"priced":false}],
        "by_agent":[],"by_source":[{"name":"chat","totals":{"calls":3,"in":400,"out":40},"cost":0.0123,"priced":false}],"by_kind":null,
        "per_day":[{"date":"2026-11-20","tokens":0,"cost":0},{"date":"2026-11-21","tokens":440,"cost":0.0123}],"unpriced":["big"]},
        "prices":[{"profile":"big","model":"m2","price_in":null,"price_out":null},{"profile":"fast","model":"m1","price_in":0.5,"price_out":1.5}]}"""

    @Test fun the_servers_usage_answer_is_read_as_it_is_sent() {
        val u = AppJson.decodeFromString<UsageDto>(serverJson)
        assertTrue(u.available)
        assertEquals(3L, u.report.total.totals.calls)
        assertEquals(300L, u.report.by_model[0].totals.tokensIn)
        assertEquals(1L, u.report.by_model[0].totals.estimated)
        assertFalse(u.report.by_model[1].priced)
        assertTrue(u.report.by_kind.isEmpty()) // null from the server
        assertEquals(listOf("big"), u.report.unpriced)
        assertNull(u.prices[0].price_in); assertEquals(0.5, u.prices[1].price_in!!, 0.0)
    }

    @Test fun an_unavailable_ledger_decodes_to_an_empty_report() {
        val u = AppJson.decodeFromString<UsageDto>("""{"available":false}""")
        assertFalse(u.available); assertEquals(0L, u.report.total.totals.calls)
    }

    @Test fun dollars_are_written_as_the_server_writes_them() {
        assertEquals("$0", Cost.money(0.0)); assertEquals("under $0.01", Cost.money(0.004)); assertEquals("$0.01", Cost.money(0.0123))
        assertEquals("$9.99", Cost.money(9.99)); assertEquals("$42", Cost.money(41.6))
    }

    @Test fun tokens_are_short() {
        assertEquals("999", Cost.tokens(999)); assertEquals("1.5k", Cost.tokens(1500)); assertEquals("12k", Cost.tokens(12_000)); assertEquals("1.2M", Cost.tokens(1_200_000))
    }

    @Test fun one_request_is_not_plural() {
        assertEquals("1 request", Cost.requests(1)); assertEquals("0 requests", Cost.requests(0)); assertEquals("2 requests", Cost.requests(2))
        val l = UsageLine("x", UsageTotals(1, 120, 14), 0.0, false)
        assertEquals("1 request · 120 in · 14 out", Cost.usage(l))
    }

    @Test fun a_row_without_a_price_says_so() {
        assertEquals("no price", Cost.price(UsageLine("x", UsageTotals(1, 1, 1), 0.0, false)))
        assertEquals("$0.50", Cost.price(UsageLine("x", UsageTotals(1, 1, 1), 0.5, true)))
        assertEquals("$0.50+ (some with no price)", Cost.price(UsageLine("x", UsageTotals(1, 1, 1), 0.5, false)))
    }

    private fun day(t: Long) = UsageDay("2026-11-0$t", t, 0.0)

    @Test fun bars_are_scaled_to_the_busiest_day() {
        val h = Cost.barHeights(listOf(UsageDay("a", 0, 0.0), UsageDay("b", 50, 0.0), UsageDay("c", 100, 0.0)))
        assertEquals(listOf(0f, 0.5f, 1f), h)
    }

    @Test fun a_quiet_day_is_still_seen_and_an_empty_one_is_not_drawn_tall() {
        val h = Cost.barHeights(listOf(UsageDay("a", 1, 0.0), UsageDay("b", 100_000, 0.0), UsageDay("c", 0, 0.0)))
        assertEquals(0.04f, h[0], 0.0001f); assertEquals(1f, h[1], 0f); assertEquals(0f, h[2], 0f)
    }

    @Test fun no_data_gives_no_bars_and_no_division_by_zero() {
        assertEquals(emptyList<Float>(), Cost.barHeights(emptyList()))
        assertEquals(listOf(0f, 0f), Cost.barHeights(listOf(day(0), day(0))))
    }

    @Test fun dates_read_naturally() {
        assertEquals("3 Nov", Cost.shortDate("2026-11-03")); assertEquals("nonsense", Cost.shortDate("nonsense"))
        assertEquals("21 Nov, 1.5k tokens", Cost.dayWords(UsageDay("2026-11-21", 1500, 0.0)))
    }

    @Test fun both_price_boxes_empty_removes_the_price() = assertEquals(Cost.PriceEdit.Clear, Cost.parsePrice("", "  "))

    @Test fun two_numbers_set_a_price_and_a_comma_or_dollar_sign_is_forgiven() {
        assertEquals(Cost.PriceEdit.Set(2.5, 10.0), Cost.parsePrice("2.5", "10"))
        assertEquals(Cost.PriceEdit.Set(2.5, 0.0), Cost.parsePrice("$2,5", "0"))
    }

    @Test fun one_box_alone_negative_huge_or_not_a_number_is_refused() {
        assertTrue(Cost.parsePrice("2", "") is Cost.PriceEdit.Bad)
        assertTrue(Cost.parsePrice("", "2") is Cost.PriceEdit.Bad)
        assertTrue(Cost.parsePrice("-1", "2") is Cost.PriceEdit.Bad)
        assertTrue(Cost.parsePrice("1", "100001") is Cost.PriceEdit.Bad)
        assertTrue(Cost.parsePrice("abc", "2") is Cost.PriceEdit.Bad)
        assertTrue(Cost.parsePrice("NaN", "2") is Cost.PriceEdit.Bad)
        assertEquals(Cost.PriceEdit.Set(100000.0, 0.0), Cost.parsePrice("100000", "0"))
    }

    @Test fun a_price_shows_without_trailing_zeros() {
        assertEquals("", Cost.priceText(null)); assertEquals("2", Cost.priceText(2.0)); assertEquals("2.5", Cost.priceText(2.5)); assertEquals("0.075", Cost.priceText(0.075))
    }
}

class DigestTest {
    @Test fun the_servers_digest_answer_is_read() {
        val d = AppJson.decodeFromString<DigestDto>("""{"schedule":{"available":true,"enabled":true,"at":"08:00","weekly":true,"next":"2026-11-23T08:00:00Z"},"text":"## Away\n- one thing"}""")
        assertTrue(d.schedule.available && d.schedule.enabled && d.schedule.weekly)
        assertEquals("08:00", d.schedule.at); assertEquals("## Away\n- one thing", d.text)
        assertNull(AppJson.decodeFromString<DigestDto>("""{"schedule":{}}""").text)
    }

    @Test fun the_schedule_is_said_plainly() {
        assertTrue(Digest.scheduleLine(DigestSchedule(true, false), "").startsWith("Off."))
        assertEquals("Arrives every day at 07:30.", Digest.scheduleLine(DigestSchedule(true, true, "07:30", false), ""))
        assertEquals("Arrives every Monday at 08:00. Next: Mon 23 Nov, 08:00.", Digest.scheduleLine(DigestSchedule(true, true, "", true), "Mon 23 Nov, 08:00"))
    }

    @Test fun the_clock_is_two_digits_and_bad_times_are_refused() {
        assertEquals("08:05", Digest.clock(8, 5)); assertNull(Digest.clock(24, 0)); assertNull(Digest.clock(1, 60))
        assertEquals(7, Digest.hourOf("07:30")); assertEquals(30, Digest.minuteOf("07:30")); assertEquals(8, Digest.hourOf("")); assertEquals(0, Digest.minuteOf(""))
    }

    @Test fun an_empty_digest_says_nothing_happened() {
        assertEquals("Nothing happened on its own in that time.", Digest.body("  "))
        assertEquals("Nothing happened on its own in that time.", Digest.body(null))
        assertEquals("x", Digest.body(" x "))
        assertEquals(listOf(24, 168), Digest.Span.entries.map { it.hours })
    }
}

class AgentFileTest {
    private val agent = "---\ntalaria-agent: 1\nname: Scribe\nicon: x\nlooks-only: false\n---\nWrite short notes."

    @Test fun an_agent_file_is_recognised_by_its_header() {
        assertTrue(AgentFile.looksLikeAgentFile(agent))
        assertTrue(AgentFile.looksLikeAgentFile("\uFEFF\n  $agent"))
        assertTrue(AgentFile.looksLikeAgentFile(agent.replace("\n", "\r\n")))
    }

    @Test fun plain_shared_text_still_goes_to_the_chat() {
        assertFalse(AgentFile.looksLikeAgentFile("Check out this link https://example.com"))
        assertFalse(AgentFile.looksLikeAgentFile("--- \nmeeting notes\n---\nnothing here"))
        assertFalse(AgentFile.looksLikeAgentFile("---\ntitle: a blog post\n---\nbody that mentions talaria-agent: 1 later"))
        assertFalse(AgentFile.looksLikeAgentFile("name: Scribe\ntalaria-agent: 1"))
        assertFalse(AgentFile.looksLikeAgentFile(null)); assertFalse(AgentFile.looksLikeAgentFile(""))
    }

    @Test fun a_file_named_agent_md_is_always_for_review_whatever_it_holds() {
        assertTrue(AgentFile.isAgentShare("Scribe.agent.md", "garbage")); assertTrue(AgentFile.isAgentShare("X.AGENT.MD", null))
        assertFalse(AgentFile.isAgentShare("notes.md", "# notes")); assertFalse(AgentFile.isAgentShare("notes.txt", "hello"))
        assertTrue(AgentFile.isAgentShare("notes.txt", agent))
    }

    @Test fun only_text_types_are_read_to_look_for_an_agent_and_pictures_and_pdfs_never_are() {
        assertTrue(AgentFile.worthReading("text/plain", "a.txt")); assertTrue(AgentFile.worthReading("text/markdown", null))
        assertFalse(AgentFile.worthReading("image/png", "a.png")); assertFalse(AgentFile.worthReading("application/pdf", "a.pdf"))
        assertTrue(AgentFile.worthReading("application/octet-stream", "Scribe.agent.md"))
    }

    @Test fun reading_stops_just_past_the_limit() {
        assertEquals(AgentFile.READ_LIMIT + 1, AgentFile.readLimited(java.io.ByteArrayInputStream(ByteArray(AgentFile.READ_LIMIT * 3))).size)
        assertEquals(5, AgentFile.readLimited(java.io.ByteArrayInputStream("hello".toByteArray())).size)
        assertEquals(0, AgentFile.readLimited(java.io.ByteArrayInputStream(ByteArray(0))).size)
    }

    @Test fun a_shared_file_is_named_as_the_server_names_it() {
        assertEquals("Researcher.agent.md", AgentFile.fileName("Researcher"))
        assertEquals("My-helper.agent.md", AgentFile.fileName("My helper"))
        assertEquals("agent.agent.md", AgentFile.fileName("***"))
    }

    @Test fun the_servers_preview_is_read() {
        val p = AppJson.decodeFromString<AgentPreviewDto>("""{"agent":{"name":"Scribe","icon":"x","instructions":"Write short notes.","read_only":true},"exists":true}""")
        assertEquals("Scribe", p.agent.name); assertTrue(p.exists); assertTrue(p.agent.read_only)
    }
}

class ClipAskTest {
    @Test fun short_text_becomes_a_message_to_edit() {
        val r = ClipAsk.read("  hello world \n", true, false) as ClipAsk.Result.Text
        assertEquals("About this text: hello world", r.text); assertFalse(r.cut)
    }

    @Test fun long_text_is_cut_to_about_four_thousand_characters() {
        val r = ClipAsk.read("a".repeat(10_000), true, false) as ClipAsk.Result.Text
        assertTrue(r.cut); assertEquals(ClipAsk.prompt("a".repeat(4000) + "…"), r.text)
        assertEquals(4000, ClipAsk.read("b".repeat(4000), true, false).let { (it as ClipAsk.Result.Text).text.removePrefix("About this text: ").length })
    }

    @Test fun a_cut_never_leaves_half_an_emoji() {
        val text = "a".repeat(3999) + "😀" + "tail"
        val r = ClipAsk.read(text, true, false) as ClipAsk.Result.Text
        assertTrue(r.cut); assertFalse(r.text.any { Character.isHighSurrogate(it) })
    }

    @Test fun a_clip_marked_sensitive_is_never_used_even_if_it_is_text() {
        assertEquals(ClipAsk.Result.Sensitive, ClipAsk.read("hunter2", true, true))
        assertEquals(ClipAsk.Result.Sensitive, ClipAsk.read(null, false, true))
    }

    @Test fun empty_and_non_text_clips_are_told_apart() {
        assertEquals(ClipAsk.Result.Empty, ClipAsk.read(null, true, false)); assertEquals(ClipAsk.Result.Empty, ClipAsk.read("  \n", true, false))
        assertEquals(ClipAsk.Result.NoText, ClipAsk.read("content://x", false, false))
        assertEquals("android.content.extra.IS_SENSITIVE", ClipAsk.SENSITIVE_KEY)
    }

    @Test fun what_is_handed_over_joins_what_was_typed_without_losing_it() {
        assertEquals("About this text: x", Handoff.merge("", "About this text: x"))
        assertEquals("my draft\n\nAbout this text: x", Handoff.merge("my draft  ", "About this text: x"))
    }

    @Test fun the_same_words_are_not_written_twice() {
        assertEquals("What is on my screen?", Handoff.merge("What is on my screen?", "What is on my screen?"))
    }

    @Test fun a_handoff_is_taken_once() {
        Handoff.put("hello", null)
        assertEquals("hello", Handoff.take().first); assertNull(Handoff.take().first)
    }
}

class AssistTest {
    @Test fun a_screenshot_is_waited_for_only_when_android_says_one_is_coming() {
        assertTrue(AssistRules.expectsScreenshot(AssistRules.SHOW_WITH_SCREENSHOT or 1))
        assertFalse(AssistRules.expectsScreenshot(1)); assertFalse(AssistRules.expectsScreenshot(0))
    }

    @Test fun the_screen_question_is_only_filled_in_with_a_screenshot() {
        assertEquals("What is on my screen?", AssistRules.draft(true)); assertNull(AssistRules.draft(false))
    }

    @Test fun listening_needs_the_switch_and_no_screenshot_waiting() {
        assertTrue(AssistRules.listen(true, false)); assertFalse(AssistRules.listen(true, true)); assertFalse(AssistRules.listen(false, false))
    }

    @Test fun the_system_assist_action_opens_the_assistant_screen() {
        assertEquals(QuickAction.Assist, QuickAction.from(android.content.Intent.ACTION_ASSIST))
    }
}

class TaskProgressTest {
    private fun run(id: String, status: String = "running", started: String = "2026-11-21T10:00:00Z", doing: String = "", name: String = "Researcher") =
        RunSummary(id = id, name = name, status = status, started = started, doing = doing)

    @Test fun only_running_tasks_get_a_notice_and_only_the_newest_three() {
        val runs = listOf(
            run("a", started = "2026-11-21T10:00:00Z"), run("b", started = "2026-11-21T10:05:00Z"), run("c", status = "done", started = "2026-11-21T10:06:00Z"),
            run("d", started = "2026-11-21T10:07:00Z"), run("e", started = "2026-11-21T10:08:00Z"),
        )
        assertEquals(listOf("e", "d", "b"), TaskProgress.items(runs).map { it.runId })
        assertEquals(emptyList<ProgressItem>(), TaskProgress.items(listOf(run("x", status = "failed"), run("y", status = "stopped"))))
    }

    @Test fun the_notice_says_the_latest_step_in_words() {
        assertEquals("web fetch https://example.com", TaskProgress.words("web_fetch https://example.com"))
        assertEquals("Working…", TaskProgress.words("")); assertEquals("Working…", TaskProgress.words("   "))
        val long = TaskProgress.words("read_file " + "x".repeat(300))
        assertEquals(110, long.length); assertTrue(long.endsWith("…"))
        assertEquals("Researcher", TaskProgress.items(listOf(run("a", doing = "search_web cats"))).single().title)
        assertEquals("Task", TaskProgress.items(listOf(run("a", name = ""))).single().title)
    }

    @Test fun a_notice_is_looked_at_no_more_than_every_five_seconds() {
        assertTrue(TaskProgress.due(0, 1_000)); assertFalse(TaskProgress.due(1_000, 5_999)); assertTrue(TaskProgress.due(1_000, 6_000))
        assertEquals(5_000L, TaskProgress.MIN_GAP_MS); assertEquals(3, TaskProgress.MAX)
    }

    @Test fun notice_ids_stay_in_their_own_range_and_differ_between_runs() {
        val ids = listOf("run-1", "run-2", "xyz", "").map { TaskProgress.id(it) }
        assertTrue(ids.all { it in 3000..3999 }); assertEquals(TaskProgress.id("run-1"), TaskProgress.id("run-1")); assertNotNull(ids)
        assertTrue(TaskProgress.id("run-1") != TaskProgress.id("run-2"))
    }

    @Test fun notices_of_tasks_that_ended_or_fell_out_of_the_newest_three_are_removed() {
        val items = TaskProgress.items(listOf(run("a"), run("b")))
        val shown = setOf(TaskProgress.id("a"), TaskProgress.id("b"), TaskProgress.id("gone"))
        assertEquals(setOf(TaskProgress.id("gone")), TaskProgress.gone(shown, items))
        assertEquals(shown, TaskProgress.gone(shown, emptyList()))
    }

    @Test fun a_notice_is_only_changed_when_what_it_says_changed() {
        val a = TaskProgress.items(listOf(run("a", doing = "read_file a.txt")))
        val shown = mapOf(TaskProgress.id("a") to a[0].text)
        assertEquals(emptyList<ProgressItem>(), TaskProgress.changed(shown, a))
        val b = TaskProgress.items(listOf(run("a", doing = "read_file b.txt"), run("n", started = "2026-11-21T11:00:00Z")))
        assertEquals(setOf("a", "n"), TaskProgress.changed(shown, b).map { it.runId }.toSet())
    }
}
