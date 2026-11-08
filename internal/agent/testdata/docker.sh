#!/bin/sh
printf '<%s>' "$@" >> "$FAKE_DIR/argv"
printf '\n' >> "$FAKE_DIR/argv"
if [ "$1" = version ]; then
 echo probe >> "$FAKE_DIR/probes"
 if [ -f "$FAKE_DIR/events-fail" ]; then echo 'daemon unavailable' >&2; exit 8; fi
 echo '27.0.0'
 exit
fi
if [ "$1" = events ]; then
 echo $$ >> "$FAKE_DIR/event-pids"
 echo $$ > "$FAKE_DIR/event-pid"
 while :; do
  if [ -f "$FAKE_DIR/events-fail" ]; then exit 8; fi
  if [ -f "$FAKE_DIR/events-input" ]; then cat "$FAKE_DIR/events-input"; rm "$FAKE_DIR/events-input"; fi
  sleep 0.02
 done
fi
if [ "$1" = container ] && [ "$2" = inspect ]; then
 if [ "$4" = '{"labels":{{json .Config.Labels}},"created_at":{{json .Created}},"started_at":{{json .State.StartedAt}}}' ]; then
  printf '{"labels":'
  if [ -f "$FAKE_DIR/container-labels-$5" ]; then cat "$FAKE_DIR/container-labels-$5"; else printf '{}'; fi
  printf '}\n'
  exit
 fi
 if [ -f "$FAKE_DIR/fail-container-image" ]; then echo 'inspect failed' >&2; exit 8; fi
 if [ -f "$FAKE_DIR/container-image-$5" ]; then cat "$FAKE_DIR/container-image-$5"; else echo '{"id":"sha256:old","ref":"example"}'; fi
 exit
fi
if [ "$1" = image ] && [ "$2" = inspect ]; then
 if [ -f "$FAKE_DIR/fail-local-image" ]; then echo 'inspect failed' >&2; exit 8; fi
 if [ -f "$FAKE_DIR/local-image-$5" ]; then cat "$FAKE_DIR/local-image-$5"; else echo 'sha256:new'; fi
 exit
fi
shift
while [ "$#" -gt 0 ]; do
 case "$1" in
 --project-name) shift 2;;
 --project-directory) project="$2"; shift 2;;
 --env-file) envfile="$2"; shift 2;;
 --file) compose="$2"; shift 2;;
 *) break;;
 esac
done
action="$1"
shift
case "$action" in
 config)
  case "$*" in
   *--quiet*)
    cp "$envfile" "$FAKE_DIR/validated-env"
    printf '%s' "$PWD" > "$FAKE_DIR/cwd"
    if grep -q INVALID "$compose"; then echo 'invalid compose' >&2; exit 17; fi
    ;;
   *--services*) printf 'web\nworker\n';;
   *--images*) if [ -f "$FAKE_DIR/images" ]; then cat "$FAKE_DIR/images"; fi;;
  esac;;
  ps)
  printf '%s\n' "$project" >> "$FAKE_DIR/ps-calls"
  touch "$FAKE_DIR/ps-${project##*/}"
  while [ -f "$FAKE_DIR/block-all" ]; do sleep 0.02; done
  if [ "${project##*/}" = slow ]; then
   while [ -f "$FAKE_DIR/block-slow" ]; do sleep 0.02; done
  fi
  if [ -f "$FAKE_DIR/failps" ]; then echo 'daemon unavailable' >&2; exit 8; fi
  if [ -f "$FAKE_DIR/ps" ]; then cat "$FAKE_DIR/ps"; else echo '[]'; fi;;
 logs)
  echo $$ > "$FAKE_DIR/log-pid"
  echo 'web | hello'
  if [ -f "$FAKE_DIR/log-exit" ]; then exit 3; fi
  sleep 300 &
  echo $! > "$FAKE_DIR/log-child"
  wait;;
 *)
  if [ "$action" = pull ] || [ "$action" = up ]; then printf '%s' "$DOCKER_CONFIG" > "$FAKE_DIR/pull-config"; fi
  echo $$ > "$FAKE_DIR/op-pid"
  echo "$action started"
  touch "$FAKE_DIR/started"
  while [ -f "$FAKE_DIR/block" ]; do sleep 0.02; done
  if [ -f "$FAKE_DIR/fail" ]; then echo 'docker failed' >&2; exit 7; fi
  echo "$action finished";;
esac
