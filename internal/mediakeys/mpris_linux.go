package mediakeys

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"

	"github.com/jmnser/tunetty/internal/audio"
)

// MPRIS names, see https://specifications.freedesktop.org/mpris-spec/latest/.
const (
	busName     = "org.mpris.MediaPlayer2.tunetty"
	objectPath  = dbus.ObjectPath("/org/mpris/MediaPlayer2")
	rootIface   = "org.mpris.MediaPlayer2"
	playerIface = "org.mpris.MediaPlayer2.Player"
	noTrack     = dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/NoTrack")
)

// Start publishes tunetty on the D-Bus session bus as an MPRIS player, which
// desktop environments bind the media keys to. It fails without a session
// bus; the player works the same, just without system controls.
func Start(engine *audio.Engine) (stop func(), err error) {
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	if err = conn.Auth(nil); err != nil {
		return nil, err
	}
	if err = conn.Hello(); err != nil {
		return nil, err
	}

	w := newWatcher(engine)
	p := &player{engine: engine, watcher: w}
	if err = conn.Export(root{}, objectPath, rootIface); err != nil {
		return nil, err
	}
	if err = conn.ExportWithMap(p, renamed, objectPath, playerIface); err != nil {
		return nil, err
	}
	p.props, err = prop.Export(conn, objectPath, properties(engine))
	if err != nil {
		return nil, err
	}
	node := &introspect.Node{
		Name: string(objectPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{Name: rootIface, Methods: introspect.Methods(root{}), Properties: p.props.Introspection(rootIface)},
			{
				Name:       playerIface,
				Methods:    playerMethods(p),
				Properties: p.props.Introspection(playerIface),
				Signals:    []introspect.Signal{{Name: "Seeked", Args: []introspect.Arg{{Name: "Position", Type: "x"}}}},
			},
		},
	}
	if err = conn.Export(introspect.NewIntrospectable(node), objectPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return nil, err
	}

	// The name is claimed last, so clients that react to it find the object
	// complete. A second instance gets a unique name, as the spec suggests.
	if err = requestName(conn, busName); err != nil {
		if err = requestName(conn, fmt.Sprintf("%s.instance%d", busName, os.Getpid())); err != nil {
			return nil, err
		}
	}

	p.conn = conn
	w.start(p.update)
	return func() {
		w.stop()
		_ = conn.Close()
	}, nil
}

func requestName(conn *dbus.Conn, name string) error {
	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("bus name %s is taken", name)
	}
	return nil
}

func properties(engine *audio.Engine) prop.Map {
	ro := func(v any) *prop.Prop { return &prop.Prop{Value: v, Emit: prop.EmitTrue} }
	fixed := func(v any) *prop.Prop { return &prop.Prop{Value: v, Emit: prop.EmitConst} }
	return prop.Map{
		rootIface: {
			"CanQuit":             fixed(false),
			"CanRaise":            fixed(false),
			"HasTrackList":        fixed(false),
			"Identity":            fixed("tunetty"),
			"SupportedUriSchemes": fixed([]string{}),
			"SupportedMimeTypes":  fixed([]string{}),
		},
		playerIface: {
			"PlaybackStatus": ro("Stopped"),
			"Metadata":       ro(metadata(nil)),
			"Volume": {
				Value:    1.0,
				Writable: true,
				Emit:     prop.EmitTrue,
				Callback: func(c *prop.Change) *dbus.Error {
					v, _ := c.Value.(float64)
					engine.SetVolume(min(max(v, 0), 1))
					return nil
				},
			},
			// Position changes continuously; clients poll it and listen for
			// Seeked, so it never emits PropertiesChanged.
			"Position":      {Value: int64(0), Emit: prop.EmitFalse},
			"Rate":          fixed(1.0),
			"MinimumRate":   fixed(1.0),
			"MaximumRate":   fixed(1.0),
			"CanGoNext":     fixed(true),
			"CanGoPrevious": fixed(true),
			"CanPlay":       fixed(true),
			"CanPause":      fixed(true),
			"CanSeek":       fixed(true),
			"CanControl":    fixed(true),
		},
	}
}

// root implements org.mpris.MediaPlayer2. tunetty owns a terminal, so it can
// neither be raised nor quit from outside.
type root struct{}

func (root) Raise() *dbus.Error { return nil }
func (root) Quit() *dbus.Error  { return nil }

// renamed maps Go method names to D-Bus ones. Seek would clash with the
// signature go vet expects of io.Seeker.
var renamed = map[string]string{"SeekRelative": "Seek"}

func playerMethods(p *player) []introspect.Method {
	ms := introspect.Methods(p)
	for i, m := range ms {
		if name, ok := renamed[m.Name]; ok {
			ms[i].Name = name
		}
	}
	return ms
}

// player implements org.mpris.MediaPlayer2.Player.
type player struct {
	engine  *audio.Engine
	watcher *watcher
	props   *prop.Properties
	conn    *dbus.Conn
}

func (p *player) do(fn func()) *dbus.Error {
	fn()
	p.watcher.refresh()
	return nil
}

func (p *player) PlayPause() *dbus.Error { return p.do(p.engine.TogglePause) }
func (p *player) Play() *dbus.Error      { return p.do(p.engine.Play) }
func (p *player) Pause() *dbus.Error     { return p.do(p.engine.Pause) }
func (p *player) Stop() *dbus.Error      { return p.do(p.engine.Stop) }
func (p *player) Next() *dbus.Error      { return p.do(p.engine.Next) }
func (p *player) Previous() *dbus.Error  { return p.do(p.engine.Prev) }

// SeekRelative implements Seek, which moves by offset microseconds. Past the
// end it skips to the next track.
func (p *player) SeekRelative(offset int64) *dbus.Error {
	return p.do(func() {
		st := p.engine.Status()
		if st.Track == nil {
			return
		}
		to := st.Position + time.Duration(offset)*time.Microsecond
		if st.Track.Duration > 0 && to >= st.Track.Duration {
			p.engine.Next()
			return
		}
		p.engine.SeekTo(max(to, 0))
	})
}

// SetPosition jumps to pos microseconds, if track is still the current one.
func (p *player) SetPosition(track dbus.ObjectPath, pos int64) *dbus.Error {
	return p.do(func() {
		st := p.engine.Status()
		to := time.Duration(pos) * time.Microsecond
		if st.Track == nil || track != trackPath(st.Track) || to < 0 ||
			(st.Track.Duration > 0 && to > st.Track.Duration) {
			return
		}
		p.engine.SeekTo(to)
	})
}

// OpenUri is not supported: SupportedUriSchemes is empty.
func (p *player) OpenUri(string) *dbus.Error { //nolint:staticcheck // name fixed by the MPRIS spec
	return dbus.MakeFailedError(errors.New("opening URIs is not supported"))
}

func (p *player) update(st audio.Status, changed, seeked bool) {
	pos := st.Position.Microseconds()
	p.props.SetMust(playerIface, "Position", pos)
	if seeked {
		_ = p.conn.Emit(objectPath, playerIface+".Seeked", pos)
	}
	if !changed {
		return
	}
	set := func(name string, v any) {
		if !reflect.DeepEqual(p.props.GetMust(playerIface, name), v) {
			p.props.SetMust(playerIface, name, v)
		}
	}
	set("PlaybackStatus", playbackStatus(st.State))
	set("Metadata", metadata(st.Track))
	set("Volume", st.Volume)
}

func playbackStatus(s audio.State) string {
	switch s {
	case audio.StatePlaying:
		return "Playing"
	case audio.StatePaused:
		return "Paused"
	default:
		return "Stopped"
	}
}

func metadata(t *audio.Track) map[string]dbus.Variant {
	if t == nil {
		return map[string]dbus.Variant{"mpris:trackid": dbus.MakeVariant(noTrack)}
	}
	m := map[string]dbus.Variant{
		"mpris:trackid": dbus.MakeVariant(trackPath(t)),
		"xesam:title":   dbus.MakeVariant(t.Title),
		"xesam:album":   dbus.MakeVariant(t.Album),
		"xesam:artist":  dbus.MakeVariant([]string{t.Artist}),
	}
	if t.Duration > 0 {
		m["mpris:length"] = dbus.MakeVariant(t.Duration.Microseconds())
	}
	return m
}

// trackPath turns a Subsonic ID, which may hold any character, into the
// object path MPRIS identifies tracks by.
func trackPath(t *audio.Track) dbus.ObjectPath {
	return dbus.ObjectPath("/org/jmnser/tunetty/track/t" + hex.EncodeToString([]byte(t.ID)))
}
