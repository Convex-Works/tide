#!/bin/sh
# EXPERIMENT (do not merge): every 10 s, for each WebKit web content process,
# record CPU time, scheduler state and wait channel of its main thread, and a
# native stack of every thread whose state is not plain sleeping-in-poll.
out="$1"
while :; do
  now=$(date -u +%H:%M:%S)
  for stat in /proc/[0-9]*/stat; do
    pid=${stat#/proc/}; pid=${pid%/stat}
    comm=$(cat "/proc/${pid}/comm" 2>/dev/null) || continue
    case "${comm}" in
      WebKitWebProces* | WPEWebProcess) ;;
      *) continue ;;
    esac
    # utime+stime in clock ticks, fields 14 and 15.
    cpu=$(awk '{print $14 + $15}' "/proc/${pid}/stat" 2>/dev/null)
    state=$(awk '{print $3}' "/proc/${pid}/stat" 2>/dev/null)
    wchan=$(cat "/proc/${pid}/wchan" 2>/dev/null)
    echo "${now} pid=${pid} cpu=${cpu} main_state=${state} main_wchan=${wchan}" >>"${out}"
    prev="/tmp/watchdog-${pid}.cpu"
    last=$(cat "${prev}" 2>/dev/null || echo "${cpu}")
    echo "${cpu}" >"${prev}"
    # A main thread that burned >5 s of CPU in 10 s, or sits in anything other
    # than the poll a GLib main loop idles in, gets a stack.
    if [ $((cpu - last)) -gt 500 ] || { [ -n "${wchan}" ] && [ "${wchan}" != "do_sys_poll" ] && [ "${wchan}" != "0" ]; }; then
      echo "--- ${now} stack pid=${pid} delta_ticks=$((cpu - last))" >>"${out}"
      timeout 20 eu-stack -p "${pid}" -n 40 >>"${out}" 2>&1 || timeout 20 gdb -p "${pid}" -batch -ex "thread apply all bt 40" >>"${out}" 2>&1
    fi
  done
  sleep 10
done
