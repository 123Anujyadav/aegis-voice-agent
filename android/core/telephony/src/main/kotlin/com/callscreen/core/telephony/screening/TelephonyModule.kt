package com.callscreen.core.telephony.screening

import android.content.Context
import com.callscreen.core.telephony.callerlist.AtomicRecordFile
import com.callscreen.core.telephony.callerlist.CallerListStore
import dagger.Module
import dagger.Provides
import dagger.hilt.InstallIn
import dagger.hilt.android.qualifiers.ApplicationContext
import dagger.hilt.components.SingletonComponent
import java.io.File
import javax.inject.Singleton

/**
 * One caller list per process, shared by the screening service and the
 * screens that edit it, so a change applies to the next call at once (P-CO-2).
 */
@Module
@InstallIn(SingletonComponent::class)
public object TelephonyModule {
    /** The file in no-backup app storage: never copied off the device. */
    public const val CALLER_LIST_FILE: String = "caller_list.json"

    @Provides
    @Singleton
    public fun callerListStore(@ApplicationContext context: Context): CallerListStore =
        CallerListStore(AtomicRecordFile(File(context.noBackupFilesDir, CALLER_LIST_FILE)))
}
