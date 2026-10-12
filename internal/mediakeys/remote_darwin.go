package mediakeys

import (
	"errors"
	"runtime"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/jmnser/tunetty/internal/audio"
)

const mediaPlayerPath = "/System/Library/Frameworks/MediaPlayer.framework/MediaPlayer"

// MPRemoteCommandHandlerStatusSuccess.
const handlerSuccess = 0

// MPNowPlayingPlaybackState values.
const (
	statePlaying = 1
	statePaused  = 2
	stateStopped = 3
)

var (
	selAddTarget       = objc.RegisterName("addTargetWithHandler:")
	selRemoveTarget    = objc.RegisterName("removeTarget:")
	selSetEnabled      = objc.RegisterName("setEnabled:")
	selPositionTime    = objc.RegisterName("positionTime")
	selSetInfo         = objc.RegisterName("setNowPlayingInfo:")
	selSetState        = objc.RegisterName("setPlaybackState:")
	selDictionary      = objc.RegisterName("dictionary")
	selSetObject       = objc.RegisterName("setObject:forKey:")
	selStringWithUTF8  = objc.RegisterName("stringWithUTF8String:")
	selNumberForDouble = objc.RegisterName("numberWithDouble:")
	selNew             = objc.RegisterName("new")
	selDrain           = objc.RegisterName("drain")
)

// Start registers tunetty with the system's remote command center, which
// routes the media keys and Control Center to the app that last reported
// playback. Handlers run on the main dispatch queue, so the main thread must
// run its run loop (see cmd/tunetty).
func Start(engine *audio.Engine) (stop func(), err error) {
	lib, err := purego.Dlopen(mediaPlayerPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, err
	}
	commands := objc.ID(objc.GetClass("MPRemoteCommandCenter")).Send(objc.RegisterName("sharedCommandCenter"))
	info := objc.ID(objc.GetClass("MPNowPlayingInfoCenter")).Send(objc.RegisterName("defaultCenter"))
	if commands == 0 || info == 0 {
		return nil, errors.New("media player framework unavailable")
	}
	n := &nowPlaying{center: info}
	for _, k := range []struct {
		dst  *objc.ID
		name string
	}{
		{&n.keyTitle, "MPMediaItemPropertyTitle"},
		{&n.keyArtist, "MPMediaItemPropertyArtist"},
		{&n.keyAlbum, "MPMediaItemPropertyAlbumTitle"},
		{&n.keyDuration, "MPMediaItemPropertyPlaybackDuration"},
		{&n.keyElapsed, "MPNowPlayingInfoPropertyElapsedPlaybackTime"},
		{&n.keyRate, "MPNowPlayingInfoPropertyPlaybackRate"},
	} {
		addr, err := purego.Dlsym(lib, k.name)
		if err != nil {
			return nil, err
		}
		// addr points at the NSString * the framework exports.
		*k.dst = **(**objc.ID)(unsafe.Pointer(&addr)) //nolint:gosec // reading an exported constant
	}

	w := newWatcher(engine)
	type target struct {
		command, token objc.ID
		block          objc.Block
	}
	var targets []target
	handle := func(command string, fn func(event objc.ID)) {
		cmd := commands.Send(objc.RegisterName(command))
		block := objc.NewBlock(func(_ objc.Block, event objc.ID) int {
			fn(event)
			w.refresh()
			return handlerSuccess
		})
		token := cmd.Send(selAddTarget, block)
		cmd.Send(selSetEnabled, true)
		targets = append(targets, target{cmd, token, block})
	}
	handle("togglePlayPauseCommand", func(objc.ID) { engine.TogglePause() })
	handle("playCommand", func(objc.ID) { engine.Play() })
	handle("pauseCommand", func(objc.ID) { engine.Pause() })
	handle("stopCommand", func(objc.ID) { engine.Stop() })
	handle("nextTrackCommand", func(objc.ID) { engine.Next() })
	handle("previousTrackCommand", func(objc.ID) { engine.Prev() })
	handle("changePlaybackPositionCommand", func(event objc.ID) {
		secs := objc.Send[float64](event, selPositionTime)
		engine.SeekTo(time.Duration(secs * float64(time.Second)))
	})

	w.start(n.update)
	return func() {
		w.stop()
		for _, t := range targets {
			t.command.Send(selRemoveTarget, t.token)
			t.command.Send(selSetEnabled, false)
			t.block.Release()
		}
		n.update(audio.Status{}, true, false)
	}, nil
}

// nowPlaying feeds MPNowPlayingInfoCenter, which shows the track in Control
// Center and decides which app the media keys go to.
type nowPlaying struct {
	center objc.ID

	keyTitle, keyArtist, keyAlbum, keyDuration, keyElapsed, keyRate objc.ID
}

// update publishes st. The system extrapolates the position from the elapsed
// time and the rate, so only changes and seeks need to be sent.
func (n *nowPlaying) update(st audio.Status, changed, seeked bool) {
	if !changed && !seeked {
		return
	}
	// The autorelease pool belongs to the thread that created it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(selNew)
	defer pool.Send(selDrain)

	state := stateStopped
	switch st.State {
	case audio.StatePlaying:
		state = statePlaying
	case audio.StatePaused:
		state = statePaused
	}
	if t := st.Track; t != nil {
		info := objc.ID(objc.GetClass("NSMutableDictionary")).Send(selDictionary)
		set := func(key, value objc.ID) { info.Send(selSetObject, value, key) }
		set(n.keyTitle, nsString(t.Title))
		set(n.keyArtist, nsString(t.Artist))
		set(n.keyAlbum, nsString(t.Album))
		if t.Duration > 0 {
			set(n.keyDuration, nsNumber(t.Duration.Seconds()))
		}
		set(n.keyElapsed, nsNumber(st.Position.Seconds()))
		rate := 0.0
		if state == statePlaying {
			rate = 1
		}
		set(n.keyRate, nsNumber(rate))
		n.center.Send(selSetInfo, info)
	} else {
		n.center.Send(selSetInfo, objc.ID(0))
	}
	n.center.Send(selSetState, state)
}

func nsString(s string) objc.ID {
	return objc.ID(objc.GetClass("NSString")).Send(selStringWithUTF8, s)
}

func nsNumber(f float64) objc.ID {
	return objc.ID(objc.GetClass("NSNumber")).Send(selNumberForDouble, f)
}
