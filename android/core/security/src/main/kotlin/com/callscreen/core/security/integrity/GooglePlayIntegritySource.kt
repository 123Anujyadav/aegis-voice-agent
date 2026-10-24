package com.callscreen.core.security.integrity

import android.content.Context
import com.google.android.gms.tasks.Tasks
import com.google.android.play.core.integrity.IntegrityManagerFactory
import com.google.android.play.core.integrity.StandardIntegrityException
import com.google.android.play.core.integrity.StandardIntegrityManager.PrepareIntegrityTokenRequest
import com.google.android.play.core.integrity.StandardIntegrityManager.StandardIntegrityTokenRequest
import com.google.android.play.core.integrity.model.StandardIntegrityErrorCode
import java.util.concurrent.ExecutionException
import java.util.concurrent.TimeUnit

/**
 * [IntegritySource] over Google's Play Integrity standard API.
 *
 * - [cloudProjectNumber] is the Google Cloud project linked to the app in Play
 *   Console. It is an identifier, not a secret.
 * - Calls block (Tasks.await), so they must run off the main thread; identity
 *   requests run on the IO dispatcher.
 * - Device-only and compile-checked: Google's service does not exist on the
 *   JVM, so the unit tests exercise [PlayIntegrityTokens] through a fake
 *   source.
 * - **Never verified against Google here.** That needs the project linked in
 *   Play Console, the app installed from Play, and identity's server-side
 *   verifier configured (internal/playintegrity, unwired).
 */
public class GooglePlayIntegritySource(
    private val context: Context,
    private val cloudProjectNumber: Long,
) : IntegritySource {

    override fun prepare(timeoutMillis: Long): IntegrityProvider {
        val manager = IntegrityManagerFactory.createStandard(context.applicationContext)
        val tokenProvider = Tasks.await(
            manager.prepareIntegrityToken(
                PrepareIntegrityTokenRequest.builder().setCloudProjectNumber(cloudProjectNumber).build(),
            ),
            timeoutMillis,
            TimeUnit.MILLISECONDS,
        )
        return IntegrityProvider { requestHash, requestTimeoutMillis ->
            try {
                Tasks.await(
                    tokenProvider.request(StandardIntegrityTokenRequest.builder().setRequestHash(requestHash).build()),
                    requestTimeoutMillis,
                    TimeUnit.MILLISECONDS,
                ).token()
            } catch (e: ExecutionException) {
                val cause = e.cause
                if (cause is StandardIntegrityException && cause.errorCode == StandardIntegrityErrorCode.INTEGRITY_TOKEN_PROVIDER_INVALID) {
                    throw IntegrityProviderInvalidException()
                }
                throw IllegalStateException("integrity token request failed")
            }
        }
    }
}
