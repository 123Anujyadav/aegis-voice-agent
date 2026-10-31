package com.callscreen.app.session

import android.app.Application
import android.os.Looper
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.navigation.NavHostController
import androidx.navigation.compose.rememberNavController
import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.app.navigation.AegisNavGraph
import com.callscreen.core.auth.EnrolOutcome
import com.callscreen.core.auth.IdentityAuthenticator
import com.callscreen.core.auth.SessionEndCause
import com.callscreen.core.auth.SessionState
import com.callscreen.core.datastore.session.BlobStore
import com.callscreen.core.datastore.session.EncryptedSessionStore
import com.callscreen.core.datastore.session.SessionKey
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.network.identity.DeviceProofs
import com.callscreen.core.network.identity.IdentityClient
import java.io.IOException
import java.net.InetAddress
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

/**
 * The graph over the app's real wiring: [SessionSignals.of] over a real
 * [IdentityAuthenticator], its real IdentityClient against a local fake of
 * identity, and the real EncryptedSessionStore over an in-memory key and byte
 * store. Nothing here reaches a real server, Android Keystore or DataStore.
 */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34], application = Application::class)
class SessionWiringTest {

    @get:Rule
    val compose = createComposeRule()

    private class MemoryBlobs : BlobStore {
        var bytes: ByteArray? = null
        var unreadable = false
        override suspend fun read(): ByteArray? {
            // What DataStore throws for a corrupt or unreadable file (CorruptionException is an IOException).
            if (unreadable) throw IOException("corrupt session file")
            return bytes
        }
        override suspend fun write(bytes: ByteArray) { this.bytes = bytes.copyOf() }
        override suspend fun clear() { bytes = null }
    }

    private class MemoryKey : SessionKey {
        var key: SecretKey? = null
        override fun getOrCreate(): SecretKey = key ?: KeyGenerator.getInstance("AES").apply { init(256) }.generateKey().also { key = it }
        override fun existing(): SecretKey? = key
        override fun delete() { key = null }
    }

    private lateinit var server: MockWebServer
    private val blobs = MemoryBlobs()
    private val key = MemoryKey()

    @Before
    fun start() {
        server = MockWebServer().apply { start(InetAddress.getByName("127.0.0.1"), 0) }
    }

    @After
    fun stop() = server.shutdown()

    /** A fresh authenticator over the same phone's store: what a new process sees at launch. */
    private fun authenticator(): IdentityAuthenticator {
        val http = IdentityClient.defaultHttpClient().newBuilder().readTimeout(2, TimeUnit.SECONDS).build()
        val client = IdentityClient(server.url("/").toString(), DeviceProofs { _, _ -> "proof" }, httpClient = http)
        return IdentityAuthenticator(client, EncryptedSessionStore(key, blobs), io = Dispatchers.IO)
    }

    private fun enrol() = runBlocking {
        server.enqueue(
            MockResponse().setResponseCode(200).setHeader("Content-Type", "application/json").setBody(
                """{"accessToken":"access-1","tokenType":"DPoP","expiresIn":900,"refreshToken":"${"1".repeat(64)}","sessionId":"sess_${"b".repeat(64)}"}""",
            ),
        )
        assertEquals(EnrolOutcome.Enrolled, authenticator().enrol("req_" + "a".repeat(64), "123456"))
    }

    private fun graph(start: String, session: SessionSignals): NavHostController {
        lateinit var nav: NavHostController
        compose.setContent {
            nav = rememberNavController()
            CallScreenTheme {
                AegisNavGraph(navController = nav, startDestination = start, includeDesignPreviews = false, session = session)
            }
        }
        compose.waitForIdle()
        return nav
    }

    private fun NavHostController.routes(): List<String> =
        currentBackStack.value.mapNotNull { entry -> entry.destination.route?.takeIf { it != graph.route } }

    /**
     * Past the splash. Its store read runs on the IO dispatcher and comes
     * back through the main looper, which the wait has to run.
     */
    private fun NavHostController.afterSplashIs(route: String) {
        compose.mainClock.advanceTimeBy(2_500)
        compose.waitUntil(timeoutMillis = 5_000) {
            shadowOf(Looper.getMainLooper()).idle()
            routes() == listOf(route)
        }
    }

    // Unconfined: the notice's collector is attached by the time the graph is idle.
    private fun signals(auth: IdentityAuthenticator?, io: CoroutineDispatcher = Dispatchers.Unconfined) =
        SessionSignals.of(io) { auth }

    @Test
    fun anEnrolledPhoneStartsAtCallsWithoutSendingAnything() {
        enrol()
        val auth = authenticator()
        val nav = graph(AegisDestinations.SPLASH, signals(auth))
        nav.afterSplashIs(AegisDestinations.CALLS_FEED)
        assertEquals(SessionState.SignedIn, auth.sessionState.value)
        assertEquals("the launch sent a request", 1, server.requestCount)
    }

    @Test
    fun aPhoneWithoutASessionStartsAtWelcome() {
        val auth = authenticator()
        val nav = graph(AegisDestinations.SPLASH, signals(auth))
        nav.afterSplashIs(AegisDestinations.WELCOME)
        assertEquals(SessionState.SignedOut(SessionEndCause.NOT_SIGNED_IN), auth.sessionState.value)
        assertEquals(0, server.requestCount)
    }

    @Test
    fun aCorruptSessionFileStartsAtWelcomeWithoutCrashingOrClaimingASignOut() {
        enrol()
        blobs.unreadable = true
        val auth = authenticator()
        val nav = graph(AegisDestinations.SPLASH, signals(auth))
        nav.afterSplashIs(AegisDestinations.WELCOME)
        // The state is left as it was: no notice, and nothing says the server ended the session.
        assertEquals(SessionState.Unknown, auth.sessionState.value)
        compose.onNodeWithText(SignedOutCopy.TITLE).assertDoesNotExist()
        assertEquals("the launch sent a request", 1, server.requestCount)
    }

    @Test
    fun signingOutOnThisPhoneShowsTheNotice() {
        enrol()
        val auth = authenticator()
        runBlocking { auth.isSignedIn() }
        val nav = graph(AegisDestinations.CALLS_FEED, signals(auth))
        runBlocking { auth.signOut() }
        compose.waitUntil(timeoutMillis = 5_000) { nav.routes() == listOf(AegisDestinations.SIGNED_OUT) }
        compose.onNodeWithText(SignedOutCopy.TITLE).assertExists()
        compose.onNodeWithText(SignedOutCopy.YOU_SIGNED_OUT).assertExists()
    }

    @Test
    fun noIdentityServerBehavesAsNoSessionHandling() {
        val nav = graph(AegisDestinations.SPLASH, signals(null))
        nav.afterSplashIs(AegisDestinations.WELCOME)
    }

    @Test
    fun theIdentityFlowIsBuiltOnceOffTheMainThreadAndOnlyWhenFirstUsed() {
        val built = AtomicInteger()
        var onMain: Boolean? = null
        val session = SessionSignals.of(Dispatchers.IO) {
            built.incrementAndGet()
            onMain = Looper.myLooper() == Looper.getMainLooper()
            authenticator()
        }
        assertEquals("built before first use", 0, built.get())
        runBlocking {
            session.state()
            session.hasStoredSession()
        }
        assertEquals(1, built.get())
        assertFalse("built on the main thread", onMain!!)
    }
}
