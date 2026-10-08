package ui

import (
	"fmt"
	"math/rand"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jmnser/tunetty/internal/art"
	"github.com/jmnser/tunetty/internal/audio"
	"github.com/jmnser/tunetty/internal/config"
	"github.com/jmnser/tunetty/internal/subsonic"
)

// view identifies the active top level screen.
type view int

// Top level views, in tab order.
const (
	viewArtists view = iota
	viewAlbums
	viewSongs
	viewPlaylists
	viewStarred
	viewQueue
)

var viewNames = []string{"Artists", "Albums", "Songs", "Playlists", "Starred", "Queue"}

// browseLevel tracks how deep the user has drilled into a view.
type browseLevel int

// Browse levels within a view.
const (
	levelRoot browseLevel = iota
	levelAlbums
	levelTracks
)

// mode is the interaction mode; overlays capture all key input.
type mode int

// Interaction modes.
const (
	modeBrowse mode = iota
	modeFinder
	modeFilter
	modeHelp
)

// Options configures the UI model.
type Options struct {
	Client  *subsonic.Client
	Engine  *audio.Engine
	Config  config.Config
	Caps    art.Capabilities
	Version string
	// ServerInfo is the result of the startup ping, shown in the header.
	ServerInfo subsonic.ServerInfo
}

// Model is the bubbletea model driving the whole interface.
type Model struct {
	cfg        config.Config
	client     *subsonic.Client
	engine     *audio.Engine
	renderer   *art.Renderer
	caps       art.Capabilities
	theme      Theme
	keys       KeyMap
	version    string
	apiTimeout time.Duration

	width, height int
	ready         bool

	view  view
	level browseLevel
	mode  mode

	// One list per view keeps scroll position when switching tabs.
	lists map[view]*itemList
	// Breadcrumb of what the user drilled into. All three are nil at
	// levelRoot; crumbs() derives the displayed trail from them.
	crumbArtist *subsonic.Artist
	crumbAlbum  *subsonic.Album
	crumbList   *subsonic.Playlist
	// navSeq numbers navigation steps. Drill-down responses carry the value
	// current when they were requested and are dropped once the user has
	// navigated elsewhere.
	navSeq int

	// Cached catalogue.
	artists   []subsonic.Artist
	albums    []subsonic.Album
	playlists []subsonic.Playlist
	starred   *subsonic.Starred
	// songs is the library as far as it is loaded, a page at a time from the
	// first visit to Songs on. songsDone marks it complete; songsBusy marks a
	// request in flight, so scrolling does not ask for the same page twice.
	songs     []subsonic.Song
	songsDone bool
	songsBusy bool

	// Finder state.
	index      index
	finderQ    string
	finderHits []match
	finderCur  int
	// finderSeq numbers finder edits, for debouncing the server search and
	// dropping responses to an outdated query or a closed finder.
	finderSeq int

	// Filter state (local, applies to the current list).
	filterQ    string
	filterBase []listItem

	// Cover art.
	covers      *coverCache
	currentArt  *art.Image
	currentCID  string
	artRendered art.Rendered
	artCols     int
	artRows     int
	// coverPending is the id of the cover being fetched, so repeated resizes
	// do not request it again.
	coverPending string

	// stars holds star toggles made in this session, keyed by kind and id.
	// They override the server state carried in cached catalogue data.
	stars map[string]bool

	status     string
	statusTime time.Time
	err        error
	loading    int

	serverInfo  subsonic.ServerInfo
	lastScrobID string
	rng         *rand.Rand
}

// New builds the model.
func New(o Options) *Model {
	theme := NewTheme(o.Config.UI.Theme, o.Config.UI.Accent)
	m := &Model{
		cfg:        o.Config,
		client:     o.Client,
		engine:     o.Engine,
		caps:       o.Caps,
		renderer:   art.NewRenderer(art.Protocol(o.Config.Art.Protocol), o.Caps),
		theme:      theme,
		keys:       DefaultKeys(o.Config.Keybinds),
		version:    o.Version,
		apiTimeout: o.Config.Server.Timeout.D(),
		lists:      map[view]*itemList{},
		covers:     newCoverCache(o.Config.Art.CacheSize),
		artCols:    o.Config.Art.Width,
		artRows:    o.Config.Art.Height,
		serverInfo: o.ServerInfo,
		stars:      map[string]bool{},
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec // shuffle order, not security
	}
	for v := viewArtists; v <= viewQueue; v++ {
		m.lists[v] = &itemList{}
	}
	return m
}

// Init starts the initial data load and the background listeners.
func (m *Model) Init() tea.Cmd {
	m.engine.SetVolume(m.cfg.Audio.Volume)
	m.loading = 3
	return tea.Batch(
		m.loadArtists(),
		m.loadAlbums(subsonic.AlbumsNewest, m.cfg.UI.PageSize),
		m.loadPlaylists(),
		listenEngine(m.engine.Events()),
		tick(),
	)
}

// Update handles every message. Framework level messages are handled here;
// data loading results are routed to updateData to keep both readable.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		// The cover has to be re-rendered because the cell area changed, and
		// fetched if it was skipped while the terminal was too small.
		m.renderArt()
		return m, m.loadPendingCover()

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tickMsg:
		if m.status != "" && time.Since(m.statusTime) > 4*time.Second {
			m.status = ""
		}
		return m, tick()

	case engineMsg:
		return m, tea.Batch(m.handleEngineEvent(msg.event), listenEngine(m.engine.Events()))
	}
	return m, m.updateData(msg)
}

// updateData folds loaded catalogue data into the model.
func (m *Model) updateData(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case artistsMsg:
		m.loadingDone()
		m.artists = msg.artists
		m.reindex()
		m.refreshIfShowing(viewArtists)

	case albumsMsg:
		m.loadingDone()
		m.albums = msg.albums
		m.reindex()
		m.refreshIfShowing(viewAlbums)

	case playlistsMsg:
		m.loadingDone()
		m.playlists = msg.playlists
		m.reindex()
		m.refreshIfShowing(viewPlaylists)

	case songsMsg:
		return m.applySongs(msg)

	case starredMsg:
		m.loadingDone()
		m.starred = msg.starred
		if m.view == viewStarred {
			m.refreshList()
		}

	case artistMsg:
		m.loadingDone()
		if msg.seq != m.navSeq {
			return nil // the user navigated away while it loaded
		}
		m.crumbArtist = msg.artist
		m.crumbAlbum, m.crumbList = nil, nil
		m.level = levelAlbums
		m.enterLevel()

	case albumMsg:
		m.loadingDone()
		if msg.seq != m.navSeq {
			return nil
		}
		// crumbArtist stays set when the album was opened from an artist.
		m.crumbAlbum = msg.album
		m.crumbList = nil
		m.level = levelTracks
		m.enterLevel()
		return m.previewCover()

	case playlistMsg:
		m.loadingDone()
		if msg.seq != m.navSeq {
			return nil
		}
		m.crumbList = msg.playlist
		m.crumbArtist, m.crumbAlbum = nil, nil
		m.level = levelTracks
		m.enterLevel()

	case finderDebounceMsg:
		if msg.seq == m.finderSeq && m.mode == modeFinder {
			return m.searchRemote(m.finderQ, msg.seq)
		}

	case searchMsg:
		// A response for an earlier query, or one arriving after the finder
		// closed, must not clobber the hits.
		if msg.seq == m.finderSeq && m.mode == modeFinder {
			m.index.setRemote(msg.result)
			m.runFinder()
		}

	case starMsg:
		m.stars[starKey(msg.kind, msg.id)] = msg.on
		if msg.on {
			m.setStatus("starred " + msg.kind)
		} else {
			m.setStatus("unstarred " + msg.kind)
		}
		m.refreshList()
		// The starred list is stale now; reload it while it is on screen,
		// otherwise on the next visit.
		if m.view != viewStarred {
			m.starred = nil
			return nil
		}
		m.loading++
		return m.loadStarred()

	case coverMsg:
		if msg.id == m.coverPending {
			m.coverPending = ""
		}
		if msg.err != nil {
			m.covers.markMissing(msg.id)
			return nil
		}
		m.covers.put(msg.id, msg.img)
		if msg.id == m.currentCID {
			m.currentArt = msg.img
			m.renderArt()
		}

	case enqueueMsg:
		return m.applyEnqueue(msg)

	case statusMsg:
		m.setStatus(msg.text)

	case errMsg:
		if msg.counted {
			m.loadingDone()
		}
		m.err = msg.err
		m.statusTime = time.Now()
	}
	return nil
}

// applySongs folds a page, or the whole library, into the Songs tab.
func (m *Model) applySongs(msg songsMsg) tea.Cmd {
	m.loadingDone()
	m.songsBusy = false
	switch {
	case msg.done && msg.offset == 0:
		m.songs = msg.songs // a full load replaces whatever pages there were
	case msg.offset == len(m.songs):
		m.songs = append(m.songs, msg.songs...)
	default:
		return nil // a stale page from before a refresh
	}
	m.songsDone = msg.done
	m.refreshIfShowing(viewSongs)
	if msg.shuffle {
		return m.shuffleSongs(append([]subsonic.Song(nil), m.songs...))
	}
	if m.songsDone {
		m.setStatus(plural(len(m.songs), "song"))
	} else {
		m.setStatus(plural(len(m.songs), "song") + " loaded, scroll for more")
	}
	return nil
}

// applyEnqueue adds a background loaded collection to the queue.
func (m *Model) applyEnqueue(msg enqueueMsg) tea.Cmd {
	if len(msg.songs) == 0 {
		m.setStatus("nothing to queue in " + msg.label)
		return nil
	}
	tracks := toTracks(m.client, m.cfg.Audio, msg.songs)
	if msg.next {
		m.engine.InsertNext(tracks...)
		m.setStatus(fmt.Sprintf("queued next: %s (%s)", msg.label, plural(len(tracks), "track")))
		return nil
	}
	m.engine.Enqueue(tracks...)
	m.setStatus(fmt.Sprintf("queued at end: %s (%s)", msg.label, plural(len(tracks), "track")))
	return nil
}

// refreshIfShowing rebuilds the list only when v's top level is on screen.
func (m *Model) refreshIfShowing(v view) {
	if m.view == v && m.level == levelRoot {
		m.refreshList()
	}
}

// enterLevel rebuilds the list after drilling down and resets the cursor.
func (m *Model) enterLevel() {
	m.refreshList()
	m.lists[m.view].moveTo(0)
}

func (m *Model) loadingDone() {
	if m.loading > 0 {
		m.loading--
	}
}

func (m *Model) setStatus(s string) {
	m.status = s
	m.statusTime = time.Now()
	m.err = nil
}

// layout recomputes the geometry of every region.
func (m *Model) layout() {
	// Shrink the cover on small terminals so the list keeps usable space.
	m.artCols = m.cfg.Art.Width
	m.artRows = m.cfg.Art.Height
	if m.artCols > m.width/2 {
		m.artCols = max(8, m.width/2)
	}
	maxArtRows := max((m.height-8)/2, 3)
	if m.artRows > maxArtRows {
		m.artRows = maxArtRows
	}
	if m.width < 60 || m.height < 16 || m.renderer.Protocol() == art.ProtocolNone {
		// Too cramped for artwork, or art is off; the now playing band
		// collapses to text.
		m.artCols, m.artRows = 0, 0
	}

	listH := m.contentHeight()
	for _, l := range m.lists {
		l.setSize(m.width, listH)
	}
}

// contentHeight is the number of rows the browsing list may occupy.
func (m *Model) contentHeight() int {
	// header(1) + tabs(1) + separator(1) + now playing band + progress(1) + status(1)
	chrome := 5 + m.nowPlayingRows()
	return max(m.height-chrome, 3)
}

// nowPlayingRows is the height of the band holding the cover and metadata.
func (m *Model) nowPlayingRows() int {
	if m.artRows > 0 {
		return m.artRows
	}
	return 2
}

// ------------------------------------------------------------- cover art

// ensureCover makes id the displayed cover, loading it if it is not cached.
// The id is recorded even while artwork is hidden, so a later resize shows
// the right cover rather than a stale one.
func (m *Model) ensureCover(id string) tea.Cmd {
	if m.renderer.Protocol() == art.ProtocolNone {
		return nil
	}
	if id == m.currentCID && m.currentArt != nil {
		return nil
	}
	m.currentCID = id
	m.currentArt = nil
	if img, ok := m.covers.get(id); ok && id != "" {
		m.currentArt = img
	}
	m.renderArt()
	return m.loadPendingCover()
}

// loadPendingCover fetches the current cover if it is wanted, displayable
// and neither cached, known missing nor already in flight.
func (m *Model) loadPendingCover() tea.Cmd {
	id := m.currentCID
	if id == "" || m.currentArt != nil || m.artCols == 0 || id == m.coverPending ||
		m.renderer.Protocol() == art.ProtocolNone || m.covers.missing(id) {
		return nil
	}
	m.coverPending = id
	// Ask the server for roughly the pixel size we will display, so it does
	// the downscaling and we transfer a fraction of the bytes.
	px := clamp(m.artCols*max(m.caps.CellWidth, 1), 64, 1000)
	return m.loadCover(id, px)
}

// renderArt rasterises the current cover into the reserved cell block.
func (m *Model) renderArt() {
	if m.artCols <= 0 || m.artRows <= 0 || m.currentArt == nil {
		m.artRendered = art.Rendered{}
		return
	}
	r, err := m.renderer.Render(m.currentArt, m.artCols, m.artRows)
	if err != nil {
		m.artRendered = art.Rendered{}
		return
	}
	m.artRendered = r
}

// ------------------------------------------------------------ engine events

func (m *Model) handleEngineEvent(ev audio.Event) tea.Cmd {
	switch ev.Kind {
	case audio.EventTrackStarted:
		var cmds []tea.Cmd
		if s, ok := songOf(ev.Track); ok {
			if c := m.ensureCover(coverIDOf(s)); c != nil {
				cmds = append(cmds, c)
			}
			if s.ID != m.lastScrobID {
				m.lastScrobID = s.ID
				if c := m.scrobble(s.ID, false); c != nil {
					cmds = append(cmds, c)
				}
			}
		}
		if m.view == viewQueue {
			m.refreshList()
		}
		return tea.Batch(cmds...)

	case audio.EventTrackFinished:
		if s, ok := songOf(ev.Track); ok {
			return m.scrobble(s.ID, true)
		}

	case audio.EventQueueFinished:
		m.setStatus("queue finished")

	case audio.EventQueueChanged:
		if m.view == viewQueue {
			m.refreshList()
		}

	case audio.EventError:
		m.err = ev.Err
		m.statusTime = time.Now()
	}
	return nil
}

func coverIDOf(s subsonic.Song) string {
	if s.CoverArt != "" {
		return s.CoverArt
	}
	if s.AlbumID != "" {
		return s.AlbumID
	}
	return s.ID
}

// ------------------------------------------------------------------ helpers

// crumbs is the breadcrumb trail of the current drill-down.
func (m *Model) crumbs() []string {
	var out []string
	if m.crumbArtist != nil {
		out = append(out, m.crumbArtist.Name)
	}
	if m.level == levelTracks {
		switch {
		case m.crumbAlbum != nil:
			out = append(out, m.crumbAlbum.Name)
		case m.crumbList != nil:
			out = append(out, m.crumbList.Name)
		}
	}
	return out
}

// beginNav starts a navigation step, invalidating drill-down responses still
// in flight, and returns the sequence number for a new request.
func (m *Model) beginNav() int {
	m.navSeq++
	return m.navSeq
}

func starKey(kind, id string) string { return kind + ":" + id }

// isStarred returns the star state of an item, preferring a toggle made in
// this session over the server state the cached data carries.
func (m *Model) isStarred(kind, id string, server bool) bool {
	if on, ok := m.stars[starKey(kind, id)]; ok {
		return on
	}
	return server
}

// currentSongs returns the tracks the active list represents, used by the
// queue and play actions.
func (m *Model) currentSongs() []subsonic.Song {
	items := m.lists[m.view].items
	out := make([]subsonic.Song, 0, len(items))
	for _, it := range items {
		if s, ok := it.Data.(subsonic.Song); ok {
			out = append(out, s)
		}
	}
	return out
}

// describeServer renders the connection line in the header.
func (m *Model) describeServer() string {
	if m.serverInfo.Type == "" {
		return m.client.BaseURL()
	}
	return fmt.Sprintf("%s %s", m.serverInfo.Type, m.serverInfo.Version)
}

// shufflePlay replaces the queue with the current list's tracks in random
// order. On the Songs tab that shuffles the whole library, loading the pages
// not fetched yet first.
func (m *Model) shufflePlay() tea.Cmd {
	if m.view == viewSongs && !m.songsDone && m.mode != modeFilter {
		if m.songsBusy {
			m.setStatus("songs are still loading")
			return nil
		}
		m.songsBusy = true
		m.loading++
		m.setStatus("loading the whole library to shuffle")
		return m.loadAllSongs()
	}
	return m.shuffleSongs(m.currentSongs())
}

// shuffleSongs plays songs in random order, replacing the queue.
func (m *Model) shuffleSongs(songs []subsonic.Song) tea.Cmd {
	if len(songs) == 0 {
		m.setStatus("no tracks to shuffle here")
		return nil
	}
	m.rng.Shuffle(len(songs), func(i, j int) { songs[i], songs[j] = songs[j], songs[i] })
	m.engine.SetQueue(toTracks(m.client, m.cfg.Audio, songs), 0)
	m.setStatus("shuffling " + plural(len(songs), "track"))
	return m.ensureCover(coverIDOf(songs[0]))
}

// moreSongs loads the next page of the Songs tab once the cursor is within
// a screen of the end of what is loaded.
func (m *Model) moreSongs() tea.Cmd {
	l := m.lists[viewSongs]
	if m.view != viewSongs || m.mode == modeFilter || m.songsDone || m.songsBusy ||
		l.cursor < len(l.items)-l.height {
		return nil
	}
	return m.requestSongPage(len(m.songs))
}

// requestSongPage starts loading the page of songs at offset.
func (m *Model) requestSongPage(offset int) tea.Cmd {
	m.songsBusy = true
	m.loading++
	return m.loadSongPage(offset)
}

// shuffleQueue randomises the pending part of the queue.
func (m *Model) shuffleQueue() {
	q := m.engine.Queue()
	st := m.engine.Status()
	from := st.Index + 1
	if from < 1 || from >= len(q) {
		m.setStatus("nothing left to shuffle")
		return
	}
	rest := q[from:]
	m.rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	// Reordering with Move would fire an event per step, so the queue is
	// replaced wholesale; the audible track keeps playing throughout.
	m.engine.ReplaceQueue(append(q[:from:from], rest...))
	m.setStatus(fmt.Sprintf("shuffled %d upcoming tracks", len(rest)))
}
