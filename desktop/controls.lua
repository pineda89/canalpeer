-- CanalPeer's controls over the video, like the iPhone app's: they show on
-- mouse move, hide after 4 s unless paused, and every button sends the same
-- script-message the keys do (input.conf), so the app decides everything.

local assdraw = require "mp.assdraw"

local HIDE_AFTER = 4
local INK = "F7F5F3" -- BGR of #f3f5f7
local ACCENT = "A0B85F" -- BGR of #5fb8a0
local PANEL = "1F1916" -- BGR of #16191f
-- No dimming of the picture: a thin dark outline keeps the icons readable on any frame.
local OUTLINE = "\\bord1.5\\3c&H15110F&\\shad0"

local overlay = mp.create_osd_overlay("ass-events")
local visible, paused, hide_timer = false, false, nil
local buttons = {} -- {x1, y1, x2, y2, action}, rebuilt on every draw
local bar = nil -- {x1, y1, x2, y2} of the progress bar, movies only
local vbar = nil -- {x1, y1, x2, y2} of the volume bar
local audio_menu = false -- the list of audio tracks is open
local rows = {} -- {x1, y1, x2, y2, track id} of the list's rows
local last_click = 0
-- The settings panel: the app tells the mode (script-message canalpeer-vpn).
local settings_open = false
local vpn_mode, vpn_inuse = "auto", false
local panel_x = nil -- left edge of the open panel
local vpn_rows = {} -- {x1, y1, x2, y2, mode} of its options
local VPN_MODES = {
    {"auto", "Automatic", "Direct; through WARP if nothing connects."},
    {"on", "On", "All the app goes through WARP."},
    {"off", "Off", "Never through WARP."},
}

local function send(action) mp.commandv("script-message", "canalpeer", action) end

-- shape draws one button centred on (cx, cy), r = half its size.
local function shape(ass, kind, cx, cy, r)
    ass:new_event()
    ass:append(string.format("{\\an7\\pos(0,0)%s\\1c&H%s&}", OUTLINE, INK))
    ass:draw_start()
    if kind == "play" then
        ass:move_to(cx - r * 0.5, cy - r * 0.7); ass:line_to(cx + r * 0.7, cy); ass:line_to(cx - r * 0.5, cy + r * 0.7)
    elseif kind == "pause" then
        ass:rect_cw(cx - r * 0.55, cy - r * 0.7, cx - r * 0.15, cy + r * 0.7)
        ass:rect_cw(cx + r * 0.15, cy - r * 0.7, cx + r * 0.55, cy + r * 0.7)
    elseif kind == "stop" then -- back to the launcher: an arrow pointing left
        local t = r * 0.13
        ass:move_to(cx - r * 0.7, cy); ass:line_to(cx - r * 0.1, cy - r * 0.6); ass:line_to(cx + r * 0.05, cy - r * 0.45)
        ass:line_to(cx - r * 0.3, cy - t); ass:line_to(cx + r * 0.7, cy - t); ass:line_to(cx + r * 0.7, cy + t)
        ass:line_to(cx - r * 0.3, cy + t); ass:line_to(cx + r * 0.05, cy + r * 0.45); ass:line_to(cx - r * 0.1, cy + r * 0.6)
    elseif kind == "volume" or kind == "mute" then -- a speaker, with sound or crossed out
        ass:move_to(cx - r * 0.8, cy - r * 0.3); ass:line_to(cx - r * 0.4, cy - r * 0.3); ass:line_to(cx, cy - r * 0.75)
        ass:line_to(cx, cy + r * 0.75); ass:line_to(cx - r * 0.4, cy + r * 0.3); ass:line_to(cx - r * 0.8, cy + r * 0.3)
        if kind == "volume" then
            ass:rect_cw(cx + r * 0.25, cy - r * 0.3, cx + r * 0.4, cy + r * 0.3)
            ass:rect_cw(cx + r * 0.55, cy - r * 0.6, cx + r * 0.7, cy + r * 0.6)
        else
            local t = r * 0.1
            for _, d in ipairs({1, -1}) do
                local x1, y1, x2, y2 = cx + r * 0.25, cy - d * r * 0.35, cx + r * 0.85, cy + d * r * 0.35
                ass:move_to(x1 - t, y1 + d * t); ass:line_to(x1 + t, y1 - d * t); ass:line_to(x2 + t, y2 - d * t); ass:line_to(x2 - t, y2 + d * t)
            end
        end
    elseif kind == "audio" then -- a speech bubble
        ass:round_rect_cw(cx - r * 0.75, cy - r * 0.6, cx + r * 0.75, cy + r * 0.35, r * 0.2)
        ass:move_to(cx - r * 0.45, cy + r * 0.3); ass:line_to(cx - r * 0.45, cy + r * 0.8); ass:line_to(cx - r * 0.05, cy + r * 0.3)
    elseif kind == "gear" then -- eight teeth round a hole
        local n, ro, ri = 8, r * 0.8, r * 0.58
        for i = 0, n * 4 - 1 do
            local a = (i - 0.5) * math.pi * 2 / (n * 4)
            local rr = (i % 4 < 2) and ro or ri
            local x, y = cx + rr * math.cos(a), cy + rr * math.sin(a)
            if i == 0 then ass:move_to(x, y) else ass:line_to(x, y) end
        end
        local hole = r * 0.25 -- drawn the other way round: a hole in the fill
        for i = 0, 11 do
            local a = -i * math.pi * 2 / 12
            local x, y = cx + hole * math.cos(a), cy + hole * math.sin(a)
            if i == 0 then ass:move_to(x, y) else ass:line_to(x, y) end
        end
    elseif kind == "fullscreen" then
        local a, t = r * 0.7, r * 0.18
        for _, s in ipairs({{-1, -1}, {1, -1}, {-1, 1}, {1, 1}}) do
            local x, y = cx + s[1] * a, cy + s[2] * a
            ass:rect_cw(math.min(x, x - s[1] * a * 0.6), math.min(y, y - s[2] * t), math.max(x, x - s[1] * a * 0.6), math.max(y, y - s[2] * t))
            ass:rect_cw(math.min(x, x - s[1] * t), math.min(y, y - s[2] * a * 0.6), math.max(x, x - s[1] * t), math.max(y, y - s[2] * a * 0.6))
        end
    end
    ass:draw_stop()
end

local function label(ass, text, cx, cy, size, color)
    ass:new_event()
    ass:append(string.format("{\\an5\\pos(%d,%d)\\fs%d\\b1%s\\1c&H%s&}%s", cx, cy, size, OUTLINE, color, text))
end

-- line writes text starting at x, centred on y.
local function line(ass, text, x, y, size, color, bold)
    ass:new_event()
    ass:append(string.format("{\\an4\\pos(%d,%d)\\fs%d\\b%d%s\\1c&H%s&}%s", x, y, size, bold and 1 or 0, OUTLINE, color, text))
end

-- panel draws the settings over the right of the picture, under the top buttons.
local function panel(ass, w, h, s, top)
    vpn_rows, panel_x = {}, nil
    if not settings_open then return end
    panel_x = w * 0.52
    ass:new_event()
    ass:append(string.format("{\\an7\\pos(0,0)\\bord0\\shad0\\1c&H%s&\\1a&H20&}", PANEL))
    ass:draw_start()
    ass:rect_cw(panel_x, 0, w, h)
    ass:draw_stop()
    local x, y = panel_x + 32 * s, top + 64 * s
    line(ass, "Settings", x, y, 30 * s, INK, true)
    y = y + 52 * s
    line(ass, "VPN (WARP)", x, y, 24 * s, INK, true)
    local help = VPN_MODES[1][3]
    for _, m in ipairs(VPN_MODES) do
        y = y + 40 * s
        local on = m[1] == vpn_mode
        if on then help = m[3] end
        line(ass, (on and "● " or "    ") .. m[2], x, y, 24 * s, on and ACCENT or INK, on)
        vpn_rows[#vpn_rows + 1] = {panel_x, y - 18 * s, w, y + 18 * s, m[1]}
    end
    y = y + 44 * s
    line(ass, help, x, y, 19 * s, INK, false)
    if vpn_inuse then line(ass, "In use now: through WARP", x, y + 30 * s, 19 * s, ACCENT, false) end
end

local LANGS = {
    spa = "Spanish", es = "Spanish", eng = "English", en = "English", cat = "Catalan", ca = "Catalan",
    fra = "French", fre = "French", fr = "French", deu = "German", ger = "German", de = "German",
    ita = "Italian", it = "Italian", por = "Portuguese", pt = "Portuguese", eus = "Basque", baq = "Basque",
    glg = "Galician", gl = "Galician", rus = "Russian", ru = "Russian", ara = "Arabic", ar = "Arabic",
}

local function audio_tracks()
    local list = {}
    for _, t in ipairs(mp.get_property_native("track-list") or {}) do
        if t.type == "audio" then list[#list + 1] = t end
    end
    return list
end

-- track_name says what a viewer needs to choose: language, title, channels.
local function track_name(t, n)
    local parts = {}
    local lang = t.lang and (LANGS[t.lang:lower()] or t.lang:upper())
    parts[#parts + 1] = lang or ("Track " .. n)
    if t.title and t.title ~= "" then parts[#parts + 1] = t.title end
    local ch = t["demux-channel-count"]
    if ch == 6 then parts[#parts + 1] = "5.1" elseif ch == 8 then parts[#parts + 1] = "7.1" elseif ch == 1 then parts[#parts + 1] = "Mono" end
    return table.concat(parts, " · ")
end

local function clock(t)
    t = math.max(0, math.floor(t))
    local h, m, sec = math.floor(t / 3600), math.floor(t / 60) % 60, t % 60
    if h > 0 then return string.format("%d:%02d:%02d", h, m, sec) end
    return string.format("%d:%02d", m, sec)
end

-- rect draws a bar's stretch from xa to xb, alpha "00" solid to "A0" faint.
local function rect(ass, xa, xb, y, t, alpha)
    ass:new_event()
    ass:append(string.format("{\\an7\\pos(0,0)%s\\1c&H%s&\\1a&H%s&}", OUTLINE, INK, alpha))
    ass:draw_start()
    ass:rect_cw(xa, y - t, xb, y + t)
    ass:draw_stop()
end

-- progress draws the movie's bar along the bottom. Live streams report a
-- growing duration too, but are not seekable (measured).
local function progress(ass, w, h, s)
    bar = nil
    local dur = mp.get_property_number("duration")
    if not dur or dur <= 0 or not mp.get_property_bool("seekable") then return end
    local pos = mp.get_property_number("time-pos") or 0
    local x1, x2, y, t = 120 * s, w - 120 * s, h - 40 * s, 3 * s
    rect(ass, x1, x2, y, t, "A0")
    rect(ass, x1, x1 + (x2 - x1) * math.min(pos / dur, 1), y, t, "00")
    label(ass, clock(pos), 60 * s, y, 22 * s, INK)
    label(ass, clock(dur), w - 60 * s, y, 22 * s, INK)
    bar = {x1, y - 16 * s, x2, y + 16 * s, dur}
end

local function draw()
    local w, h = mp.get_osd_size()
    if not visible or not w or w == 0 then
        overlay:remove()
        buttons, bar, vbar, rows, audio_menu, vpn_rows, panel_x = {}, nil, nil, {}, false, {}, nil
        return
    end
    local s = math.max(0.6, math.min(h / 720, 2))
    local ass = assdraw.ass_new()
    local top, r = 48 * s, 18 * s
    panel(ass, w, h, s, top) -- first: the top buttons stay on it

    buttons = {}
    local function button(kind, action, cx, cy, r)
        if kind == "-5" or kind == "+5" then
            label(ass, kind, cx, cy, 44 * s, INK)
        elseif kind == "info" then
            label(ass, "i", cx, cy, 40 * s, INK)
        else
            shape(ass, kind, cx, cy, r)
        end
        buttons[#buttons + 1] = {cx - r * 1.4, cy - r * 1.4, cx + r * 1.4, cy + r * 1.4, action}
    end
    local cx, cy, gap = w / 2, h / 2, 150 * s
    button("-5", "back", cx - gap, cy, 26 * s)
    button(paused and "play" or "pause", "pause", cx, cy, 36 * s)
    button("+5", "forward", cx + gap, cy, 26 * s)
    button("stop", "stop", 48 * s, top, r)
    -- Volume: the speaker mutes, the bar beside it sets the level (0-100).
    local muted, vol = mp.get_property_bool("mute"), mp.get_property_number("volume") or 100
    button((muted or vol == 0) and "mute" or "volume", "mute", 104 * s, top, r)
    local vx1, vx2 = 134 * s, 254 * s
    rect(ass, vx1, vx2, top, 3 * s, "A0")
    if not muted then rect(ass, vx1, vx1 + (vx2 - vx1) * math.min(vol / 100, 1), top, 3 * s, "00") end
    vbar = {vx1, top - 16 * s, vx2, top + 16 * s}
    button("info", "info", w - 110 * s, top, r)
    button("gear", "settings", w - 172 * s, top, r)
    local tracks = audio_tracks()
    rows = {}
    if #tracks > 1 then
        button("audio", "audiomenu", w - 234 * s, top, r)
        if audio_menu then
            local y, size = top + 56 * s, 24 * s
            for n, t in ipairs(tracks) do
                local text = (t.selected and "● " or "    ") .. track_name(t, n)
                ass:new_event()
                ass:append(string.format("{\\an6\\pos(%d,%d)\\fs%d\\b1%s\\1c&H%s&}%s", w - 48 * s, y, size, OUTLINE, INK, text))
                rows[#rows + 1] = {w - 420 * s, y - 18 * s, w - 40 * s, y + 18 * s, t.id}
                y = y + 40 * s
            end
        end
    else
        audio_menu = false
    end
    button("fullscreen", "fullscreen", w - 48 * s, top, r)
    if not settings_open then progress(ass, w, h, s) else bar = nil end
    overlay.res_x, overlay.res_y, overlay.data = w, h, ass.text
    overlay:update()
end

-- Esc closes the open panel instead of leaving the channel.
local function set_settings(open)
    settings_open = open
    if open then
        audio_menu = false
        mp.add_forced_key_binding("ESC", "canalpeer-settings-esc", function()
            set_settings(false)
            draw()
        end)
    else
        mp.remove_key_binding("canalpeer-settings-esc")
    end
end

local function show()
    visible = true
    if hide_timer then hide_timer:kill() end
    hide_timer = mp.add_timeout(HIDE_AFTER, function()
        if not paused and not settings_open then
            visible = false
            draw()
        end
    end)
    draw()
end

local function hit(x, y)
    for _, b in ipairs(buttons) do
        if x >= b[1] and x <= b[3] and y >= b[2] and y <= b[4] then return b[5] end
    end
end

-- A click on a button acts; elsewhere it shows the controls, and a double click
-- toggles full screen (kept here, so two quick clicks on +5 are not a double click).
mp.add_forced_key_binding("MBTN_LEFT", "canalpeer-click", function()
    local pos = mp.get_property_native("mouse-pos")
    local action = visible and pos and hit(pos.x, pos.y)
    local now = mp.get_time()
    local row, vrow = nil, nil
    for _, rw in ipairs(visible and pos and rows or {}) do
        if pos.x >= rw[1] and pos.x <= rw[3] and pos.y >= rw[2] and pos.y <= rw[4] then row = rw end
    end
    for _, rw in ipairs(visible and pos and vpn_rows or {}) do
        if pos.x >= rw[1] and pos.x <= rw[3] and pos.y >= rw[2] and pos.y <= rw[4] then vrow = rw end
    end
    if vrow then
        -- Always sent: the app ignores its own mode, and what is drawn may be
        -- stale. A new one it saves and starts the channel again.
        if vrow[5] ~= vpn_mode then vpn_inuse = false end
        vpn_mode = vrow[5]
        send("vpn " .. vrow[5])
        last_click = 0
    elseif action == "settings" then
        set_settings(not settings_open)
        last_click = 0
    elseif visible and pos and panel_x and pos.x >= panel_x and not action then
        last_click = 0 -- the panel's empty space: no double click to full screen
    elseif row then
        mp.set_property_number("aid", row[5]) -- kept across reloads; the app resets it per link
        audio_menu = false
        last_click = 0
    elseif visible and pos and vbar and pos.x >= vbar[1] and pos.x <= vbar[3] and pos.y >= vbar[2] and pos.y <= vbar[4] then
        mp.set_property_number("volume", math.floor((pos.x - vbar[1]) / (vbar[3] - vbar[1]) * 100 + 0.5))
        mp.set_property_bool("mute", false)
        last_click = 0
    elseif action == "mute" then -- the player's own, not the app's: nothing to decide
        mp.commandv("cycle", "mute")
        last_click = 0
    elseif action == "audiomenu" then
        audio_menu = not audio_menu
        if audio_menu then set_settings(false) end
        last_click = 0
    elseif visible and pos and bar and pos.x >= bar[1] and pos.x <= bar[3] and pos.y >= bar[2] and pos.y <= bar[4] then
        -- A movie: jump to the clicked moment (live streams draw no bar).
        mp.commandv("seek", tostring((pos.x - bar[1]) / (bar[3] - bar[1]) * bar[5]), "absolute+exact")
        last_click = 0
    elseif action then
        send(action)
        last_click = 0
    elseif now - last_click < 0.35 then
        send("fullscreen")
        last_click = 0
    else
        last_click = now
    end
    show()
end)

mp.register_script_message("canalpeer-vpn", function(mode, inuse)
    vpn_mode, vpn_inuse = mode, inuse == "true"
    draw()
end)
-- Ask for the mode: what the app told before this handler existed is lost.
-- At load the app may not be connected yet (IPC), so again with each file.
send("vpn-state")
mp.register_event("file-loaded", function() send("vpn-state") end)

mp.observe_property("mouse-pos", "native", function(_, pos)
    if pos and pos.hover then show() end
end)
mp.observe_property("pause", "bool", function(_, v)
    paused = v
    if v then show() elseif visible then show() end
end)
-- The wheel and m change it too (input.conf): show where it ended up. Not at
-- start, when mpv reports the initial values.
for _, name in ipairs({"volume", "mute"}) do
    local first = true
    mp.observe_property(name, "native", function()
        if first then first = false return end
        show()
    end)
end
mp.observe_property("osd-dimensions", "native", function() draw() end)
mp.observe_property("duration", "number", function() draw() end)
mp.observe_property("seekable", "bool", function() draw() end)
mp.observe_property("track-list", "native", function() draw() end)
-- The bar's clock moves once a second while it shows.
local last_second = -1
mp.observe_property("time-pos", "number", function(_, t)
    if visible and t and math.floor(t) ~= last_second then
        last_second = math.floor(t)
        draw()
    end
end)
