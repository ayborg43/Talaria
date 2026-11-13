package app.talaria

import app.talaria.voice.Engine
import app.talaria.voice.ErrorAction
import app.talaria.voice.Engines
import app.talaria.voice.Pick
import app.talaria.voice.Support
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class EngineTest {
    @Test fun thePhoneIsTheDefaultWhenItCanListen() {
        assertEquals(Pick.Use(Engine.Phone), Engines.choose(Engines.PHONE, serverReady = true, phone = Support.OnDevice, phoneWhy = ""))
        assertEquals(Pick.Use(Engine.Phone), Engines.choose(Engines.PHONE, serverReady = false, phone = Support.Online, phoneWhy = ""))
    }

    @Test fun theServerIsUsedWhenAskedForAndReady() {
        assertEquals(Pick.Use(Engine.Server), Engines.choose(Engines.SERVER, serverReady = true, phone = Support.OnDevice, phoneWhy = ""))
    }

    @Test fun theOtherEngineCoversForOneThatCannotWork() {
        assertEquals(Pick.Use(Engine.Server), Engines.choose(Engines.PHONE, serverReady = true, phone = Support.None, phoneWhy = "no"))
        assertEquals(Pick.Use(Engine.Phone), Engines.choose(Engines.SERVER, serverReady = false, phone = Support.OnDevice, phoneWhy = ""))
    }

    @Test fun whenNeitherCanWorkTheMessageSaysWhy() {
        val p = Engines.choose(Engines.PHONE, serverReady = false, phone = Support.None, phoneWhy = "This phone has no speech recognition service.")
        assertTrue(p is Pick.Unavailable && p.message.contains("no speech recognition service") && p.message.contains("server"))
    }

    @Test fun anOfflinePackIsRequiredWhenSpeechMustStayOnThePhone() {
        assertEquals(Support.OnDevice, Engines.support(onDeviceExists = true, onlineExists = true, localOnly = true))
        assertEquals(Support.None, Engines.support(onDeviceExists = false, onlineExists = true, localOnly = true))
        assertEquals(Support.Online, Engines.support(onDeviceExists = false, onlineExists = true, localOnly = false))
        assertEquals(Support.None, Engines.support(onDeviceExists = false, onlineExists = false, localOnly = false))
        assertTrue(Engines.why(false, true, true).contains("turn that setting off"))
        assertEquals("", Engines.why(true, true, true))
    }

    @Test fun recognizerErrorsAreSortedIntoWhatToDo() {
        assertEquals(ErrorAction.ListenAgain, Engines.onError(6)) // nothing said
        assertEquals(ErrorAction.ListenAgain, Engines.onError(7)) // nothing understood
        assertEquals(ErrorAction.RetryOnce, Engines.onError(8))   // busy
        assertEquals(ErrorAction.NeedsLanguagePack, Engines.onError(12))
        assertEquals(ErrorAction.NeedsLanguagePack, Engines.onError(13))
        assertTrue((Engines.onError(9) as ErrorAction.Stop).message.contains("microphone"))
        assertTrue((Engines.onError(2) as ErrorAction.Stop).message.contains("offline"))
        assertTrue((Engines.onError(99) as ErrorAction.Stop).message.contains("99"))
    }
}
