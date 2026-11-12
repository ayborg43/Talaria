package app.talaria

import app.talaria.widget.Look
import app.talaria.widget.WidgetState
import app.talaria.widget.widgetWords
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class QuickTest {
    @Test fun every_quick_action_is_found_by_its_intent_action_and_others_are_not() {
        QuickAction.entries.forEach { assertEquals(it, QuickAction.from(it.action)) }
        assertNull(QuickAction.from("android.intent.action.SEND"))
        assertNull(QuickAction.from(null))
    }

    @Test fun the_actions_are_namespaced_so_no_other_app_can_trigger_them_by_accident() {
        assertEquals(QuickAction.entries.size, QuickAction.entries.map { it.action }.toSet().size)
        QuickAction.entries.forEach { assert(it.action.startsWith("app.talaria.action.")) }
    }

    @Test fun signed_out_says_so() = assertEquals("Signed out", widgetWords(WidgetState(false, 3, 2)).chip)

    @Test fun waiting_approvals_come_before_running_tasks() {
        val w = widgetWords(WidgetState(true, pending = 2, tasks = 3))
        assertEquals("2 waiting", w.chip); assertEquals(Look.Asking, w.look)
        assertEquals("One thing needs your answer. Tap to see it.", widgetWords(WidgetState(true, 1, 0)).detail)
    }

    @Test fun running_tasks_are_counted() {
        assertEquals("1 running", widgetWords(WidgetState(true, 0, 1)).chip)
        assertEquals("Talaria is working on 4 tasks in the background.", widgetWords(WidgetState(true, 0, 4)).detail)
        assertEquals(Look.Working, widgetWords(WidgetState(true, 0, 1)).look)
    }

    @Test fun nothing_going_on_is_quiet() {
        val w = widgetWords(WidgetState(true, 0, 0))
        assertEquals("All quiet", w.chip); assertEquals(Look.Quiet, w.look)
    }
}
