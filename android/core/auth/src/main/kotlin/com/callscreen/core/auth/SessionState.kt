package com.callscreen.core.auth

/**
 * Whether this phone holds a session, as [IdentityAuthenticator] last saw its
 * store. It changes only when the authenticator reads, writes or clears the
 * stored session, and nothing is sent to observe it.
 *
 * It routes the app (cold start, the signed-out notice). It never authorises
 * anything: [SignedIn] means a session is stored and can be opened, not that
 * the server will still accept it.
 */
public sealed interface SessionState {
    /** The store has not been looked at yet in this process. */
    public data object Unknown : SessionState

    /** A session is stored on this phone and can be opened. */
    public data object SignedIn : SessionState

    /** No session is stored, and [cause] says why. */
    public data class SignedOut(val cause: SessionEndCause) : SessionState
}

/** Why no session is stored. */
public enum class SessionEndCause {
    /** Nothing was ever stored on this phone. */
    NOT_SIGNED_IN,

    /** The server ended the session (expired, revoked, or the device key no longer matches). */
    SESSION_ENDED,

    /** The stored session could not be opened safely and was cleared. */
    STORED_SESSION_UNRECOVERABLE,

    /** New tokens could not be stored, so the session was cleared. */
    STORAGE_FAILED,

    /** [IdentityAuthenticator.signOut] removed the session from this phone. */
    SIGNED_OUT_ON_THIS_DEVICE,

    /** The server confirmed the account's deletion. */
    ACCOUNT_DELETED,
    ;

    internal companion object {
        fun of(reason: SignedOutReason): SessionEndCause = when (reason) {
            SignedOutReason.NOT_SIGNED_IN -> NOT_SIGNED_IN
            SignedOutReason.SESSION_ENDED -> SESSION_ENDED
            SignedOutReason.STORED_SESSION_UNRECOVERABLE -> STORED_SESSION_UNRECOVERABLE
            SignedOutReason.STORAGE_FAILED -> STORAGE_FAILED
        }
    }
}
