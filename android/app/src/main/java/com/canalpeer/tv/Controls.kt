package com.canalpeer.tv

const val HIDE_AFTER_MS = 4_000L

/** When the controls show: for a while after the last touch or key, and always while paused. */
class Controls(private val now: () -> Long) {
    private var touchedAt: Long? = null
    private var hidden = false // on purpose: wins over a pause, until the next touch

    fun touched() {
        touchedAt = now()
        hidden = false
    }

    fun hide() {
        hidden = true
    }

    fun visible(paused: Boolean): Boolean {
        if (hidden) return false
        val at = touchedAt ?: return paused
        return paused || now() - at <= HIDE_AFTER_MS
    }
}

enum class Back { HIDE_CONTROLS, CALLER, LAUNCHER }

/**
 * Back closes the controls first, then returns to whatever opened the link: viewers
 * pick channels in another app. Only a waiting update sends them to the launcher,
 * where it is offered. Stop always goes to the launcher.
 */
fun backAction(controlsShown: Boolean, updateWaiting: Boolean) = when {
    controlsShown -> Back.HIDE_CONTROLS
    updateWaiting -> Back.LAUNCHER
    else -> Back.CALLER
}
