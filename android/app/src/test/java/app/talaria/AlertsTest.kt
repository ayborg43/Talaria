package app.talaria

import app.talaria.data.Message
import app.talaria.service.Alerts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AlertsTest {
    private fun ask(token: String, state: String = "pending", role: String = "approval") = Message(id = token.hashCode().toLong(), role = role, state = state, token = token)

    @Test fun onlyWaitingApprovalsNotYetAnnouncedAreFresh() {
        val all = listOf(ask("a"), ask("b"), ask("c", state = "allowed"), ask("d", role = "assistant"), ask(""))
        assertEquals(listOf("a", "b"), Alerts.fresh(all, emptyList()).map { it.token })
        assertEquals(listOf("b"), Alerts.fresh(all, listOf("a")).map { it.token })
        assertTrue(Alerts.fresh(all, listOf("a", "b")).isEmpty())
    }

    @Test fun announcedListKeepsTheNewestAndNeverDuplicates() {
        var l = emptyList<String>()
        repeat(5) { l = Alerts.remember(l, "t$it", cap = 3) }
        assertEquals(listOf("t2", "t3", "t4"), l)
        assertEquals(l, Alerts.remember(l, "t3", cap = 3))
    }

    @Test fun quietHoursWithinOneDay() {
        assertTrue(Alerts.inQuietHours(13 * 60, 12 * 60, 14 * 60))
        assertFalse(Alerts.inQuietHours(14 * 60, 12 * 60, 14 * 60)) // the end is outside
        assertTrue(Alerts.inQuietHours(12 * 60, 12 * 60, 14 * 60)) // the start is inside
    }

    @Test fun quietHoursAcrossMidnight() {
        val from = 22 * 60; val to = 7 * 60
        assertTrue(Alerts.inQuietHours(23 * 60, from, to))
        assertTrue(Alerts.inQuietHours(0, from, to))
        assertTrue(Alerts.inQuietHours(6 * 60 + 59, from, to))
        assertFalse(Alerts.inQuietHours(7 * 60, from, to))
        assertFalse(Alerts.inQuietHours(12 * 60, from, to))
        assertFalse(Alerts.inQuietHours(21 * 60 + 59, from, to))
    }

    @Test fun equalTimesMeanNoQuietHours() {
        assertFalse(Alerts.inQuietHours(0, 480, 480))
        assertFalse(Alerts.inQuietHours(480, 480, 480))
    }

    @Test fun clockIsTwoDigits() {
        assertEquals("22:00", Alerts.clock(22 * 60))
        assertEquals("07:05", Alerts.clock(7 * 60 + 5))
        assertEquals("00:00", Alerts.clock(0))
    }

    @Test fun approvalTextLosesFencesAndChatBoilerplate() {
        val raw = "Allow shell?\n```\ncommand: echo one\n```\n\nI will carry on with anything else, and do this once you say yes."
        assertEquals("Allow shell?\ncommand: echo one", Alerts.plain(raw))
        assertEquals("Allow shell?", Alerts.plain("Allow shell?"))
    }
}
