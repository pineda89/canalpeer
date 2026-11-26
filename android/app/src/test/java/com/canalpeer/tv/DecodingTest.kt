package com.canalpeer.tv

import org.junit.Assert.assertEquals
import org.junit.Test

class DecodingTest {
    // Android lists hardware first; names as on a Fire TV.
    private val listed = listOf("OMX.MTK.VIDEO.DECODER.AVC", "OMX.amlogic.avc", "c2.android.avc", "OMX.google.h264")
    private val software = setOf("c2.android.avc", "OMX.google.h264")

    @Test fun hardwareFirstThenSoftwareByDefault() = assertEquals(
        listOf("OMX.MTK.VIDEO.DECODER.AVC", "OMX.amlogic.avc", "c2.android.avc", "OMX.google.h264"),
        decoderOrder(listed, softwareFirst = false) { it in software })

    // The viewer's choice goes first; the hardware stays behind it, so no channel is left without picture.
    @Test fun softwareFirstThenHardwareWhenChosen() = assertEquals(
        listOf("c2.android.avc", "OMX.google.h264", "OMX.MTK.VIDEO.DECODER.AVC", "OMX.amlogic.avc"),
        decoderOrder(listed, softwareFirst = true) { it in software })

    @Test fun aDeviceListingSoftwareFirstStillGetsHardwareFirst() = assertEquals(
        listOf("OMX.amlogic.avc", "OMX.google.h264"),
        decoderOrder(listOf("OMX.google.h264", "OMX.amlogic.avc"), softwareFirst = false) { it in software })

    // An emulator's hardware decoder started, then failed on every frame: the next try skips it.
    @Test fun aDecoderThatFailedIsSkipped() = assertEquals(
        listOf("OMX.amlogic.avc", "c2.android.avc", "OMX.google.h264"),
        decoderOrder(listed, softwareFirst = false, failed = { it == "OMX.MTK.VIDEO.DECODER.AVC" }) { it in software })

    // Better to retry a failed one than to have none at all.
    @Test fun whenAllFailedAllAreTriedAgain() = assertEquals(
        listOf("OMX.amlogic.avc", "OMX.google.h264"),
        decoderOrder(listOf("OMX.google.h264", "OMX.amlogic.avc"), softwareFirst = false, failed = { true }) { it in software })
}
