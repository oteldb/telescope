#!/bin/sh
# Stands in for a service while the README recording is made: JSON on stdout,
# paced so the list fills the way a real stream does, then quiet, so the frame
# holds still while the recording walks through what arrived.
set -eu

service=${1:-api}

emit() {
	printf '{"ts":"%s","level":"%s","service":"%s",%s}\n' \
		"$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)" "$1" "$service" "$2"
	sleep 0.3
}

case $service in
api)
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
	# The place that does not survive the recording: three lines, then the
	# session drops. What it says on the way out is its own, and the note
	# telescope writes under it is the view's.
	emit info '"msg":"cache warm","keys":1284'
	emit info '"msg":"GET session hit","duration_ms":1'
	emit info '"msg":"GET session hit","duration_ms":1'
	printf 'Connection to cache-1 closed by remote host.\n' >&2
	exit 255
	;;
esac

sleep 600
