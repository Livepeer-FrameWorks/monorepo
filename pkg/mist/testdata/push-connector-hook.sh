#!/bin/sh
# Records the complete PUSH_REWRITE payload (one record per trigger, lines joined by
# a tab) so the contract can assert the connector line Mist appended, then allows the
# push under its requested stream name (line 3).
payload="$(cat)"
printf '%s\n' "$payload" | tr '\n' '\t' >> /tmp/push-connector-audit
printf '\n' >> /tmp/push-connector-audit
printf '%s\n' "$payload" | sed -n '3p'
