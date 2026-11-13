#!/usr/bin/env bash
# A second bootstrap pass must be a no-op for every declared section.
bootstrap_section_unchanged() {
  local section=$1 log=$2
  grep -Eq "^$section: created=0 updated=0 noop=[1-9][0-9]*( stopped=0)?\$" "$log"
}
