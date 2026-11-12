package app.talaria

import app.talaria.data.ChatMeta
import app.talaria.data.FileMeta
import app.talaria.ui.Artifact
import app.talaria.ui.MakeKind
import app.talaria.ui.arrangeChats
import app.talaria.ui.expiresIn
import app.talaria.ui.fileSize
import app.talaria.ui.kindOf
import app.talaria.ui.madeFiles
import app.talaria.ui.searchIn
import app.talaria.ui.senderName
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant

class MakeTest {
    private val now = Instant.parse("2026-11-12T12:00:00Z")

    @Test fun expiry_reads_in_the_unit_that_fits() {
        assertEquals("expires in 30m", expiresIn("2026-11-12T11:30:00Z", 1, now))
        assertEquals("expires in 11h", expiresIn("2026-11-12T11:00:00Z", 12, now))
        assertEquals("expires in 5d", expiresIn("2026-11-10T12:00:00Z", 168, now))
        assertEquals("expiring", expiresIn("2026-11-01T12:00:00Z", 12, now))
        assertEquals("", expiresIn("not a date", 12, now))
    }

    @Test fun only_made_files_of_a_known_kind_are_listed_newest_first() {
        val files = listOf(
            FileMeta(id = "1", origin = "upload", mime = "image/png", created = "2026-11-12T10:00:00Z"),
            FileMeta(id = "2", origin = "generated", kind = "doc", created = "2026-11-12T08:00:00Z"),
            FileMeta(id = "3", origin = "generated", mime = "application/pdf", created = "2026-11-12T09:00:00Z"),
            FileMeta(id = "4", origin = "generated", kind = "mystery", created = "2026-11-12T11:00:00Z"),
        )
        assertEquals(listOf("3", "2"), madeFiles(files).map { it.id })
        assertEquals("pdf", kindOf(files[2])); assertEquals("doc", kindOf(files[1])); assertEquals("", kindOf(files[0]))
    }

    @Test fun kinds_know_what_to_ask_for() {
        assertEquals("Make a slide deck about ", MakeKind.Slides.ask)
        assertTrue(MakeKind.Pdf !in MakeKind.startable)
        assertNull(MakeKind.of("nope"))
    }

    @Test fun conversations_that_began_with_an_upload_are_named_for_the_file() {
        assertEquals("Sent photo.png", app.talaria.ui.chatTitle("Uploaded photo.png — /api/files/abc123"))
        assertEquals("Taxes", app.talaria.ui.chatTitle("Taxes"))
        assertEquals("New chat", app.talaria.ui.chatTitle(""))
    }

    @Test fun sizes() { assertEquals("512 B", fileSize(512)); assertEquals("2 KB", fileSize(2048)); assertEquals("1.5 MB", fileSize(1572864)) }

    @Test fun pinned_conversations_come_first_and_search_filters_titles() {
        val chats = listOf(ChatMeta(id = "a", title = "Taxes"), ChatMeta(id = "b", title = "Holiday plans"), ChatMeta(id = "c", title = "Taxes again", agent = "Inbox"))
        assertEquals(listOf("c", "a", "b"), arrangeChats(chats, setOf("c"), "").map { it.id })
        assertEquals(listOf("c", "a"), arrangeChats(chats, setOf("c"), "tax").map { it.id })
        assertEquals(listOf("c"), arrangeChats(chats, emptySet(), "inbox").map { it.id })
    }

    @Test fun message_search_needs_every_word() {
        val msgs = listOf("Send the report to Amina", "Amina called", "report ready")
        assertEquals(listOf("Send the report to Amina"), searchIn(msgs, { it }, "amina REPORT"))
        assertEquals(msgs, searchIn(msgs, { it }, "  "))
    }

    @Test fun artifacts_are_read_leniently_and_unknown_kinds_are_ignored() {
        val plan = Artifact.of(Json.parseToJsonElement("""{"kind":"plan","title":"Move house","status":"open","steps":[{"done":true,"text":"Pack"},{"done":false,"text":"Book van"}]}"""))!!
        assertEquals("Move house", plan.str("title"))
        assertEquals(listOf(true, false), plan.list("steps").map { it.bool("done") })
        assertEquals("", plan.str("missing"))
        assertNull(Artifact.of(Json.parseToJsonElement("""{"kind":"hologram"}""")))
        assertNull(Artifact.of(Json.parseToJsonElement("[1,2]")))
        assertNull(Artifact.of(null))
    }

    @Test fun spending_numbers_may_arrive_as_text() {
        val a = Artifact.of(Json.parseToJsonElement("""{"kind":"spending","spending":"120.50","income":300,"count":"12"}"""))!!
        assertEquals(120.5, a.num("spending"), 0.001); assertEquals(300.0, a.num("income"), 0.001); assertEquals(12.0, a.num("count"), 0.001)
    }

    @Test fun sender_names_lose_the_address() {
        assertEquals("Amina Bello", senderName("Amina Bello <amina@x.com>"))
        assertEquals("amina@x.com", senderName("amina@x.com"))
        assertEquals("(unknown)", senderName(""))
    }
}
