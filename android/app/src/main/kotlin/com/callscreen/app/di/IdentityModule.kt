package com.callscreen.app.di

import android.content.Context
import com.callscreen.BuildConfig
import com.callscreen.core.auth.AccessTokenProofs
import com.callscreen.core.auth.EdgeApiClient
import com.callscreen.core.auth.EnrolOutcome
import com.callscreen.core.auth.IdentityAuthenticator
import com.callscreen.core.datastore.session.DataStoreBlobStore
import com.callscreen.core.datastore.session.EncryptedSessionStore
import com.callscreen.core.datastore.session.KeystoreSessionKey
import com.callscreen.core.network.identity.DeviceProofs
import com.callscreen.core.network.identity.IdentityClient
import com.callscreen.core.network.identity.IdentityError
import com.callscreen.core.network.identity.IdentityResult
import com.callscreen.core.network.identity.IntegrityTokens
import com.callscreen.core.network.identity.OtpChallenge
import com.callscreen.core.security.dpop.DpopProofFactory
import com.callscreen.core.security.dpop.KeystoreProofKey
import com.callscreen.core.security.integrity.GooglePlayIntegritySource
import com.callscreen.core.security.integrity.PlayIntegrityTokens
import com.callscreen.feature.onboarding.PhoneVerificationService
import dagger.Module
import dagger.Provides
import dagger.hilt.InstallIn
import dagger.hilt.android.qualifiers.ApplicationContext
import dagger.hilt.components.SingletonComponent
import javax.inject.Singleton

/**
 * The app's one identity flow. There is exactly one [IdentityAuthenticator]
 * per process, so its single-flight refresh lock covers every caller.
 * [authenticator] is null when BuildConfig.IDENTITY_BASE_URL is unset or not
 * an https origin: sign-in is then unavailable, and no host is assumed.
 */
public class IdentityGraph(public val authenticator: IdentityAuthenticator?)

/**
 * edge-api's protected routes. [client] is null when sign-in is unavailable
 * or BuildConfig.EDGE_API_BASE_URL is unset or not an https origin: protected
 * requests are then unavailable, and no host is assumed.
 */
public class EdgeGraph(public val client: EdgeApiClient?)

/**
 * The device key, shared by identity's and edge-api's proofs, which must both
 * come from the key the session is bound to. It is created in Android
 * Keystore on first use, inside a client call on the IO dispatcher, never on
 * the main thread.
 */
internal class DeviceKey {
    val proofs: DpopProofFactory by lazy { DpopProofFactory(KeystoreProofKey.loadOrCreate()) }
}

@Module
@InstallIn(SingletonComponent::class)
internal object IdentityModule {

    @Provides
    @Singleton
    fun deviceKey(): DeviceKey = DeviceKey()

    @Provides
    @Singleton
    fun identityGraph(@ApplicationContext context: Context, key: DeviceKey): IdentityGraph {
        val origin = EndpointConfig.httpsOrigin(BuildConfig.IDENTITY_BASE_URL) ?: return IdentityGraph(null)
        val http = EndpointConfig.httpClient(origin, BuildConfig.IDENTITY_CERT_PINS) ?: return IdentityGraph(null)
        val client = IdentityClient(
            origin,
            DeviceProofs { method, htu -> key.proofs.proof(method, htu) },
            integrityTokens(context),
            http,
        )
        val store = EncryptedSessionStore(KeystoreSessionKey(), DataStoreBlobStore(context))
        return IdentityGraph(IdentityAuthenticator(client, store))
    }

    @Provides
    @Singleton
    fun edgeGraph(identity: IdentityGraph, key: DeviceKey): EdgeGraph {
        val auth = identity.authenticator ?: return EdgeGraph(null)
        val origin = EndpointConfig.httpsOrigin(BuildConfig.EDGE_API_BASE_URL) ?: return EdgeGraph(null)
        val http = EndpointConfig.httpClient(origin, BuildConfig.EDGE_API_CERT_PINS) ?: return EdgeGraph(null)
        return EdgeGraph(EdgeApiClient(origin, auth, AccessTokenProofs { method, htu, token -> key.proofs.proof(method, htu, token) }, http))
    }

    @Provides
    fun phoneVerification(graph: IdentityGraph): PhoneVerificationService {
        val auth = graph.authenticator ?: return NotConfigured
        return object : PhoneVerificationService {
            override val isConfigured: Boolean = true
            override suspend fun requestCode(msisdn: String): IdentityResult<OtpChallenge> = auth.requestCode(msisdn)
            override suspend fun enrol(requestId: String, code: String): EnrolOutcome = auth.enrol(requestId, code)
        }
    }

    /**
     * Play Integrity tokens bound to each proof (ADR-0031), when
     * BuildConfig.PLAY_INTEGRITY_CLOUD_PROJECT_NUMBER is configured. Otherwise
     * none: the request carries no Integrity-Token, and identity records the
     * session as unverified. A missing or failed token is never treated as a
     * verified device.
     */
    private fun integrityTokens(context: Context): IntegrityTokens =
        EndpointConfig.integrityTokens(BuildConfig.PLAY_INTEGRITY_CLOUD_PROJECT_NUMBER) { project ->
            val tokens = PlayIntegrityTokens(GooglePlayIntegritySource(context, project))
            tokens::tokenFor
        }

    /** Sign-in in a build with no identity server: nothing is ever sent. */
    private object NotConfigured : PhoneVerificationService {
        override val isConfigured: Boolean = false
        override suspend fun requestCode(msisdn: String): IdentityResult<OtpChallenge> =
            IdentityResult.Failure(IdentityError.TemporarilyUnavailable)
        override suspend fun enrol(requestId: String, code: String): EnrolOutcome =
            EnrolOutcome.Failed(IdentityError.TemporarilyUnavailable)
    }
}
