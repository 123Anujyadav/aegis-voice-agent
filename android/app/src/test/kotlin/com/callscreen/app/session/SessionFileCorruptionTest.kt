package com.callscreen.app.session

import android.app.Application
import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.core.auth.EnrolOutcome
import com.callscreen.core.auth.IdentityAuthenticator
import com.callscreen.core.auth.SessionEndCause
import com.callscreen.core.auth.SessionState
import com.callscreen.core.datastore.session.DataStoreBlobStore
import com.callscreen.core.datastore.session.EncryptedSessionStore
import com.callscreen.core.datastore.session.SessionKey
import com.callscreen.core.datastore.session.StoredSessionResult
import com.callscreen.core.network.identity.DeviceProofs
import com.callscreen.core.network.identity.IdentityClient
import com.callscreen.core.network.identity.IdentityRoutes
import java.io.File
import java.net.InetAddress
import java.util.concurrent.TimeUnit
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

/**
 * A corrupt session FILE (OD-13, production-readiness F3) through the app's
 * real session storage: the shipped `preferencesDataStore` delegate and
 * [DataStoreBlobStore] on Robolectric's file system, under the real
 * [EncryptedSessionStore] and [IdentityAuthenticator], against a local fake
 * of identity. Only the sealing key is in memory (Android Keystore does not
 * exist on the JVM).
 *
 * The delegate keeps one DataStore per process, which here means one per
 * Robolectric sandbox, bound to the first file it opens. So each test runs
 * at its own SDK level, which gives it its own sandbox and a delegate that
 * has not opened anything yet; and each checks the file on disk, so a store
 * bound to another file could not pass it. No other :app unit test uses
 * DataStoreBlobStore.
 */
@RunWith(RobolectricTestRunner::class)
@Config(application = Application::class)
class SessionFileCorruptionTest {

    private class MemoryKey : SessionKey {
        var key: SecretKey? = null
        override fun getOrCreate(): SecretKey = key ?: KeyGenerator.getInstance("AES").apply { init(256) }.generateKey().also { key = it }
        override fun existing(): SecretKey? = key
        override fun delete() { key = null }
    }

    private lateinit var server: MockWebServer
    private val key = MemoryKey()

    private val context: Application get() = RuntimeEnvironment.getApplication()

    /** Where the delegate keeps the session: filesDir/datastore/<name>.preferences_pb. */
    private val file: File get() = File(context.filesDir, "datastore/callscreen_identity_session.preferences_pb")

    /** Not valid Preferences data: every field tag is invalid. */
    private val garbage = ByteArray(64) { 0xFF.toByte() }

    @Before
    fun start() {
        server = MockWebServer().apply { start(InetAddress.getByName("127.0.0.1"), 0) }
    }

    @After
    fun stop() {
        server.shutdown()
    }

    private fun writeCorruptFile() {
        assertTrue(file.parentFile!!.isDirectory || file.parentFile!!.mkdirs())
        file.writeBytes(garbage)
    }

    /** A fresh authenticator over this phone's real session store: what a new process sees at launch. */
    private fun authenticator(): IdentityAuthenticator {
        val http = IdentityClient.defaultHttpClient().newBuilder().readTimeout(2, TimeUnit.SECONDS).build()
        val client = IdentityClient(server.url("/").toString(), DeviceProofs { _, _ -> "proof" }, httpClient = http)
        return IdentityAuthenticator(client, EncryptedSessionStore(key, DataStoreBlobStore(context)), io = Dispatchers.IO)
    }

    private fun afterSplash(auth: IdentityAuthenticator): String =
        runBlocking { SessionRouting.afterSplash(SessionSignals.of(Dispatchers.Unconfined) { auth }) }

    @Test
    @Config(sdk = [33])
    fun b1ACorruptSessionFileReadsAsAbsentAndIsReplaced() {
        writeCorruptFile()
        val sealingKey = key.getOrCreate()

        val read = runBlocking { EncryptedSessionStore(key, DataStoreBlobStore(context)).read() }

        assertEquals(StoredSessionResult.Absent, read)
        // The delegate read THIS file, and replaced it with a valid, empty one.
        assertTrue(file.isFile)
        assertFalse("the corrupt bytes must be replaced", file.readBytes().contentEquals(garbage))
        // No key is touched: the sealing key stays, unlike an unopenable record's.
        assertSame(sealingKey, key.existing())
    }

    @Test
    @Config(sdk = [34])
    fun b2AfterACorruptSessionFileThePhoneIsSignedOutAndEnrolsAgain() {
        writeCorruptFile()
        val auth = authenticator()

        // Launch: no crash, onboarding, signed out as "not signed in" (no notice), nothing sent.
        assertEquals(AegisDestinations.WELCOME, afterSplash(auth))
        assertEquals(SessionState.SignedOut(SessionEndCause.NOT_SIGNED_IN), auth.sessionState.value)
        assertEquals(0, server.requestCount)
        assertFalse("the corrupt bytes must be replaced", file.readBytes().contentEquals(garbage))

        // Signing in again stores a session.
        server.enqueue(
            MockResponse().setResponseCode(200).setHeader("Content-Type", "application/json").setBody(
                """{"accessToken":"access-1","tokenType":"DPoP","expiresIn":900,"refreshToken":"${"1".repeat(64)}","sessionId":"sess_${"b".repeat(64)}"}""",
            ),
        )
        assertEquals(EnrolOutcome.Enrolled, runBlocking { auth.enrol("req_" + "a".repeat(64), "123456") })
        assertEquals(SessionState.SignedIn, auth.sessionState.value)
        assertTrue(runBlocking { auth.isSignedIn() })

        // Exactly one enrolment reached identity, and nothing else.
        assertEquals(1, server.requestCount)
        assertEquals(IdentityRoutes.ENROLMENT, server.takeRequest().path)

        // Stored in the session file, and found by the next launch.
        assertTrue(file.length() > 0)
        assertEquals(AegisDestinations.CALLS_FEED, afterSplash(authenticator()))
        assertEquals(1, server.requestCount)
    }

    @Test
    @Config(sdk = [35])
    fun b3AMissingSessionFileIsTheNormalAbsentSession() {
        assertTrue(!file.exists() || file.delete())
        val auth = authenticator()

        assertEquals(StoredSessionResult.Absent, runBlocking { EncryptedSessionStore(key, DataStoreBlobStore(context)).read() })
        assertEquals(AegisDestinations.WELCOME, afterSplash(auth))
        assertEquals(SessionState.SignedOut(SessionEndCause.NOT_SIGNED_IN), auth.sessionState.value)
        assertEquals(0, server.requestCount)
        assertFalse("reading a missing file must not create one", file.exists())
    }
}
