#!/bin/sh
printf '<%s>' "$@" >> "$FAKE_DIR/argv"
printf '\n' >> "$FAKE_DIR/argv"
case "$1" in
 inspect) cat "$FAKE_DIR/inspect"; exit;;
 ps) if [ -f "$FAKE_DIR/container-gone" ]; then exit; fi; exit 8;;
 exec) shift;;
 *) exit 99;;
esac
if [ "$1" = --user ]; then
 shift 3
 echo cleanup >> "$FAKE_DIR/cleanups"
 if [ -f "$FAKE_DIR/cleanup-fail" ]; then exit 8; fi
 if [ -f "$FAKE_DIR/cleanup-block" ]; then sleep 300; exit; fi
 exec "$@"
fi
[ "$1" = -it ] || exit 98
shift 3
marker=$1
shift 4
if [ -f "$FAKE_DIR/attach-fail" ]; then
 setsid env "$marker" sh -c 'while :; do sleep 10; done' </dev/null >/dev/null 2>&1 &
 echo $! > "$FAKE_DIR/detached-pid"
 sleep 0.05
 exit 8
fi
if [ -f "$FAKE_DIR/bootstrap-fail" ]; then echo missing prerequisites; exit 125; fi
[ -t 0 ] && [ -t 1 ] && [ -t 2 ] && stty size </dev/tty > "$FAKE_DIR/tty-size" || exit 97
env "$marker" "$@"
