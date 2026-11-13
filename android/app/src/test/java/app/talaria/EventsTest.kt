package app.talaria

import app.talaria.phone.Op
import app.talaria.phone.PhoneTriggers
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class EventsTest {
    @Test fun eventsAreDescribedInPlainWords() {
        assertEquals("Battery at 14%", PhoneTriggers.words("battery_low", buildJsonObject { put("level", 14) }))
        assertEquals("Charger plugged in", PhoneTriggers.words("charging_start", buildJsonObject {}))
        assertEquals("Charger unplugged", PhoneTriggers.words("charging_stop", buildJsonObject {}))
        assertEquals("Joined Wi-Fi Home", PhoneTriggers.words("wifi", buildJsonObject { put("name", "Home") }))
        assertEquals("Connected to a Bluetooth device", PhoneTriggers.words("bluetooth", buildJsonObject { put("name", "") }))
    }

    @Test fun theEventSwitchIsPartOfWhatCanBeSharedAndTheCameraIsAQuickAction() {
        assertTrue(Op.TRIGGERS in Op.all)
        assertEquals(QuickAction.Photo, QuickAction.from("app.talaria.action.PHOTO"))
        assertTrue(QuickAction.entries.all { it.action.startsWith("app.talaria.action.") })
    }
}
