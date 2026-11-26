package com.canalpeer.tv

import android.app.Activity
import android.app.AlertDialog
import android.app.PictureInPictureParams
import android.content.Intent
import android.content.pm.ActivityInfo
import android.content.pm.PackageManager
import android.content.res.Configuration
import android.media.AudioManager
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.util.Rational
import android.view.KeyEvent
import android.view.View
import android.view.WindowManager
import android.widget.ImageButton
import android.widget.SeekBar
import android.widget.TextView
import android.widget.Toast
import androidx.media3.common.C
import androidx.media3.common.TrackSelectionOverride
import androidx.media3.common.Tracks
import androidx.annotation.OptIn
import androidx.media3.common.MediaItem
import androidx.media3.common.MimeTypes
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.DefaultHttpDataSource
import androidx.media3.exoplayer.DefaultLoadControl
import androidx.media3.exoplayer.DefaultRenderersFactory
import androidx.media3.exoplayer.ExoPlaybackException
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.analytics.AnalyticsListener
import androidx.media3.exoplayer.mediacodec.MediaCodecSelector
import androidx.media3.exoplayer.source.ProgressiveMediaSource
import androidx.media3.exoplayer.util.EventLogger
import androidx.media3.ui.DefaultTimeBar
import androidx.media3.ui.PlayerView
import androidx.media3.ui.TimeBar
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import java.io.File
import java.lang.ref.WeakReference
import java.util.Locale
import mobile.Mobile
import mobile.Options
import mobile.Session
import mobile.Status

private const val TAG = "canalpeer"
private const val SEEK_STEP_MS = 5_000L
private const val MOVIE_STEP_MS = 60_000L
private const val LIVE_MS = 13_000L // the core never jumps nearer than ~12 s to the edge

class PlayerActivity : Activity() {
    private val main = Handler(Looper.getMainLooper())
    private lateinit var statusView: TextView
    private lateinit var infoView: TextView
    private lateinit var view: PlayerView
    private var session: Session? = null
    private var player: ExoPlayer? = null
    private var last: Status? = null
    private var buffering = false
    private var bufferingSince = 0L
    private var everReady = false
    private var cuts = CutTracker { System.currentTimeMillis() }
    private val shown = PlayClock { System.currentTimeMillis() }
    private val controls = Controls { System.currentTimeMillis() }
    private var touch = false // phones and tablets get the iPhone's controls; TVs open them with OK
    private lateinit var controlsView: View
    private val audio by lazy { getSystemService(AudioManager::class.java) }
    private var fullscreen = false
    private var updateWaiting = false
    private var videoDecoder = "" // shown in the info line: support asks for it
    private val failedDecoders = mutableSetOf<String>()
    private lateinit var settings: SettingsPanel
    private lateinit var sendSheet: SendSheet
    private var contentId = "" // what Send to TV sends

    private val poll = object : Runnable {
        override fun run() {
            val s = session ?: return
            val st = s.status()
            last = st
            if (st.phase == "playing" && player == null) startPlayer(s.url())
            sendSheet.render() // a movie is known once resolved
            unstick(st)
            render()
            if (st.phase != "failed") main.postDelayed(this, 1000)
        }
    }

    private val listener = object : Player.Listener {
        override fun onIsPlayingChanged(isPlaying: Boolean) = shown.playing(isPlaying)

        override fun onTracksChanged(tracks: Tracks) = renderControls()

        override fun onPlaybackStateChanged(state: Int) {
            if (state == Player.STATE_IDLE) return // stop() on reopen: the cut goes on
            if (state == Player.STATE_ENDED && last?.vod == true) return leave() // the movie ended
            if (state == Player.STATE_BUFFERING && !buffering) bufferingSince = System.currentTimeMillis()
            buffering = state == Player.STATE_BUFFERING
            if (state == Player.STATE_READY) everReady = true
            when (state) {
                Player.STATE_BUFFERING -> cuts.onBuffering()
                Player.STATE_READY -> cuts.onReady()?.let(::logCut)
            }
            render()
        }

        // Never leave the viewer on an error: reopen the local stream and carry on.
        override fun onPlayerError(error: PlaybackException) {
            // The video decoder broke mid-stream: the reopen below picks the next one.
            if (error is ExoPlaybackException && error.type == ExoPlaybackException.TYPE_RENDERER &&
                MimeTypes.isVideo(error.rendererFormat?.sampleMimeType) && videoDecoder.isNotEmpty()) {
                Log.w(TAG, "video decoder $videoDecoder failed; trying the next one")
                failedDecoders += videoDecoder
            }
            cuts.onError()
            if (!buffering) bufferingSince = System.currentTimeMillis()
            buffering = true
            render()
            main.postDelayed({
                if (last?.phase != "failed") player?.let { reopen(it) }
            }, 1000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_player)
        Mobile.reportOpen(if (touchDevice(packageManager)) "android" else "android-tv", BuildConfig.VERSION_NAME, vpnMode, historyDir())
        statusView = findViewById(R.id.status)
        infoView = findViewById(R.id.info)
        view = findViewById(R.id.player)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        controlsView = findViewById(R.id.controls)
        touch = touchDevice(packageManager)
        settings = SettingsPanel(this, findViewById(android.R.id.content),
            { settingRows(onVpn = ::switchVpn, onDecoding = { redecode() }) },
            { if (last?.outbound == "warp") getString(R.string.vpn_in_use) else null })
        sendSheet = SendSheet(this, findViewById(android.R.id.content), ::playingOn)
        setUpControls()
        // One player at a time: a link opened from another app while one plays (in
        // picture in picture, say) replaces it instead of playing both.
        current?.get()?.finish()
        current = WeakReference(this)
        // A link may open the player straight, the launcher never seen: the route is set before the update check.
        Mobile.setVpn(vpnMode, historyDir())
        // Back returns to whatever opened the link; only a waiting update sends the viewer to the launcher.
        Updater.check(this) { updateWaiting = true }
    }

    override fun onStart() {
        super.onStart()
        TvReceiver.start(this)
        startSession()
    }

    // A channel sent from a phone while this one plays: switch to it here.
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        sendSheet.close() // it would send the old channel
        restartSession()
    }

    private fun startSession() {
        contentId = ""
        val id = try {
            Mobile.normalize(intent.dataString ?: "")
        } catch (e: Exception) {
            showFailure(getString(R.string.err_bad_link))
            return
        }
        contentId = id
        session = try {
            Mobile.start(Options().apply {
                contentID = id
                cacheDir = this@PlayerActivity.cacheDir.absolutePath
                dataDir = historyDir()
                vpn = vpnMode
            })
        } catch (e: Exception) {
            Log.e(TAG, "start failed", e)
            showFailure(getString(R.string.err_network))
            return
        }
        main.post(poll)
    }

    @Deprecated("Back is the only way out on a TV remote")
    override fun onBackPressed() = when (backAction(!touch && controlsShown(), updateWaiting)) {
        Back.HIDE_CONTROLS -> {
            controls.hide()
            render()
        }
        Back.LAUNCHER -> leave()
        Back.CALLER -> finish()
    }

    fun leave() = toLauncher()

    // The TV took the channel, or a DLNA TV is to get it from this phone: this phone's
    // player stops (the core's stream has one reader) and says where it went.
    private fun playingOn(t: Target) {
        stopPlayback()
        if (t.kind == Kind.DLNA) DlnaSendService.start(this, contentId, t)
        startActivity(PlayingOnActivity.intent(this, t))
        finish()
    }

    // Home while watching on a phone keeps the channel in a small window. Closing that
    // window stops the activity, and onStop ends the stream as always.
    override fun onUserLeaveHint() {
        super.onUserLeaveHint()
        sendSheet.close() // discovery and its multicast lock stop in the small window
        if (player != null) enterPip()
    }

    // Phones only: on a TV the remote can hardly get back from the small window.
    private fun pipAllowed() = Build.VERSION.SDK_INT >= 26 && touch &&
        packageManager.hasSystemFeature(PackageManager.FEATURE_PICTURE_IN_PICTURE)

    private fun enterPip() {
        if (Build.VERSION.SDK_INT < 26 || !pipAllowed() || isInPictureInPictureMode) return
        enterPictureInPictureMode(PictureInPictureParams.Builder().setAspectRatio(Rational(16, 9)).build())
    }

    override fun onPictureInPictureModeChanged(inPip: Boolean, config: Configuration) {
        super.onPictureInPictureModeChanged(inPip, config)
        if (inPip) {
            statusView.visibility = View.GONE
            infoView.visibility = View.GONE
        }
        render()
    }

    override fun onDestroy() {
        super.onDestroy()
        if (current?.get() === this) current = null
    }

    // Leaving the screen ends the stream: nothing downloads or uploads in the background.
    override fun onStop() {
        super.onStop()
        sendSheet.close()
        stopPlayback()
        finish()
        TvReceiver.stop()
    }

    // The player goes first: a paused player that stopped reading must not keep the server busy.
    private fun stopPlayback() {
        main.removeCallbacksAndMessages(null)
        player?.release()
        player = null
        session?.stop()
        session = null
    }

    // Same channel, new route: the session restarts, as Stop and play again would.
    private fun switchVpn(mode: String) {
        Mobile.setVpn(mode, historyDir())
        restartSession()
    }

    private fun restartSession() {
        stopPlayback()
        // Another channel: what the last one showed and how it cut say nothing of it.
        shown.reset()
        cuts = CutTracker { System.currentTimeMillis() }
        last = null
        everReady = false
        buffering = false
        startSession()
        render()
    }

    // The decoder order is read when the player re-prepares (startPlayer's selector).
    private fun redecode() {
        player?.let {
            it.stop()
            reopen(it)
        }
    }

    private fun controlsShown() = controlsView.visibility == View.VISIBLE

    // TV: the controls open on the centre button, or on Stop while a message covers it.
    private fun showControls() {
        controls.touched()
        render()
        val centre = findViewById<View>(R.id.centre)
        findViewById<View>(if (centre.visibility == View.VISIBLE) R.id.playPause else R.id.stop).requestFocus()
    }

    // The player view holds focus and would swallow keys before onKeyDown.
    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        if (sendSheet.isOpen && event.keyCode == KeyEvent.KEYCODE_BACK) {
            if (event.action == KeyEvent.ACTION_DOWN) sendSheet.close()
            return true
        }
        // The settings panel takes the keys (Back included) while open; OK presses its options.
        if (settings.isOpen) {
            controls.touched()
            if (event.action == KeyEvent.ACTION_DOWN && settings.onKey(event.keyCode)) return true
            return super.dispatchKeyEvent(event)
        }
        // TV with the controls up: the arrows and OK move between and press them, as
        // anywhere in Android; every key keeps them up a while longer.
        if (!touch && controlsShown() && event.keyCode in NAV_KEYS) {
            controls.touched()
            val handled = super.dispatchKeyEvent(event)
            renderControls()
            return handled
        }
        val handled = when (event.keyCode) {
            KeyEvent.KEYCODE_MEDIA_PLAY_PAUSE, KeyEvent.KEYCODE_MEDIA_PAUSE, KeyEvent.KEYCODE_MEDIA_PLAY,
            KeyEvent.KEYCODE_DPAD_CENTER, KeyEvent.KEYCODE_ENTER, KeyEvent.KEYCODE_MENU,
            KeyEvent.KEYCODE_DPAD_LEFT, KeyEvent.KEYCODE_DPAD_RIGHT,
            KeyEvent.KEYCODE_MEDIA_REWIND, KeyEvent.KEYCODE_MEDIA_FAST_FORWARD -> true
            KeyEvent.KEYCODE_DPAD_UP, KeyEvent.KEYCODE_DPAD_DOWN -> last?.vod == true
            else -> false
        }
        if (!handled) return super.dispatchKeyEvent(event)
        if (event.action != KeyEvent.ACTION_DOWN || event.repeatCount > 0) return true
        when (event.keyCode) {
            // Pausing keeps downloading into the core's queue, so resuming leaves a bigger buffer.
            KeyEvent.KEYCODE_MEDIA_PLAY_PAUSE -> player?.let { it.playWhenReady = !it.playWhenReady }
            KeyEvent.KEYCODE_MEDIA_PAUSE -> player?.playWhenReady = false
            KeyEvent.KEYCODE_MEDIA_PLAY -> player?.playWhenReady = true
            KeyEvent.KEYCODE_MENU -> chooseAudio()
            KeyEvent.KEYCODE_DPAD_LEFT, KeyEvent.KEYCODE_MEDIA_REWIND -> jump(-SEEK_STEP_MS)
            KeyEvent.KEYCODE_DPAD_RIGHT, KeyEvent.KEYCODE_MEDIA_FAST_FORWARD -> jump(SEEK_STEP_MS)
            KeyEvent.KEYCODE_DPAD_DOWN -> jump(-MOVIE_STEP_MS)
            KeyEvent.KEYCODE_DPAD_UP -> jump(MOVIE_STEP_MS)
            else -> if (touch) toggleInfo() else showControls() // OK: on a TV, the controls
        }
        render()
        return true
    }

    private var seekToast: Toast? = null

    // Back replays what the core kept; forward drops buffer toward live.
    private fun jump(deltaMs: Long) {
        val p = player ?: return
        if (session == null) return
        if (last?.vod == true) {
            // A movie is a file: ExoPlayer seeks in it with HTTP ranges.
            val end = if (p.duration == C.TIME_UNSET) Long.MAX_VALUE else p.duration // unknown before it loads
            val to = (p.currentPosition + deltaMs).coerceIn(0L, end)
            p.seekTo(to)
            if (!touch) { // the remote has no bar to look at: say where it landed
                seekToast?.cancel()
                seekToast = Toast.makeText(this, "${clock(to)} / ${clock(p.duration)}", Toast.LENGTH_SHORT).also { it.show() }
            }
            return
        }
        p.stop() // close the stream first, so the old connection reads nothing more
        cuts.onSeek()
        val behind = reopen(p, deltaMs)
        seekToast?.cancel()
        seekToast = Toast.makeText(this,
            if (behind <= LIVE_MS) getString(R.string.live) else getString(R.string.behind_live, behind / 1000),
            Toast.LENGTH_SHORT).also { it.show() }
    }

    private fun audioOptions(p: Player) = p.currentTracks.groups.filter { it.type == C.TRACK_TYPE_AUDIO }
        .flatMap { g -> (0 until g.length).filter(g::isTrackSupported).map { g to it } }

    private fun chooseAudio() {
        val p = player ?: return
        val options = audioOptions(p)
        if (options.size < 2) {
            Toast.makeText(this, R.string.single_audio, Toast.LENGTH_SHORT).show()
            return
        }
        val labels = options.mapIndexed { i, (g, t) ->
            val f = g.getTrackFormat(t)
            val name = f.label ?: f.language?.let { Locale.forLanguageTag(it).displayLanguage }?.takeIf { it.isNotBlank() }
                ?: getString(R.string.track_n, i + 1)
            when (f.channelCount) {
                6 -> "$name · 5.1"
                8 -> "$name · 7.1"
                else -> name
            }
        }.toTypedArray()
        val current = options.indexOfFirst { (g, t) -> g.isTrackSelected(t) }
        AlertDialog.Builder(this)
            .setTitle(R.string.audio_track)
            .setSingleChoiceItems(labels, current) { d, i ->
                val (g, t) = options[i]
                p.trackSelectionParameters = p.trackSelectionParameters.buildUpon()
                    .setOverrideForType(TrackSelectionOverride(g.mediaTrackGroup, t))
                    .build()
                d.dismiss()
            }
            .show()
    }

    @OptIn(UnstableApi::class)
    private fun startPlayer(url: String) {
        // min == max keeps ExoPlayer reading continuously, so the Go pipe never fills.
        val load = DefaultLoadControl.Builder()
            .setBufferDurationsMs(10_000, 10_000, 5_000, 5_000)
            .build()
        // Video: hardware first, software if it fails to start (an emulator's did), or
        // the other way round when the viewer chose it in the settings. Read on every
        // prepare, so a change in the panel applies on the next re-prepare.
        val decoders = MediaCodecSelector { mime, secure, tunneling ->
            val all = MediaCodecSelector.DEFAULT.getDecoderInfos(mime, secure, tunneling)
            if (MimeTypes.isVideo(mime)) decoderOrder(all, softwareVideo, { it.name in failedDecoders }) { it.softwareOnly } else all
        }
        // FFmpeg only for what the device can't decode (MP2, and AC3/DTS on some).
        val renderers = DefaultRenderersFactory(this)
            .setExtensionRendererMode(DefaultRenderersFactory.EXTENSION_RENDERER_MODE_ON)
            .setMediaCodecSelector(decoders)
            .setEnableDecoderFallback(true)
        val p = ExoPlayer.Builder(this, renderers).setLoadControl(load).build()
        p.addAnalyticsListener(object : AnalyticsListener {
            override fun onVideoDecoderInitialized(t: AnalyticsListener.EventTime, name: String, initializedMs: Long, durationMs: Long) {
                videoDecoder = name
                Log.i(TAG, "video decoder $name")
            }
        })
        // The server is the core on this same device: silence means the broadcast is
        // late, not a broken link. The default 8 s read timeout made ExoPlayer reopen
        // the stream and throw away its buffer during a 20 s source stall (measured).
        val http = DefaultHttpDataSource.Factory().setReadTimeoutMs(5 * 60_000)
        val source = ProgressiveMediaSource.Factory(http)
            .createMediaSource(MediaItem.fromUri(url))
        p.addListener(listener)
        if (BuildConfig.DEBUG) p.addAnalyticsListener(EventLogger())
        p.setMediaSource(source)
        p.playWhenReady = true
        p.prepare()
        view.player = p
        player = p
    }

    private var lastUnstick = 0L

    private fun unstick(st: Status) {
        val p = player ?: return
        val now = System.currentTimeMillis()
        if (!shouldReopen(st.phase, st.queuedMs, cuts.openForMs(), now - lastUnstick)) return
        lastUnstick = now
        Log.w(TAG, "player stuck for ${cuts.openForMs()} ms with ${st.queuedMs} ms queued; reopening")
        p.stop()
        reopen(p)
    }

    // Every re-prepare goes through here: the core first moves to what is on screen
    // (plus deltaMs), so the player neither skips nor replays. Returns how far
    // behind the newest data playback resumes.
    private fun reopen(p: ExoPlayer, deltaMs: Long = 0): Long {
        val behind = session?.jump(deltaMs, shown.shownMs()) ?: 0
        shown.reset()
        p.prepare()
        return behind
    }

    private fun setUpControls() {
        fun button(id: Int, action: () -> Unit) = findViewById<View>(id).setOnClickListener {
            action()
            controls.touched()
            render()
        }
        view.setOnClickListener {
            if (controls.visible(paused())) controls.hide() else controls.touched()
            render()
        }
        button(R.id.back5) { jump(-SEEK_STEP_MS) }
        button(R.id.forward5) { jump(SEEK_STEP_MS) }
        button(R.id.playPause) { player?.let { it.playWhenReady = !it.playWhenReady } }
        button(R.id.stop) { leave() }
        button(R.id.infoButton) { toggleInfo() }
        findViewById<View>(R.id.settingsButton).let { gear -> button(R.id.settingsButton) { settings.open(returnTo = gear) } }
        button(R.id.fullscreen) { setFullscreen(!fullscreen) }
        if (touch) { // phones send; a TV is where channels go
            findViewById<View>(R.id.sendButton).visibility = View.VISIBLE
            button(R.id.sendButton) { if (contentId.isNotEmpty()) sendSheet.open(contentId) { last.let { dlnaBlock(it?.phase, it?.vod == true) } } }
        }
        if (pipAllowed()) {
            findViewById<View>(R.id.pipButton).visibility = View.VISIBLE
            button(R.id.pipButton) { enterPip() }
        }
        // A TV is always full screen. Volume stays: many boxes' remotes have no volume
        // keys for the box itself (a viewer missed it); on a device whose volume Android
        // fixes (some send it over HDMI), the bar could not move anything.
        if (!touch) findViewById<View>(R.id.fullscreen).visibility = View.GONE
        if (audio.isVolumeFixed) findViewById<View>(R.id.volumeRow).visibility = View.GONE
        button(R.id.audioButton) { chooseAudio() }
        button(R.id.volumeButton) { audio.adjustStreamVolume(AudioManager.STREAM_MUSIC, AudioManager.ADJUST_TOGGLE_MUTE, 0) }
        findViewById<SeekBar>(R.id.volumeBar).apply {
            max = audio.getStreamMaxVolume(AudioManager.STREAM_MUSIC)
            setOnSeekBarChangeListener(object : SeekBar.OnSeekBarChangeListener {
                override fun onProgressChanged(bar: SeekBar, value: Int, fromUser: Boolean) {
                    if (!fromUser) return
                    audio.setStreamVolume(AudioManager.STREAM_MUSIC, value, 0)
                    controls.touched()
                }
                override fun onStartTrackingTouch(bar: SeekBar) = controls.touched()
                override fun onStopTrackingTouch(bar: SeekBar) {
                    controls.touched()
                    renderControls()
                }
            })
        }
        findViewById<DefaultTimeBar>(R.id.timeBar).addListener(object : TimeBar.OnScrubListener {
            override fun onScrubStart(timeBar: TimeBar, position: Long) = controls.touched()
            override fun onScrubMove(timeBar: TimeBar, position: Long) = controls.touched()
            override fun onScrubStop(timeBar: TimeBar, position: Long, canceled: Boolean) {
                if (!canceled) player?.seekTo(position)
                controls.touched()
                renderControls()
            }
        })
        if (touch) controls.touched() // a phone shows them at first; a TV waits for OK
        renderControls()
    }

    private fun toggleInfo() {
        infoView.visibility = if (infoView.visibility == View.VISIBLE) View.GONE else View.VISIBLE
    }

    private fun paused() = player?.playWhenReady == false

    private fun renderControls() {
        // A picture in picture window is too small to touch anything in.
        controlsView.visibility = if ((controls.visible(paused()) || settings.isOpen) && !isInPictureInPictureMode) View.VISIBLE else View.GONE
        // While a message is up (finding, loading, failed) there is nothing to seek: the message wins.
        findViewById<View>(R.id.centre).visibility = if (statusView.visibility == View.VISIBLE) View.GONE else View.VISIBLE
        findViewById<ImageButton>(R.id.playPause).apply {
            setImageResource(if (paused()) R.drawable.ic_play else R.drawable.ic_pause)
            contentDescription = getString(if (paused()) R.string.play else R.string.pause)
        }
        // Read back every time: the side buttons change it too.
        val volume = audio.getStreamVolume(AudioManager.STREAM_MUSIC)
        val muted = audio.isStreamMute(AudioManager.STREAM_MUSIC) || volume == 0
        findViewById<SeekBar>(R.id.volumeBar).progress = if (muted) 0 else volume
        findViewById<ImageButton>(R.id.volumeButton).apply {
            setImageResource(if (muted) R.drawable.ic_volume_off else R.drawable.ic_volume)
            contentDescription = getString(if (muted) R.string.unmute else R.string.mute)
        }
        findViewById<ImageButton>(R.id.fullscreen).setImageResource(
            if (fullscreen) R.drawable.ic_fullscreen_exit else R.drawable.ic_fullscreen)
        val p = player
        // Live channels and movies alike: shown once there is a choice to make.
        findViewById<View>(R.id.audioButton).visibility =
            if (p != null && audioOptions(p).size > 1) View.VISIBLE else View.GONE
        val movie = last?.vod == true && p != null && p.duration != C.TIME_UNSET
        findViewById<View>(R.id.progress).visibility = if (movie) View.VISIBLE else View.GONE
        if (movie && p != null) {
            findViewById<DefaultTimeBar>(R.id.timeBar).apply {
                setDuration(p.duration)
                setPosition(p.currentPosition)
                setBufferedPosition(p.bufferedPosition)
            }
            findViewById<TextView>(R.id.position).text = clock(p.currentPosition)
            findViewById<TextView>(R.id.duration).text = clock(p.duration)
        }
        // Hide on time even when nothing else redraws the screen.
        main.removeCallbacks(hideCheck)
        if (controlsView.visibility == View.VISIBLE) main.postDelayed(hideCheck, HIDE_AFTER_MS + 100)
    }

    private val hideCheck = Runnable { renderControls() }

    // Landscape whatever the rotation lock says, without the system bars; back to following the phone after.
    private fun setFullscreen(on: Boolean) {
        fullscreen = on
        requestedOrientation = if (on) ActivityInfo.SCREEN_ORIENTATION_SENSOR_LANDSCAPE else ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED
        WindowCompat.getInsetsController(window, window.decorView).apply {
            systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
            if (on) hide(WindowInsetsCompat.Type.systemBars()) else show(WindowInsetsCompat.Type.systemBars())
        }
    }

    private fun render() {
        renderControls()
        val st = last ?: return
        val bufferingMs = if (buffering) System.currentTimeMillis() - bufferingSince else 0L
        val via = if (st.outbound == "warp") " · " + getString(R.string.via_warp) else ""
        val overlay = overlayFor(st.phase, paused(), buffering, everReady, bufferingMs)
        // Paused with the controls up, Play is right there: the message would hide it.
        when (if (overlay == Overlay.PAUSED && controlsShown()) Overlay.NONE else overlay) {
            Overlay.FAILED -> showFailure(getString(when (st.err) {
                "not_found" -> R.string.err_not_found
                "no_peers" -> R.string.err_no_peers
                "no_space" -> R.string.err_no_space
                "vpn" -> R.string.err_vpn
                else -> R.string.err_network
            }))
            Overlay.RESOLVING -> show(getString(R.string.resolving))
            Overlay.SEARCHING -> show(getString(R.string.searching, st.peers) + via)
            Overlay.PAUSED -> show(getString(R.string.paused))
            Overlay.LOADING -> show(getString(R.string.loading))
            Overlay.RECONNECTING -> show(getString(R.string.reconnecting, st.peers) + via)
            Overlay.NONE -> statusView.visibility = View.GONE
        }
        val bufferedMs = st.queuedMs + (player?.totalBufferedDuration ?: 0L)
        infoView.text = getString(R.string.info, st.peers, cuts.count, bufferedMs / 1000, BuildConfig.VERSION_NAME) +
            " · ↑ %.1f Mbps".format(st.upBps * 8 / 1e6) + via +
            if (videoDecoder.isEmpty()) "" else "\n$videoDecoder"
        settings.refresh() // the "in use now" line follows the route
        renderControls() // after the message, which decides whether the centre buttons show
    }

    private fun show(text: String) {
        if (isInPictureInPictureMode) return // the picture is the whole window
        statusView.text = text
        statusView.visibility = View.VISIBLE
    }

    private fun showFailure(text: String) = show(text + "\n\n" + getString(R.string.press_back))

    private fun logCut(cut: Cut) {
        if (!BuildConfig.DEBUG) return // read with adb run-as, which only a debug build allows
        val st = last
        val line = """{"start":${cut.startMs},"ms":${cut.durationMs},"reason":"${cut.reason}",""" +
            """"peers":${st?.peers ?: 0},"phase":"${st?.phase ?: ""}"}"""
        File(filesDir, "cuts.jsonl").appendText(line + "\n")
    }
}

private val NAV_KEYS = setOf(KeyEvent.KEYCODE_DPAD_UP, KeyEvent.KEYCODE_DPAD_DOWN, KeyEvent.KEYCODE_DPAD_LEFT,
    KeyEvent.KEYCODE_DPAD_RIGHT, KeyEvent.KEYCODE_DPAD_CENTER, KeyEvent.KEYCODE_ENTER)

// Stop lands on the launcher, also when a browser link opened the player straight
// (then nothing of ours is under it). Updates are offered there. NO_USER_ACTION:
// this is not the viewer leaving for another app, so no picture in picture.
fun Activity.toLauncher() {
    if (isTaskRoot) startActivity(Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NO_USER_ACTION))
    finish()
}

// The player on screen, so a new link replaces it.
private var current: WeakReference<PlayerActivity>? = null

// Stop sent from a phone: as the Stop button.
fun leaveCurrentPlayer() = current?.get()?.leave()

// clock formats a movie position as 1:02:03 or 2:03.
private fun clock(ms: Long): String {
    val t = maxOf(0L, ms) / 1000
    return if (t >= 3600) "%d:%02d:%02d".format(t / 3600, t / 60 % 60, t % 60) else "%d:%02d".format(t / 60, t % 60)
}
