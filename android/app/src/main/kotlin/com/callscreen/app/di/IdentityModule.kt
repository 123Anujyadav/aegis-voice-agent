package com.callscreen.app.di

import android.content.Context
import com.callscreen.BuildConfig
import com.callscreen.core.auth.EnrolOutcome
import com.callscreen.core.auth.IdentityAuthenticator
import com.callscreen.core.datastore.session.DataStoreBlobStore
import com.callscreen.core.datastore.session.EncryptedSessionStore
import com.callscreen.core.datastore.session.KeystoreSessionKey
import com.callscreen.core.network.identity.DeviceProofs
import com.callscreen.core.network.identity.IdentityClient
import com.callscreen.core.network.identity.IdentityError
import com.callscreen.core.network.identity.IdentityResult
import com.callscreen.core.network.identity.OtpChallenge
import com.callscreen.core.security.dpop.DpopProofFactory
import com.callscreen.core.security.dpop.KeystoreProofKey
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
 * [authenticator] is null when the build has no identity base URL
 * (BuildConfig.IDENTITY_BASE_URL), or one that is not an https origin: sign-in
 * is then unavailable, and no host is assumed.
 */
public class IdentityGraph(public val authenticator: IdentityAuthenticator?)

@Module
@InstallIn(SingletonComponent::class)
internal object IdentityModule {

    @Provides
    @Singleton
    fun identityGraph(@ApplicationContext context: Context): IdentityGraph {
        val base = BuildConfig.IDENTITY_BASE_URL
        if (base.isEmpty()) return IdentityGraph(null)
        // The Keystore device key is created on first use, when the client
        // signs a proof on its IO dispatcher, not here on the main thread.
        val proofs by lazy { DpopProofFactory(KeystoreProofKey.loadOrCreate()) }
        val client = try {
            IdentityClient(base, DeviceProofs { method, htu -> proofs.proof(method, htu) })
        } catch (e: IllegalArgumentException) {
            return IdentityGraph(null)
        }
        val store = EncryptedSessionStore(KeystoreSessionKey(), DataStoreBlobStore(context))
        return IdentityGraph(IdentityAuthenticator(client, store))
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

    /** Sign-in in a build with no identity server: nothing is ever sent. */
    private object NotConfigured : PhoneVerificationService {
        override val isConfigured: Boolean = false
        override suspend fun requestCode(msisdn: String): IdentityResult<OtpChallenge> =
            IdentityResult.Failure(IdentityError.TemporarilyUnavailable)
        override suspend fun enrol(requestId: String, code: String): EnrolOutcome =
            EnrolOutcome.Failed(IdentityError.TemporarilyUnavailable)
    }
}
