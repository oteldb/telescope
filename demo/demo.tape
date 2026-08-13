# The README recording. Run from the repository root:
#
#     vhs demo/demo.tape
#
# It reads demo/home as the config directory, so the places it opens are the
# two scripts in demo/emit.sh and not anything on this machine. The build runs
# before that, since a Go run under a config directory of ours leaves its
# telemetry there.

Output demo/demo.gif

Set Shell bash
Set FontSize 14
Set Width 1280
Set Height 720
Set Padding 16
Set Framerate 24
Set TypingSpeed 70ms

Hide
Type "export XDG_STATE_HOME=$(mktemp -d) && go build -o $XDG_STATE_HOME/telescope ./cmd/telescope"
Enter
Type "export XDG_CONFIG_HOME=$PWD/demo/home PATH=$XDG_STATE_HOME:$PATH && clear"
Enter
Sleep 20s
Show

Type "telescope"
Sleep 500ms
Enter
Sleep 2s

Enter
Sleep 6s

Type "/"
Sleep 700ms
Type "serv"
Sleep 1200ms
Tab
Sleep 800ms
Type "wo"
Sleep 1200ms
Tab
Sleep 800ms
Enter
Sleep 2500ms

Type "/"
Sleep 500ms
Ctrl+U
Sleep 300ms
Type "level>=warn"
Sleep 1s
Enter
Sleep 3s

Type "/"
Sleep 500ms
Ctrl+U
Sleep 500ms
Enter
Sleep 1s

Type "t"
Sleep 2s
Type "t"
Sleep 2s
Type "t"
Sleep 1s

Type "g"
Sleep 500ms
Down@250ms 6
Sleep 1s

# The two lines the failure was written on, one per service. Which of them the
# cursor opens does not matter: both carry the trace, and "f" on it is the other
# one coming back with it.
Type "/"
Sleep 500ms
Ctrl+U
Sleep 300ms
Type "failed"
Sleep 1200ms
Enter
Sleep 2s

Enter
Sleep 2500ms
Down@400ms 3
Sleep 1500ms
Type "f"
Sleep 5s

Hide
Type "q"
Sleep 1s
