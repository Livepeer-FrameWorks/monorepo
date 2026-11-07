#!/usr/bin/env python3
"""Checks the external simulator's wire contract without starting a stack."""

import hashlib
import ast
import hmac
import importlib.util
import json
import os
import re
import pathlib
import http.client
import threading
import unittest
from unittest.mock import patch
import xml.etree.ElementTree as ET

SPEC = importlib.util.spec_from_file_location("billing_provider", pathlib.Path(__file__).with_name("billing-provider.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class BillingProviderTest(unittest.TestCase):
    def setUp(self):
        self.provider = MODULE.Provider()

    def call(self, method, path, body=b""):
        status, value = self.provider.dispatch(method, path, {}, body, "fixture-test")
        self.assertEqual(status, 200, value)
        return value

    def subscription(self):
        customer = self.call("POST", "/v1/customers", b"name=ContractBuyer")
        price = self.call("POST", "/v1/prices", b"unit_amount=1000&currency=eur")
        checkout = self.call("POST", "/v1/checkout/sessions", ("mode=subscription&customer=" + customer["id"] + "&line_items[0][price]=" + price["id"]).encode())
        self.call("POST", "/__test/complete", json.dumps({"session_id": checkout["id"]}).encode())
        return self.provider.objects[checkout["subscription"]]

    def test_metadata_merges_deletes_and_enforces_capacity_before_mutation(self):
        sub = self.subscription()
        path = "/v1/subscriptions/" + sub["id"]
        self.call("POST", path, b"metadata[older]=pending&metadata[newer]=pending")
        self.call("POST", path, b"metadata[older]=&metadata[context]=stable")
        self.assertNotIn("older", sub["metadata"])
        self.assertEqual(sub["metadata"]["newer"], "pending")
        sub["metadata"] = {"key" + str(index): "value" for index in range(50)}
        before = dict(sub["metadata"])
        status, _ = self.provider.dispatch("POST", path, {}, b"metadata[overflow]=value", "fixture-test")
        self.assertEqual(status, 400)
        self.assertEqual(sub["metadata"], before)
        self.call("POST", path, b"metadata[key0]=&metadata[replacement]=pending")
        self.assertEqual(len(sub["metadata"]), 50)
        for key, value in (("k" * 41, "v"), ("normal", "v" * 501)):
            before = dict(sub["metadata"])
            status, _ = self.provider.dispatch("POST", path, {}, ("metadata[" + key + "]=" + value).encode(), "fixture-test")
            self.assertEqual(status, 400)
            self.assertEqual(sub["metadata"], before)

    def open_invoice(self, sub):
        self.call("POST", "/v1/subscriptions/" + sub["id"], b"pause_collection[behavior]=keep_as_draft")
        events = self.call("POST", "/__test/renew", json.dumps({"subscription_id": sub["id"]}).encode())
        invoice = json.loads(events[0]["body"])["data"]["object"]
        self.call("POST", "/v1/invoices/" + invoice["id"] + "/finalize", b"auto_advance=true")
        self.call("POST", "/v1/subscriptions/" + sub["id"], b"pause_collection=")
        return invoice["id"]

    def test_invoice_retains_legal_footer_and_brand_alias(self):
        sub = self.subscription()
        customer = self.provider.objects[sub["customer"]]
        legal_identity = "DDVTech B.V.; Example Street 1\nAmsterdam; NL000000000B01"
        customer["invoice_settings"] = {"footer": legal_identity}
        invoice = self.provider.invoice(sub)
        self.assertEqual(invoice.get("footer"), legal_identity)
        self.assertEqual(invoice["account_name"], "FrameWorks-Stack-Test")
        customer["invoice_settings"]["footer"] = "Changed later"
        self.assertEqual(invoice["footer"], legal_identity)

    def test_draft_buyer_tracks_customer_until_finalization_then_freezes(self):
        sub = self.subscription()
        invoice = self.provider.invoice(sub, draft=True)
        customer_id, invoice_id = sub["customer"], invoice["id"]
        self.call("POST", "/v1/customers/" + customer_id, b"name=UpdatedBuyer&email=updated%40example.test&address[line1]=NewStreet&address[city]=Amsterdam&address[postal_code]=1000&address[country]=NL")
        self.call("POST", "/v1/customers/" + customer_id + "/tax_ids", b"type=eu_vat&value=NL000000000B01")
        draft = self.call("GET", "/v1/invoices/" + invoice_id)
        self.assertEqual((draft["customer_name"], draft["customer_email"], draft["customer_address"]["country"]), ("UpdatedBuyer", "updated@example.test", "NL"))
        self.assertEqual(draft["customer_tax_ids"][0]["value"], "NL000000000B01")
        self.call("POST", "/v1/customers/" + customer_id, b"name=FinalBuyer&address[country]=DE")
        finalized = self.call("POST", "/v1/invoices/" + invoice_id + "/finalize", b"auto_advance=true")
        frozen = {key: json.loads(json.dumps(finalized[key])) for key in ("customer_name", "customer_email", "customer_address", "customer_tax_ids")}
        self.assertEqual((frozen["customer_name"], frozen["customer_address"]["country"]), ("FinalBuyer", "DE"))
        self.call("POST", "/v1/customers/" + customer_id, b"name=ChangedAfter&email=after%40example.test&address[country]=US")
        self.call("POST", "/v1/customers/" + customer_id + "/tax_ids", b"type=eu_vat&value=DE123456789")
        retained = self.call("GET", "/v1/invoices/" + invoice_id)
        self.assertEqual({key: retained[key] for key in frozen}, frozen)

    def test_payment_outcomes_do_not_invent_success(self):
        for outcome, expected_status in (("declined", "requires_payment_method"), ("requires_action", "requires_action"), ("processing", "processing")):
            with self.subTest(outcome=outcome):
                sub = self.subscription()
                invoice = self.open_invoice(sub)
                self.call("POST", "/__test/payment_behavior", json.dumps({"customer_id": sub["customer"], "outcome": outcome}).encode())
                status, result = self.provider.dispatch("POST", "/v1/invoices/" + invoice + "/pay", {}, b"", "fixture-test")
                self.assertEqual(status, 200 if outcome == "processing" else 402, result)
                stored = self.provider.objects[invoice]
                self.assertEqual((stored["status"], stored["amount_paid"]), ("open", 0))
                self.assertTrue(stored["auto_advance"])
                self.assertEqual(self.provider.objects[stored["payment_intent"]]["status"], expected_status)

    def test_automatic_collection_is_async_and_processing_settles_once(self):
        sub = self.subscription()
        invoice = self.open_invoice(sub)
        self.call("POST", "/__test/payment_behavior", json.dumps({"customer_id": sub["customer"], "outcome": "processing"}).encode())
        self.assertEqual(self.provider.objects[invoice]["status"], "open")
        self.call("POST", "/__test/collect", json.dumps({"invoice_id": invoice}).encode())
        self.assertEqual((self.provider.objects[invoice]["status"], self.provider.objects[invoice]["amount_paid"]), ("open", 0))
        attempts = self.provider.objects[invoice]["attempt_count"]
        self.call("POST", "/__test/collect", json.dumps({"invoice_id": invoice}).encode())
        self.assertEqual(self.provider.objects[invoice]["attempt_count"], attempts)
        self.call("POST", "/__test/settle", json.dumps({"invoice_id": invoice}).encode())
        self.assertEqual(self.provider.objects[invoice]["status"], "paid")
        self.assertEqual(sum(o["object"] == "invoice_payment" and o["invoice"] == invoice and o["status"] == "paid" for o in self.provider.objects.values()), 1)

    def test_executed_402_and_500_replay_but_preexecution_errors_do_not(self):
        sub = self.subscription()
        invoice = self.open_invoice(sub)
        self.call("POST", "/__test/payment_behavior", json.dumps({"customer_id": sub["customer"], "outcome": "declined"}).encode())
        args = ("POST", "/v1/invoices/" + invoice + "/pay", {}, b"", "fixture-test", {"Idempotency-Key": "declined-attempt"})
        declined = self.provider.dispatch(*args)
        self.assertEqual(declined[0], 402)
        self.call("POST", "/__test/payment_behavior", json.dumps({"customer_id": sub["customer"], "outcome": "succeeded"}).encode())
        self.assertEqual(self.provider.dispatch(*args), declined)
        self.assertEqual(self.provider.objects[invoice]["status"], "open")
        self.call("POST", "/__test/fault", json.dumps({"method": "POST", "path": "/v1/customers", "status": 500, "after_execution": True}).encode())
        create = ("POST", "/v1/customers", {}, b"name=Uncertain", "fixture-test", {"Idempotency-Key": "uncertain-create"})
        failure = self.provider.dispatch(*create)
        self.assertEqual(failure[0], 500)
        count = len(self.provider.objects)
        self.assertEqual(self.provider.dispatch(*create), failure)
        self.assertEqual(len(self.provider.objects), count)
        self.call("POST", "/__test/fault", json.dumps({"method": "POST", "path": "/v1/customers", "status": 409, "after_execution": False}).encode())
        conflict = ("POST", "/v1/customers", {}, b"name=Retry", "fixture-test", {"Idempotency-Key": "concurrent-attempt"})
        self.assertEqual(self.provider.dispatch(*conflict)[0], 409)
        self.assertEqual(self.provider.dispatch(*conflict)[0], 200)

    def test_error_if_incomplete_does_not_apply_failed_upgrade(self):
        sub = self.subscription()
        before = json.loads(json.dumps(sub))
        price = self.call("POST", "/v1/prices", b"unit_amount=5000&currency=eur")
        self.call("POST", "/__test/payment_behavior", json.dumps({"customer_id": sub["customer"], "outcome": "requires_action"}).encode())
        status, _ = self.provider.dispatch("POST", "/v1/subscriptions/" + sub["id"], {}, ("items[0][price]=" + price["id"] + "&billing_cycle_anchor=now&payment_behavior=error_if_incomplete&proration_behavior=none").encode(), "fixture-test")
        self.assertEqual(status, 402)
        self.assertEqual(sub, before)

    def test_proration_none_suppresses_native_credit(self):
        for proration, expected in (("none", 5000), ("create_prorations", 4000)):
            with self.subTest(proration=proration):
                sub = self.subscription()
                price = self.call("POST", "/v1/prices", b"unit_amount=5000&currency=eur")
                updated = self.call("POST", "/v1/subscriptions/" + sub["id"], ("items[0][price]=" + price["id"] + "&billing_cycle_anchor=now&proration_behavior=" + proration).encode())
                self.assertEqual(self.provider.objects[updated["latest_invoice"]]["total_excluding_tax"], expected)

    def test_mixed_invoice_fixtures_preserve_ownership_and_payment_outcomes(self):
        sub = self.subscription()
        self.call("POST", "/v1/subscriptions/" + sub["id"], b"metadata[frameworks_tax_profile]=paid-profile&pause_collection[behavior]=keep_as_draft&metadata[frameworks_tax_review]=true&metadata[frameworks_tax_pause_owned]=true&metadata[frameworks_tax_hold_started]=123")
        source = self.provider.objects[sub["latest_invoice"]]
        source["metadata"]["frameworks_tax_profile"] = "paid-profile"
        manual = self.call("POST", "/__test/unrelated_invoice", json.dumps({"subscription_id": sub["id"]}).encode())
        adjustment = self.call("POST", "/__test/held_adjustment", json.dumps({"subscription_id": sub["id"]}).encode())
        stopped = self.call("POST", "/__test/operator_paused_invoice", json.dumps({"subscription_id": sub["id"]}).encode())
        self.assertIsNone(manual["parent"])
        self.assertEqual(adjustment["billing_reason"], "subscription_update")
        self.assertEqual(adjustment["parent"]["subscription_details"]["metadata"]["frameworks_tax_review"], "true")
        self.assertEqual((stopped["status"], stopped["auto_advance"], stopped["metadata"]["frameworks_tax_recovered_hold"]), ("open", False, "true"))
        invoices = [self.open_invoice(sub), self.open_invoice(sub)]
        for invoice, outcome in zip(invoices, ("declined", "requires_action")):
            self.call("POST", "/__test/payment_behavior", json.dumps({"invoice_id": invoice, "outcome": outcome}).encode())
        for invoice, expected in zip(invoices, ("requires_payment_method", "requires_action")):
            self.call("POST", "/__test/collect", json.dumps({"invoice_id": invoice}).encode())
            self.assertEqual(self.provider.objects[self.provider.objects[invoice]["payment_intent"]]["status"], expected)
        self.assertEqual((manual["status"], adjustment["status"], stopped["auto_advance"]), ("open", "draft", False))

    def test_hosted_settlement_has_exact_finalized_transfer_receipt(self):
        auth = {"from": "0x" + "3" * 40, "to": "0x" + "4" * 40, "value": "1210000"}
        requirements = {"network": "eip155:8453", "asset": "0x" + "5" * 40}
        body = json.dumps({"paymentPayload": {"payload": {"authorization": auth}}, "paymentRequirements": requirements}).encode()
        verified = self.call("POST", "/verify", body)
        self.assertEqual(verified, {"isValid": True, "payer": auth["from"]})
        settled = self.call("POST", "/settle", body)
        receipt = self.call("POST", "/rpc/base", json.dumps({"jsonrpc": "2.0", "id": 1, "method": "eth_getTransactionReceipt", "params": [settled["transaction"]]}).encode())["result"]
        self.assertEqual(receipt["status"], "0x1")
        self.assertEqual(receipt["logs"][0]["address"], requirements["asset"])
        self.assertEqual(int(receipt["logs"][0]["data"], 16), int(auth["value"]))
        self.assertEqual(receipt["logs"][0]["topics"][2], "0x" + auth["to"][2:].zfill(64))

    def test_subscription_renewal_uses_exclusive_tax_and_signed_events(self):
        customer = self.call("POST", "/v1/customers", b"name=Stack&metadata[tenant_id]=tenant&address[country]=NL")
        price = self.call("POST", "/v1/prices", b"unit_amount=1000&currency=eur")
        rate = self.call("POST", "/v1/tax_rates", b"percentage=21&inclusive=false&country=NL")
        session = self.call("POST", "/v1/checkout/sessions", ("mode=subscription&customer=" + customer["id"] + "&line_items[0][price]=" + price["id"] + "&subscription_data[default_tax_rates][0]=" + rate["id"] + "&subscription_data[metadata][tenant_id]=tenant").encode())
        events = self.call("POST", "/__test/complete", json.dumps({"session_id": session["id"]}).encode())
        invoice = json.loads(events[-1]["body"])["data"]["object"]
        self.assertEqual((invoice["total_excluding_tax"], invoice["total"]), (1000, 1210))
        self.assertEqual(invoice["lines"]["data"][0]["taxes"], invoice["total_taxes"])
        self.assertEqual(invoice["lines"]["data"][0]["taxes"][0]["amount"], 210)
        self.assertEqual(invoice["lines"]["data"][0]["taxes"][0]["tax_behavior"], "exclusive")
        self.assertEqual(invoice["lines"]["data"][0]["taxes"][0]["tax_rate_details"]["tax_rate"], rate["id"])
        renewed = self.call("POST", "/__test/renew", json.dumps({"subscription_id": session["subscription"]}).encode())
        self.assertEqual(len(renewed), 1)
        draft = json.loads(renewed[0]["body"])["data"]["object"]
        self.assertEqual(draft["status"], "draft")
        self.assertTrue(draft["auto_advance"])
        self.assertEqual(draft["lines"]["data"][0]["period"]["start"], invoice["lines"]["data"][0]["period"]["end"])
        renewed = self.call("POST", "/__test/collect", json.dumps({"invoice_id": draft["id"]}).encode())
        self.assertEqual(len(renewed), 2)
        event = renewed[-1]
        timestamp = event["signature"].split(",")[0][2:]
        digest = hmac.new(b"whsec_stack_fixture", (timestamp + "." + event["body"]).encode(), hashlib.sha256).hexdigest()
        self.assertEqual(event["signature"], "t=" + timestamp + ",v1=" + digest)
        renewal = json.loads(event["body"])["data"]["object"]
        self.assertEqual(renewal["total"], 1210)
        self.assertEqual(renewal["lines"]["data"][0]["taxes"], renewal["total_taxes"])
        self.assertFalse(rate["inclusive"])

    def test_vies_is_wire_soap_and_pdf_is_original_bytes(self):
        soap = self.call("POST", "/vies", b"<countryCode>AT</countryCode><vatNumber>U12345678</vatNumber>")
        self.assertIn(b"<valid>true</valid>", soap)
        self.assertIn(b"<countryCode>AT</countryCode>", soap)
        date = ET.fromstring(soap).find(".//{urn:ec.europa.eu:taxud:vies:services:checkVat:types}requestDate").text
        self.assertRegex(date, r"^\d{4}-\d{2}-\d{2}\+01:00$")
        self.assertTrue(self.call("GET", "/in_stack1.pdf").startswith(b"%PDF-"))

    def test_read_only_webhook_endpoint_matches_gateway(self):
        with patch.dict(os.environ, {"GATEWAY_PUBLIC_URL": "https://gateway.stack.test/"}):
            endpoints = self.call("GET", "/v1/webhook_endpoints")["data"]
        self.assertEqual(endpoints[0]["url"], "https://gateway.stack.test/webhooks/billing/stripe")
        self.assertNotIn("*", endpoints[0]["enabled_events"])
        self.assertTrue({"invoice.finalized", "credit_note.created", "credit_note.updated", "credit_note.voided"}.issubset(endpoints[0]["enabled_events"]))
        checker = pathlib.Path(__file__).parents[2].joinpath("api_billing/internal/stripe/webhook_configuration.go").read_text()
        required = re.search(r"var requiredBillingWebhookEvents = \[\]string\{(.*?)\n\}", checker, re.S)[1]
        self.assertEqual(set(endpoints[0]["enabled_events"]), set(re.findall(r'"([^"]+)"', required)))
        self.assertEqual(self.provider.objects, {})
        self.assertEqual([r["method"] for r in self.provider.requests], ["GET"])

    def test_negative_ordinary_price_rejected_without_creating_object(self):
        before = dict(self.provider.objects)
        status, error = self.provider.dispatch("POST", "/v1/prices", {}, b"unit_amount=-100&currency=eur", "fixture-test")
        self.assertEqual(status, 400, error)
        self.assertEqual(self.provider.objects, before)

    def test_keep_as_draft_requires_explicit_recovery(self):
        customer = self.call("POST", "/v1/customers", b"name=Stack")
        price = self.call("POST", "/v1/prices", b"unit_amount=1000&currency=eur")
        rate = self.call("POST", "/v1/tax_rates", b"percentage=21&inclusive=false")
        session = self.call("POST", "/v1/checkout/sessions", ("mode=subscription&customer=" + customer["id"] + "&line_items[0][price]=" + price["id"]).encode())
        self.call("POST", "/__test/complete", json.dumps({"session_id": session["id"]}).encode())
        subscription = session["subscription"]
        synced = self.call("POST", "/v1/subscriptions/" + subscription, ("items=&default_tax_rates[0]=" + rate["id"]).encode())
        self.assertIsInstance(synced["items"], dict)
        self.assertEqual(len(synced["items"]["data"]), 1)
        self.call("POST", "/v1/subscriptions/" + subscription, b"pause_collection[behavior]=keep_as_draft")
        events = self.call("POST", "/__test/renew", json.dumps({"subscription_id": subscription}).encode())
        self.assertEqual([json.loads(e["body"])["type"] for e in events], ["invoice.created"])
        draft = json.loads(events[0]["body"])["data"]["object"]
        self.assertEqual((draft["status"], draft["amount_paid"]), ("draft", 0))
        self.call("POST", "/v1/subscriptions/" + subscription, b"pause_collection=")
        self.assertEqual(self.provider.objects[draft["id"]]["status"], "draft")
        updated = self.call("POST", "/v1/invoices/" + draft["id"], ("auto_advance=false&default_tax_rates[0]=" + rate["id"]).encode())
        self.assertEqual((updated["status"], updated["total"]), ("draft", 1210))
        self.assertIs(updated["auto_advance"], False)
        repriced = self.call("POST", "/v1/invoices/" + draft["id"] + "/update_lines", ("lines[0][id]=" + updated["lines"]["data"][0]["id"] + "&lines[0][tax_rates][0]=" + rate["id"]).encode())
        self.assertEqual(repriced["lines"]["data"][0]["taxes"][0]["amount"], 210)
        finalized = self.call("POST", "/v1/invoices/" + draft["id"] + "/finalize")
        self.assertEqual((finalized["status"], finalized["amount_paid"]), ("open", 0))
        self.assertIs(finalized["auto_advance"], False)
        args = ("POST", "/v1/invoices/" + draft["id"] + "/pay", {}, b"", "fixture-test", {"Idempotency-Key": "pay-held-renewal"})
        status, paid = self.provider.dispatch(*args)
        self.assertEqual(status, 200)
        self.assertEqual(self.provider.dispatch(*args), (status, paid))
        self.assertEqual((paid["status"], paid["amount_paid"]), ("paid", 1210))
        self.assertEqual(sum(obj["object"] == "invoice_payment" and obj["invoice"] == draft["id"] for obj in self.provider.objects.values()), 1)
        self.call("POST", "/v1/subscriptions/" + subscription, b"pause_collection[behavior]=keep_as_draft")
        next_events = self.call("POST", "/__test/renew", json.dumps({"subscription_id": subscription}).encode())
        next_draft = json.loads(next_events[0]["body"])["data"]["object"]
        advancing = self.call("POST", "/v1/invoices/" + next_draft["id"] + "/finalize", b"auto_advance=true")
        self.assertIs(advancing["auto_advance"], True)

    def test_idempotency_retries_do_not_duplicate_and_failures_do_not_poison_key(self):
        args = ("POST", "/v1/prices", {}, b"unit_amount=1000&currency=eur", "fixture-test")
        headers = {"Idempotency-Key": "same-price"}
        first = self.provider.dispatch(*args, headers)
        second = self.provider.dispatch(*args, headers)
        self.assertEqual(first, second)
        self.assertEqual(len(self.provider.objects), 1)
        status, _ = self.provider.dispatch("POST", "/v1/prices", {}, b"unit_amount=2000", "fixture-test", headers)
        self.assertEqual(status, 400)
        status, _ = self.provider.dispatch("POST", "/v1/prices", {}, b"unit_amount=-1", "fixture-test", {"Idempotency-Key": "retry-after-validation"})
        self.assertEqual(status, 400)
        status, _ = self.provider.dispatch("POST", "/v1/prices", {}, b"unit_amount=1", "fixture-test", {"Idempotency-Key": "retry-after-validation"})
        self.assertEqual(status, 200)

    def test_paid_upgrade_inline_credit_keeps_original_zero_tax(self):
        customer = self.call("POST", "/v1/customers", b"name=Stack")
        price = self.call("POST", "/v1/prices", b"unit_amount=1000&currency=eur")
        new_price = self.call("POST", "/v1/prices", b"unit_amount=10000&currency=eur")
        zero = self.call("POST", "/v1/tax_rates", b"percentage=0&inclusive=false")
        rate = self.call("POST", "/v1/tax_rates", b"percentage=21&inclusive=false")
        session = self.call("POST", "/v1/checkout/sessions", ("mode=subscription&customer=" + customer["id"] + "&line_items[0][price]=" + price["id"]).encode())
        self.call("POST", "/__test/complete", json.dumps({"session_id": session["id"]}).encode())
        sub = session["subscription"]
        # The pinned Stripe subscription API explicitly permits signed inline
        # credit prices, unlike POST /v1/prices.
        inline = self.call("POST", "/v1/subscriptions/" + sub, ("billing_cycle_anchor=now&proration_behavior=none&items[0][price]=" + new_price["id"] + "&default_tax_rates[0]=" + rate["id"] + "&add_invoice_items[0][quantity]=1&add_invoice_items[0][price_data][unit_amount]=-1000&add_invoice_items[0][price_data][currency]=eur&add_invoice_items[0][tax_rates][0]=" + zero["id"]).encode())
        self.assertIsInstance(inline["billing_cycle_anchor"], int)
        credited_inline = self.provider.objects[inline["latest_invoice"]]
        self.assertEqual((credited_inline["status"], credited_inline["total_excluding_tax"], credited_inline["total"]), ("paid", 9000, 11100))
        self.assertEqual(credited_inline["lines"]["data"][1]["taxes"][0]["amount"], 0)
        self.assertEqual(self.call("GET", "/v1/invoices/" + credited_inline["id"] + "/lines"), credited_inline["lines"])

    def test_compose_installs_a_single_valid_policy_object(self):
        compose = pathlib.Path(__file__).parents[2].joinpath("docker-compose.stack.yml").read_text()
        value = next(line.split(": ", 1)[1] for line in compose.splitlines() if line.strip().startswith("CRYPTO_TAX_POLICY_JSON:"))
        policy = json.loads(ast.literal_eval(value))
        self.assertEqual(policy["supplier_country"], "NL")
        self.assertEqual(policy["non_eu_businesses"]["US"]["rate_bps"], 0)

    def test_http_chunked_stripe_body_preserves_customer_and_metadata(self):
        previous = MODULE.STATE
        MODULE.STATE = self.provider
        server = MODULE.ThreadingHTTPServer(("127.0.0.1", 0), MODULE.Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        connection = http.client.HTTPConnection(*server.server_address)
        try:
            chunks = iter([b"name=Stack&metadata%5Btenant_", b"id%5D=tenant-fixture&email=stack%40example.test"])
            connection.request("POST", "/v1/customers", body=chunks, headers={"Content-Type": "application/x-www-form-urlencoded"}, encode_chunked=True)
            response = connection.getresponse()
            self.assertEqual(response.status, 200)
            customer = json.load(response)
            self.assertEqual(customer.get("name"), "Stack")
            self.assertEqual(customer.get("metadata"), {"tenant_id": "tenant-fixture"})
        finally:
            connection.close()
            server.shutdown()
            server.server_close()
            thread.join()
            MODULE.STATE = previous

    def test_stripe_checkout_indexed_fields_are_json_arrays(self):
        session = self.call("POST", "/v1/checkout/sessions", b"mode=payment&payment_method_types[0]=card&payment_method_types[1]=ideal")
        self.assertEqual(session["payment_method_types"], ["card", "ideal"])


if __name__ == "__main__":
    unittest.main()
