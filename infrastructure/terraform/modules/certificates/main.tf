resource "tls_private_key" "private_key" {
  algorithm = "RSA"
  rsa_bits  = 4096
}

resource "acme_registration" "reg" {
  account_key_pem = tls_private_key.private_key.private_key_pem
  email_address   = var.email
}

resource "acme_certificate" "certificate" {
  for_each = var.domains
  
  account_key_pem = acme_registration.reg.account_key_pem
  common_name     = each.key
  
  subject_alternative_names = each.value.sans
  
  dns_challenge {
    provider = "cloudflare"
    config = {
      CF_API_TOKEN = var.cloudflare_token
    }
  }
}

# Store certificates in Vault for Ansible to retrieve
resource "vault_generic_secret" "certificates" {
  for_each = acme_certificate.certificate
  
  path = "secret/frameworks/certificates/${each.key}"
  
  data_json = jsonencode({
    certificate = each.value.certificate_pem
    private_key = each.value.private_key_pem
    issuer_pem  = each.value.issuer_pem
  })
} 