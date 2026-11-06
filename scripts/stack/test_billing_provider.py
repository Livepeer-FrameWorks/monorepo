#!/usr/bin/env python3
"""Checks the external simulator's wire contract without starting a stack."""

import hashlib
import ast
import hmac
import importlib.util
import json
import os
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
        self.assertRegex(date, r"^\d{4}-\d{2}-\d{2}(Z|[+-]\d{2}:\d{2})$")
        self.assertTrue(self.call("GET", "/in_stack1.pdf").startswith(b"%PDF-"))

    def test_read_only_webhook_endpoint_matches_gateway(self):
        with patch.dict(os.environ, {"GATEWAY_PUBLIC_URL": "https://gateway.stack.test/"}):
            endpoints = self.call("GET", "/v1/webhook_endpoints")["data"]
        self.assertEqual(endpoints, [{"id": "we_stack", "object": "webhook_endpoint", "status": "enabled", "url": "https://gateway.stack.test/webhooks/billing/stripe", "enabled_events": ["*"]}])
        self.assertEqual(self.provider.objects, {})
        self.assertEqual([r["method"] for r in self.provider.requests], ["GET"])

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
