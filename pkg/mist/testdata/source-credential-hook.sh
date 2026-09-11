#!/bin/sh
# CONN_PLAY payload: stream name, connected host, connector, request URL. The
# request URL is where a forwarded source credential has to appear. Record each
# payload followed by an explicit terminator so one connection's lines cannot be
# read as part of another's, then admit the play.
cat >> /tmp/conn-play-payloads.txt
printf '\n===RECORD===\n' >> /tmp/conn-play-payloads.txt
printf 'true\n'
