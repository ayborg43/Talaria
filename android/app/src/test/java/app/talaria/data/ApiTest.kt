package app.talaria.data

import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

class ApiTest {
    private lateinit var server: MockWebServer
    private lateinit var api: Api
    private var cookie = "tal_session=abc123"
    private var lost = 0

    private lateinit var origin: String

    @Before fun start() {
        server = MockWebServer().also { it.start() }
        origin = server.url("/").toString().trimEnd('/')
        api = Api(object : Credentials { override val baseUrl get() = origin; override val cookie get() = this@ApiTest.cookie })
        api.onAuthLost = { lost++ }
    }

    @After fun stop() { server.shutdown() }

    @Test fun login_returns_the_session_cookie_and_sends_the_headers_the_server_checks() = runBlocking {
        server.enqueue(MockResponse().setResponseCode(200).addHeader("Set-Cookie", "tal_session=tok123; Path=/; HttpOnly; SameSite=Strict").setBody("""{"ok":true}"""))
        assertEquals("tal_session=tok123", api.login(origin, "pw"))
        val req = server.takeRequest()
        assertEquals("/api/login", req.path)
        assertEquals("1", req.getHeader("X-Talaria"))
        assertEquals(origin, req.getHeader("Origin"))
        assertTrue(req.body.readUtf8().contains("\"password\":\"pw\""))
    }

    @Test fun a_wrong_password_says_so() = runBlocking {
        server.enqueue(MockResponse().setResponseCode(401).setBody("""{"error":"That password is not right."}"""))
        val e = runCatching { api.login(origin, "bad") }.exceptionOrNull() as ApiException
        assertEquals(401, e.code)
        assertEquals("That password is not right.", e.message)
    }

    @Test fun too_many_tries_passes_on_the_servers_words() = runBlocking {
        server.enqueue(MockResponse().setResponseCode(429).setBody("""{"error":"Wait 30 seconds."}"""))
        assertEquals("Wait 30 seconds.", (runCatching { api.login(origin, "x") }.exceptionOrNull() as ApiException).message)
    }

    @Test fun a_refused_origin_explains_the_proxy_case() = runBlocking {
        server.enqueue(MockResponse().setResponseCode(403))
        assertTrue((runCatching { api.login(origin, "x") }.exceptionOrNull() as ApiException).message!!.contains("public address"))
    }

    @Test fun a_response_with_no_cookie_is_an_error() {
        server.enqueue(MockResponse().setResponseCode(200).setBody("{}"))
        assertThrows(ApiException::class.java) { runBlocking { api.login(origin, "pw") } }
    }

    @Test fun requests_carry_the_cookie_and_a_401_ends_the_session() = runBlocking {
        server.enqueue(MockResponse().setBody("""{"ok":1}"""))
        api.get("/api/state")
        assertEquals("tal_session=abc123", server.takeRequest().getHeader("Cookie"))
        server.enqueue(MockResponse().setResponseCode(401).setBody("""{"error":"sign in first"}"""))
        assertThrows(AuthLost::class.java) { runBlocking { api.get("/api/state") } }
        assertEquals(1, lost)
    }

    @Test fun server_errors_carry_the_servers_message() = runBlocking {
        server.enqueue(MockResponse().setResponseCode(410).setBody("""{"error":"That question has expired."}"""))
        val e = runCatching { api.post("/api/approve", "{}") }.exceptionOrNull() as ApiException
        assertEquals(410, e.code); assertEquals("That question has expired.", e.message)
    }

    @Test fun a_server_that_is_not_there_gives_words_not_a_stack_trace() = runBlocking {
        server.shutdown()
        val e = runCatching { api.get("/api/state") }.exceptionOrNull()!!
        assertTrue(e.message!!.contains("reach", ignoreCase = true))
    }

    @Test fun an_upload_is_multipart_with_the_file_field_the_server_reads() = runBlocking {
        server.enqueue(MockResponse().setBody("""{"id":"x"}"""))
        api.upload("a.png", "image/png", byteArrayOf(1, 2, 3))
        val req = server.takeRequest()
        assertTrue(req.getHeader("Content-Type")!!.startsWith("multipart/form-data"))
        val body = req.body.readUtf8()
        assertTrue(body.contains("name=\"file\"; filename=\"a.png\""))
        assertTrue(body.contains("image/png"))
    }

    @Test fun probe_recognises_a_talaria_server_by_its_manifest() = runBlocking {
        server.enqueue(MockResponse().setBody("""{"name":"Talaria"}"""))
        assertTrue(api.probe(origin))
        server.enqueue(MockResponse().setBody("""{"name":"Something else"}"""))
        assertFalse(api.probe(origin))
    }

    @Test fun messages_from_the_stream_replace_by_id() {
        val a = Message(id = 1, role = "tool", state = "running"); val b = Message(id = 2, role = "assistant", text = "hi")
        val list = upsert(upsert(emptyList(), a), b)
        val done = upsert(list, a.copy(state = "ok"))
        assertEquals(listOf("ok", ""), done.map { it.state })
        assertEquals(2, done.size)
    }
}
