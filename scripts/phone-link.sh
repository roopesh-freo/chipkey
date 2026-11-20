#!/bin/bash
# Keeps every plugged-in phone reachable over USB and the port forward in
# place on each of them.
#
# A brief USB glitch leaves adb unable to see a phone until its server is
# restarted, and every disconnect drops "adb reverse". The app then cannot
# reach the verifier. Run this in a spare terminal for the whole demo:
#
#   scripts/phone-link.sh          # forwards tcp:7447, expects 2 phones
#   scripts/phone-link.sh 7447 1   # one phone only
PORT="${1:-7447}"
WANT="${2:-2}"
state=""
while true; do
  phones=$(adb devices 2>/dev/null | awk 'NR>1 && $2=="device" {print $1}')
  count=$(printf '%s\n' "$phones" | grep -c .)
  for serial in $phones; do
    adb -s "$serial" reverse "tcp:$PORT" "tcp:$PORT" >/dev/null 2>&1
  done
  if [ "$count" -ge "$WANT" ]; then
    if [ "$state" != "up" ]; then echo "$(date +%T) $count phone(s) linked, forward tcp:$PORT in place"; state=up; fi
    sleep 1
  else
    if [ "$state" != "down$count" ]; then echo "$(date +%T) $count of $WANT phone(s) visible, restarting adb"; state="down$count"; fi
    adb kill-server >/dev/null 2>&1
    adb start-server >/dev/null 2>&1
    sleep 3
  fi
done
