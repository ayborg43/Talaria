package app.talaria

import app.talaria.data.AppJson
import app.talaria.data.RetentionDto
import app.talaria.ui.Retention
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class RetentionTest {
    private val real = """{"available":true,"keep_days":90,"history_keep_days":0,"grace_days":30,"would_archive":2,"would_delete":1,
        "last_at":"2026-11-22T06:00:00+01:00","last_archived":3,"last_deleted":1,"last_pruned":12,"can_prune_history":true}"""

    @Test fun the_servers_answer_is_read_as_it_is_sent() {
        val v = AppJson.decodeFromString<RetentionDto>(real)
        assertTrue(v.available && v.can_prune_history)
        assertEquals(90, v.keep_days); assertEquals(30, v.grace_days); assertEquals(2, v.would_archive); assertEquals(1, v.would_delete)
        assertEquals(3, v.last_archived); assertEquals(12, v.last_pruned)
    }

    @Test fun the_default_and_unavailable_answers_decode() {
        val d = AppJson.decodeFromString<RetentionDto>("""{"available":true,"keep_days":0,"history_keep_days":0,"grace_days":30,"would_archive":0,"would_delete":0,"can_prune_history":false}""")
        assertEquals("", d.last_at); assertFalse(d.can_prune_history); assertEquals(0, d.keep_days)
        assertFalse(AppJson.decodeFromString<RetentionDto>("""{"available":false,"keep_days":0,"history_keep_days":0,"grace_days":30,"would_archive":0,"would_delete":0,"can_prune_history":false}""").available)
    }

    @Test fun the_choices_have_their_labels_and_zero_is_forever() {
        assertEquals(listOf(0, 30, 90, 180, 365), Retention.archiveChoices.map { it.days })
        assertEquals(listOf("Forever", "30 days", "90 days", "6 months", "A year"), Retention.archiveChoices.map { it.label })
        assertEquals(listOf("Forever", "90 days", "6 months", "A year", "Two years"), Retention.historyChoices.map { it.label })
        assertEquals("6 months", Retention.label(Retention.archiveChoices, 180)); assertEquals("Forever", Retention.label(Retention.archiveChoices, 0))
    }

    @Test fun every_offered_choice_is_a_number_the_server_accepts() {
        Retention.archiveChoices.forEach { assertTrue(it.days == 0 || it.days in 7..3650) }
        Retention.historyChoices.forEach { assertTrue(it.days == 0 || it.days in 30..3650) }
    }

    @Test fun a_number_the_list_does_not_have_is_shown_as_days_and_stays_in_the_list() {
        assertEquals("45 days", Retention.label(Retention.archiveChoices, 45)); assertEquals("1 day", Retention.label(Retention.archiveChoices, 1))
        val offered = Retention.offered(Retention.archiveChoices, 45)
        assertEquals(listOf(0, 30, 45, 90, 180, 365), offered.map { it.days }); assertEquals("45 days", offered[2].label)
        assertEquals(Retention.archiveChoices, Retention.offered(Retention.archiveChoices, 90)); assertEquals(Retention.archiveChoices, Retention.offered(Retention.archiveChoices, 0))
    }

    @Test fun the_status_line_says_what_is_due() {
        assertEquals("If it ran now: 2 to archive, 1 to delete.", Retention.dueLine(AppJson.decodeFromString(real)))
        assertEquals("Nothing is due now.", Retention.dueLine(RetentionDto(available = true, keep_days = 90)))
        assertEquals("If it ran now: 0 to archive, 4 to delete.", Retention.dueLine(RetentionDto(available = true, keep_days = 90, would_delete = 4)))
    }

    @Test fun the_last_tidy_line_names_only_what_happened() {
        val v = AppJson.decodeFromString<RetentionDto>(real)
        assertEquals("Last tidy Sun 22 Nov, 06:00: 3 archived, 1 deleted, 12 messages forgotten.", Retention.lastLine(v, "Sun 22 Nov, 06:00"))
        assertEquals("Last tidy: 1 archived.", Retention.lastLine(RetentionDto(last_at = "x", last_archived = 1), ""))
        assertEquals("Last tidy: 1 message forgotten.", Retention.lastLine(RetentionDto(last_at = "x", last_pruned = 1), ""))
        assertEquals("Last tidy: nothing to do.", Retention.lastLine(RetentionDto(last_at = "x"), ""))
        assertNull(Retention.lastLine(RetentionDto(), ""))
    }

    @Test fun the_notes_and_warning_follow_the_settings() {
        assertTrue(Retention.archiveNote(0, 30).startsWith("Nothing is archived"))
        assertTrue(Retention.archiveNote(90, 30).contains("90 days is archived") && Retention.archiveNote(90, 30).contains("30 days later"))
        assertNull(Retention.historyWarning(0))
        assertTrue(Retention.historyWarning(365)!!.contains("older than a year") && Retention.historyWarning(365)!!.contains("will not remember"))
    }

    @Test fun tidy_now_is_offered_only_when_something_is_set_and_the_confirmation_says_what_happens() {
        assertFalse(Retention.canTidy(RetentionDto(available = true)))
        assertFalse(Retention.canTidy(RetentionDto(available = true, history_keep_days = 90, can_prune_history = false)))
        assertTrue(Retention.canTidy(RetentionDto(available = true, keep_days = 30)))
        assertTrue(Retention.canTidy(RetentionDto(available = true, history_keep_days = 90, can_prune_history = true)))
        val c = Retention.confirmText(RetentionDto(available = true, keep_days = 90, would_archive = 1, would_delete = 2, history_keep_days = 730, can_prune_history = true))
        assertTrue(c.contains("1 conversation will be archived.") && c.contains("2 conversations will be deleted for good and cannot be brought back") && c.contains("older than two years"))
        assertFalse(Retention.confirmText(RetentionDto(available = true, keep_days = 90)).contains("searchable"))
        assertTrue(Retention.confirmText(RetentionDto(available = true, keep_days = 90)).startsWith("No conversation is due"))
        assertFalse(Retention.confirmText(RetentionDto(available = true, keep_days = 90, would_archive = 3)).contains("deleted"))
    }
}
