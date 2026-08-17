#!/bin/sh
# Stands in for a service while the README recording is made: JSON on stdout,
# paced so the list fills the way a real stream does, then quiet, so the frame
# holds still while the recording walks through what arrived.
#
# It writes a quarter of an hour of backlog first, all at once and dated as it
# happened. A service that started when the recording did has no shape to show:
# the volume panel would be three bars of whatever arrived in the last second,
# and the list would open on an empty screen filling up, which is not what
# opening a log looks like. What the backlog says is deliberately dull — the two
# lines the recording goes looking for are the live ones, and a `failed` in the
# history would put the cursor somewhere else.
set -eu

service=${1:-api}
now=$(date -u +%s)

# past writes a line as of some seconds ago, and emit writes one now.
past() {
	printf '{"ts":"%s","level":"%s","service":"%s",%s}\n' \
		"$(date -u -d "@$((now - $1))" +%Y-%m-%dT%H:%M:%S.000Z)" "$2" "$service" "$3"
}

emit() {
	printf '{"ts":"%s","level":"%s","service":"%s",%s}\n' \
		"$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)" "$1" "$service" "$2"
	sleep 0.3
}

# backlogFrom is where the history starts and step is how far apart its lines
# are. Fifteen minutes at ten seconds is a couple of hundred lines: enough for
# the panel above the list to be a shape rather than a handful of bars.
backlogFrom=900
step=10

case $service in
api)
	n=0
	ago=$backlogFrom
	while [ "$ago" -gt 20 ]; do
		n=$((n + 1))
		case $((n % 12)) in
		3) past "$ago" info '"msg":"POST /orders 201","duration_ms":'$((20 + n % 30)) ;;
		7) past "$ago" info '"msg":"GET /health 200","duration_ms":1' ;;
		11) past "$ago" warn '"msg":"slow query","duration_ms":'$((600 + n % 300))',"table":"orders"' ;;
		*) past "$ago" info '"msg":"GET /orders 200","duration_ms":'$((8 + n % 15)) ;;
		esac
		# Five minutes ago billing started timing out and stopped a minute later.
		# A log is read for the minute it went wrong, and a panel of even bars
		# would give the reader nothing to aim at.
		if [ "$ago" -lt 330 ] && [ "$ago" -gt 260 ]; then
			past "$ago" error '"msg":"upstream timed out","upstream":"billing","error":"context deadline exceeded"'
			past "$ago" warn '"msg":"retrying","attempt":1,"upstream":"billing"'
		fi
		ago=$((ago - step))
	done

	emit info '"msg":"listening","addr":"127.0.0.1:8080"'
	emit info '"msg":"GET /orders 200","duration_ms":12,"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"'
	emit info '"msg":"POST /orders 201","duration_ms":31,"trace_id":"8a3c60f7d188f8fa79d48a391a778fa6"'
	emit warn '"msg":"slow query","duration_ms":812,"table":"orders"'
	emit info '"msg":"GET /health 200","duration_ms":1'
	emit error '"msg":"upstream request failed","upstream":"billing","error":"context deadline exceeded","trace_id":"1b4a2f9c0e5d8877a1c2b3d4e5f60718","caller":"internal/ui/app.go:118"'
	emit info '"msg":"GET /orders 200","duration_ms":9'
	emit warn '"msg":"retrying","attempt":2,"upstream":"billing"'
	emit info '"msg":"POST /orders 201","duration_ms":27'
	;;
worker)
	n=0
	job=1000
	ago=$backlogFrom
	while [ "$ago" -gt 20 ]; do
		n=$((n + 1))
		job=$((job + 1))
		case $((n % 9)) in
		4) past "$ago" info '"msg":"job started","job_id":'$job',"kind":"receipt"' ;;
		8) past "$ago" warn '"msg":"queue depth rising","queue":"orders","depth":'$((200 + n * 7)) ;;
		*) past "$ago" info '"msg":"job done","job_id":'$job',"duration_ms":'$((90 + n % 80)) ;;
		esac
		# The same minute, from the other side of the call.
		if [ "$ago" -lt 330 ] && [ "$ago" -gt 260 ]; then
			past "$ago" error '"msg":"job abandoned","job_id":'$job',"error":"billing: connection reset by peer"'
		fi
		ago=$((ago - step))
	done

	emit info '"msg":"worker started","queue":"orders","workers":4'
	emit info '"msg":"job started","job_id":91,"kind":"invoice"'
	emit info '"msg":"job done","job_id":91,"duration_ms":143'
	emit info '"msg":"job started","job_id":92,"kind":"invoice"'
	emit error '"msg":"job failed","job_id":92,"error":"billing: connection reset by peer","trace_id":"1b4a2f9c0e5d8877a1c2b3d4e5f60718"'
	emit warn '"msg":"queue depth rising","queue":"orders","depth":1284'
	emit info '"msg":"job started","job_id":93,"kind":"invoice"'
	emit info '"msg":"job done","job_id":93,"duration_ms":118'
	;;
cache)
	# The place that does not survive the recording: a quiet history, three
	# lines, then the session drops. What it says on the way out is its own, and
	# the note telescope writes under it is the view's.
	n=0
	ago=$backlogFrom
	while [ "$ago" -gt 20 ]; do
		n=$((n + 1))
		if [ $((n % 3)) -eq 0 ]; then
			past "$ago" info '"msg":"GET session hit","duration_ms":1'
		fi
		ago=$((ago - step))
	done

	emit info '"msg":"cache warm","keys":1284'
	emit info '"msg":"GET session hit","duration_ms":1'
	emit info '"msg":"GET session hit","duration_ms":1'
	printf 'Connection to cache-1 closed by remote host.\n' >&2
	exit 255
	;;
esac

sleep 600
