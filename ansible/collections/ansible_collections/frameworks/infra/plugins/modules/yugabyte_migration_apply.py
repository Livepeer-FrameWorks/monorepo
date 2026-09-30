#!/usr/bin/python
# -*- coding: utf-8 -*-
# SPDX-License-Identifier: AGPL-3.0-or-later

from __future__ import absolute_import, division, print_function

__metaclass__ = type

DOCUMENTATION = r"""
---
module: yugabyte_migration_apply
short_description: Apply one FrameWorks migration item to a YugabyteDB database, one statement at a time
description:
  - Opens one autocommit YSQL session and holds the per-database migration advisory lock on it for the whole item.
  - Runs the item's statements one at a time, each in its own transaction, as the item's owner role, then the
    optional verification and the ledger insert. The ledger row is written only after every statement succeeded.
  - YugabyteDB runs DDL outside transaction blocks, and a schema change on a colocated database aborts a transaction
    that already wrote rows (SQLSTATE 40001). A migration file sent as one transaction can therefore neither keep
    nor roll back its work; applied statement by statement, a failed or interrupted item leaves the statements before
    the failure applied and no ledger row, and its rerun executes every statement again.
  - With I(bounded), each statement runs under I(lock_timeout) and I(statement_timeout), and a statement that fails
    on a lock timeout, a serialization failure, or a deadlock is executed again after I(retry_delay) seconds, up to
    I(retries) times. The statements before it are not repeated.
options:
  db:
    description: Database the item applies to. Also names the advisory lock.
    type: str
    required: true
  owner:
    description: Role the statements run as.
    type: str
    required: true
  label:
    description: Item name used in failure messages.
    type: str
    default: ""
  statements:
    description: Statements to execute, in order, each sent as one query.
    type: list
    elements: str
    required: true
  bounded:
    description: Apply I(lock_timeout), I(statement_timeout), and the statement retry.
    type: bool
    default: true
  lock_timeout:
    description: Session lock_timeout while bounded statements run.
    type: str
    default: 5s
  statement_timeout:
    description: Session statement_timeout while bounded statements run.
    type: str
    default: 15min
  retries:
    description: Further attempts for a bounded statement that failed on a lock timeout, serialization failure, or deadlock.
    type: int
    default: 6
  retry_delay:
    description: Seconds to wait before each further attempt.
    type: int
    default: 10
  invalid_index_repair:
    description:
      - Statement run inside the advisory lock before the item's statements, under a 60s statement_timeout and a 5s
        lock_timeout, then both reset. Empty runs nothing.
    type: str
    default: ""
  invalid_index_verify:
    description: Statement run after the item's statements, before the ledger insert. Empty runs nothing.
    type: str
    default: ""
  ledger_insert:
    description: Statement that records the item in the migration ledger.
    type: str
    required: true
extends_documentation_fragment:
  - community.postgresql.postgres
author:
  - FrameWorks Infra (@frameworks-network)
"""

EXAMPLES = r"""
- name: Apply migration (Yugabyte)
  frameworks.infra.yugabyte_migration_apply:
    login_host: 127.0.0.1
    login_port: 5433
    login_user: yugabyte
    db: purser
    owner: purser
    label: purser/v0.3.11/expand/001_example.sql
    statements:
      - ALTER TABLE purser.t ADD COLUMN IF NOT EXISTS c int
    ledger_insert: >-
      INSERT INTO _migrations (version, phase, seq, filename, checksum, transactional)
      VALUES ('v0.3.11', 'expand', 1, '001_example.sql', 'checksum', true)
"""

RETURN = r"""
statements:
  description: Number of statements executed.
  returned: always
  type: int
retried:
  description: Number of statement attempts repeated after a lock timeout, serialization failure, or deadlock.
  returned: always
  type: int
"""

import time

from ansible.module_utils.basic import AnsibleModule
from ansible.module_utils.common.text.converters import to_native
from ansible_collections.community.postgresql.plugins.module_utils.postgres import (
    connect_to_db,
    ensure_required_libs,
    get_conn_params,
    postgres_common_argument_spec,
)

# lock_not_available (lock_timeout), serialization_failure, deadlock_detected: the statement's own transaction was
# rolled back, so executing it again is safe.
RETRYABLE_SQLSTATES = ("55P03", "40001", "40P01")


def sqlstate(exc):
    return getattr(exc, "pgcode", None) or getattr(exc, "sqlstate", None) or ""


def is_retryable(exc):
    return sqlstate(exc) in RETRYABLE_SQLSTATES or "canceling statement due to lock timeout" in to_native(exc)


def summary(statement):
    line = " ".join(statement.split())
    return line if len(line) <= 160 else line[:157] + "..."


def main():
    argument_spec = postgres_common_argument_spec()
    argument_spec.update(
        db=dict(type="str", required=True, aliases=["login_db"]),
        owner=dict(type="str", required=True),
        label=dict(type="str", default=""),
        statements=dict(type="list", elements="str", required=True),
        bounded=dict(type="bool", default=True),
        lock_timeout=dict(type="str", default="5s"),
        statement_timeout=dict(type="str", default="15min"),
        retries=dict(type="int", default=6),
        retry_delay=dict(type="int", default=10),
        invalid_index_repair=dict(type="str", default=""),
        invalid_index_verify=dict(type="str", default=""),
        ledger_insert=dict(type="str", required=True),
    )
    module = AnsibleModule(argument_spec=argument_spec, supports_check_mode=False)
    ensure_required_libs(module)
    params = module.params
    label = params["label"] or params["db"]

    conn_params = get_conn_params(module, params, warn_db_default=False)
    db_connection, dummy = connect_to_db(module, conn_params, autocommit=True)
    cursor = db_connection.cursor()
    stage = "advisory lock"
    total = len(params["statements"])
    retried = 0

    def run(sql):
        cursor.execute(sql)

    try:
        cursor.execute(
            "SELECT pg_advisory_lock(hashtext('frameworks_migrations'), hashtext(%s))",
            (params["db"],),
        )
        if params["invalid_index_repair"]:
            stage = "invalid index repair"
            run("SET statement_timeout = '60s'")
            run("SET lock_timeout = '5s'")
            run(params["invalid_index_repair"])
            run("RESET lock_timeout")
            run("RESET statement_timeout")
        stage = "session setup"
        if params["bounded"]:
            cursor.execute("SELECT set_config('lock_timeout', %s, false)", (params["lock_timeout"],))
            cursor.execute("SELECT set_config('statement_timeout', %s, false)", (params["statement_timeout"],))
        run('SET ROLE "%s"' % params["owner"].replace('"', '""'))
        for index, statement in enumerate(params["statements"], start=1):
            stage = "statement %d of %d (%s)" % (index, total, summary(statement))
            attempt = 0
            while True:
                try:
                    run(statement)
                    break
                except Exception as exc:
                    if not params["bounded"] or attempt >= params["retries"] or not is_retryable(exc):
                        raise
                    attempt += 1
                    retried += 1
                    module.warn(
                        "%s: %s failed with SQLSTATE %s, attempt %d of %d follows in %ds: %s"
                        % (label, stage, sqlstate(exc), attempt + 1, params["retries"] + 1,
                           params["retry_delay"], to_native(exc).strip())
                    )
                    time.sleep(params["retry_delay"])
        stage = "reset role"
        run("RESET ROLE")
        if params["invalid_index_verify"]:
            stage = "invalid index verification"
            run(params["invalid_index_verify"])
        stage = "ledger insert"
        run(params["ledger_insert"])
        stage = "advisory unlock"
        cursor.execute(
            "SELECT pg_advisory_unlock(hashtext('frameworks_migrations'), hashtext(%s))",
            (params["db"],),
        )
    except Exception as exc:
        module.fail_json(
            msg="%s was not recorded: %s failed with SQLSTATE %s: %s. Statements before it stay applied; rerunning "
            "the migration executes every statement of the item again." % (label, stage, sqlstate(exc) or "unknown",
                                                                            to_native(exc).strip()),
            statements=total,
            retried=retried,
        )
    finally:
        db_connection.close()

    module.exit_json(changed=True, statements=total, retried=retried)


if __name__ == "__main__":
    main()
