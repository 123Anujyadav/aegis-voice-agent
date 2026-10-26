package com.callscreen.app

import androidx.test.ext.junit.runners.AndroidJUnit4
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The app can open a network socket at all. Android refuses sockets to an app
 * without android.permission.INTERNET ("Permission denied"), so without it
 * sign-in and every server call fail on a device even with hosts configured.
 * A loopback connection proves it with no outside network.
 */
@RunWith(AndroidJUnit4::class)
class NetworkPermissionTest {

    @Test
    fun theAppCanOpenASocket() {
        ServerSocket(0, 1, InetAddress.getLoopbackAddress()).use { server ->
            Socket(InetAddress.getLoopbackAddress(), server.localPort).use { client ->
                server.accept().use { accepted ->
                    client.getOutputStream().write(42)
                    client.getOutputStream().flush()
                    assertEquals(42, accepted.getInputStream().read())
                }
            }
        }
    }
}
