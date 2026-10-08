# tunetty for tmux. Load it from ~/.tmux.conf:
#   source-file /path/to/tunetty.tmux

# Now playing in the status line, refreshed every 2 seconds.
set -g status-interval 2
set -ag status-right ' #(tunetty status)'

# prefix + M opens tunetty in a popup. It runs in its own "tunetty" session,
# so detaching (prefix + d) closes the popup and the music keeps playing.
bind-key M display-popup -E -w 90% -h 90% 'TMUX= tmux new-session -A -s tunetty tunetty'

# Playback from any pane, without prefix, on the media key layout of a Mac
# keyboard: F7 previous, F8 play/pause, F9 next, F10 mute, F11/F12 volume.
# Shift+F7/F9 seek. Shells leave these keys alone.
bind-key -n F7    run-shell -b 'tunetty ctl prev'
bind-key -n F8    run-shell -b 'tunetty ctl play-pause'
bind-key -n F9    run-shell -b 'tunetty ctl next'
bind-key -n F10   run-shell -b 'tunetty ctl mute'
bind-key -n F11   run-shell -b 'tunetty ctl volume-down'
bind-key -n F12   run-shell -b 'tunetty ctl volume-up'
bind-key -n S-F7  run-shell -b 'tunetty ctl seek-back'
bind-key -n S-F9  run-shell -b 'tunetty ctl seek-forward'
