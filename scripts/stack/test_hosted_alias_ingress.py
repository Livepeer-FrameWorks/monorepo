#!/usr/bin/env python3
"""Require the hosted-ingress wrapper to execute its named qualification test."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class HostedIngressSelectionTest(unittest.TestCase):
    def setUp(self):
        work = tempfile.TemporaryDirectory()
        self.addCleanup(work.cleanup)
        self.repo = Path(work.name)
        (self.repo / "scripts").mkdir()
        (self.repo / "config").mkdir()
        source = Path(__file__).resolve().parents[1] / "test-hosted-alias-ingress.sh"
        self.script = self.repo / "scripts" / source.name
        shutil.copyfile(source, self.script)
        (self.repo / "config/infrastructure.yaml").write_text(
            "images:\n  - name: nginx\n    image: nginx\n    digest: sha256:fixture\n")
        (self.repo / "Makefile").write_text(
            "test-privateer test-cli:\n"
            "\t@printf '%s\\n' '$(GO_TEST_FLAGS)' >> \"$$FIXTURE_FLAGS\"\n"
            "\t@cat \"$$FIXTURE_OUTPUT\"\n"
            "\t@exit \"$$FIXTURE_EXIT\"\n")
        self.env = dict(os.environ, MAKE=shutil.which("make"),
                        FIXTURE_FLAGS=str(self.repo / "flags"),
                        FIXTURE_OUTPUT=str(self.repo / "output"))
        self.env.pop("MAKEFLAGS", None)
        self.env.pop("MFLAGS", None)

    def run_wrapper(self, output, exit_code=0):
        (self.repo / "output").write_text(output + "\n")
        (self.repo / "flags").write_text("")
        return subprocess.run(["bash", str(self.script)], cwd=self.repo,
                              env=dict(self.env, FIXTURE_EXIT=str(exit_code)),
                              capture_output=True, text=True)

    def test_no_selected_tests_is_a_failure(self):
        result = self.run_wrapper("ok example/agent 1s [no tests to run]")
        self.assertNotEqual(result.returncode, 0, result.stdout)

    def test_another_test_cannot_prove_the_ingress_test_passed(self):
        result = self.run_wrapper("--- PASS: TestOtherIngress (0.01s)")
        self.assertNotEqual(result.returncode, 0, result.stdout)

    def test_skipped_ingress_is_a_failure(self):
        result = self.run_wrapper("--- SKIP: TestTenantAliasPrivateerDockerIngressLifecycle (0.00s)")
        self.assertNotEqual(result.returncode, 0, result.stdout)

    def test_named_pass_preserves_the_regex_anchor_through_make(self):
        result = self.run_wrapper("--- PASS: TestTenantAliasPrivateerDockerIngressLifecycle (0.01s)\n"
                                  "--- PASS: TestNginxRootConfigAcceptsMaximumLengthAlias (0.01s)\n"
                                  "--- PASS: TestNginxAccessLogRedactsPublishingCredentials (0.01s)")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.repo / "flags").read_text().strip(),
                         "-run ^TestTenantAliasPrivateerDockerIngressLifecycle$ -v -timeout 180s\n"
                         "-run ^TestNginxRootConfigAcceptsMaximumLengthAlias$ -v -timeout 180s\n"
                         "-run ^TestNginxAccessLogRedactsPublishingCredentials$ -v -timeout 180s")

    def test_root_config_cannot_hide_missing_credential_redaction(self):
        result = self.run_wrapper("--- PASS: TestTenantAliasPrivateerDockerIngressLifecycle (0.01s)\n"
                                  "--- PASS: TestNginxRootConfigAcceptsMaximumLengthAlias (0.01s)")
        self.assertNotEqual(result.returncode, 0, result.stdout)

    def test_first_test_cannot_hide_missing_root_config_validation(self):
        result = self.run_wrapper("--- PASS: TestTenantAliasPrivateerDockerIngressLifecycle (0.01s)")
        self.assertNotEqual(result.returncode, 0, result.stdout)

    def test_failed_make_stays_failed_even_with_a_pass_marker(self):
        result = self.run_wrapper("--- PASS: TestTenantAliasPrivateerDockerIngressLifecycle (0.01s)", 1)
        self.assertNotEqual(result.returncode, 0, result.stdout)


if __name__ == "__main__":
    unittest.main()
